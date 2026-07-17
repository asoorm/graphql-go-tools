package search

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// TestPartialUnion_ExclusiveMembersBecomeNulls is the Section 7.1 end-to-end assertion with D6
// member-narrowing active (obligation.Build classifies at its tail): the value-type union
// `Action = Common | OnlyA` (A) / `Common | OnlyB` (B) plans with NO error (T2 exemption -- a
// narrowed goal is NOT an ErrNoValidPlan), the members outside the intersection
// Intersect_s Mem_s(Action) = {Common} are lowered to response-only nulls, and the intersection member
// Common is covered.
func TestPartialUnion_ExclusiveMembersBecomeNulls(t *testing.T) {
	h := buildPartialUnionH(t)
	o := buildPartialUnionObligations(t, h) // Build's tail runs ClassifyNarrowing (D6 default-on)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("partial union must plan, not error: %v", err) // NOT ErrNoValidPlan (T2 exemption)
	}
	// OnlyA and OnlyB are value-type exclusive members: response-only nulls, not covered.
	if !goalInNulls(t, o, res.Cover.Nulls, "OnlyA", "a") {
		t.Fatal("OnlyA.a must be a response-only null (D6)")
	}
	if !goalInNulls(t, o, res.Cover.Nulls, "OnlyB", "b") {
		t.Fatal("OnlyB.b must be a response-only null (D6)")
	}
	// Common is in the intersection: covered, not null.
	if goalInNulls(t, o, res.Cover.Nulls, "Common", "c") {
		t.Fatal("Common.c is in the intersection and must be covered")
	}
	if _, covered := res.Cover.Selected[goalFor(t, o, "Common", "c")]; !covered {
		t.Fatal("Common.c must be a covered (Selected) goal")
	}
}

// TestPartialUnion_SingleCandidateParentKeepsFullMemberSet is the audit-case-5 regression tripwire
// for the D6 asymmetry: when the parent field is resolvable in exactly ONE subgraph, P(g) is that
// singleton and the intersection Intersect_{s in P} Mem_s(U) is that subgraph's FULL member set -- nothing is
// exempted, and in particular no intersection is taken against subgraphs that know the abstract
// type but can NOT resolve the parent field. Here Wrapper.action exists only in B
// (Mem_B(Action) = {Common, OnlyB}); a distractor subgraph A declares Action with the smaller
// Mem_A(Action) = {Common} but has no (Wrapper,A).action node. Intersecting against A would wrongly
// null OnlyB.b; the D6 P(g) definition keeps both members covered.
func TestPartialUnion_SingleCandidateParentKeepsFullMemberSet(t *testing.T) {
	h := buildSingleCandidateParentH(t)
	const schema = `
schema { query: Query }
type Query { wrapper: Wrapper }
type Wrapper { id: ID! action: Action }
union Action = Common | OnlyB
type Common { c: String }
type OnlyB { b: String }
`
	o := buildObligations(t, h, schema, `{ wrapper { action { __typename ... on Common { c } ... on OnlyB { b } } } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("single-candidate parent must plan: %v", err)
	}
	if len(res.Cover.Nulls) != 0 {
		t.Fatalf("P(g)={B}: nothing may be exempted, got Nulls %v", res.Cover.Nulls)
	}
	for _, tc := range []struct{ typ, field string }{{"Common", "c"}, {"OnlyB", "b"}} {
		if _, covered := res.Cover.Selected[goalFor(t, o, tc.typ, tc.field)]; !covered {
			t.Fatalf("%s.%s must be covered (full member set of the sole capable subgraph)", tc.typ, tc.field)
		}
	}
}

// buildSingleCandidateParentH hand-builds the audit-case-5 H: the parent field Wrapper.action is
// resolvable ONLY in B; distractor subgraph A knows the union Action with a strictly smaller member
// set {Common} but has no (Wrapper,A).action node, so A not in P(g).
func buildSingleCandidateParentH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})

	// B: the sole subgraph resolving the parent chain Query.wrapper -> Wrapper.action.
	qwB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "wrapper", Subgraph: 2})
	wB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrapper", Subgraph: 2})
	waB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrapper", Field: "action", Subgraph: 2})
	aB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Action", Subgraph: 2})
	cB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Common", Subgraph: 2})
	obB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "OnlyB", Subgraph: 2})
	ccB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Common", Field: "c", Subgraph: 2})
	bbB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "OnlyB", Field: "b", Subgraph: 2})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "wrapper", Head: qwB, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: wB, Tails: []hypergraph.NodeID{qwB}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "action", Head: waB, Tails: []hypergraph.NodeID{wB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: aB, Tails: []hypergraph.NodeID{waB}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Common", Head: cB, Tails: []hypergraph.NodeID{aB}, Weight: 1, Members: []string{"Common", "OnlyB"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "OnlyB", Head: obB, Tails: []hypergraph.NodeID{aB}, Weight: 1, Members: []string{"Common", "OnlyB"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "c", Head: ccB, Tails: []hypergraph.NodeID{cB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: bbB, Tails: []hypergraph.NodeID{obB}, Weight: 1})

	// A: distractor -- declares Action with Mem_A = {Common} but does NOT resolve Wrapper.action
	// (no (Wrapper,A).action node), so it must never participate in the intersection.
	aA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Action", Subgraph: 1})
	cA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Common", Subgraph: 1})
	ccA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Common", Field: "c", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Common", Head: cA, Tails: []hypergraph.NodeID{aA}, Weight: 1, Members: []string{"Common"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "c", Head: ccA, Tails: []hypergraph.NodeID{cA}, Weight: 1})
	return b.Build()
}

// goalInNulls reports whether the goal resolving field `field` on type `typ` is in the
// response-only-null set.
func goalInNulls(t *testing.T, o *obligation.Tree, nulls []obligation.GoalID, typ, field string) bool {
	t.Helper()
	for _, g := range nulls {
		ob := o.Ob(g)
		if ob.Type == typ && ob.Field == field {
			return true
		}
	}
	return false
}
