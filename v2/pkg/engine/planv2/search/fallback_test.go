package search

// fallback_test.go pins the TYPED-LOUD contract of the D10 completeness-preserving fall-back
// (FORMAL_SPEC D10 amendment -- typed-loud fall-back): every firing of either fall-back branch
// (the goal-loop root-pin reversion in search.go, the per-field scoped-walk reversion in scope.go)
// is recorded as a RouteFallback on the Result, and a search that never falls back records none.
// Recording is observability only -- these tests also re-assert the covers are what they were.

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

// buildForeignRootOnlyH is buildTwoRootSharedTypeH MINUS the Descent under the requested root:
// Query.products (w=1000) exists as a root Field edge but leads nowhere, while Query.node (w=10)
// is the ONLY route into (Item,A) and its id -- the foreign-root model-gap shape. A goal under
// `products` is root-pinnable (products IS a modelled root edge) but unreachable on its pinned
// table, so BOTH fall-back branches must fire for it: root-pin in the goal loop, scoped-walk in
// the per-field kappa trace.
func buildForeignRootOnlyH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qProducts := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "products", Subgraph: 1})
	qNode := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "node", Subgraph: 1})
	item := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Item", Subgraph: 1})
	itemID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Item", Field: "id", Subgraph: 1})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "products", Head: qProducts, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "node", Head: qNode, Tails: []hypergraph.NodeID{r}, Weight: 10})
	// NO Descent from qProducts -- the model gap. Only node descends into Item.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: item, Tails: []hypergraph.NodeID{qNode}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: itemID, Tails: []hypergraph.NodeID{item}, Weight: 1})
	return b.Build()
}

// TestSearchRouteFallbackTypedRecord: on the foreign-root-only H, the Item.id goal under
// `products` is reachable ONLY through the unrequested root `node`. The plan must still be
// produced (completeness, honest scope 1) -- and both fall-back firings must now be TYPED records
// on the Result: one root-pin (goal loop) and one scoped-walk (kappa trace), each naming the goal
// coordinate, the anchored root field, the serving node's subgraph, and the foreign route taken.
func TestSearchRouteFallbackTypedRecord(t *testing.T) {
	h := buildForeignRootOnlyH(t)
	o := buildObligations(t, h,
		`schema { query: Query } type Query { products: Item node: Item } type Item { id: ID }`,
		`{ products { id } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("fallback preserves completeness -- the plan must be produced, got %v", err)
	}

	// The plan itself is the pre-amendment one: it enters via the foreign root `node`.
	var usedNode bool
	for _, e := range res.Cover.Edges {
		if h.Edge(e).Label == "node" {
			usedNode = true
		}
	}
	if !usedNode {
		t.Fatal("witness precondition: the cover must route through the foreign root `node`")
	}

	byKind := map[RouteFallbackKind][]RouteFallback{}
	for _, f := range res.RouteFallbacks {
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}
	if len(byKind[RouteFallbackRootPin]) != 1 || len(byKind[RouteFallbackScopedWalk]) != 1 {
		t.Fatalf("want exactly one root-pin and one scoped-walk fallback record, got %+v", res.RouteFallbacks)
	}
	for _, f := range res.RouteFallbacks {
		if f.Coordinate != "Item.id" {
			t.Errorf("%s fallback: want coordinate Item.id, got %q", f.Kind, f.Coordinate)
		}
		if f.RootField != "products" {
			t.Errorf("%s fallback: want root field products, got %q", f.Kind, f.RootField)
		}
		if f.Subgraph != "A" {
			t.Errorf("%s fallback: want subgraph A, got %q", f.Kind, f.Subgraph)
		}
		if f.Node != res.Cover.Selected[f.Goal] {
			t.Errorf("%s fallback: Node must be the goal's selected serving node", f.Kind)
		}
		var routeViaNode bool
		for _, e := range f.Route {
			if h.Edge(e).Label == "node" {
				routeViaNode = true
			}
		}
		if !routeViaNode {
			t.Errorf("%s fallback: Route must be the foreign-root route actually taken (via `node`), got %v", f.Kind, f.Route)
		}
	}
}

// TestSearchNoFallbackRecordsNone: where a path-consistent route exists (the original two-root
// shared-type H), root pinning serves the goal on its pinned table and the scoped kappa trace reaches
// it under its mask -- no fall-back fires, so no RouteFallback may be recorded.
func TestSearchNoFallbackRecordsNone(t *testing.T) {
	h := buildTwoRootSharedTypeH(t)
	o := buildObligations(t, h,
		`schema { query: Query } type Query { products: Item node: Item } type Item { id: ID }`,
		`{ products { id } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("path-consistent cover must plan, got %v", err)
	}
	if len(res.RouteFallbacks) != 0 {
		t.Fatalf("no fall-back fired -- RouteFallbacks must be empty, got %+v", res.RouteFallbacks)
	}
}

// TestSearchNoFallbackOnSiblingScopes: the buyer/seller sibling witness exercises the scoped-walk
// masking heavily (two kappa's split over the same node) but every masked route EXISTS -- the fall-back
// must not fire, so the typed record stays empty. Guards against over-reporting: a mask that
// succeeds is a preference satisfied, not a fallback.
func TestSearchNoFallbackOnSiblingScopes(t *testing.T) {
	h := buildBuyerSellerH(t)
	o := buildObligations(t, h,
		`schema { query: Query } type Query { order: Order } type Order { id: ID buyer: User seller: User } type User { id: ID rating: Int }`,
		`{ order { buyer { rating } seller { rating } } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("buyer/seller cover must plan, got %v", err)
	}
	if len(res.RouteFallbacks) != 0 {
		t.Fatalf("all scoped routes exist -- RouteFallbacks must be empty, got %+v", res.RouteFallbacks)
	}
}
