package search

// mutationpin_test.go -- the mutation-root subgraph pin (FORMAL_SPEC D10 amendment --
// mutation-root subgraph pin; FS-ROOT-6), with its enumerated ORACLE per EXTENDING.md: the pin's
// JOINT per-root-field subgraph choice is a kernel-adjacent decision, so it is checked against
// independent brute-force enumeration over committed mutation-root instances -- for each candidate
// subgraph, the instance is REBUILT with only that subgraph's mutation entrance and every goal's
// minimum tree cost enumerated by bruteForceMinTreeCost (no settle, no masks); the search's chosen
// entrance must be the enumerated argmin, and each goal's pinned settle value must equal the
// enumerated minimum on the chosen single-entrance instance.
//
// The instance is the mutations_3 shape reduced to the kernel: Mutation.submit shareable in A and
// B, payload entity Report with alpha only in A and beta only in B, entity jumps Report@A <-> B
// keyed on Report.id. Per-goal cheapest routing splits (alpha via A's root, beta via B's) -- the
// double-execution; the pin must route BOTH through one entrance.

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// mutPinFixture is one built mutation-root instance plus the handles the assertions need.
type mutPinFixture struct {
	h *hypergraph.Hypergraph
	// entrance edges by subgraph name ("A"/"B"); absent when the build excluded the entrance.
	entrance map[string]hypergraph.EdgeID
	// candidate nodes per goal coordinate, for the brute-force oracle (goal candidates are the
	// field-resolution nodes; alpha lives only in A, beta only in B).
	alphaNode, betaNode hypergraph.NodeID
}

// buildMutationPinH builds the instance. includeA/includeB control which subgraphs' mutation
// entrances exist (the brute-force oracle enumerates the single-entrance variants); withJumps
// controls the Report entity jumps (false = the unpinnable shape); rootKind is "mutation" or
// "query" (the query twin keeps the split legal); wA/wB are the entrance weights.
func buildMutationPinH(t *testing.T, includeA, includeB, withJumps bool, rootKind string, wA, wB int64) mutPinFixture {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	ownerType := "Mutation"
	if rootKind == "query" {
		ownerType = "Query"
	}
	root := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: rootKind})
	fx := mutPinFixture{entrance: map[string]hypergraph.EdgeID{}}

	reportA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Report", Subgraph: 1})
	reportB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Report", Subgraph: 2})
	idA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Report", Field: "id", Subgraph: 1})
	idB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Report", Field: "id", Subgraph: 2})
	alphaA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Report", Field: "alpha", Subgraph: 1})
	betaB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Report", Field: "beta", Subgraph: 2})
	fx.alphaNode, fx.betaNode = alphaA, betaB

	if includeA {
		submitA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: ownerType, Field: "submit", Subgraph: 1})
		fx.entrance["A"] = b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "submit",
			Head: submitA, Tails: []hypergraph.NodeID{root}, Weight: wA})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: reportA, Tails: []hypergraph.NodeID{submitA}, Weight: 0})
	}
	if includeB {
		submitB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: ownerType, Field: "submit", Subgraph: 2})
		fx.entrance["B"] = b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "submit",
			Head: submitB, Tails: []hypergraph.NodeID{root}, Weight: wB})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: reportB, Tails: []hypergraph.NodeID{submitB}, Weight: 0})
	}
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idA, Tails: []hypergraph.NodeID{reportA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idB, Tails: []hypergraph.NodeID{reportB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "alpha", Head: alphaA, Tails: []hypergraph.NodeID{reportA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "beta", Head: betaB, Tails: []hypergraph.NodeID{reportB}, Weight: 1})
	if withJumps {
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: reportB,
			Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 1001, KeySelection: "id"})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: reportA,
			Tails: []hypergraph.NodeID{idB}, KeyTails: []hypergraph.NodeID{idB}, Weight: 1001, KeySelection: "id"})
	}
	fx.h = b.Build()
	return fx
}

const mutPinSchema = `
schema { query: Query mutation: Mutation }
type Query { submit: Report }
type Mutation { submit: Report }
type Report { id: ID alpha: String beta: String }
`

// mutationPinOps builds the obligation tree for the mutation (or query-twin) operation.
func mutationPinObligations(t *testing.T, h *hypergraph.Hypergraph, rootKind string) *obligation.Tree {
	t.Helper()
	op := "mutation { submit { alpha beta } }"
	if rootKind == "query" {
		op = "{ submit { alpha beta } }"
	}
	return buildObligations(t, h, mutPinSchema, op)
}

// coverUsesEntrance reports whether the cover's edge set contains the given entrance edge.
func coverUsesEntrance(c *Cover, e hypergraph.EdgeID) bool {
	for _, ce := range c.Edges {
		if ce == e {
			return true
		}
	}
	return false
}

// bfGoalSum enumerates, on a SINGLE-entrance rebuild of the instance, the brute-force minimum
// tree cost of each goal (alpha, beta) and returns their sum -- the oracle's per-candidate figure.
// Inf-propagating: an unreachable goal yields Inf.
func bfGoalSum(t *testing.T, includeA, includeB bool, wA, wB int64) (alpha, beta, sum int64) {
	t.Helper()
	fx := buildMutationPinH(t, includeA, includeB, true, "mutation", wA, wB)
	alpha = bruteForceMinTreeCost(fx.h, Sum, []hypergraph.NodeID{fx.alphaNode})
	beta = bruteForceMinTreeCost(fx.h, Sum, []hypergraph.NodeID{fx.betaNode})
	if alpha >= Inf || beta >= Inf {
		return alpha, beta, Inf
	}
	return alpha, beta, alpha + beta
}

// TestMutationRootPin_JointChoiceVsBruteForce is the enumerated ORACLE (EXTENDING.md: a change to
// search decisions grows brute-force instances): the pin's chosen entrance must be the argmin of
// the per-candidate enumerated goal-cost sums, and each goal's pinned settle value must equal the
// enumerated minimum on the chosen single-entrance instance. Asymmetric weights make the argmin
// strict in both directions.
func TestMutationRootPin_JointChoiceVsBruteForce(t *testing.T) {
	cases := []struct {
		name   string
		wA, wB int64
		want   string // expected pinned subgraph name
	}{
		{name: "B strictly cheaper", wA: 1000, wB: 900, want: "B"},
		{name: "A strictly cheaper", wA: 900, wB: 1000, want: "A"},
		{name: "tie breaks to name order", wA: 1000, wB: 1000, want: "A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The oracle: enumerate both single-entrance instances independently of the kernel.
			alphaA, betaA, sumA := bfGoalSum(t, true, false, tc.wA, tc.wB)
			alphaB, betaB, sumB := bfGoalSum(t, false, true, tc.wA, tc.wB)
			wantSub := "A"
			wantAlpha, wantBeta := alphaA, betaA
			if sumB < sumA { // ties break to name order, and "A" < "B"
				wantSub, wantAlpha, wantBeta = "B", alphaB, betaB
			}
			if wantSub != tc.want {
				t.Fatalf("oracle disagrees with the case's expectation: enumerated argmin %q, case wants %q (sums A=%d B=%d)",
					wantSub, tc.want, sumA, sumB)
			}

			fx := buildMutationPinH(t, true, true, true, "mutation", tc.wA, tc.wB)
			o := mutationPinObligations(t, fx.h, "mutation")
			res, err := Search(fx.h, o, defaultCfg())
			if err != nil {
				t.Fatal(err)
			}
			chosen, other := fx.entrance[wantSub], fx.entrance[map[string]string{"A": "B", "B": "A"}[wantSub]]
			if !coverUsesEntrance(res.Cover, chosen) {
				t.Fatalf("cover does not use the enumerated-argmin entrance %s (edges %v)", wantSub, res.Cover.Edges)
			}
			if coverUsesEntrance(res.Cover, other) {
				t.Fatalf("cover uses BOTH mutation entrances -- the root field's side effect executes twice (edges %v)", res.Cover.Edges)
			}
			// Per-goal I3 against the enumerated single-entrance minimum.
			gAlpha := goalFor(t, o, "Report", "alpha")
			gBeta := goalFor(t, o, "Report", "beta")
			if got := res.PiFor(gAlpha)[res.Cover.Selected[gAlpha]]; got != wantAlpha {
				t.Fatalf("alpha pinned settle value %d != enumerated minimum %d on the %s-only instance", got, wantAlpha, wantSub)
			}
			if got := res.PiFor(gBeta)[res.Cover.Selected[gBeta]]; got != wantBeta {
				t.Fatalf("beta pinned settle value %d != enumerated minimum %d on the %s-only instance", got, wantBeta, wantSub)
			}
			// Every walk of every goal enters through the chosen entrance and never the other.
			for g, walk := range res.Cover.Walks {
				usesOther := false
				for _, e := range walk {
					if e == other {
						usesOther = true
					}
				}
				if usesOther {
					t.Fatalf("goal %d's walk enters through the non-chosen mutation entrance", g)
				}
			}

			// Dual-mode equality: the operation-scoped mode must make the identical joint choice.
			scoped, err := Search(fx.h, o, Config{Combine: Sum, PreflightCap: 1 << 30, StateCap: 1 << 20, OperationScoped: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(scoped.Cover.Edges) != len(res.Cover.Edges) {
				t.Fatalf("op-scoped cover differs: %v vs %v", scoped.Cover.Edges, res.Cover.Edges)
			}
			for i := range res.Cover.Edges {
				if scoped.Cover.Edges[i] != res.Cover.Edges[i] {
					t.Fatalf("op-scoped cover differs at %d: %v vs %v", i, scoped.Cover.Edges, res.Cover.Edges)
				}
			}
		})
	}
}

// TestMutationRootPin_UnpinnableFailsLoud: without the entity jumps no single subgraph anchors
// both halves, yet BOTH halves are root-reachable -- the pre-pin planner emitted the silently
// double-executing split. The obligated outcome is the typed refusal (FS-ROOT-6 error condition,
// FS-PLAN-6), with the mutation-specific reason.
func TestMutationRootPin_UnpinnableFailsLoud(t *testing.T) {
	fx := buildMutationPinH(t, true, true, false, "mutation", 1000, 1000)
	o := mutationPinObligations(t, fx.h, "mutation")
	_, err := Search(fx.h, o, defaultCfg())
	nvp, ok := err.(*ErrNoValidPlan)
	if !ok {
		t.Fatalf("want *ErrNoValidPlan, got %v", err)
	}
	if !strings.Contains(nvp.Reason, "mutation root field not single-subgraph servable") {
		t.Fatalf("reason %q does not carry the mutation-pin diagnosis", nvp.Reason)
	}
}

// TestMutationRootPin_QueryTwinStillSplits: the SAME shape as a QUERY keeps the split (reads are
// idempotent; FS-ROOT-1 deliberately permits it) -- the pin must not leak into query routing.
func TestMutationRootPin_QueryTwinStillSplits(t *testing.T) {
	fx := buildMutationPinH(t, true, true, false, "query", 1000, 1000)
	o := mutationPinObligations(t, fx.h, "query")
	res, err := Search(fx.h, o, defaultCfg())
	if err != nil {
		t.Fatal(err)
	}
	if !coverUsesEntrance(res.Cover, fx.entrance["A"]) || !coverUsesEntrance(res.Cover, fx.entrance["B"]) {
		t.Fatalf("query twin must keep the per-goal split (both entrances); edges %v", res.Cover.Edges)
	}
}

// TestMutationRootPin_SingleEntranceUntouched: a mutation root field declared in ONE subgraph
// computes no pin (the common path) -- plan identical to the pre-pin behavior.
func TestMutationRootPin_SingleEntranceUntouched(t *testing.T) {
	fx := buildMutationPinH(t, true, false, true, "mutation", 1000, 1000)
	o := mutationPinObligations(t, fx.h, "mutation")
	res, err := Search(fx.h, o, defaultCfg())
	if err != nil {
		t.Fatal(err)
	}
	if !coverUsesEntrance(res.Cover, fx.entrance["A"]) {
		t.Fatalf("single-entrance mutation must plan through its only root; edges %v", res.Cover.Edges)
	}
}
