package search

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

func TestCombineSumAndMax(t *testing.T) {
	if got := combine(Sum, []int64{1000, 1, 2}); got != 1003 {
		t.Fatalf("sum want 1003 got %d", got)
	}
	if got := combine(Max, []int64{1000, 1, 2}); got != 1000 {
		t.Fatalf("max want 1000 got %d", got)
	}
	if got := combine(Sum, []int64{Inf, 1}); got != Inf {
		t.Fatalf("Inf must saturate")
	}
	if got := combine(Max, []int64{Inf, 1}); got != Inf {
		t.Fatalf("Inf must saturate under max")
	}
	if got := combine(Sum, nil); got != 0 {
		t.Fatalf("empty combine (root-parented edge) must be 0, got %d", got)
	}
}

func TestLessKeyedOnFNotWeight(t *testing.T) {
	// Two edges: a has larger w(e) but smaller f(e). less must order by f(e) (A-2), not w(e).
	b := hypergraph.NewBuilder()
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "q"})
	x := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "X", Subgraph: 1})
	y := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Y", Subgraph: 1})
	ea := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: x, Tails: []hypergraph.NodeID{r}, Weight: 5})
	eb := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: y, Tails: []hypergraph.NodeID{r}, Weight: 1})
	h := b.Build()
	fa, fb := int64(5), int64(100) // f(ea)=5 < f(eb)=100 even though we could contrive w(ea)>w(eb)
	if !less(h, ea, eb, fa, fb) {
		t.Fatal("less must order by f(e) ascending (A-2), not by w(e)")
	}
	if less(h, eb, ea, fb, fa) {
		t.Fatal("less must be asymmetric on distinct f(e)")
	}
}

func TestCoverCostFoldsEdgesOnce(t *testing.T) {
	b := hypergraph.NewBuilder()
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "q"})
	x := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "X", Subgraph: 1})
	e := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: x, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	h := b.Build()
	// same edge listed twice in the set must be counted once (W2/C.3)
	if got := coverCost(h, []hypergraph.EdgeID{e, e}); got != 1000 {
		t.Fatalf("folded cost want 1000 got %d", got)
	}
}

// TestCompetingDerivationSettlesAtMin mirrors tla/PlannerSearchCompeting.cfg: node fx has two
// incoming derivations -- a direct one at f=1001 and a jump detour at f=2012. The cost layer must
// compute both tentative head values correctly and the C.4 order must pick the minimum (1001), so
// SETTLE's EXTRACT-MIN settles fx at 1001, never 2012 (first-writer-wins would admit 2012). This is
// the R1 guard for the f(e) queue key (PROOFS A-2 / spec C.4 step 1).
func TestCompetingDerivationSettlesAtMin(t *testing.T) {
	b := hypergraph.NewBuilder()
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "q"})
	mid := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Mid", Subgraph: 2})
	fx := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Fx", Subgraph: 1})
	// direct: f = w(1001) + pi[r]=0 = 1001
	eDirect := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "fx", Head: fx, Tails: []hypergraph.NodeID{r}, Weight: 1001})
	// detour: f = w(1012) + pi[mid]=1000 = 2012
	eDetour := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: fx, Tails: []hypergraph.NodeID{mid}, Weight: 1012})
	h := b.Build()

	// pi is settled for the tails (r=0, mid=1000); fx itself is unsettled (its incoming edges are ready).
	pi := make([]int64, h.NumNodes())
	pi[r] = 0
	pi[mid] = 1000

	fDirect := tentativeF(h, eDirect, pi, Sum)
	fDetour := tentativeF(h, eDetour, pi, Sum)
	if fDirect != 1001 {
		t.Fatalf("direct tentative f want 1001 got %d", fDirect)
	}
	if fDetour != 2012 {
		t.Fatalf("detour tentative f want 2012 got %d", fDetour)
	}
	// EXTRACT-MIN pops the smaller f first: the direct derivation settles fx at 1001.
	if !less(h, eDirect, eDetour, fDirect, fDetour) {
		t.Fatal("competing derivation: less must pick the min f(e) (direct 1001 < detour 2012)")
	}
	if less(h, eDetour, eDirect, fDetour, fDirect) {
		t.Fatal("competing derivation: detour must not precede direct")
	}
}

// TestTieBreakDeterministicAndOrderIndependent asserts the C.4 total order breaks an exact f(e) tie
// deterministically (here on the field label, step 4) and -- critically -- that the winner does not
// depend on the order edges were inserted into the builder (the determinism property the permutation
// harness guards). Two Field edges from the same root, equal weight -> equal f; labels "a" < "b".
func TestTieBreakDeterministicAndOrderIndependent(t *testing.T) {
	// build constructs the two competing edges in the given insertion order and returns the
	// hypergraph plus the ids of the "a"- and "b"-labelled edges.
	build := func(aFirst bool) (h *hypergraph.Hypergraph, eA, eB hypergraph.EdgeID) {
		b := hypergraph.NewBuilder()
		r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "q"})
		x := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "X", Subgraph: 1})
		y := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Y", Subgraph: 1})
		mkA := func() {
			eA = b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: x, Tails: []hypergraph.NodeID{r}, Weight: 1000})
		}
		mkB := func() {
			eB = b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: y, Tails: []hypergraph.NodeID{r}, Weight: 1000})
		}
		if aFirst {
			mkA()
			mkB()
		} else {
			mkB()
			mkA()
		}
		return b.Build(), eA, eB
	}

	const f int64 = 1000 // both edges have identical f(e)

	h1, a1, b1 := build(true)
	if !less(h1, a1, b1, f, f) {
		t.Fatal("tie must break to label \"a\" over \"b\" (C.4 step 4)")
	}
	if less(h1, b1, a1, f, f) {
		t.Fatal("tie-break must be antisymmetric: \"b\" must not precede \"a\"")
	}

	// Same semantic input, edges inserted in the opposite order: the winner must be unchanged.
	h2, a2, b2 := build(false)
	if !less(h2, a2, b2, f, f) {
		t.Fatal("tie-break winner must be independent of edge insertion order (determinism)")
	}
	if less(h2, b2, a2, f, f) {
		t.Fatal("tie-break must stay antisymmetric under permuted insertion order")
	}
}
