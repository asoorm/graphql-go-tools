package bench

import "testing"

// TestFetchCountNeverExceedsOldPlanner is the M1 acceptance-gate assertion (task brief "M1
// Correctness Bars -- Fetch count"): for every differential-corpus fixture where BOTH planners plan,
// planv2's fetch count (co-location pass on -- the default in lower) must be <= v1's.
// partial-union is excluded from the comparison (v1 cannot plan it at all -- see fixtures_test.go);
// it is logged, not compared. Table-logs every fixture's (v1, planv2) fetch counts regardless of
// outcome so the M1 report can quote the full table, not just the passing cases.
func TestFetchCountNeverExceedsOldPlanner(t *testing.T) {
	type row struct {
		name        string
		oldN, newN  int
		v1Plannable bool
	}
	var rows []row

	for _, c := range differentialCorpus(t) {
		if !c.V1Plannable {
			newN := fetchCount(planNew(t, c))
			rows = append(rows, row{name: c.Name, newN: newN, v1Plannable: false})
			t.Logf("%-24s v1=n/a (unplannable) planv2=%d", c.Name, newN)
			continue
		}
		oldN := fetchCount(planOld(t, c))
		newN := fetchCount(planNew(t, c))
		rows = append(rows, row{name: c.Name, oldN: oldN, newN: newN, v1Plannable: true})
		t.Logf("%-24s v1=%d planv2=%d", c.Name, oldN, newN)
		if newN > oldN {
			t.Errorf("%s: planv2 fetch count %d > v1 %d (M1 bar violated)", c.Name, newN, oldN)
		}
	}

	if len(rows) == 0 {
		t.Fatal("empty differential corpus -- fixtures missing?")
	}

	t.Log("--- fetch-count table (co-location pass ON) ---")
	for _, r := range rows {
		if r.v1Plannable {
			t.Logf("%-24s v1=%-3d planv2=%-3d %s", r.name, r.oldN, r.newN, verdict(r.newN <= r.oldN))
		} else {
			t.Logf("%-24s v1=n/a  planv2=%-3d (v1-unplannable, excluded from comparison)", r.name, r.newN)
		}
	}
}

func verdict(ok bool) string {
	if ok {
		return "OK (planv2 <= v1)"
	}
	return "VIOLATED"
}
