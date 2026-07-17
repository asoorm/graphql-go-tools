// Package bench is the M1 Task-13 performance harness: old-vs-new planning speed/allocations
// (BenchmarkPlanningOldVsNew), the fetch-count-never-exceeds-old-planner bar
// (TestFetchCountNeverExceedsOldPlanner, fetchcount_test.go), closing the M1 performance story.
//
// SCOPE: this package benchmarks NewPlanner+Plan together (construction included), matching the M1
// task brief's helper shape -- planOld/planNew build a fresh planner from the fixture's
// plan.Configuration AND plan a freshly-parsed operation on every b.N iteration. This is NOT
// steady-state per-request cost (a real gateway builds the planner once per supergraph and reuses it
// across many Plan calls) -- see BENCHMARKS.md's caveats for the steady-state-vs-construction-included
// distinction.
package bench

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// fetchCount counts fetches in a plan's response: len(RawFetches) after planning -- the same quantity
// for both planners, since both emit the same RawFetches output.
func fetchCount(p plan.Plan) int {
	sp, ok := p.(*plan.SynchronousResponsePlan)
	if !ok || sp == nil || sp.Response == nil {
		return 0
	}
	return len(sp.Response.RawFetches)
}

// planOld builds a fresh v1 planner from c.Config and plans a freshly-parsed operation. Any
// planning error is a hard test/benchmark failure -- every corpusCase in this package is expected to
// plan on BOTH planners except partial-union (V1Plannable=false), which is only ever passed to
// planNew.
func planOld(tb testing.TB, c corpusCase) plan.Plan {
	tb.Helper()
	pl, err := plan.NewPlanner(c.Config)
	if err != nil {
		tb.Fatalf("%s: v1 NewPlanner: %v", c.Name, err)
	}
	op, def, name, report := c.parse(tb)
	out := pl.Plan(op, def, name, report)
	if report.HasErrors() {
		tb.Fatalf("%s: v1 Plan: %s", c.Name, report.Error())
	}
	return out
}

// planNew builds a fresh planv2 planner from c.Config and plans a freshly-parsed operation. The
// co-location pass is on by default in lower -- no extra wiring needed, so the fetch-count bar is
// measured with co-location enabled.
func planNew(tb testing.TB, c corpusCase) plan.Plan {
	tb.Helper()
	pl, err := planv2.NewPlanner(c.Config)
	if err != nil {
		tb.Fatalf("%s: planv2 NewPlanner: %v", c.Name, err)
	}
	op, def, name, report := c.parse(tb)
	out := pl.Plan(op, def, name, report)
	if report.HasErrors() {
		tb.Fatalf("%s: planv2 Plan: %s", c.Name, report.Error())
	}
	return out
}

var _ = resolve.FetchItem{} // RawFetches element type, pinned for the fetchCount contract

// BenchmarkPlanningOldVsNew is the M1 acceptance-gate benchmark (task brief "M1 Correctness Bars --
// Planning speed"): v1 plan.NewPlanner+Plan vs planv2.NewPlanner+Plan on the differential-corpus
// subset where both planners plan (entity-jump, cross-subgraph-roots, requires-chain,
// multi-hop-entity), PLUS partial-union planv2-only (v1 cannot plan it at all -- see fixtures_test.go
// and differential.KnownDivergences). Sub-benchmark names are suffixed "/v1" and "/planv2" so
// `benchstat` can pair them directly (`benchstat -filter ".../v1" -filter ".../planv2"` or the
// standard old.txt/new.txt split). Run with -benchmem for ns/op + allocs/op.
func BenchmarkPlanningOldVsNew(b *testing.B) {
	for _, c := range differentialCorpus(b) {
		c := c
		if c.V1Plannable {
			b.Run(c.Name+"/v1", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					planOld(b, c)
				}
			})
		} else {
			b.Logf("%s: v1-unplannable (planning-time deadlock); benchmarked planv2-only", c.Name)
		}
		b.Run(c.Name+"/planv2", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				planNew(b, c)
			}
		})
	}
}

// BenchmarkPlanningSyntheticOldVsNew is the larger-instance companion to BenchmarkPlanningOldVsNew:
// deep nesting (~10 levels), wide selection (~50 sibling fields), and an 8-subgraph fan-out -- none
// of which appear in the audit/differential corpus, sized specifically to stress the planners beyond
// the small hand-written fixtures. Same /v1 vs /planv2 naming convention for benchstat.
func BenchmarkPlanningSyntheticOldVsNew(b *testing.B) {
	for _, c := range syntheticCorpus(b) {
		c := c
		b.Run(c.Name+"/v1", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				planOld(b, c)
			}
		})
		b.Run(c.Name+"/planv2", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				planNew(b, c)
			}
		})
	}
}
