package bench

// patho_bench_test.go is the pathological-shape planning-time sweep (BENCHMARKS.md Section 7): planv2 and
// v1 in-process legs over the MxDxS grid the M15 brief asks for. Run per-cell (so one exploding
// cell cannot starve the rest) with e.g.:
//
//	go test ./pkg/engine/planv2/bench/ -run '^$' \
//	  -bench 'BenchmarkPathoPlanning/M=10_D=4_S=10/walk$' -benchtime=5x -timeout=400s
//
// The sub-benchmark grid is M in {5,10,20} x D in {2,3,4,5} x S in {10,20} (Replicas fixed at 2 -- the
// per-step @shareable alternative count), ops walk/frag/leaffan -- see patho_gen_test.go for what
// each family stresses. Conventions match scale_bench_test.go: H built once per shape and reused
// (compile excluded, reported by BenchmarkPathoHypergraphBuild), fresh parse+normalize per
// iteration, same search budget as the planv2 facade. The v1 leg is construction-inclusive
// (NewPlanner is us-scale/lazy) and SKIPs with the planner's error text where v1 cannot plan the
// cell at all -- a skip there is a finding, not a harness failure.

import (
	"fmt"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/lower"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// pathoGrid is the sweep grid (the M15 pathological brief's parameter ranges).
var (
	pathoImplSweep  = []int{5, 10, 20}
	pathoLevelSweep = []int{2, 3, 4, 5}
	pathoSubSweep   = []int{10, 20}
	pathoReplicas   = 2
)

// pathoGridParams enumerates the sweep's shape configs in report order.
func pathoGridParams() []pathoGenParams {
	var out []pathoGenParams
	for _, s := range pathoSubSweep {
		for _, m := range pathoImplSweep {
			for _, d := range pathoLevelSweep {
				out = append(out, pathoGenParams{Seed: 1, Levels: d, Impls: m, Subgraphs: s, Replicas: pathoReplicas})
			}
		}
	}
	return out
}

func pathoCellName(p pathoGenParams) string {
	return fmt.Sprintf("M=%d_D=%d_S=%d", p.Impls, p.Levels, p.Subgraphs)
}

// BenchmarkPathoHypergraphBuild times hypergraph.Build alone per shape (the compile-once cost
// excluded from BenchmarkPathoPlanning, mirroring the Section 5/Section 6 split).
func BenchmarkPathoHypergraphBuild(b *testing.B) {
	for _, p := range pathoGridParams() {
		p := p
		sg := generatePathoSupergraph(b, p)
		b.Run(pathoCellName(p), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h, err := hypergraph.Build(sg.DataSources, scaleHypergraphCfg())
				if err != nil {
					b.Fatalf("hypergraph.Build: %v", err)
				}
				if i == 0 {
					b.Logf("%s: SDLBytes=%d |V|=%d |E|=%d", pathoCellName(p), sg.SDLBytes, h.NumNodes(), h.NumEdges())
				}
			}
		})
	}
}

// BenchmarkPathoPlanning is the planv2 steady-state leg: obligation.Build + search.Search +
// lower.Lower per iteration, H reused, fresh parse+normalize per iteration (identical surface to
// Section 6's BenchmarkScalePlanning).
func BenchmarkPathoPlanning(b *testing.B) {
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
					res, err := search.Search(h, o, scaleSearchConfig)
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

// BenchmarkPathoV1Planning is the v1 leg: fresh plan.NewPlanner + Plan per iteration
// (construction-inclusive, like Section 6's v1 leg). Cells v1 cannot plan SKIP with the error text -- the
// sweep records those as `err`, mirroring the Section 6.3 convention.
func BenchmarkPathoV1Planning(b *testing.B) {
	for _, p := range pathoGridParams() {
		p := p
		sg := generatePathoSupergraph(b, p)
		schemaSDL := pathoSchemaSDL(p)
		for _, op := range pathoOps(p) {
			op := op
			b.Run(pathoCellName(p)+"/"+op.Name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					pl, err := plan.NewPlanner(planConfig(sg.DataSources))
					if err != nil {
						b.Fatalf("v1 NewPlanner: %v", err)
					}
					operation, def, report := parseAndNormalize(b, schemaSDL, op.Op)
					out := pl.Plan(operation, def, "", report)
					if report.HasErrors() {
						b.Skipf("v1 cannot plan this cell: %s", report.Error())
					}
					if i == 0 && fetchCount(out) == 0 {
						b.Skip("v1 produced a zero-fetch plan for this cell")
					}
				}
			})
		}
	}
}
