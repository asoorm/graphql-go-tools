package lower

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// newPath lowers via the obligation-driven path -- now the shipping default (zero-value LowerConfig).
func newPath(t *testing.T, h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result, op, def *ast.Document) *plan.SynchronousResponsePlan {
	t.Helper()
	p, err := lowerWithConfig(h, o, res, op, def, LowerConfig{}, nil, InfoConfig{})
	if err != nil {
		t.Fatalf("obligation-driven lower: %v", err)
	}
	return p
}

// newPathDocs returns the fetch documents (QueryPlan.Query) of a plan.
func newPathDocs(p *plan.SynchronousResponsePlan) []string {
	var out []string
	for _, f := range p.Response.RawFetches {
		out = append(out, f.Fetch.(*resolve.SingleFetch).QueryPlan.Query)
	}
	return out
}

// TestNewPathBuyerSellerTwoEntityFetches is the canonical demand-1 witness on the NEW path:
// `order { buyer { rating } seller { rating } }` where buyer and seller are the SAME User entity at
// two sibling response positions. The shipping path emits ONE `_entities` fetch (seller dropped); the
// obligation-driven path must emit BOTH ids at the root AND TWO entity fetches, one at order.buyer and
// one at order.seller, each with its own ResponsePath/FetchPath.
func TestNewPathBuyerSellerTwoEntityFetches(t *testing.T) {
	h := buildBuyerSellerH(t)
	o, op, def := buildTree(t, h,
		`schema { query: Query } type Query { order: Order } type Order { id: ID buyer: User seller: User } type User { id: ID rating: Int }`,
		`{ order { buyer { rating } seller { rating } } }`)
	res := runSearch(t, h, o)
	p := newPath(t, h, o, res, op, def)

	var rootDocs []string
	entityPaths := map[string]int{}
	for _, f := range p.Response.RawFetches {
		sf := f.Fetch.(*resolve.SingleFetch)
		if sf.RequiresEntityFetch {
			entityPaths[f.ResponsePath]++
			if !strings.Contains(sf.QueryPlan.Query, "... on User { rating }") {
				t.Fatalf("entity fetch at %q must select rating: %s", f.ResponsePath, sf.QueryPlan.Query)
			}
			if len(sf.DependsOnFetchIDs) != 1 {
				t.Fatalf("entity fetch at %q must depend on the root fetch, deps=%v", f.ResponsePath, sf.DependsOnFetchIDs)
			}
		} else {
			rootDocs = append(rootDocs, sf.QueryPlan.Query)
		}
	}

	// Exactly two entity fetches, at the two DISTINCT sibling positions -- the conflation fix.
	if entityPaths["order.buyer"] != 1 || entityPaths["order.seller"] != 1 || len(entityPaths) != 2 {
		t.Fatalf("want two entity fetches at order.buyer and order.seller, got %v", entityPaths)
	}
	// The root fetch must select BOTH keys (seller no longer dropped).
	if len(rootDocs) != 1 {
		t.Fatalf("want exactly one root fetch, got %d", len(rootDocs))
	}
	// D11.6: the entity-key parent selects __typename alongside the key (v1 `{order {buyer {__typename id}
	// seller {__typename id}}}`), so each object yields a typed representation.
	if !strings.Contains(rootDocs[0], "buyer { __typename id }") || !strings.Contains(rootDocs[0], "seller { __typename id }") {
		t.Fatalf("root fetch must select both buyer.__typename/id and seller.__typename/id: %s", rootDocs[0])
	}

	// The FetchPath of each entity fetch must decompose to its real response object position.
	for _, f := range p.Response.RawFetches {
		sf := f.Fetch.(*resolve.SingleFetch)
		if !sf.RequiresEntityFetch {
			continue
		}
		if len(f.FetchPath) != 2 ||
			f.FetchPath[0].Kind != resolve.FetchItemPathElementKindObject || f.FetchPath[0].Path[0] != "order" ||
			f.FetchPath[1].Kind != resolve.FetchItemPathElementKindObject || f.FetchPath[1].Path[0] != strings.TrimPrefix(f.ResponsePath, "order.") {
			t.Fatalf("entity fetch at %q has wrong FetchPath: %+v", f.ResponsePath, f.FetchPath)
		}
	}

	// The plan must survive the untouched postprocess pipeline (two EntityFetches, root first).
	postprocess.NewProcessor().Process(p)
}

// TestNewPathSelfReferentialFriendsTerminates is the self-referential witness on the NEW path:
// `order { buyer { friends { friends { id } } } }`, all in subgraph A, no entity jump. The shipping
// path collapses every friends level onto the one (User,a) node and prints the SHORTEST route
// (`query { order { buyer { id } } }`); the obligation-driven printer walks O(Q) (bounded by query
// depth, so it terminates) and preserves the full nesting.
func TestNewPathSelfReferentialFriendsTerminates(t *testing.T) {
	h := buildSelfRefFriendsH(t)
	o, op, def := buildTree(t, h,
		`schema { query: Query } type Query { order: Order } type Order { id: ID buyer: User } type User { id: ID friends: [User] }`,
		`{ order { buyer { friends { friends { id } } } } }`)
	res := runSearch(t, h, o)
	p := newPath(t, h, o, res, op, def)

	docs := newPathDocs(p)
	if len(docs) != 1 {
		t.Fatalf("self-referential shape is one single-subgraph fetch, got %d: %v", len(docs), docs)
	}
	// The nested friends chain must be preserved verbatim (not collapsed to the shortest id route).
	if !strings.Contains(docs[0], "friends { friends { id } }") {
		t.Fatalf("new path must preserve the self-referential nesting, got: %s", docs[0])
	}
	if strings.Contains(docs[0], "buyer { id }") {
		t.Fatalf("new path must NOT collapse to the shortest buyer.id route, got: %s", docs[0])
	}
	if !strings.Contains(docs[0], "order { buyer { friends { friends { id } } } }") {
		t.Fatalf("new path document malformed: %s", docs[0])
	}
}

// TestNewPathPartialUnionShape is Section 7.1 on the NEW path: OnlyA/OnlyB are D6-narrowed (response-only
// nulls) and must keep their response-shape place while NO fetch document selects them; the covered
// Common member is still resolved (TypeMove + Field c) with __typename for gate resolution.
func TestNewPathPartialUnionShape(t *testing.T) {
	h, o, res, op, def := planPartialUnion(t)
	p := newPath(t, h, o, res, op, def)

	assertResponseShape(t, p, "{ wrapper { action { __typename ... on Common { c } ... on OnlyA { a } ... on OnlyB { b } } } }")
	assertNoFetchProduces(t, p, "OnlyA", "a")
	assertNoFetchProduces(t, p, "OnlyB", "b")

	docs := newPathDocs(p)
	if len(docs) != 1 {
		t.Fatalf("Section 7.1 is a single fetch, got %d", len(docs))
	}
	for _, want := range []string{"__typename", "... on Common { c }"} {
		if !strings.Contains(docs[0], want) {
			t.Fatalf("Section 7.1 fetch document must contain %q; doc=%q", want, docs[0])
		}
	}
}

// TestNewPathEntityJumpShape is Section 7.2 on the NEW path: one root fetch (keys + @requires) and one entity
// fetch at response path "product" selecting shippingEstimate, response shape { product { shippingEstimate } }.
func TestNewPathEntityJumpShape(t *testing.T) {
	h, o, res, op, def := planEntityJump(t)
	p := newPath(t, h, o, res, op, def)

	assertResponseShape(t, p, "{ product { shippingEstimate } }")

	var root, jump int
	for _, f := range p.Response.RawFetches {
		sf := f.Fetch.(*resolve.SingleFetch)
		if sf.RequiresEntityFetch {
			jump++
			if f.ResponsePath != "product" {
				t.Fatalf("entity fetch must attach at response path %q, got %q", "product", f.ResponsePath)
			}
			if !strings.Contains(sf.QueryPlan.Query, "shippingEstimate") {
				t.Fatalf("entity fetch must select shippingEstimate: %s", sf.QueryPlan.Query)
			}
			if len(sf.QueryPlan.DependsOnFields) != 2 {
				t.Fatalf("entity fetch must carry key + requires representations, got %+v", sf.QueryPlan.DependsOnFields)
			}
		} else {
			root++
			for _, want := range []string{"id", "organization", "dimensions"} {
				if !strings.Contains(sf.QueryPlan.Query, want) {
					t.Fatalf("root fetch must supply key/@requires field %q; doc=%q", want, sf.QueryPlan.Query)
				}
			}
			if f.ResponsePath != "" {
				t.Fatalf("root fetch must attach at the root (empty path), got %q", f.ResponsePath)
			}
		}
	}
	if root != 1 || jump != 1 {
		t.Fatalf("want one root and one entity fetch, got root=%d jump=%d", root, jump)
	}

	postprocess.NewProcessor().Process(p)
}

// TestNewPathSelfRefWithJumpAtDeepPosition is the COMBINED witness the parallel report flagged as the
// untested axis (concern 4): a self-referential type WHERE a deep, kappa-collapsed response position also
// requires an entity jump. `order { buyer { friends { friends { rating } } } }` -- all `friends` levels
// collapse onto the single (User,A) node (the self-reference), and `rating` lives in subgraph B behind
// an EntityJump keyed on User.id. The two axes the isolated witnesses each exercised (self-reference
// with no jump; a jump with no self-reference) are here BOTH active at the same deep position.
//
// FIXED (concern 4, resolved by the O(Q)-driven walkSpine): the jump is now attributed to the DEEP
// friends.friends position, not the shallow order.buyer one. walkSpine no longer reconstructs the
// spine from a node-keyed producedByWalk map (which collapsed every `friends` depth onto the single
// (User,A) node); it keys group creation and position attribution on the goal's OBLIGATION POSITION
// (the O(Q) hop stack), consulting kappa(g) only to locate the subgraph transition relative to the
// collapse-free leaf. Result: the `id` key is injected at the deep friends.friends position, `rating`
// resolves in a per-position `_entities` fetch attached there, and the root document keeps the full
// self-referential nesting `order { buyer { friends { friends { id } } } }`.
func TestNewPathSelfRefWithJumpAtDeepPosition(t *testing.T) {
	h := buildSelfRefFriendsJumpH(t)
	o, op, def := buildTree(t, h,
		`schema { query: Query } type Query { order: Order } type Order { id: ID buyer: User } type User { id: ID rating: Int friends: [User] }`,
		`{ order { buyer { friends { friends { rating } } } } }`)
	res := runSearch(t, h, o)
	p := newPath(t, h, o, res, op, def)

	var rootDoc, entityDoc, entityPath string
	var entityDeps []int
	var entityBatch bool
	roots, entities := 0, 0
	for _, f := range p.Response.RawFetches {
		sf := f.Fetch.(*resolve.SingleFetch)
		// D11.6: the entity fetch here lands under the `friends: [User]` list, so it is a BATCH fetch
		// (RequiresEntityBatchFetch, RequiresEntityFetch=false) -- v1 requiresEntityBatchFetch. Count both
		// single and batch as entity fetches.
		if sf.RequiresEntityFetch || sf.RequiresEntityBatchFetch {
			entities++
			entityDoc = sf.QueryPlan.Query
			entityPath = f.ResponsePath
			entityDeps = sf.DependsOnFetchIDs
			entityBatch = sf.RequiresEntityBatchFetch
		} else {
			roots++
			rootDoc = sf.QueryPlan.Query
		}
	}
	if roots != 1 || entities != 1 {
		t.Fatalf("want one root + one entity fetch, got root=%d entity=%d", roots, entities)
	}
	// The array-landing entity fetch MUST batch (crosses the `friends` list boundary).
	if !entityBatch {
		t.Fatalf("entity fetch under the `friends` list must be a BATCH fetch (RequiresEntityBatchFetch)")
	}
	// The root document keeps the full self-referential nesting and injects the key (+ __typename, D11.6)
	// at the DEEP position (never inline `rating`, never a shallow `buyer { ... id }`).
	if rootDoc != "query { order { buyer { friends { friends { __typename id } } } } }" {
		t.Fatalf("root document not the deep-key WANT shape: %s", rootDoc)
	}
	// The entity fetch is non-empty, selects rating, attaches at the deep friends.friends position
	// (with `@` list markers for the two `friends` list hops), and depends on the root fetch.
	if !strings.Contains(entityDoc, "... on User { rating }") {
		t.Fatalf("entity fetch must select `... on User { rating }`: %s", entityDoc)
	}
	if entityPath != "order.buyer.friends.@.friends" {
		t.Fatalf("entity fetch must attach at the deep friends.friends position, got %q", entityPath)
	}
	if len(entityDeps) != 1 || entityDeps[0] != 0 {
		t.Fatalf("entity fetch must depend on the root fetch, deps=%v", entityDeps)
	}
}

// --- hand-built witness hypergraphs (mirrors search/search_test.go) -----------------------------

// buildSelfRefFriendsJumpH is buildSelfRefFriendsH plus an EntityJump: (User,A).id keys into (User,B),
// where `rating` lives -- so a deep self-referential position (friends.friends) needs an entity jump.
func buildSelfRefFriendsJumpH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qOrder := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "order", Subgraph: 1})
	orderA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Order", Subgraph: 1})
	buyer := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Order", Field: "buyer", Subgraph: 1})
	userA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "User", Subgraph: 1})
	friends := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "User", Field: "friends", Subgraph: 1})
	userAID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "User", Field: "id", Subgraph: 1})
	userB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "User", Subgraph: 2})
	userBRating := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "User", Field: "rating", Subgraph: 2})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "order", Head: qOrder, Tails: []hypergraph.NodeID{r}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: orderA, Tails: []hypergraph.NodeID{qOrder}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "buyer", Head: buyer, Tails: []hypergraph.NodeID{orderA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{buyer}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "friends", Head: friends, Tails: []hypergraph.NodeID{userA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{friends}, Weight: 0}) // self-reference
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: userAID, Tails: []hypergraph.NodeID{userA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{
		Kind: hypergraph.EdgeEntityJump, Head: userB, Tails: []hypergraph.NodeID{userAID},
		KeyTails: []hypergraph.NodeID{userAID}, Weight: 5,
	})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "rating", Head: userBRating, Tails: []hypergraph.NodeID{userB}, Weight: 1})
	return b.Build()
}

// buildBuyerSellerH: Query.order -> (Order,A) with sibling fields buyer/seller BOTH descending to the
// single shared (User,A); User.id (A) keys an EntityJump into (User,B) where rating lives.
func buildBuyerSellerH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qOrder := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "order", Subgraph: 1})
	orderA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Order", Subgraph: 1})
	buyer := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Order", Field: "buyer", Subgraph: 1})
	seller := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Order", Field: "seller", Subgraph: 1})
	userA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "User", Subgraph: 1})
	userAID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "User", Field: "id", Subgraph: 1})
	userB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "User", Subgraph: 2})
	userBRating := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "User", Field: "rating", Subgraph: 2})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "order", Head: qOrder, Tails: []hypergraph.NodeID{r}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: orderA, Tails: []hypergraph.NodeID{qOrder}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "buyer", Head: buyer, Tails: []hypergraph.NodeID{orderA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "seller", Head: seller, Tails: []hypergraph.NodeID{orderA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{buyer}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{seller}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: userAID, Tails: []hypergraph.NodeID{userA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{
		Kind: hypergraph.EdgeEntityJump, Head: userB, Tails: []hypergraph.NodeID{userAID},
		KeyTails: []hypergraph.NodeID{userAID}, Weight: 5,
	})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "rating", Head: userBRating, Tails: []hypergraph.NodeID{userB}, Weight: 1})
	return b.Build()
}

// buildSelfRefFriendsH: (User,A) carries a `friends` field whose Descent returns to (User,A) itself.
func buildSelfRefFriendsH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qOrder := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "order", Subgraph: 1})
	orderA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Order", Subgraph: 1})
	buyer := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Order", Field: "buyer", Subgraph: 1})
	userA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "User", Subgraph: 1})
	friends := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "User", Field: "friends", Subgraph: 1})
	userID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "User", Field: "id", Subgraph: 1})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "order", Head: qOrder, Tails: []hypergraph.NodeID{r}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: orderA, Tails: []hypergraph.NodeID{qOrder}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "buyer", Head: buyer, Tails: []hypergraph.NodeID{orderA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{buyer}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "friends", Head: friends, Tails: []hypergraph.NodeID{userA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{friends}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: userID, Tails: []hypergraph.NodeID{userA}, Weight: 1})
	return b.Build()
}
