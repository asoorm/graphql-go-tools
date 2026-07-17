package audit

// gap_test.go extends the search package's TestTreeVsFoldedGapMeasured pattern (property_test.go
// Section 8/P1: folded C(K) <= tree Sum(pi), measurement not assertion beyond that one inequality) to run over
// the REAL audit corpus fixtures instead of only the small synthetic hypergraphs -- the M1 task-13
// "close the tree-vs-folded gap measurement" deliverable.
//
// Reaching search.Result (Pi, Cover) requires driving the planv2 pipeline one level below the
// facade (hypergraph.Build -> obligation.Build -> search.Search), since planv2.Planner.Plan only
// returns the LOWERED plan.Plan, not the intermediate search.Result. This file therefore duplicates
// the facade's three-call pipeline (planv2.go) using this package's own BuildDataSources/
// parseAndNormalize helpers (runner.go) to build the SAME (H, O) pair Run() would plan through, then
// calls search.Search directly.

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// searchConfig mirrors planv2.go's package-level searchConfig var (unexported there, so restated
// here) -- the fixed Algorithm-A budget the facade runs under (Section 6.1/Section 6.3/L7).
var searchConfig = search.Config{Combine: search.Sum, PreflightCap: 1 << 30, StateCap: 1 << 24}

// rootType mirrors planv2.NewPlanner's RootType map (query/mutation -> root type name).
var rootType = map[string]string{"query": "Query", "mutation": "Mutation"}

// TestTreeVsFoldedGapMeasured_AuditCorpus is the audit-corpus companion to
// search.TestTreeVsFoldedGapMeasured: for every audit case planv2 can build (H, O) and Search for --
// which is a SUPERSET of the plan-level PASS/GAP set, since this bypasses lowering (Task 8) and the
// M1 field-argument gap entirely, so it also covers cases runner.go SKIPs for argument-lowering
// reasons -- it asserts ONLY the P1 direction (folded <= tree) and logs the gap distribution (per
// case, plus max/mean across the corpus). No folded-optimality claim is made anywhere in this file.
func TestTreeVsFoldedGapMeasured_AuditCorpus(t *testing.T) {
	cases, err := loadCorpus("testdata")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("empty corpus")
	}

	type gapRow struct {
		suite, name string
		treeSum     int64
		cost        int64
		gap         int64
	}
	var rows []gapRow
	var skipped int

	for _, c := range cases {
		if c.Skip != "" {
			skipped++
			continue
		}
		h, o, ok := buildSearchInputs(t, c)
		if !ok {
			skipped++
			continue
		}
		res, err := search.Search(h, o, searchConfig)
		if err != nil {
			skipped++
			continue
		}

		// PiFor(g), not the bare res.Pi: audit fixtures (unlike the search package's synthetic
		// property-test instances, which are D10-vacuous by construction -- see search.go's
		// buildRootTables doc) exercise D10 path-consistency masking on multi-root operations, so a
		// goal's selected candidate may have been chosen against its MASKED settle table. Using the
		// unmasked res.Pi here would compare the folded cost of the MASKED derivation against the
		// wrong (unmasked, potentially cheaper via a foreign-root shortcut) tree cost and spuriously
		// report folded > tree.
		var treeSum int64
		for _, g := range o.Goals() {
			if v, covered := res.Cover.Selected[g]; covered {
				treeSum += res.PiFor(g)[v]
			}
		}
		if res.Cover.Cost > treeSum {
			t.Fatalf("%s/%s: folded C(K)=%d must be <= tree sum(pi)=%d (P1)", c.Suite, c.Name, res.Cover.Cost, treeSum)
		}
		rows = append(rows, gapRow{
			suite: c.Suite, name: c.Name,
			treeSum: treeSum, cost: res.Cover.Cost, gap: treeSum - res.Cover.Cost,
		})
	}

	if len(rows) == 0 {
		t.Fatal("no audit case yielded a search.Result -- corpus or pipeline changed?")
	}

	var sum, max int64
	for _, r := range rows {
		t.Logf("%-30s tree=%-8d folded=%-8d gap=%-8d", r.suite+"/"+r.name, r.treeSum, r.cost, r.gap)
		sum += r.gap
		if r.gap > max {
			max = r.gap
		}
	}
	mean := float64(sum) / float64(len(rows))
	t.Logf("tree-vs-folded gap over %d audit cases (%d skipped/unplannable at this layer): max=%d mean=%.1f",
		len(rows), skipped, max, mean)
}

// buildSearchInputs drives the facade's pipeline (hypergraph.Build -> obligation.Build) one level
// below planv2.Planner.Plan, using this package's own config/parse helpers so the (H, O) pair is
// built from the SAME inputs Run() plans through. Returns ok=false for any case that cannot reach
// (H, O) -- config errors, normalization errors, or obligation.Build errors -- which is the intended
// wider "unplannable at this layer" set (a superset of runner.go's SKIP reasons); this file only
// measures the gap on cases that DO reach search.Search successfully.
func buildSearchInputs(t *testing.T, c Case) (*hypergraph.Hypergraph, *obligation.Tree, bool) {
	t.Helper()
	dataSources, _, err := BuildDataSources(c)
	if err != nil {
		return nil, nil, false
	}
	h, err := hypergraph.Build(dataSources, hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: rootType,
	})
	if err != nil {
		return nil, nil, false
	}
	op, def, report := parseAndNormalize(c.Definition, c.Operation)
	if report.HasErrors() {
		return nil, nil, false
	}
	o, err := obligation.Build(op, def, "", h)
	if err != nil {
		return nil, nil, false
	}
	return h, o, true
}
