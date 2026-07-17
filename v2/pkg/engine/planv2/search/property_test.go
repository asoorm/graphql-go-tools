package search

// property_test.go carries the three headline PROOFS obligations (T1/I1, T2/I2, T3/I3), the T5
// counting bounds, and the L5 determinism corollary. Per the plan's determinism requirement these run
// on DETERMINISTIC, TABLE-DRIVEN seeds (math/rand seeded from a fixed seed table) rather than an
// external property-testing dependency -- every instance is reproducible from its seed, and the
// bounded shape (<=8 nodes, <=12 edges, <=3 tails/edge) keeps the independent brute-force oracle
// (bruteforce_test.go) feasible.
//
// Instances are ACYCLIC by construction: edges run strictly from lower- to higher-indexed nodes. On a
// DAG every derivation is automatically irredundant (no branch can repeat a label), so SETTLE's
// shortest-B-tree pi and the oracle's min-over-irredundant-derivations coincide exactly (T3.1's splice
// argument is trivially satisfied) -- the I3 equality is then a clean check with no cycle corner cases.
// The obligation tree is a fixed `{ g0 g1 g2 }` operation whose three field goals map, via candFor, to
// whichever `Query.g{i}` field nodes the randomized H contains; only H's edge structure/weights vary.

import (
	"math/rand"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// seeds is the fixed table every property test iterates. Deterministic and reproducible.
func seeds() []int64 {
	out := make([]int64, 0, 240)
	for s := int64(1); s <= 240; s++ {
		out = append(out, s)
	}
	return out
}

// TestI1_SoundnessWalkReplay (PROOFS T1): a walk-replay checker asserts clauses 1-2 on the emitted
// cover -- each edge exists, and the tail-before-head order is realizable. (Clause 3, subgraph-schema
// validation, has no meaning in the pure kernel and is discharged at the audit/differential layer,
// Tasks 10-11.)
func TestI1_SoundnessWalkReplay(t *testing.T) {
	for _, s := range seeds() {
		h, o := genReachableInstance(t, s)
		res, err := Search(h, o, defaultCfg())
		if err != nil {
			continue // typed refusals (ErrPlanTooLarge/StateCap) are not soundness failures
		}
		replayValidWalk(t, s, h, res.Cover)
	}
}

// TestI2_CompletenessSeededAndSevered (PROOFS T2): a construction-seeded cover must yield a plan;
// severing the sole edge to a goal must yield ErrNoValidPlan naming an obligation an INDEPENDENT
// reachability oracle confirms unreachable.
func TestI2_CompletenessSeededAndSevered(t *testing.T) {
	for _, s := range seeds() {
		h, o := genReachableInstance(t, s) // seeded cover: every goal reachable by construction
		if _, err := Search(h, o, defaultCfg()); err != nil {
			t.Fatalf("seed %d: seeded cover must plan, got %v", s, err)
		}

		h2, o2, severed := genSeveredInstance(t, s)
		_, err := Search(h2, o2, defaultCfg())
		nvp, ok := err.(*ErrNoValidPlan)
		if !ok {
			t.Fatalf("seed %d: severed instance must ErrNoValidPlan, got %v", s, err)
		}
		if reachableByOracle(h2, o2, nvp.Obligation) {
			t.Fatalf("seed %d: named obligation %d (%s) is actually reachable", s, nvp.Obligation, severed)
		}
	}
}

// TestI3_TreeOptimalityVsBruteForce (PROOFS T3): for every covered goal, A's pi[v*_g] equals the
// minimum C.2 tree cost over all irredundant derivations, computed by the INDEPENDENT oracle. Also
// measures the tree-vs-folded gap pi-C (measurement, not assertion -- no folded optimality is claimed).
func TestI3_TreeOptimalityVsBruteForce(t *testing.T) {
	for _, s := range seeds() {
		h, o := genSmallInstance(t, s)
		res, err := Search(h, o, defaultCfg())
		if err != nil {
			continue
		}
		for _, g := range o.Goals() {
			v, covered := res.Cover.Selected[g]
			if !covered {
				continue
			}
			bf := bruteForceMinTreeCost(h, Sum, o.Cand(g))
			if got := res.Pi[v]; got != bf {
				t.Fatalf("seed %d goal %d: A pi=%d != brute-force min tree cost=%d", s, g, got, bf)
			}
		}
		recordTreeVsFoldedGap(t, h, res)
	}
}

// TestTreeVsFoldedGapMeasured measures the pi-C(K) gap across the table (C.3 folded <= tree): asserts
// only the direction C(K) <= Sum tree-pi and records the empirical gap; no folded optimality is asserted.
func TestTreeVsFoldedGapMeasured(t *testing.T) {
	var maxGap int64
	for _, s := range seeds() {
		h, o := genSmallInstance(t, s)
		res, err := Search(h, o, defaultCfg())
		if err != nil {
			continue
		}
		var treeSum int64
		for _, g := range o.Goals() {
			if v, covered := res.Cover.Selected[g]; covered {
				treeSum += res.Pi[v]
			}
		}
		if res.Cover.Cost > treeSum {
			t.Fatalf("seed %d: folded C(K)=%d must be <= Sum tree-pi=%d (C.3)", s, res.Cover.Cost, treeSum)
		}
		if gap := treeSum - res.Cover.Cost; gap > maxGap {
			maxGap = gap
		}
	}
	t.Logf("max measured tree-vs-folded gap over %d seeds: %d", len(seeds()), maxGap)
}

// TestT5_PushExtractBounds (PROOFS T5): instrumented counters assert push/extract/state counts <= |E|
// and visited-expansion counts <= |V| (the L4 sharing bound).
func TestT5_PushExtractBounds(t *testing.T) {
	for _, s := range seeds() {
		h, o := genReachableInstance(t, s)
		res, err := Search(h, o, defaultCfg())
		if err != nil {
			continue
		}
		e, v := int64(h.NumEdges()), int64(h.NumNodes())
		if res.Stats.Pushes > e || res.Stats.Extracts > e || res.Stats.States > e {
			t.Fatalf("seed %d: push/extract/state must be <= |E|=%d: %+v", s, e, res.Stats)
		}
		if res.Stats.Visited > v {
			t.Fatalf("seed %d: visited expansions must be <= |V|=%d: %+v (L4 sharing)", s, v, res.Stats)
		}
	}
}

// TestDeterminism_PermutationInvariance (PROOFS T3 / L5): semantically equal inputs in permuted
// builder-insertion order produce identical plans and identical costs.
func TestDeterminism_PermutationInvariance(t *testing.T) {
	for _, s := range seeds() {
		h1, o1 := genReachableInstance(t, s)
		res1, err1 := Search(h1, o1, defaultCfg())

		h2, o2 := genReachableInstancePermuted(t, s) // same semantic instance, reversed edge insertion
		res2, err2 := Search(h2, o2, defaultCfg())

		assertSameOutcome(t, s, res1, err1, res2, err2)
	}
}

// --- deterministic instance generator ----------------------------------------------------

const (
	numGoals  = 3
	maxInterm = 4  // 1 root + <=4 intermediates + 3 goals <= 8 nodes
	maxEdges  = 12 // brute-force / T5 bound
	maxTails  = 3
	goalType  = "Wrap"
	// Goals are nested under a wrapper field so each g{i} is a genuine leaf obligation. A FLAT
	// `{ g0 g1 g2 }` would make g0 obligation-0 (the root sentinel), causing g1/g2 to parent onto it
	// and g0 to be misread as a non-leaf and dropped from G(O) (see obligation/tree.go's
	// root-sentinel note) -- the wrapper avoids that so all three fields are goals.
	goalSchema  = `schema { query: Query } type Query { root: Wrap } type Wrap { g0: Int g1: Int g2: Int }`
	goalOpQuery = `{ root { g0 g1 g2 } }`
)

// edgeSpec is a semantic edge (identity = kind + head + sorted tails); weight is a deterministic
// function of the identity, so the built H is independent of insertion order (permutation-safe).
type edgeSpec struct {
	kind  hypergraph.EdgeKind
	head  hypergraph.NodeID
	tails []hypergraph.NodeID
}

func specWeight(s edgeSpec) int64 {
	var sum int
	for _, t := range s.tails {
		sum += int(t)
	}
	return int64(1 + ((int(s.kind)*7 + int(s.head)*3 + sum) % 5)) // 1..5, deterministic
}

// instancePlan is the deterministic node/edge layout for a seed. r=0, intermediates 1..M (objects),
// goals M+1..M+3 (Query.g{i} field nodes). Acyclic: every edge tail index < head index.
type instancePlan struct {
	numNodes int
	goalNode [numGoals]hypergraph.NodeID
	specs    []edgeSpec
}

func planInstance(seed int64, guarantee bool, forbidden int) instancePlan {
	rng := rand.New(rand.NewSource(seed))
	m := 1 + rng.Intn(maxInterm) // 1..4 intermediates
	numNodes := 1 + m + numGoals
	var p instancePlan
	p.numNodes = numNodes
	for i := 0; i < numGoals; i++ {
		p.goalNode[i] = hypergraph.NodeID(1 + m + i)
	}

	forbid := func(n hypergraph.NodeID) bool {
		return forbidden >= 0 && n == p.goalNode[forbidden]
	}

	// Guaranteed direct root->goal edges (except the severed goal) so a cover exists by construction.
	if guarantee {
		for i := 0; i < numGoals; i++ {
			if forbid(p.goalNode[i]) {
				continue
			}
			p.specs = append(p.specs, edgeSpec{hypergraph.EdgeField, p.goalNode[i], []hypergraph.NodeID{0}})
		}
	}

	budget := maxEdges - len(p.specs)
	nRand := rng.Intn(budget + 1)
	for k := 0; k < nRand; k++ {
		head := hypergraph.NodeID(1 + rng.Intn(numNodes-1)) // never the root
		if forbid(head) {
			continue // keep the severed goal a sink with no derivation
		}
		maxT := int(head)
		if maxT > maxTails {
			maxT = maxTails
		}
		if maxT < 1 {
			continue
		}
		tc := 1 + rng.Intn(maxT)
		// distinct tails strictly below head, none the forbidden goal
		seen := map[hypergraph.NodeID]bool{}
		var tails []hypergraph.NodeID
		for len(tails) < tc {
			cand := hypergraph.NodeID(rng.Intn(int(head)))
			if seen[cand] || forbid(cand) {
				if len(seen) >= int(head) {
					break
				}
				seen[cand] = true
				continue
			}
			seen[cand] = true
			tails = append(tails, cand)
		}
		if len(tails) == 0 {
			continue
		}
		kind := hypergraph.EdgeKind(1 + rng.Intn(4)) // Field/Descent/TypeMove/EntityJump
		p.specs = append(p.specs, edgeSpec{kind, head, tails})
	}
	return p
}

// buildFromPlan assembles the hypergraph, inserting edge specs in the given order (identity-based
// dedup + deterministic Build make the result order-independent).
func buildFromPlan(p instancePlan, order []int) *hypergraph.Hypergraph {
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"}) // id 0
	m := p.numNodes - 1 - numGoals
	for j := 0; j < m; j++ {
		b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: intermTypeName(j), Subgraph: 1})
	}
	for i := 0; i < numGoals; i++ {
		b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: goalType, Field: goalFieldName(i), Subgraph: 1})
	}
	for _, idx := range order {
		s := p.specs[idx]
		b.AddEdge(hypergraph.Edge{Kind: s.kind, Head: s.head, Tails: s.tails, Weight: specWeight(s)})
	}
	return b.Build()
}

func identityOrder(n int) []int {
	o := make([]int, n)
	for i := range o {
		o[i] = i
	}
	return o
}

func reverseOrder(n int) []int {
	o := make([]int, n)
	for i := range o {
		o[i] = n - 1 - i
	}
	return o
}

func intermTypeName(j int) string { return "M" + string(rune('A'+j)) }
func goalFieldName(i int) string  { return "g" + string(rune('0'+i)) }

func genReachableInstance(t *testing.T, seed int64) (*hypergraph.Hypergraph, *obligation.Tree) {
	t.Helper()
	p := planInstance(seed, true, -1)
	h := buildFromPlan(p, identityOrder(len(p.specs)))
	return h, buildObligations(t, h, goalSchema, goalOpQuery)
}

func genReachableInstancePermuted(t *testing.T, seed int64) (*hypergraph.Hypergraph, *obligation.Tree) {
	t.Helper()
	p := planInstance(seed, true, -1)
	h := buildFromPlan(p, reverseOrder(len(p.specs))) // same specs, reversed insertion order
	return h, buildObligations(t, h, goalSchema, goalOpQuery)
}

func genSmallInstance(t *testing.T, seed int64) (*hypergraph.Hypergraph, *obligation.Tree) {
	t.Helper()
	// Guarantee reachability so most goals are covered and actually compared against the oracle; the
	// random extra edges still add competing derivations and shared sub-hyperpaths, so the min-tree
	// comparison and the tree-vs-folded gap stay non-trivial.
	p := planInstance(seed, true, -1)
	h := buildFromPlan(p, identityOrder(len(p.specs)))
	return h, buildObligations(t, h, goalSchema, goalOpQuery)
}

// genSeveredInstance rebuilds the seed's instance with one goal deliberately unreachable (no edge ever
// heads it), the other goals still guaranteed -- the outcome-equivalent of severing that goal's sole
// required edge (I2). Returns the severed goal's field name.
func genSeveredInstance(t *testing.T, seed int64) (*hypergraph.Hypergraph, *obligation.Tree, string) {
	t.Helper()
	sever := int(seed % numGoals)
	p := planInstance(seed, true, sever)
	h := buildFromPlan(p, identityOrder(len(p.specs)))
	return h, buildObligations(t, h, goalSchema, goalOpQuery), goalFieldName(sever)
}

// --- independent oracles & checkers ------------------------------------------------------

// reachableNodes is an INDEPENDENT B-hyperpath reachability fixpoint (a node is reachable iff it is a
// root or some edge has all tails reachable). Uses no settle/cost.go state.
func reachableNodes(h *hypergraph.Hypergraph) map[hypergraph.NodeID]bool {
	reach := map[hypergraph.NodeID]bool{}
	for _, r := range h.Roots() {
		reach[r] = true
	}
	for changed := true; changed; {
		changed = false
		for e := 0; e < h.NumEdges(); e++ {
			edge := h.Edge(hypergraph.EdgeID(e))
			if reach[edge.Head] {
				continue
			}
			all := true
			for _, tl := range edge.Tails {
				if !reach[tl] {
					all = false
					break
				}
			}
			if all {
				reach[edge.Head] = true
				changed = true
			}
		}
	}
	return reach
}

func reachableByOracle(h *hypergraph.Hypergraph, o *obligation.Tree, g obligation.GoalID) bool {
	reach := reachableNodes(h)
	for _, v := range o.Cand(g) {
		if reach[v] {
			return true
		}
	}
	return false
}

// replayValidWalk checks T1 clauses 1-2 on the emitted cover: every edge id is valid, and there is a
// firing order in which each cover edge's tails are all available (roots, or heads of earlier-fired
// cover edges) before its head -- i.e. the folded cover is a realizable B-hyperpath. Each Selected
// node must end up available.
func replayValidWalk(t *testing.T, seed int64, h *hypergraph.Hypergraph, cover *Cover) {
	t.Helper()
	avail := map[hypergraph.NodeID]bool{}
	for _, r := range h.Roots() {
		avail[r] = true
	}
	fired := make([]bool, len(cover.Edges))
	for progress := true; progress; {
		progress = false
		for i, e := range cover.Edges {
			if fired[i] {
				continue
			}
			if int(e) >= h.NumEdges() {
				t.Fatalf("seed %d: cover edge %d does not exist in H", seed, e)
			}
			edge := h.Edge(e)
			ready := true
			for _, tl := range edge.Tails {
				if !avail[tl] {
					ready = false
					break
				}
			}
			if ready {
				fired[i] = true
				avail[edge.Head] = true
				progress = true
			}
		}
	}
	for i, e := range cover.Edges {
		if !fired[i] {
			t.Fatalf("seed %d: cover edge %d not realizable (tails never available) -- unsound walk", seed, e)
		}
	}
	for g, v := range cover.Selected {
		if !avail[v] {
			t.Fatalf("seed %d: selected node %d for goal %d not produced by the cover", seed, v, g)
		}
	}
}

// recordTreeVsFoldedGap is measurement, not assertion (T3): no folded-optimality claim exists.
func recordTreeVsFoldedGap(t *testing.T, h *hypergraph.Hypergraph, res *Result) {
	t.Helper()
	var treeSum int64
	for _, v := range res.Cover.Selected {
		treeSum += res.Pi[v]
	}
	_ = treeSum // gap = treeSum - res.Cover.Cost; aggregated in TestTreeVsFoldedGapMeasured
}

// assertSameOutcome checks the L5 determinism corollary: identical error class and, on success,
// identical folded cost, identical covered-goal selections, and identical edge count.
func assertSameOutcome(t *testing.T, seed int64, res1 *Result, err1 error, res2 *Result, err2 error) {
	t.Helper()
	if (err1 == nil) != (err2 == nil) {
		t.Fatalf("seed %d: determinism broken -- err1=%v err2=%v", seed, err1, err2)
	}
	if err1 != nil {
		if err1.Error() != err2.Error() {
			t.Fatalf("seed %d: different errors under permutation: %q vs %q", seed, err1, err2)
		}
		return
	}
	if res1.Cover.Cost != res2.Cover.Cost {
		t.Fatalf("seed %d: cover cost not permutation-invariant: %d vs %d", seed, res1.Cover.Cost, res2.Cover.Cost)
	}
	if len(res1.Cover.Edges) != len(res2.Cover.Edges) {
		t.Fatalf("seed %d: cover edge count not permutation-invariant: %d vs %d", seed, len(res1.Cover.Edges), len(res2.Cover.Edges))
	}
	if len(res1.Cover.Selected) != len(res2.Cover.Selected) {
		t.Fatalf("seed %d: selected count differs: %d vs %d", seed, len(res1.Cover.Selected), len(res2.Cover.Selected))
	}
	for g, v := range res1.Cover.Selected {
		if res2.Cover.Selected[g] != v {
			t.Fatalf("seed %d: goal %d selected node not invariant: %d vs %d", seed, g, v, res2.Cover.Selected[g])
		}
	}
}
