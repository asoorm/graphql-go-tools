package search

// nonresolvable_test.go pins the D10 fall-back NARROWING guard (FORMAL_SPEC D10 amendment --
// provable-non-resolvability narrowing) BOTH ways:
//
//   - a goal whose every candidate is provably non-resolvable (all heading keys resolvable:false,
//     root-only entry -- the audit non-resolvable-interface-object/case-02 shape) must NOT be
//     rescued by the fall-back: Search fails loud with ErrNoValidPlan;
//   - the SAME shape with a resolvable key (the class-A/B missing-jump premise intact), a shape
//     with no key metadata at all (the frozen-witness foreign-root shape), or a shape with a
//     non-root structural entry keeps the fall-back, byte-identical -- the conservative direction
//     that protects every resolvable customer shape.

import (
	"errors"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

// buildNonResolvableInterfaceObjectH mirrors audit non-resolvable-interface-object/case-02:
// subgraph a (1) resolves Query.a: Node with only Node.id; subgraph b (2) resolves Query.b: Node
// with Node.id and Node.field -- and b's Node is an @interfaceObject whose ONLY key is
// @key(id, resolvable: false), so NO entity jump into (Node,b) exists and none may be modelled.
// The key-head metadata carries exactly that: a's Node key is resolvable (interface Node @key),
// b's is not (unless the test flips resolvableInB to model the class-A/B premise instead).
func buildNonResolvableInterfaceObjectH(t *testing.T, resolvableInB bool) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "a")
	b.SetSubgraphName(2, "b")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})

	// subgraph a: Query.a -> Node { id }
	qA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "a", Subgraph: 1})
	nodeA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Node", Subgraph: 1})
	nodeAID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Node", Field: "id", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: qA, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: nodeA, Tails: []hypergraph.NodeID{qA}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: nodeAID, Tails: []hypergraph.NodeID{nodeA}, Weight: 1})

	// subgraph b: Query.b -> Node { id field } -- field lives ONLY here.
	qB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "b", Subgraph: 2})
	nodeB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Node", Subgraph: 2})
	nodeBID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Node", Field: "id", Subgraph: 2})
	nodeBField := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Node", Field: "field", Subgraph: 2})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: qB, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: nodeB, Tails: []hypergraph.NodeID{qB}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: nodeBID, Tails: []hypergraph.NodeID{nodeB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "field", Head: nodeBField, Tails: []hypergraph.NodeID{nodeB}, Weight: 1})

	// NO EntityJump a->b: b's key is resolvable:false (or, in the resolvableInB variant, a modelled
	// jump is MISSING -- the class-A/B gap shape the guard must leave alone).
	b.MarkKeyHead(1, "Node", true) // a: interface Node @key(id) -- resolvable
	b.MarkKeyHead(2, "Node", resolvableInB)
	return b.Build()
}

const nonResolvableSchema = `schema { query: Query } type Query { a: Node b: Node } interface Node { id: ID field: String }`

// TestSearchProvablyNonResolvableFailsLoud: the case-02 shape must NOT be rescued into a wrong
// plan by the root-pin fall-back -- Search returns ErrNoValidPlan (the honest reject the audit
// expects), with the narrowing reason.
func TestSearchProvablyNonResolvableFailsLoud(t *testing.T) {
	h := buildNonResolvableInterfaceObjectH(t, false)
	o := buildObligations(t, h, nonResolvableSchema, `{ a { field } }`)
	res, err := Search(h, o, defaultCfg())
	if err == nil {
		t.Fatalf("provably non-resolvable goal must fail loud, got a plan (cover %v, fallbacks %+v)",
			res.Cover.Selected, res.RouteFallbacks)
	}
	var nvp *ErrNoValidPlan
	if !errors.As(err, &nvp) {
		t.Fatalf("want *ErrNoValidPlan, got %T: %v", err, err)
	}
	if !strings.Contains(nvp.Reason, "non-resolvable") {
		t.Fatalf("want the provably-non-resolvable reason, got %q", nvp.Reason)
	}
}

// TestSearchFallbackKeptWhenKeyResolvable: the SAME shape with b's Node key resolvable is the
// class-A/B premise (a jump COULD be modelled; the model merely lacks it) -- the fall-back must
// keep firing, typed and loud, and the plan must be produced (completeness, honest scope 1).
func TestSearchFallbackKeptWhenKeyResolvable(t *testing.T) {
	h := buildNonResolvableInterfaceObjectH(t, true)
	o := buildObligations(t, h, nonResolvableSchema, `{ a { field } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("resolvable-key shape must keep the completeness fall-back, got %v", err)
	}
	if len(res.RouteFallbacks) == 0 {
		t.Fatal("the fall-back route must be recorded (typed-loud contract)")
	}
	for _, f := range res.RouteFallbacks {
		if f.Coordinate != "Node.field" {
			t.Errorf("want fallback coordinate Node.field, got %q", f.Coordinate)
		}
	}
}

// TestSearchFallbackKeptWithoutKeyMetadata: the frozen-witness foreign-root shape (a graph with NO
// key-head metadata -- union-interface-distributed/case-01's Node.id via Query.node) proves
// nothing, so the fall-back must keep firing exactly as before the narrowing.
func TestSearchFallbackKeptWithoutKeyMetadata(t *testing.T) {
	h := buildForeignRootOnlyH(t)
	o := buildObligations(t, h,
		`schema { query: Query } type Query { products: Item node: Item } type Item { id: ID }`,
		`{ products { id } }`)
	res, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("metadata-free shape must keep the completeness fall-back, got %v", err)
	}
	if len(res.RouteFallbacks) == 0 {
		t.Fatal("the fall-back must fire (nothing is proven without key metadata)")
	}
}

// TestPredicateStructuralGuards pins condition 2 directly: a candidate whose object is entered by
// anything besides a root-anchored Descent -- a Descent from a NON-root field (a parent an eventual
// jump could feed) or an abstract TypeMove -- is NOT proven, even with the key metadata present.
func TestPredicateStructuralGuards(t *testing.T) {
	t.Run("descent from a non-root field keeps the fall-back", func(t *testing.T) {
		b := hypergraph.NewBuilder()
		b.SetSubgraphName(1, "s")
		r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
		qParent := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "parent", Subgraph: 1})
		parent := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Parent", Subgraph: 1})
		child := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Parent", Field: "child", Subgraph: 1})
		childObj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Child", Subgraph: 1})
		secret := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Child", Field: "secret", Subgraph: 1})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "parent", Head: qParent, Tails: []hypergraph.NodeID{r}, Weight: 1000})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: parent, Tails: []hypergraph.NodeID{qParent}, Weight: 0})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "child", Head: child, Tails: []hypergraph.NodeID{parent}, Weight: 1})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: childObj, Tails: []hypergraph.NodeID{child}, Weight: 0})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "secret", Head: secret, Tails: []hypergraph.NodeID{childObj}, Weight: 1})
		b.MarkKeyHead(1, "Child", false) // Child's only key is resolvable:false ...
		h := b.Build()
		// ... but (Child,s) is entered by a Descent from the NON-root field Parent.child: a jump
		// into Parent could feed it, so nothing is proven.
		if provablyNonResolvable(h, []hypergraph.NodeID{secret}) {
			t.Fatal("a candidate behind a non-root descent must NOT be proven non-resolvable")
		}
	})

	t.Run("TypeMove entry keeps the fall-back", func(t *testing.T) {
		b := hypergraph.NewBuilder()
		b.SetSubgraphName(1, "s")
		r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
		qU := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "u", Subgraph: 1})
		u := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "U", Subgraph: 1})
		member := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "M", Subgraph: 1})
		mf := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "M", Field: "f", Subgraph: 1})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "u", Head: qU, Tails: []hypergraph.NodeID{r}, Weight: 1000})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: u, Tails: []hypergraph.NodeID{qU}, Weight: 0})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "M", Head: member, Tails: []hypergraph.NodeID{u}, Weight: 1, Members: []string{"M"}})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "f", Head: mf, Tails: []hypergraph.NodeID{member}, Weight: 1})
		b.MarkKeyHead(1, "M", false)
		h := b.Build()
		if provablyNonResolvable(h, []hypergraph.NodeID{mf}) {
			t.Fatal("a candidate behind a TypeMove entry must NOT be proven non-resolvable")
		}
	})
}
