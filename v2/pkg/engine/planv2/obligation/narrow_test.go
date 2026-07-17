package obligation

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
)

// TestClassifyNarrowing_PartialUnion is the D6 intersection rule at obligation granularity (Section 7.1):
// with Mem_A(Action) = {Common, OnlyA} and Mem_B(Action) = {Common, OnlyB}, the intersection over
// P = {A, B} (both resolve the parent field Wrapper.action) is {Common}. Common is coverable (not
// exempt); OnlyA and OnlyB, both value types outside the intersection, are exempt (D6-narrowed ->
// response-only nulls). Build activates classification at its tail (narrowing is on by default);
// ClassifyNarrowing stays exported for re-classification and is idempotent.
func TestClassifyNarrowing_PartialUnion(t *testing.T) {
	h := buildPartialUnionH(t)
	op, def := parseOp(t, partialUnionSchema, partialUnionOperation)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}

	assertVerdicts := func() {
		t.Helper()
		if tree.Exempt(goalFor(t, tree, "Common", "c")) {
			t.Fatal("Common.c is in Intersect_s Mem_s(Action) = {Common} and must NOT be exempt")
		}
		if !tree.Exempt(goalFor(t, tree, "OnlyA", "a")) {
			t.Fatal("OnlyA.a is a value type outside the intersection and must be exempt (D6)")
		}
		if !tree.Exempt(goalFor(t, tree, "OnlyB", "b")) {
			t.Fatal("OnlyB.b is a value type outside the intersection and must be exempt (D6)")
		}
	}

	assertVerdicts() // active straight out of Build (default activation)

	tree.ClassifyNarrowing(h) // exported re-classification is idempotent
	assertVerdicts()
}

// TestExempt_NilSafeWithoutClassification pins the low-level contract: a Tree built without going
// through Build (no classification run) reports false for every goal -- Exempt is nil-safe, never a
// panic, and "no verdict" means "no narrowing" (the conservative direction: goals stay cover
// requirements).
func TestExempt_NilSafeWithoutClassification(t *testing.T) {
	tree := &Tree{
		obligations: []Obligation{{ID: 0, Kind: Field, Parent: 0, Type: "Query", Field: "f"}},
		goalOb:      []ObID{0},
	}
	if tree.Exempt(GoalID(0)) {
		t.Fatal("Exempt must be false when classification has not run")
	}
	if tree.Exempt(GoalID(99)) {
		t.Fatal("Exempt must be false (not a panic) for out-of-range goals")
	}
}

// entityUnionSchema/entityUnionOp is a partial union whose members include an ENTITY (Ent, with a
// D7 EntityJump in H). PROOFS T2's precondition ("all members of U value types") then forbids
// narrowing entirely, even for the value member Val that lies outside the intersection.
const entityUnionSchema = `
schema { query: Query }
type Query { thing: Thing }
union Thing = Ent | Val
type Ent { id: ID! ev: String }
type Val { vv: String }
`

const entityUnionOp = `{ thing { __typename ... on Ent { ev } ... on Val { vv } } }`

// TestClassifyNarrowing_EntityMembersNeverNarrow pins the T2 entity gate: with Mem_A(Thing) = {Ent}
// and Mem_B(Thing) = {Ent, Val}, the pure intersection is {Ent}, so Val lies outside it. But Ent is
// an entity (a D7 edge has head (Ent,*)), so NO member of Thing is exempt -- the entity/value
// distinction falls out of "does a D7 edge exist" (D6 note), never a heuristic.
func TestClassifyNarrowing_EntityMembersNeverNarrow(t *testing.T) {
	h := buildEntityMemberUnionH(t)
	op, def := parseOp(t, entityUnionSchema, entityUnionOp)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}

	if tree.Exempt(goalFor(t, tree, "Val", "vv")) {
		t.Fatal("Val.vv must NOT be exempt: Thing has an entity member (T2 precondition)")
	}
	if tree.Exempt(goalFor(t, tree, "Ent", "ev")) {
		t.Fatal("Ent.ev must NOT be exempt: entity members are individually reachable via D7")
	}
}

// nestedRefinementSchema/nestedRefinementOp nest an interface refinement inside an interface
// refinement: <Query.thing> -> <I1 |> I2> -> <I2 |> C2> -> <C2.leaf>.
const nestedRefinementSchema = `
schema { query: Query }
type Query { thing: I1 }
interface I1 { x: String }
interface I2 implements I1 { x: String y: String }
type C2 implements I1 & I2 { x: String y: String leaf: String }
type COther implements I1 { x: String }
`

const nestedRefinementOp = `{ thing { ... on I2 { ... on C2 { leaf } } } }`

// TestClassifyNarrowing_NestedRefinementRestrictsP is the review counterexample for nested
// abstract-in-abstract narrowing (FORMAL_SPEC D6, nested-refinements paragraph). Both A and B
// resolve the outer field Query.thing, but only A can produce the intervening context I2
// (I2 in Mem_A(I1); I2 not in Mem_B(I1) -- B's I1 instances are only ever COther). B-origin parents can
// therefore never reach the inner refinement <I2 |> C2>, so P for that refinement is restricted to
// {A}, where C2 in Mem_A(I2) -- C2.leaf must be COVERED. Intersecting Mem_s(I2) over the unrestricted
// {A, B} would see Mem_B(I2) = {} and wrongly null it.
func TestClassifyNarrowing_NestedRefinementRestrictsP(t *testing.T) {
	h := buildNestedRefinementH(t)
	op, def := parseOp(t, nestedRefinementSchema, nestedRefinementOp)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}

	// Guard the shape: the nested chain must survive normalization, or this test is vacuous.
	findRefine(t, tree.Obligations(), "I1", "I2")
	findRefine(t, tree.Obligations(), "I2", "C2")

	if tree.Exempt(goalFor(t, tree, "C2", "leaf")) {
		t.Fatal("C2.leaf must be COVERED: P is restricted to subgraphs producing the intervening I2 context (D6 nested rule)")
	}
}

// routeScopeSchema/routeScopeOp mirror the audit's partial-union/case-02: a @shareable abstract
// parent field whose second producer subgraph is UNREACHABLE (no root, non-entity parent object).
const routeScopeSchema = `
schema { query: Query }
type Query { getResponse: Response }
type Response { actions: [Action!]! }
union Action = Alpha | Beta
type Alpha { x: String }
type Beta { name: String }
`

const routeScopeOp = `{ getResponse { actions { __typename ... on Beta { name } } } }`

// TestClassifyNarrowing_RouteScopedP is the D6 route-scoping counterexample (ADVERSARIAL_REVIEW
// demand-3b). Subgraph B declares Response.actions (@shareable) but (Response,B) is an ORPHAN node in
// H -- B has no Query root and Response is not an entity, so nothing produces it. The SCHEMA-scoped
// P(<Action|>Beta>) = {A, B} narrows Intersect_s Mem_s(Action) = {Alpha} and wrongly exempts Beta; but the
// only route that yields a Response instance runs through A alone (which resolves Beta), so the
// ROUTE-scoped P = {A} keeps Intersect = {Alpha, Beta} and Beta must be COVERED. Optimistic H-reachability
// (the over-approximation) removes only nodes with NO producing path, so an unreachable partial
// producer can never contribute its narrower member set.
func TestClassifyNarrowing_RouteScopedP(t *testing.T) {
	h, err := hypergraph.Build(hgtestdata.PartialUnionUnreachableParentConfig(), hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query"},
	})
	if err != nil {
		t.Fatalf("build route-scope H: %v", err)
	}
	op, def := parseOp(t, routeScopeSchema, routeScopeOp)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Exempt(goalFor(t, tree, "Beta", "name")) {
		t.Fatal("Beta.name must be COVERED: subgraph B's (Response,B) is unreachable, so route-scoped P = {A} where Beta in Mem_A(Action)")
	}
}

// TestPromoteExemptTerminalGoal_D3DoublePrime pins D3pp: when a composite's ENTIRE field-subtree is
// D6 member-narrowed (every selected member exempt), the composite gains a coverage goal so the
// router still resolves it (surviving members' __typename) instead of dropping the whole subtree.
// Operation selects ONLY OnlyA and OnlyB -- both value-type members outside Intersect Mem_s(Action) = {Common}
// -- so `Wrapper.action` has no non-exempt field descendant and must be promoted. Common (the
// in-intersection member) is deliberately NOT selected, which is what makes `action` all-narrowed.
func TestPromoteExemptTerminalGoal_D3DoublePrime(t *testing.T) {
	h := buildPartialUnionH(t)
	const onlyExemptMembers = `{ wrapper { action { __typename ... on OnlyA { a } ... on OnlyB { b } } } }`
	op, def := parseOp(t, partialUnionSchema, onlyExemptMembers)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}
	// OnlyA/OnlyB stay exempt (D6 value-type narrowing).
	if !tree.Exempt(goalFor(t, tree, "OnlyA", "a")) || !tree.Exempt(goalFor(t, tree, "OnlyB", "b")) {
		t.Fatal("OnlyA.a and OnlyB.b must remain exempt (D6)")
	}
	// The all-narrowed composite Wrapper.action is promoted to a NON-exempt coverage goal.
	g := goalFor(t, tree, "Wrapper", "action")
	if tree.Exempt(g) {
		t.Fatal("promoted composite goal Wrapper.action must NOT be exempt (it is the typename-terminal cover)")
	}
	if len(tree.Cand(g)) == 0 {
		t.Fatal("promoted Wrapper.action goal must have candidate field-resolution nodes")
	}
}

// interfaceRefinementSchema/Op mirror InterfaceRefinementConfig: a mixin interface Detail refined
// under a Node-returning root, resolved on the concrete entity member Article.
const interfaceRefinementSchema = `
schema { query: Query }
type Query { item: Node }
interface Node { id: ID! }
interface Detail { title: String }
type Article implements Node & Detail { id: ID! title: String }
`

const interfaceRefinementOp = `{ item { ... on Detail { title } } }`

// TestExpandInterfaceRefinementGoal_D3TriplePrime pins D3ppp: a Field goal <Detail.title> whose owner
// interface Detail is never returned directly ((Detail,s) orphan, primary cand unreachable) gets its
// cand augmented with the reachable concrete member node (Article,s).title, turning a hard
// ErrNoValidPlan into a coverable goal. The goal is NOT exempt (Article is a real entity, so the D6
// entity gate blocks narrowing) -- this is the routing-gap class the fallback targets.
func TestExpandInterfaceRefinementGoal_D3TriplePrime(t *testing.T) {
	h, err := hypergraph.Build(hgtestdata.InterfaceRefinementConfig(), hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query"},
	})
	if err != nil {
		t.Fatalf("build interface-refinement H: %v", err)
	}
	op, def := parseOp(t, interfaceRefinementSchema, interfaceRefinementOp)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}
	g := goalFor(t, tree, "Detail", "title")
	if tree.Exempt(g) {
		t.Fatal("Detail.title must not be exempt (Article is an entity member; the D6 gate blocks narrowing)")
	}
	// cand must now include a concrete member field node (Article.title), and it must be reachable.
	reach := optimisticReachable(h, true)
	sawMember, memberReachable := false, false
	for _, c := range tree.Cand(g) {
		n := h.Node(c)
		if n.Type == "Article" && n.Field == "title" {
			sawMember = true
			if int(c) < len(reach) && reach[c] {
				memberReachable = true
			}
		}
	}
	if !sawMember {
		t.Fatal("D3ppp must augment cand(<Detail.title>) with the concrete member node (Article,s).title")
	}
	if !memberReachable {
		t.Fatal("the augmented member node must be reachable (coverable) -- else the fallback is inert")
	}
}

// TestClassifyNarrowing_ResolvableFalseKeyIsValueType pins both halves of the resolvable:false
// fixture (D7: @key(resolvable: false) emits no EntityJump): with the OnlyA key resolvable, a B->A
// D7 jump into (OnlyA,A) exists, OnlyA is an entity, and the T2 gate blocks ALL narrowing on
// Action; with resolvable:false as the ONLY key, no jump exists, OnlyA is a value type, and the
// exclusive members OnlyA/OnlyB narrow exactly as in the keyless partial union.
func TestClassifyNarrowing_ResolvableFalseKeyIsValueType(t *testing.T) {
	build := func(t *testing.T, resolvable bool) (*hypergraph.Hypergraph, *Tree) {
		t.Helper()
		h, err := hypergraph.Build(hgtestdata.PartialUnionEntityKeyConfig(resolvable), hypergraph.BuildConfig{
			Weights:  hypergraph.DefaultWeights(),
			RootType: map[string]string{"query": "Query"},
		})
		if err != nil {
			t.Fatalf("build entity-key H: %v", err)
		}
		op, def := parseOp(t, partialUnionSchema, partialUnionOperation)
		tree, err := Build(op, def, "", h)
		if err != nil {
			t.Fatal(err)
		}
		return h, tree
	}

	t.Run("resolvable key -> D7 jump exists, entity gate blocks narrowing", func(t *testing.T) {
		h, tree := build(t, true)
		if !hasEntityJumpHead(h, "OnlyA") {
			t.Fatal("fixture: a resolvable OnlyA key must emit a D7 jump into (OnlyA,A)")
		}
		if tree.Exempt(goalFor(t, tree, "OnlyA", "a")) {
			t.Fatal("OnlyA.a must NOT be exempt: OnlyA is an entity (D7 edge exists)")
		}
		if tree.Exempt(goalFor(t, tree, "OnlyB", "b")) {
			t.Fatal("OnlyB.b must NOT be exempt: Action has an entity member (T2 precondition)")
		}
	})

	t.Run("resolvable:false only -> no jump, value type, narrowed", func(t *testing.T) {
		h, tree := build(t, false)
		if hasEntityJumpHead(h, "OnlyA") {
			t.Fatal("fixture: @key(resolvable:false) must emit NO D7 jump (D7)")
		}
		if !tree.Exempt(goalFor(t, tree, "OnlyA", "a")) {
			t.Fatal("OnlyA.a must be exempt: its only key is resolvable:false, so it is a value type for D6")
		}
		if !tree.Exempt(goalFor(t, tree, "OnlyB", "b")) {
			t.Fatal("OnlyB.b must be exempt: outside the intersection, no entity member remains")
		}
		if tree.Exempt(goalFor(t, tree, "Common", "c")) {
			t.Fatal("Common.c is in the intersection and must NOT be exempt")
		}
	})
}

// hasEntityJumpHead reports whether any D7 EntityJump edge in H has a head node of the given type.
func hasEntityJumpHead(h *hypergraph.Hypergraph, typ string) bool {
	for e := 0; e < h.NumEdges(); e++ {
		edge := h.Edge(hypergraph.EdgeID(e))
		if edge.Kind == hypergraph.EdgeEntityJump && h.Node(edge.Head).Type == typ {
			return true
		}
	}
	return false
}

// buildEntityMemberUnionH hand-builds an H for the entity-gate test: Query.thing resolvable in both
// A and B (P = {A, B}); Mem_A(Thing) = {Ent}, Mem_B(Thing) = {Ent, Val}; and a D7 EntityJump whose
// head is (Ent, B) marks Ent as an entity type.
func buildEntityMemberUnionH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})

	// Parent field Query.thing resolvable in both subgraphs -> P(g) = {A, B}.
	qtA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "thing", Subgraph: 1})
	qtB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "thing", Subgraph: 2})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "thing", Head: qtA, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "thing", Head: qtB, Tails: []hypergraph.NodeID{r}, Weight: 1000})

	tA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Thing", Subgraph: 1})
	tB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Thing", Subgraph: 2})
	entA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Ent", Subgraph: 1})
	entB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Ent", Subgraph: 2})
	valB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Val", Subgraph: 2})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: tA, Tails: []hypergraph.NodeID{qtA}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: tB, Tails: []hypergraph.NodeID{qtB}, Weight: 0})

	// Mem_A(Thing) = {Ent}; Mem_B(Thing) = {Ent, Val}.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Ent", Head: entA, Tails: []hypergraph.NodeID{tA}, Weight: 1, Members: []string{"Ent"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Ent", Head: entB, Tails: []hypergraph.NodeID{tB}, Weight: 1, Members: []string{"Ent", "Val"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Val", Head: valB, Tails: []hypergraph.NodeID{tB}, Weight: 1, Members: []string{"Ent", "Val"}})

	// Ent is an entity: a D7 EntityJump has head (Ent, B) -> entityTypes[Ent] = true.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: entB, Tails: []hypergraph.NodeID{entA}, Weight: 1010})
	return b.Build()
}

// buildNestedRefinementH hand-builds the nested-refinement H: both subgraphs resolve Query.thing;
// A can produce the intervening interface context (I2 in Mem_A(I1)) and refine it to C2
// (C2 in Mem_A(I2)); B knows I1 but its only member is COther -- no I2, so B never originates an
// inner-refinement parent.
func buildNestedRefinementH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})

	qtA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "thing", Subgraph: 1})
	qtB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "thing", Subgraph: 2})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "thing", Head: qtA, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "thing", Head: qtB, Tails: []hypergraph.NodeID{r}, Weight: 1000})

	i1A := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "I1", Subgraph: 1})
	i1B := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "I1", Subgraph: 2})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: i1A, Tails: []hypergraph.NodeID{qtA}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: i1B, Tails: []hypergraph.NodeID{qtB}, Weight: 0})

	// A: Mem_A(I1) = {COther, I2}; Mem_A(I2) = {C2}.
	i2A := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "I2", Subgraph: 1})
	c2A := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "C2", Subgraph: 1})
	leafA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "C2", Field: "leaf", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "I2", Head: i2A, Tails: []hypergraph.NodeID{i1A}, Weight: 1, Members: []string{"COther", "I2"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "C2", Head: c2A, Tails: []hypergraph.NodeID{i2A}, Weight: 1, Members: []string{"C2"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "leaf", Head: leafA, Tails: []hypergraph.NodeID{c2A}, Weight: 1})

	// B: Mem_B(I1) = {COther} -- B lacks I2 entirely.
	coB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "COther", Subgraph: 2})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "COther", Head: coB, Tails: []hypergraph.NodeID{i1B}, Weight: 1, Members: []string{"COther"}})
	return b.Build()
}
