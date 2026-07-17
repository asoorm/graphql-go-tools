package external

import "testing"

// TestExternalCorpusOpScopedModeEquality is the customer-sweep mode-equality flag for the
// operation-scoped search (FORMAL_SPEC Section 6.5): the coordinator runs it with
// PLANNER_V2_EXTERNAL_CORPUS pointed at the private corpus (same gate and leak rules as
// TestExternalCorpusDifferential -- CI never sets the variable, so this always SKIPs in CI).
// Every (graph, operation) case must plan identically under both modes: byte-identical canonical
// fetch trees and semantic-oracle equivalence, or identical typed refusals.
func TestExternalCorpusOpScopedModeEquality(t *testing.T) {
	dir, ok := externalCorpusDir()
	if !ok {
		t.Skip("PLANNER_V2_EXTERNAL_CORPUS unset; skipping external corpus (leak rule)")
	}
	graphs, err := LoadGraphs(dir)
	if err != nil {
		t.Fatalf("load external corpus: %v", err)
	}
	if len(graphs) == 0 {
		t.Fatal("PLANNER_V2_EXTERNAL_CORPUS set but no graphs discovered -- check EXTERNAL_CORPUS.md")
	}
	var equal, unequal int
	for _, g := range graphs {
		for _, r := range RunGraphModeEquality(g) {
			if r.Equal {
				equal++
				continue
			}
			unequal++
			t.Errorf("mode inequality: %s/%s: %s", r.Graph, r.Operation, r.Detail)
		}
	}
	t.Logf("external op-scoped mode equality: %d equal, %d unequal", equal, unequal)
}

// TestSyntheticCorpusOpScopedModeEquality drives the mode-equality runner end-to-end against the
// committed synthetic corpus fixture (the same pipeline TestSyntheticCorpusPipeline exercises), so
// the customer-sweep flag is proven runnable in CI without touching any private data.
func TestSyntheticCorpusOpScopedModeEquality(t *testing.T) {
	dir := copySyntheticCorpus(t)
	graphs, err := LoadGraphs(dir)
	if err != nil {
		t.Fatalf("load synthetic corpus: %v", err)
	}
	if len(graphs) == 0 {
		t.Fatal("synthetic corpus discovered no graphs")
	}
	var compared int
	for _, g := range graphs {
		for _, r := range RunGraphModeEquality(g) {
			if !r.Equal {
				t.Errorf("mode inequality on synthetic corpus: %s/%s: %s", r.Graph, r.Operation, r.Detail)
				continue
			}
			compared++
		}
	}
	if compared == 0 {
		t.Fatal("no synthetic case compared -- the flag is vacuous")
	}
	t.Logf("synthetic corpus op-scoped mode equality: %d cases equal", compared)
}
