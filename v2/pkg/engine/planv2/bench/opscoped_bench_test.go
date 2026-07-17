package bench

// opscoped_bench_test.go -- the operation-scoped search mode's benchmark legs (BENCHMARKS.md Section 5.6;
// FORMAL_SPEC Section 6.5). Same conventions as the unscoped legs they mirror, cell for cell:
//
//   - BenchmarkScalePlanningOpScoped mirrors BenchmarkScalePlanning (Section 5.3): H built once per S,
//     fresh parse+normalize per iteration, obligation+search+lower timed -- the only difference is
//     search.Config.OperationScoped=true, so a benchstat pairing against the unscoped run isolates
//     the mode.
//   - BenchmarkPathoPlanningOpScoped mirrors BenchmarkPathoPlanning (Section 7.2) the same way.
//   - BenchmarkPlanningSmallFixturesModes is the small-schema overhead check: the differential-
//     corpus fixtures planned steady-state (planner built once, per-iteration parse+Plan) under
//     both modes with /unscoped and /scoped suffixes, so the per-plan scope-computation overhead
//     on schemas where the whole graph IS the scope is measured directly.
//
// TestScaleOpScopedSearchEquality is the ring-topology sanity gate: the search results (cover,
// selections, walks, cost) must be identical mode-vs-mode on the scale fixture -- the corpus gates
// cover real federation schemas; this covers the synthetic S-ring the Section 5 numbers are earned on.

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/lower"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// scaleSearchConfigScoped is scaleSearchConfig with the Section 6.5 operation-scoped mode selected --
// the ONLY knob that differs from the unscoped legs.
var scaleSearchConfigScoped = search.Config{
	Combine: search.Sum, PreflightCap: 1 << 30, StateCap: 1 << 24, OperationScoped: true,
}

// BenchmarkScalePlanningOpScoped is Section 5.3's sweep under the operation-scoped mode. Cell names match
// BenchmarkScalePlanning's (S=%d/span=%d), so:
//
//	go test ./pkg/engine/planv2/bench/ -run '^$' -bench 'BenchmarkScalePlanning(OpScoped)?/S=200/span=(1|50)$' -benchmem -benchtime=1x -count=5
//
// produces the Section 5.6 both-mode table in one serial run.
func BenchmarkScalePlanningOpScoped(b *testing.B) {
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
				continue
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
					res, err := search.Search(h, o, scaleSearchConfigScoped)
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

// BenchmarkPathoPlanningOpScoped is Section 7.2's grid under the operation-scoped mode (cell names match
// BenchmarkPathoPlanning's for benchstat pairing).
func BenchmarkPathoPlanningOpScoped(b *testing.B) {
	for _, p := range pathoGridParams() {
		p := p
		sg := generatePathoSupergraph(b, p)
		h, err := hypergraph.Build(sg.DataSources, scaleHypergraphCfg())
		if err != nil {
			b.Fatalf("%s: hypergraph.Build: %v", pathoCellName(p), err)
		}
		schemaSDL := pathoSchemaSDL(p)
		for _, op := range pathoOps(p) {
			op := op
			b.Run(pathoCellName(p)+"/"+op.Name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					operation, def, report := parseAndNormalize(b, schemaSDL, op.Op)
					o, err := obligation.Build(operation, def, "", h)
					if err != nil {
						b.Fatalf("obligation.Build: %v", err)
					}
					res, err := search.Search(h, o, scaleSearchConfigScoped)
					if err != nil {
						b.Fatalf("search.Search: %v", err)
					}
					out, err := lower.Lower(h, o, res, operation, def)
					if err != nil {
						b.Fatalf("lower.Lower: %v", err)
					}
					if i == 0 && fetchCount(out) == 0 {
						b.Fatal("zero fetches")
					}
					_ = report
				}
			})
		}
	}
}

// BenchmarkPlanningSmallFixturesModes is the small-schema overhead check: on schemas a few types
// wide, the scope IS (nearly) the whole graph, so the scoped mode pays the closure computation and
// sub-graph construction for no settle savings -- this measures that overhead honestly. Steady
// state: planner built once per fixture/mode (construction excluded), fresh parse per iteration.
func BenchmarkPlanningSmallFixturesModes(b *testing.B) {
	for _, c := range differentialCorpus(b) {
		c := c
		for _, mode := range []struct {
			name string
			opts planv2.Config
		}{
			{"unscoped", planv2.Config{}},
			{"scoped", planv2.Config{OperationScopedSearch: true}},
		} {
			mode := mode
			b.Run(c.Name+"/"+mode.name, func(b *testing.B) {
				pl, err := planv2.NewPlannerWithConfig(c.Config, mode.opts)
				if err != nil {
					b.Fatalf("%s: NewPlannerWithConfig: %v", c.Name, err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					op, def, name, report := c.parse(b)
					out := pl.Plan(op, def, name, report)
					if report.HasErrors() {
						b.Fatalf("%s (%s): Plan: %s", c.Name, mode.name, report.Error())
					}
					if i == 0 && fetchCount(out) == 0 {
						b.Fatalf("%s (%s): zero fetches", c.Name, mode.name)
					}
				}
			})
		}
	}
}

// TestScaleOpScopedSearchEquality asserts search-result equality mode-vs-mode on the synthetic
// scale ring at S=50 (every span), covering the entity-jump-chain topology the Section 5 numbers are
// earned on. Cover (edges, selections, cost, nulls, walks, spines) and the D10 fall-back records
// must be identical; stats are exempt (Section 6.5).
func TestScaleOpScopedSearchEquality(t *testing.T) {
	const s = 50
	sg := generateScaleSupergraph(t, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: scaleFieldsPerEntity})
	h, err := hypergraph.Build(sg.DataSources, scaleHypergraphCfg())
	if err != nil {
		t.Fatalf("hypergraph.Build: %v", err)
	}
	schemaSDL := scaleSchemaSDL(s)
	for _, span := range scaleSpans {
		if span > s {
			continue
		}
		opSDL := spanOperation(span)

		op, def, report := parseAndNormalize(t, schemaSDL, opSDL)
		oU, err := obligation.Build(op, def, "", h)
		if err != nil {
			t.Fatalf("span=%d: obligation.Build: %v", span, err)
		}
		resU, errU := search.Search(h, oU, scaleSearchConfig)

		op2, def2, report2 := parseAndNormalize(t, schemaSDL, opSDL)
		oS, err := obligation.Build(op2, def2, "", h)
		if err != nil {
			t.Fatalf("span=%d: obligation.Build (scoped leg): %v", span, err)
		}
		resS, errS := search.Search(h, oS, scaleSearchConfigScoped)

		if (errU == nil) != (errS == nil) {
			t.Fatalf("span=%d: mode error divergence: unscoped=%v scoped=%v", span, errU, errS)
		}
		if errU != nil {
			if errU.Error() != errS.Error() {
				t.Fatalf("span=%d: different errors: %v vs %v", span, errU, errS)
			}
			continue
		}
		cu, cs := resU.Cover, resS.Cover
		if cu.Cost != cs.Cost || !reflect.DeepEqual(cu.Edges, cs.Edges) ||
			!reflect.DeepEqual(cu.Selected, cs.Selected) || !reflect.DeepEqual(cu.Nulls, cs.Nulls) ||
			!reflect.DeepEqual(cu.Walks, cs.Walks) || !reflect.DeepEqual(cu.Spines, cs.Spines) ||
			!reflect.DeepEqual(resU.RouteFallbacks, resS.RouteFallbacks) {
			t.Fatalf("span=%d: search results differ between modes (cost %d vs %d, %d vs %d edges)",
				span, cu.Cost, cs.Cost, len(cu.Edges), len(cs.Edges))
		}
		_, _ = report, report2
	}
}
