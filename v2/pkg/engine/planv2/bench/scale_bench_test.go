package bench

// scale_bench_test.go is the M15 scale-proof benchmark: planning-time behavior of planv2 at
// enterprise scale (a synthetic ~200-subgraph, ~15MB-class supergraph -- see scale_gen_test.go).
// Run with:
//
//	go test ./pkg/engine/planv2/bench/ -run '^$' -bench=BenchmarkScale -benchmem -benchtime=1x
//
// -benchtime=1x is deliberate (not this package's usual 200ms): BenchmarkScaleHypergraphBuild's
// largest leg (S=200, FieldsPerEntity=300, the ~15MB-class config) builds a ~64k-node hypergraph
// per iteration -- 1x is enough to get a real number without spending minutes recompiling it under
// -benchtime's auto-scaling. Increase -benchtime (e.g. -benchtime=5x) for tighter variance at the
// cost of proportionally longer runs.
//
// scaleFieldsPerEntity=300 is the one size knob every benchmark in this file shares: measured
// (scale_gen_test.go's generator) to land the S=200 leg's aggregate SDL text at ~15.3MB -- see
// BENCHMARKS.md's Scale section for the exact figure this run produced.

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/lower"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// scaleFieldsPerEntity is shared by every benchmark in this file so the S sweep varies exactly one
// parameter (subgraph count) at a time -- see the file doc for how this value was picked.
const scaleFieldsPerEntity = 300

// scaleSubgraphSizes is the S in {50, 100, 200} sweep the M15 brief asks for.
var scaleSubgraphSizes = []int{50, 100, 200}

// scaleSpans is the operation-span sweep (number of distinct subgraphs an operation touches via
// ring entity jumps) the M15 brief asks for: 1, 5, 20, 50.
var scaleSpans = []int{1, 5, 20, 50}

// scaleSearchConfig mirrors planv2.go's package-level searchConfig exactly (same Combine/caps) so
// the phase-split benchmark below measures the identical search budget the facade runs under.
var scaleSearchConfig = search.Config{Combine: search.Sum, PreflightCap: 1 << 30, StateCap: 1 << 24}

// BenchmarkScaleHypergraphBuild times hypergraph.Build ALONE (construction, the compile-once cost
// amortized over every subsequent Plan call against that supergraph -- NOT included in
// BenchmarkScalePlanning below, which reuses one pre-built H across all its iterations). dataSources
// are generated ONCE per S outside the timed loop; only the Build call itself is measured.
func BenchmarkScaleHypergraphBuild(b *testing.B) {
	for _, s := range scaleSubgraphSizes {
		s := s
		sg := generateScaleSupergraph(b, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: scaleFieldsPerEntity})
		b.Run(fmt.Sprintf("S=%d", s), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h, err := hypergraph.Build(sg.DataSources, scaleHypergraphCfg())
				if err != nil {
					b.Fatalf("S=%d: hypergraph.Build: %v", s, err)
				}
				if i == 0 {
					b.Logf("S=%d: SDLBytes=%d TypeCount=%d |V|=%d |E|=%d", s, sg.SDLBytes, sg.TypeCount, h.NumNodes(), h.NumEdges())
				}
			}
		})
	}
}

// BenchmarkScaleV1NewPlanner is the v1 side of the "compile-once" comparison: plan.NewPlanner at
// the same S/FieldsPerEntity as BenchmarkScaleHypergraphBuild. v1's construction succeeds on this
// topology (only Plan fails -- see scale_v1_test.go); this is a genuine, valid head-to-head number
// even though the planning-time comparison below is span=1-only.
func BenchmarkScaleV1NewPlanner(b *testing.B) {
	for _, s := range scaleSubgraphSizes {
		s := s
		sg := generateScaleSupergraph(b, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: scaleFieldsPerEntity})
		b.Run(fmt.Sprintf("S=%d", s), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := plan.NewPlanner(planConfig(sg.DataSources)); err != nil {
					b.Fatalf("S=%d: v1 NewPlanner: %v", s, err)
				}
			}
		})
	}
}

// BenchmarkScalePlanning is the steady-state planning-time sweep: for each S, build H ONCE (setup,
// not timed), then for each span, time obligation.Build + search.Search + lower.Lower together per
// iteration (fresh parse+normalize per iteration, since both mutate their documents -- the same
// per-iteration-fresh-parse convention bench_test.go's planNew/planOld use). This EXCLUDES
// hypergraph.Build/NewPlanner -- see BenchmarkScaleHypergraphBuild for that leg and the file doc for
// why they're split.
func BenchmarkScalePlanning(b *testing.B) {
	for _, s := range scaleSubgraphSizes {
		s := s
		sg := generateScaleSupergraph(b, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: scaleFieldsPerEntity})
		h, err := hypergraph.Build(sg.DataSources, scaleHypergraphCfg())
		if err != nil {
			b.Fatalf("S=%d: hypergraph.Build: %v", s, err)
		}
		schemaSDL := scaleSchemaSDL(s)
		for _, span := range scaleSpans {
			if span > s {
				continue // span cannot exceed the ring size
			}
			span := span
			opSDL := spanOperation(span)
			b.Run(fmt.Sprintf("S=%d/span=%d", s, span), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					op, def, report := parseAndNormalize(b, schemaSDL, opSDL)
					o, err := obligation.Build(op, def, "", h)
					if err != nil {
						b.Fatalf("S=%d/span=%d: obligation.Build: %v", s, span, err)
					}
					res, err := search.Search(h, o, scaleSearchConfig)
					if err != nil {
						b.Fatalf("S=%d/span=%d: search.Search: %v", s, span, err)
					}
					out, err := lower.Lower(h, o, res, op, def)
					if err != nil {
						b.Fatalf("S=%d/span=%d: lower.Lower: %v", s, span, err)
					}
					if i == 0 && fetchCount(out) == 0 {
						b.Fatalf("S=%d/span=%d: zero fetches", s, span)
					}
					_ = report
				}
			})
		}
	}
}

// BenchmarkScaleV1PlanningSpan1 is the ONLY apples-to-apples v1-vs-planv2 planning-time comparison
// this topology supports (see scale_v1_test.go: v1 hard-errors on every span >= 2 here). Matches
// bench_test.go's construction-inclusive convention (fresh v1 planner + fresh parse per iteration)
// since a single-subgraph span=1 query never exercises federation at all -- comparing it against
// BenchmarkScalePlanning's phase-split (Build excluded) numbers would not be apples-to-apples; this
// benchmark is reported against v1's OWN construction-inclusive baseline from bench_test.go instead.
func BenchmarkScaleV1PlanningSpan1(b *testing.B) {
	for _, s := range scaleSubgraphSizes {
		s := s
		sg := generateScaleSupergraph(b, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: scaleFieldsPerEntity})
		schemaSDL := scaleSchemaSDL(s)
		opSDL := spanOperation(1)
		b.Run(fmt.Sprintf("S=%d", s), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				pl, err := plan.NewPlanner(planConfig(sg.DataSources))
				if err != nil {
					b.Fatalf("S=%d: v1 NewPlanner: %v", s, err)
				}
				op, def, report := parseAndNormalize(b, schemaSDL, opSDL)
				out := pl.Plan(op, def, "", report)
				if report.HasErrors() {
					b.Fatalf("S=%d: v1 Plan(span=1): %s", s, report.Error())
				}
				if i == 0 && fetchCount(out) == 0 {
					b.Fatalf("S=%d: v1 Plan(span=1): zero fetches", s)
				}
			}
		})
	}
}

// memDeltaMB reports the ALLOCATION TRAFFIC (TotalAlloc delta) between two MemStats snapshots, in
// MB. TotalAlloc is cumulative bytes allocated (monotonic, never decreases), so this is safe to
// subtract even across a GC that runs mid-measurement -- unlike HeapAlloc (current live bytes), whose
// delta can go NEGATIVE if a background GC frees more than the measured call allocates, which is
// exactly what a bare HeapAlloc subtraction hit during this benchmark's development (a large
// negative int64 rendered as a huge value after an unsigned wraparound). TotalAlloc delta is the
// standard "how much did this call allocate" proxy testing.B's own -benchmem B/op uses internally.
func memDeltaMB(before, after runtime.MemStats) float64 {
	return float64(after.TotalAlloc-before.TotalAlloc) / 1e6
}

// TestScaleMemoryProfile is a one-shot (not averaged-over-b.N) allocation measurement for the M15
// brief's "memory: peak alloc for Build + per-plan" bar, at the largest configured size (S=200,
// scaleFieldsPerEntity=300 -- the ~15MB-class leg). runtime.GC() before each snapshot bounds prior
// garbage's contribution to the delta; see memDeltaMB for why TotalAlloc (not HeapAlloc) is the
// reported figure. This is a single sample, not a distribution -- B/op from -benchmem on
// BenchmarkScaleHypergraphBuild/BenchmarkScalePlanning is the amortized companion figure.
func TestScaleMemoryProfile(t *testing.T) {
	const s = 200
	sg := generateScaleSupergraph(t, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: scaleFieldsPerEntity})

	var before, afterBuild runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	h, err := hypergraph.Build(sg.DataSources, scaleHypergraphCfg())
	if err != nil {
		t.Fatalf("hypergraph.Build: %v", err)
	}
	runtime.ReadMemStats(&afterBuild)
	t.Logf("Build(S=%d, F=%d): allocated=%.2fMB |V|=%d |E|=%d",
		s, scaleFieldsPerEntity, memDeltaMB(before, afterBuild), h.NumNodes(), h.NumEdges())

	schemaSDL := scaleSchemaSDL(s)
	op, def, report := parseAndNormalize(t, schemaSDL, spanOperation(50))

	var beforePlan, afterPlan runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&beforePlan)
	o, err := obligation.Build(op, def, "", h)
	if err != nil {
		t.Fatalf("obligation.Build: %v", err)
	}
	res, err := search.Search(h, o, scaleSearchConfig)
	if err != nil {
		t.Fatalf("search.Search: %v", err)
	}
	out, err := lower.Lower(h, o, res, op, def)
	if err != nil {
		t.Fatalf("lower.Lower: %v", err)
	}
	runtime.ReadMemStats(&afterPlan)
	if fetchCount(out) == 0 {
		t.Fatal("zero fetches for span=50 plan")
	}
	t.Logf("Plan(S=%d, span=50): allocated=%.2fMB fetches=%d", s, memDeltaMB(beforePlan, afterPlan), fetchCount(out))
	_ = report
}
