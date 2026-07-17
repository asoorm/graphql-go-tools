package search

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

func defaultCfg() Config {
	return Config{Combine: Sum, PreflightCap: 1 << 30, StateCap: 1 << 20}
}

// TestSearchPartialUnionReturnsCover pins the Section 7.1 canonical result -- "single subgraph A, single
// fetch = w_f + action + TypeMove + c = 1003" -- as the STABLE post-D6 whole-cover cost.
//
// With D6 member-narrowing active (obligation.Build classifies at its tail -- narrowing is on by
// default), OnlyA/OnlyB fall outside Intersect_s Mem_s(Action) = {Common} and become response-only nulls
// (Cover.Nulls), so the cover no longer enters subgraph B for OnlyB.b: it collapses to the
// single-subgraph-A single fetch of Section 7.1, whose folded cost is exactly 1003 (the whole-cover figure
// the pre-D6 Task-6 placeholder recorded as the transient 2008). The isolated Common.c
// sub-assertions (its tree-pi and its own folded traceback) are D6-independent -- Common is covered
// either way -- and are kept.
func TestSearchPartialUnionReturnsCover(t *testing.T) {
	h := buildPartialUnionH(t)
	o := buildPartialUnionObligations(t, h)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatal(err)
	}
	if res.Cover.Cost != 1003 {
		t.Fatalf("post-D6 stable cover cost want 1003 got %d", res.Cover.Cost)
	}

	// Section 7.1 canonical single-subgraph-A single fetch, isolated to Common.c.
	gCommon := goalFor(t, o, "Common", "c")
	vCommon := res.Cover.Selected[gCommon]
	if res.Pi[vCommon] != 1003 {
		t.Fatalf("Common.c tree-pi want 1003 got %d", res.Pi[vCommon])
	}
	commonWalk := Traceback(h, res.Back, vCommon, map[hypergraph.NodeID]bool{})
	if c := coverCost(h, commonWalk); c != 1003 {
		t.Fatalf("Section 7.1 folded cover for Common.c want 1003 got %d", c)
	}
	// The C.4 tie-break picks subgraph A over B (lexicographic) for the shared Common.c node.
	if sg := h.SubgraphName(h.Node(vCommon).Subgraph); sg != "A" {
		t.Fatalf("Common.c must resolve via subgraph A (C.4 tie-break), got %q", sg)
	}
}

// TestSearchEntityJumpFoldedCost2018 is the Section 7.2 hand computation: tree-pi(shippingEstimate)=6020 but
// the realized folded cover cost is C(K) = 2w_f + 8w_s + w_d = 2018 (C.3, P1: folded <= tree; gap 4002).
func TestSearchEntityJumpFoldedCost2018(t *testing.T) {
	h := buildEntityJumpH(t)
	o := buildEntityJumpObligations(t, h)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatal(err)
	}
	if res.Cover.Cost != 2018 {
		t.Fatalf("folded C(K) want 2018 got %d", res.Cover.Cost)
	}
	if pi := res.Pi[res.Cover.Selected[goalFor(t, o, "Product", "shippingEstimate")]]; pi != 6020 {
		t.Fatalf("tree-pi want 6020 got %d", pi)
	}
}

// TestSearchUnreachableGoalReturnsErrNoValidPlan: a goal whose sole candidate node is unreachable
// (its only derivation is a resolvable:false-style self-cyclic entity jump, D7) must surface a typed
// *ErrNoValidPlan naming that obligation with Reason "unreachable" -- never a resource guard, never a
// degraded plan (I2).
func TestSearchUnreachableGoalReturnsErrNoValidPlan(t *testing.T) {
	h, o := buildUnsatisfiableInstance(t)
	_, err := Search(h, o, defaultCfg())
	nvp, ok := err.(*ErrNoValidPlan)
	if !ok {
		t.Fatalf("want *ErrNoValidPlan, got %v", err)
	}
	if nvp.Reason != "unreachable" {
		t.Fatalf("reason want unreachable got %q", nvp.Reason)
	}
}

// TestPreflightTooLargeTrips: the L19 pre-flight structural bound rejects before any SETTLE allocation
// with a typed *ErrPlanTooLarge.
func TestPreflightTooLargeTrips(t *testing.T) {
	h := buildEntityJumpH(t)
	o := buildEntityJumpObligations(t, h)
	_, err := Search(h, o, Config{Combine: Sum, PreflightCap: 1, StateCap: 1 << 20})
	if _, ok := err.(*ErrPlanTooLarge); !ok {
		t.Fatalf("want *ErrPlanTooLarge, got %v", err)
	}
}

// TestSearchStateCapTrips: the L7 in-search backstop surfaces a typed *ErrSearchStateCap through
// Search (not just settle) -- never a silently truncated cover.
func TestSearchStateCapTrips(t *testing.T) {
	h := buildEntityJumpH(t)
	o := buildEntityJumpObligations(t, h)
	_, err := Search(h, o, Config{Combine: Sum, PreflightCap: 1 << 30, StateCap: 1})
	if _, ok := err.(*ErrSearchStateCap); !ok {
		t.Fatalf("want *ErrSearchStateCap, got %v", err)
	}
}

// TestSearchConditionedEntityJumpApplicabilityFilter exercises the D7/F5 pre-settle edge pruning
// (the resolved OPEN DESIGN ITEM). The goal `estimate` is reachable ONLY through a conditioned
// EntityJump into (Product,B) whose KeyCondition names coordinate "Product.sku". When the operation
// selects `sku`, the coordinate is present in O(Q), the jump is applicable, and the goal is covered;
// when the operation omits `sku`, the coordinate is absent, the jump is masked out before SETTLE, and
// the goal is correctly unreachable -> *ErrNoValidPlan.
func TestSearchConditionedEntityJumpApplicabilityFilter(t *testing.T) {
	const schema = `schema { query: Query } type Query { product: Product } type Product { sku: ID estimate: Float }`

	t.Run("condition satisfied -> jump applicable, goal covered", func(t *testing.T) {
		h := buildConditionedJumpH(t)
		o := buildObligations(t, h, schema, `{ product { sku estimate } }`)
		res, err := Search(h, o, defaultCfg())
		if err != nil {
			t.Fatalf("applicable conditioned jump must plan, got %v", err)
		}
		g := goalFor(t, o, "Product", "estimate")
		if _, covered := res.Cover.Selected[g]; !covered {
			t.Fatalf("estimate must be covered when Product.sku is selected")
		}
	})

	t.Run("condition unsatisfied -> jump masked, goal unreachable", func(t *testing.T) {
		h := buildConditionedJumpH(t)
		o := buildObligations(t, h, schema, `{ product { estimate } }`)
		_, err := Search(h, o, defaultCfg())
		nvp, ok := err.(*ErrNoValidPlan)
		if !ok {
			t.Fatalf("inapplicable conditioned jump must ErrNoValidPlan, got %v", err)
		}
		if got := o.Ob(nvp.Obligation); got.Type != "Product" || got.Field != "estimate" {
			t.Fatalf("named obligation want Product.estimate, got %s.%s", got.Type, got.Field)
		}
	})
}

// TestSearchPathConsistencyAvoidsForeignRoot is the focused D10 path-consistency regression: a
// two-root schema where BOTH Query.products and Query.node return the same type Item, so the object
// node (Item,A) is shared. Query.node is the globally-CHEAPEST entry (w=10 vs 1000), so the unmasked
// settle's back-pointer for (Item,A) enters via `node` -- the wrong route for the operation
// `{ products { id } }`, which never requested `node`. The cover MUST enter via `products` and MUST
// NOT contain the `node` edge (the wrong-route defect this closes). Without the masked per-goal settle
// the emitted walk exits through `node`; the test would then fail on the usedNode assertion.
func TestSearchPathConsistencyAvoidsForeignRoot(t *testing.T) {
	h := buildTwoRootSharedTypeH(t)
	o := buildObligations(t, h,
		`schema { query: Query } type Query { products: Item node: Item } type Item { id: ID }`,
		`{ products { id } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("path-consistent cover must plan, got %v", err)
	}
	var usedProducts, usedNode bool
	for _, e := range res.Cover.Edges {
		switch h.Edge(e).Label {
		case "products":
			usedProducts = true
		case "node":
			usedNode = true
		}
	}
	if !usedProducts {
		t.Fatal("cover must enter via the requested root Query.products")
	}
	if usedNode {
		t.Fatal("cover must NOT enter via the unrequested root Query.node -- wrong-route covering (D10)")
	}
}

// buildTwoRootSharedTypeH hand-builds the two-root shared-type H described on
// TestSearchPathConsistencyAvoidsForeignRoot: Query.products (w=1000) and Query.node (w=10) both
// descend to the SAME object node (Item,A); Query.node is deliberately the cheaper entry so the
// unmasked settle would route the `id` goal through it.
func buildTwoRootSharedTypeH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qProducts := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "products", Subgraph: 1})
	qNode := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "node", Subgraph: 1})
	item := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Item", Subgraph: 1})
	itemID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Item", Field: "id", Subgraph: 1})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "products", Head: qProducts, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "node", Head: qNode, Tails: []hypergraph.NodeID{r}, Weight: 10}) // cheaper foreign root
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: item, Tails: []hypergraph.NodeID{qProducts}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: item, Tails: []hypergraph.NodeID{qNode}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: itemID, Tails: []hypergraph.NodeID{item}, Weight: 1})
	return b.Build()
}

// TestScopedWalksSiblingConflation is the SEARCH half of the sibling-conflation fix (D10 cover-as-a-
// function, ADVERSARIAL_REVIEW demand 1). On the buyer/seller witness -- `order { buyer { rating }
// seller { rating } }`, both goals resolving to the single (User,b).rating through the shared (User,a)
// -- the folded Cover.Edges keeps only ONE route into (User,a) (via `buyer`), dropping `seller`. The
// per-goal scoped walks Cover.Walks MUST recover BOTH: kappa(buyer.rating) enters via `buyer` and NOT
// `seller`; kappa(seller.rating) enters via `seller` and NOT `buyer`. This asserts the additivity too:
// the folded Edges set is unchanged (still the buyer-only route).
func TestScopedWalksSiblingConflation(t *testing.T) {
	h := buildBuyerSellerH(t)
	o := buildObligations(t, h,
		`schema { query: Query } type Query { order: Order } type Order { id: ID buyer: User seller: User } type User { id: ID rating: Int }`,
		`{ order { buyer { rating } seller { rating } } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("buyer/seller cover must plan, got %v", err)
	}

	// Two distinct goals (buyer.rating, seller.rating) that fold onto the same selected node.
	var buyerG, sellerG obligation.GoalID
	var found int
	for _, g := range o.Goals() {
		ob := o.Ob(g)
		if ob.Type != "User" || ob.Field != "rating" {
			continue
		}
		pc, ok := parentCoordFor(o, g)
		if !ok {
			t.Fatalf("goal %d has no field parent", g)
		}
		switch pc {
		case "Order.buyer":
			buyerG, found = g, found+1
		case "Order.seller":
			sellerG, found = g, found+1
		}
	}
	if found != 2 {
		t.Fatalf("want two rating goals under buyer/seller, matched %d", found)
	}
	if res.Cover.Selected[buyerG] != res.Cover.Selected[sellerG] {
		t.Fatal("witness precondition: both rating goals must fold onto the SAME (User,b).rating node")
	}

	labelSet := func(walk []hypergraph.EdgeID) map[string]bool {
		m := map[string]bool{}
		for _, e := range walk {
			m[h.Edge(e).Label] = true
		}
		return m
	}
	bWalk := labelSet(res.Cover.Walks[buyerG])
	sWalk := labelSet(res.Cover.Walks[sellerG])
	if !bWalk["buyer"] || bWalk["seller"] {
		t.Fatalf("kappa(buyer.rating) must enter via `buyer` and not `seller`; got %v", bWalk)
	}
	if !sWalk["seller"] || sWalk["buyer"] {
		t.Fatalf("kappa(seller.rating) must enter via `seller` and not `buyer`; got %v", sWalk)
	}
	// Both walks must reach the shared rating leaf (kappa is a complete covering walk for its goal).
	if !bWalk["rating"] || !sWalk["rating"] {
		t.Fatalf("both scoped walks must cover the rating leaf; buyer=%v seller=%v", bWalk, sWalk)
	}

	// Additivity: the folded Cover.Edges is unchanged -- the shared-visited fold still keeps only the
	// buyer route (seller dropped), exactly as before this change.
	edgeLabels := labelSet(res.Cover.Edges)
	if !edgeLabels["buyer"] || edgeLabels["seller"] {
		t.Fatalf("folded Cover.Edges must be the unchanged buyer-only route; got %v", edgeLabels)
	}
}

// parentCoordFor exposes immediateParentCoord for the test (same package).
func parentCoordFor(o *obligation.Tree, g obligation.GoalID) (string, bool) {
	return immediateParentCoord(o, g)
}

// TestScopedWalksAncestorDivergence pins the parent-CHAIN scope of siblingEdges(g): the sibling
// divergence sits TWO hops above the goal -- `order { buyer { pets { name } } seller { pets { name } } }`
// diverges at the User level while the goals live at the Pet level. An immediate-parent-only mask sees
// only routes into Pet (`User.pets`, shared, nothing to mask) and would let both kappa's fold through the
// globally-cheapest User entry; the chain mask must still split them: kappa(buyer.pets.name) enters via
// `buyer` and NOT `seller`, and vice versa.
func TestScopedWalksAncestorDivergence(t *testing.T) {
	h := buildBuyerSellerPetsH(t)
	o := buildObligations(t, h,
		`schema { query: Query } type Query { order: Order } type Order { id: ID buyer: User seller: User } type User { id: ID pets: Pet } type Pet { name: String }`,
		`{ order { buyer { pets { name } } seller { pets { name } } } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("buyer/seller pets cover must plan, got %v", err)
	}

	var buyerG, sellerG obligation.GoalID
	var found int
	for _, g := range o.Goals() {
		ob := o.Ob(g)
		if ob.Type != "Pet" || ob.Field != "name" {
			continue
		}
		// Distinguish the two Pet.name goals by their GRANDPARENT (buyer vs seller) coordinate.
		gp := grandparentCoordFor(t, o, g)
		switch gp {
		case "Order.buyer":
			buyerG, found = g, found+1
		case "Order.seller":
			sellerG, found = g, found+1
		}
	}
	if found != 2 {
		t.Fatalf("want two Pet.name goals under buyer/seller, matched %d", found)
	}
	if res.Cover.Selected[buyerG] != res.Cover.Selected[sellerG] {
		t.Fatal("witness precondition: both Pet.name goals must fold onto the SAME (Pet,A).name node")
	}

	labelSet := func(walk []hypergraph.EdgeID) map[string]bool {
		m := map[string]bool{}
		for _, e := range walk {
			m[h.Edge(e).Label] = true
		}
		return m
	}
	bWalk := labelSet(res.Cover.Walks[buyerG])
	sWalk := labelSet(res.Cover.Walks[sellerG])
	if !bWalk["buyer"] || bWalk["seller"] {
		t.Fatalf("kappa(buyer.pets.name) must enter via `buyer` and not `seller` (chain scope); got %v", bWalk)
	}
	if !sWalk["seller"] || sWalk["buyer"] {
		t.Fatalf("kappa(seller.pets.name) must enter via `seller` and not `buyer` (chain scope); got %v", sWalk)
	}
	if !bWalk["pets"] || !bWalk["name"] || !sWalk["pets"] || !sWalk["name"] {
		t.Fatalf("both scoped walks must cover pets/name; buyer=%v seller=%v", bWalk, sWalk)
	}
}

// TestScopedWalksSelfReferentialTerminates guards the self-referential chain: `order { buyer { friends
// { friends { id } } } }` visits object type User under TWO on-chain coordinates (Order.buyer and
// User.friends) -- chainCoords must keep BOTH (masking a chain edge would sever the goal and trip the
// fallback) and the walk over the finite obligation chain must terminate. kappa here still collapses the
// friends hops onto the shortest derivation of (User,a).id -- per-POSITION placement of a repeated node
// is D11 obligation-driven lowering's job (it walks O(Q), not kappa) -- so the assertion is termination +
// a complete covering walk, not positional structure.
func TestScopedWalksSelfReferentialTerminates(t *testing.T) {
	h := buildSelfRefFriendsH(t)
	o := buildObligations(t, h,
		`schema { query: Query } type Query { order: Order } type Order { id: ID buyer: User } type User { id: ID friends: [User] }`,
		`{ order { buyer { friends { friends { id } } } } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("self-referential cover must plan, got %v", err)
	}
	var idG obligation.GoalID
	var found bool
	for _, g := range o.Goals() {
		if ob := o.Ob(g); ob.Type == "User" && ob.Field == "id" {
			idG, found = g, true
		}
	}
	if !found {
		t.Fatal("no User.id goal")
	}
	walk := res.Cover.Walks[idG]
	if len(walk) == 0 {
		t.Fatal("kappa(friends.friends.id) must be a non-empty covering walk")
	}
	labels := map[string]bool{}
	for _, e := range walk {
		labels[h.Edge(e).Label] = true
	}
	if !labels["id"] || !labels["buyer"] || !labels["order"] {
		t.Fatalf("kappa must be a complete root-anchored walk covering the id leaf; got %v", labels)
	}
}

// grandparentCoordFor returns the "Type.field" coordinate of goal g's grandparent Field obligation.
func grandparentCoordFor(t *testing.T, o *obligation.Tree, g obligation.GoalID) string {
	t.Helper()
	obs := o.Obligations()
	ob := o.Ob(g)
	for hop := 0; hop < 2; hop++ {
		if ob.Parent == obligation.NoParent || ob.Parent == ob.ID {
			t.Fatalf("goal %d chain too short at hop %d", g, hop)
		}
		ob = obs[ob.Parent]
	}
	return ob.Type + "." + ob.Field
}

// buildBuyerSellerPetsH extends the buyer/seller witness one level: buyer/seller both descend to the
// shared (User,A), whose `pets` field descends to the shared (Pet,A) carrying `name`. The divergence
// (buyer vs seller) is at the User level; the goals are at the Pet level.
func buildBuyerSellerPetsH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qOrder := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "order", Subgraph: 1})
	orderA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Order", Subgraph: 1})
	buyer := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Order", Field: "buyer", Subgraph: 1})
	seller := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Order", Field: "seller", Subgraph: 1})
	userA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "User", Subgraph: 1})
	pets := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "User", Field: "pets", Subgraph: 1})
	petA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Pet", Subgraph: 1})
	petName := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Pet", Field: "name", Subgraph: 1})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "order", Head: qOrder, Tails: []hypergraph.NodeID{r}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: orderA, Tails: []hypergraph.NodeID{qOrder}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "buyer", Head: buyer, Tails: []hypergraph.NodeID{orderA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "seller", Head: seller, Tails: []hypergraph.NodeID{orderA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{buyer}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{seller}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "pets", Head: pets, Tails: []hypergraph.NodeID{userA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: petA, Tails: []hypergraph.NodeID{pets}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "name", Head: petName, Tails: []hypergraph.NodeID{petA}, Weight: 1})
	return b.Build()
}

// buildSelfRefFriendsH hand-builds the self-referential witness: (User,A) carries a `friends` field
// whose Descent returns to (User,A) itself -- the D4 collapse that makes every friends level one node.
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
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: userA, Tails: []hypergraph.NodeID{friends}, Weight: 0}) // self-referential descent
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: userID, Tails: []hypergraph.NodeID{userA}, Weight: 1})
	return b.Build()
}

// buildBuyerSellerH hand-builds the sibling-conflation witness hypergraph: Query.order -> (Order,A) with
// sibling fields buyer/seller BOTH descending to the single shared (User,A); User.id (A) keys an
// EntityJump into (User,B) where rating lives. Both `buyer { rating }` and `seller { rating }` therefore
// fold onto the one (User,B).rating goal node through the one shared (User,A).
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

// --- fixtures & helpers ------------------------------------------------------------------

// buildObligations parses+normalizes an operation against a base schema and builds O(Q) over h.
func buildObligations(t *testing.T, h *hypergraph.Hypergraph, schema, op string) *obligation.Tree {
	t.Helper()
	opDoc := unsafeparser.ParseGraphqlDocumentString(op)
	defDoc := unsafeparser.ParseGraphqlDocumentStringWithBaseSchema(schema)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
	).NormalizeOperation(&opDoc, &defDoc, report)
	if report.HasErrors() {
		t.Fatalf("normalize operation: %s", report.Error())
	}
	tree, err := obligation.Build(&opDoc, &defDoc, "", h)
	if err != nil {
		t.Fatalf("build obligation tree: %v", err)
	}
	return tree
}

func buildPartialUnionObligations(t *testing.T, h *hypergraph.Hypergraph) *obligation.Tree {
	t.Helper()
	return buildPartialUnionTree(t, h) // defined in settle_test.go
}

func buildEntityJumpObligations(t *testing.T, h *hypergraph.Hypergraph) *obligation.Tree {
	t.Helper()
	const schema = `
schema { query: Query }
type Query { product: Product }
type Product { id: ID! organization: Organization dimensions: Dimensions shippingEstimate: Float }
type Organization { id: ID! }
type Dimensions { length: Float width: Float height: Float }
`
	return buildObligations(t, h, schema, `{ product { shippingEstimate } }`)
}

// buildConditionedJumpH hand-builds an H where the goal field (Product,B).estimate is reachable only
// via a conditioned EntityJump: enter A -> (Product,A).sku -> EntityJump{Conditions: Product.sku} ->
// (Product,B) -> estimate. sku itself is resolvable in A, so it is never the unreachable goal.
func buildConditionedJumpH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qp := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "product", Subgraph: 1})
	pa := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Product", Subgraph: 1})
	psku := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Product", Field: "sku", Subgraph: 1})
	pb := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Product", Subgraph: 2})
	pest := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Product", Field: "estimate", Subgraph: 2})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "product", Head: qp, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: pa, Tails: []hypergraph.NodeID{qp}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "sku", Head: psku, Tails: []hypergraph.NodeID{pa}, Weight: 1})
	b.AddEdge(hypergraph.Edge{
		Kind: hypergraph.EdgeEntityJump, Head: pb, Tails: []hypergraph.NodeID{psku}, Weight: 1010,
		Conditions: []hypergraph.KeyCondition{{Coordinates: []string{"Product.sku"}}},
	})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "estimate", Head: pest, Tails: []hypergraph.NodeID{pb}, Weight: 1})
	return b.Build()
}

// buildUnsatisfiableInstance builds an H whose sole goal <Query.thing> has one candidate node that is
// reachable only through a self-cyclic EntityJump (its own head is its own tail) -- the search-model
// stand-in for a @key(resolvable: false) severance (D7): need[e] never reaches zero, the head stays
// pi=Inf, and SEARCH surfaces ErrNoValidPlan.
func buildUnsatisfiableInstance(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	qthing := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "thing", Subgraph: 1})
	// self-cyclic jump: never settles, keeps the node in E so Build does not prune it.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: qthing, Tails: []hypergraph.NodeID{qthing}, Weight: 10})
	h := b.Build()
	o := buildObligations(t, h, `schema { query: Query } type Query { thing: Int }`, `{ thing }`)
	return h, o
}

// goalFor locates the goal whose obligation resolves field `field` on type `typ`.
func goalFor(t *testing.T, o *obligation.Tree, typ, field string) obligation.GoalID {
	t.Helper()
	for _, g := range o.Goals() {
		ob := o.Ob(g)
		if ob.Type == typ && ob.Field == field {
			return g
		}
	}
	t.Fatalf("no goal for %s.%s", typ, field)
	return 0
}
