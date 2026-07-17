package search

// opscope_test.go -- the operation-scoped mode's test obligations (FORMAL_SPEC Section 6.5; PROOFS Section 8):
//
//   - the CONTAINMENT oracle (S1): every edge the default mode's plan uses -- cover, per-goal
//     walks, spines, fall-back routes -- lies inside the computed scope, checked independently of
//     the scoped search path itself;
//   - the TABLE-AGREEMENT oracle (S2): a raw settle over the induced sub-graph agrees with the
//     whole-graph settle at every scope node, pi and back-edge both;
//   - the DUAL-MODE extension of the brute-force optimality oracle (I3): every property-test
//     instance (the 240-seed reachable / severed / small families -- the same 720 instances the
//     T1/T2/T3 obligations sweep) runs in BOTH modes and must produce identical plans (selections,
//     edges, walks, spines, costs, nulls, fall-back records) and identical typed errors; on the
//     small family both modes' pi must equal the independent brute-force minimum.
//
// SettleStats are deliberately NOT compared: the scoped mode settles strictly fewer states (Section 6.5
// "what may legitimately differ").

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// scopedCfg is defaultCfg with the operation-scoped mode selected.
func scopedCfg() Config {
	cfg := defaultCfg()
	cfg.OperationScoped = true
	return cfg
}

// TestOpScope_ContainmentOracle (S1): the default mode's emitted plan must lie entirely inside the
// Section 6.5 scope. This checks the scope computation against the SHIPPING plan rather than against the
// scoped search path, so a reachability bug cannot hide behind its own consumer.
func TestOpScope_ContainmentOracle(t *testing.T) {
	for _, s := range seeds() {
		for _, gen := range []func(*testing.T, int64) (*hypergraph.Hypergraph, *obligation.Tree){
			genReachableInstance, genSmallInstance,
		} {
			h, o := gen(t, s)
			res, err := Search(h, o, defaultCfg())
			if err != nil {
				continue // typed refusals carry no plan to contain
			}
			sc := computeOpScope(h, o)
			inScope := func(e hypergraph.EdgeID) bool { return sc.subEdge[e] >= 0 }
			for _, e := range res.Cover.Edges {
				if !inScope(e) {
					t.Fatalf("seed %d: cover edge %d outside the scope (S1 violated)", s, e)
				}
			}
			for g, w := range res.Cover.Walks {
				for _, e := range w {
					if !inScope(e) {
						t.Fatalf("seed %d goal %d: walk edge %d outside the scope (S1 violated)", s, g, e)
					}
				}
			}
			for g, sp := range res.Cover.Spines {
				for _, e := range sp {
					if !inScope(e) {
						t.Fatalf("seed %d goal %d: spine edge %d outside the scope (S1 violated)", s, g, e)
					}
				}
			}
			for _, f := range res.RouteFallbacks {
				for _, e := range f.Route {
					if !inScope(e) {
						t.Fatalf("seed %d goal %d: fallback route edge %d outside the scope (S1 violated)", s, f.Goal, e)
					}
				}
			}
			for _, g := range o.Goals() {
				for _, v := range o.Cand(g) {
					if sc.subNode[v] < 0 {
						t.Fatalf("seed %d goal %d: candidate node %d not seeded into the scope", s, g, v)
					}
				}
			}
		}
	}
}

// TestOpScope_TableAgreementOracle (S2): a raw settle over the induced sub-graph must agree with
// the whole-graph settle at every scope node -- pi exactly, and the back-edge mapped through the id
// translation. Checked on the unmasked table (masks only remove edges; the corpus equality gates
// cover the masked tables end-to-end).
func TestOpScope_TableAgreementOracle(t *testing.T) {
	for _, s := range seeds() {
		h, o := genReachableInstance(t, s)
		fullPi, fullBack, _, err := settle(h, defaultCfg())
		if err != nil {
			continue
		}
		sc := computeOpScope(h, o)
		subPi, subBack, _, err := settle(sc.sub, defaultCfg())
		if err != nil {
			t.Fatalf("seed %d: sub settle: %v", s, err)
		}
		for sv, ov := range sc.origNode {
			if subPi[sv] != fullPi[ov] {
				t.Fatalf("seed %d: node %d (sub %d): scoped pi=%d != full pi=%d (S2 violated)",
					s, ov, sv, subPi[sv], fullPi[ov])
			}
			subE, fullE := subBack[sv], fullBack[ov]
			switch {
			case subE == hypergraph.NoEdge && fullE == hypergraph.NoEdge:
			case subE == hypergraph.NoEdge || fullE == hypergraph.NoEdge:
				t.Fatalf("seed %d: node %d: back-edge presence differs (sub=%v full=%v)", s, ov, subE, fullE)
			case sc.origEdge[subE] != fullE:
				t.Fatalf("seed %d: node %d: scoped back=%d(orig %d) != full back=%d (S2 violated)",
					s, ov, subE, sc.origEdge[subE], fullE)
			}
		}
	}
}

// TestOpScoped_DualModeOracle extends the I3 brute-force oracle across the property-test instance
// families: every instance runs in BOTH modes; plans (trees) and costs must be identical, typed
// errors must be identical, and on the small family both modes' pi must equal the independent
// brute-force minimum tree cost.
func TestOpScoped_DualModeOracle(t *testing.T) {
	for _, s := range seeds() {
		// Reachable family (T1/T2's seeded instances).
		h, o := genReachableInstance(t, s)
		resU, errU := Search(h, o, defaultCfg())
		resS, errS := Search(h, o, scopedCfg())
		assertModeEquality(t, s, "reachable", resU, errU, resS, errS)

		// Severed family (T2's unreachable-goal instances): the typed error must be identical.
		h2, o2, _ := genSeveredInstance(t, s)
		resU2, errU2 := Search(h2, o2, defaultCfg())
		resS2, errS2 := Search(h2, o2, scopedCfg())
		assertModeEquality(t, s, "severed", resU2, errU2, resS2, errS2)

		// Small family (T3's oracle instances): both modes must match the brute-force optimum.
		h3, o3 := genSmallInstance(t, s)
		resU3, errU3 := Search(h3, o3, defaultCfg())
		resS3, errS3 := Search(h3, o3, scopedCfg())
		assertModeEquality(t, s, "small", resU3, errU3, resS3, errS3)
		if errU3 == nil {
			for _, g := range o3.Goals() {
				v, covered := resU3.Cover.Selected[g]
				if !covered {
					continue
				}
				bf := bruteForceMinTreeCost(h3, Sum, o3.Cand(g))
				if got := resU3.Pi[v]; got != bf {
					t.Fatalf("seed %d goal %d: unscoped pi=%d != brute-force=%d", s, g, got, bf)
				}
				if got := resS3.Pi[v]; got != bf {
					t.Fatalf("seed %d goal %d: SCOPED pi=%d != brute-force=%d (I3 broken in scoped mode)", s, g, got, bf)
				}
			}
		}
	}
}

// TestOpScoped_DualModeScaledInstance runs the dual-mode equality on the larger generated
// instances the scaling benchmark uses (|E| ~ 500/2000), so the equality evidence is not limited
// to <=12-edge property instances.
func TestOpScoped_DualModeScaledInstance(t *testing.T) {
	for _, size := range []int{500, 2000} {
		h, o := buildScalingInstance(t, 42, size)
		resU, errU := Search(h, o, defaultCfg())
		resS, errS := Search(h, o, scopedCfg())
		assertModeEquality(t, 42, "scaled", resU, errU, resS, errS)
	}
}

// TestOpScoped_ConcurrentSearches pins the scoped mode's concurrency contract: one immutable
// graph, many concurrent scoped Searches (the memoized support index publishes via
// hypergraph.Memo's CAS; everything else is per-call). Meaningful under -race, which the full
// suite runs.
func TestOpScoped_ConcurrentSearches(t *testing.T) {
	h, o := buildScalingInstance(t, 42, 500)
	want, err := Search(h, o, defaultCfg())
	if err != nil {
		t.Fatalf("unscoped baseline: %v", err)
	}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			res, err := Search(h, o, scopedCfg())
			if err != nil {
				done <- err
				return
			}
			if res.Cover.Cost != want.Cover.Cost || len(res.Cover.Edges) != len(want.Cover.Edges) {
				done <- fmt.Errorf("concurrent scoped result diverged: cost %d vs %d", res.Cover.Cost, want.Cover.Cost)
				return
			}
			done <- nil
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// assertModeEquality asserts everything plan-observable is identical between the two modes:
// error class and text; cover cost, edges, selections, nulls; per-goal walks and spines; the D10
// fall-back records verbatim; and pi at every selected node. Stats are exempt (Section 6.5).
func assertModeEquality(t *testing.T, seed int64, family string, resU *Result, errU error, resS *Result, errS error) {
	t.Helper()
	if (errU == nil) != (errS == nil) {
		t.Fatalf("seed %d %s: mode error divergence -- unscoped=%v scoped=%v", seed, family, errU, errS)
	}
	if errU != nil {
		if errU.Error() != errS.Error() {
			t.Fatalf("seed %d %s: different typed errors: %q vs %q", seed, family, errU, errS)
		}
		return
	}
	cu, cs := resU.Cover, resS.Cover
	if cu.Cost != cs.Cost {
		t.Fatalf("seed %d %s: cost differs: unscoped=%d scoped=%d", seed, family, cu.Cost, cs.Cost)
	}
	if !reflect.DeepEqual(cu.Edges, cs.Edges) {
		t.Fatalf("seed %d %s: cover edges differ:\n unscoped=%v\n scoped=%v", seed, family, cu.Edges, cs.Edges)
	}
	if !reflect.DeepEqual(cu.Selected, cs.Selected) {
		t.Fatalf("seed %d %s: selections differ:\n unscoped=%v\n scoped=%v", seed, family, cu.Selected, cs.Selected)
	}
	if !reflect.DeepEqual(cu.Nulls, cs.Nulls) {
		t.Fatalf("seed %d %s: nulls differ: %v vs %v", seed, family, cu.Nulls, cs.Nulls)
	}
	if !reflect.DeepEqual(cu.Walks, cs.Walks) {
		t.Fatalf("seed %d %s: walks differ:\n unscoped=%v\n scoped=%v", seed, family, cu.Walks, cs.Walks)
	}
	if !reflect.DeepEqual(cu.Spines, cs.Spines) {
		t.Fatalf("seed %d %s: spines differ:\n unscoped=%v\n scoped=%v", seed, family, cu.Spines, cs.Spines)
	}
	if !reflect.DeepEqual(resU.RouteFallbacks, resS.RouteFallbacks) {
		t.Fatalf("seed %d %s: D10 fall-back records differ (register must be mode-identical):\n unscoped=%+v\n scoped=%+v",
			seed, family, resU.RouteFallbacks, resS.RouteFallbacks)
	}
	for g, v := range cu.Selected {
		if resU.Pi[v] != resS.Pi[v] {
			t.Fatalf("seed %d %s goal %d: pi at selected node %d differs: %d vs %d",
				seed, family, g, v, resU.Pi[v], resS.Pi[v])
		}
	}
}
