package bench

// patho_verify_test.go is the pathological-shape benchmark's CORRECTNESS gate (the patho analog of
// scale_verify_test.go): the generated shape must build a valid hypergraph and actually PLAN
// end-to-end (planv2), including the fragment/leaf-fan operation families, at sizes small enough
// for a normal `go test ./...` / -race run. The expensive sweep sizes live only in
// patho_bench_test.go's -bench path.

import (
	"fmt"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

// TestPathoSupergraphComposes: hypergraph.Build runs clean across a spread of shape sizes,
// including one at the sweep's densest corner ratios (D=5, M=5).
func TestPathoSupergraphComposes(t *testing.T) {
	for _, p := range []pathoGenParams{
		{Seed: 1, Levels: 2, Impls: 2, Subgraphs: 4, Replicas: 2},
		{Seed: 1, Levels: 3, Impls: 4, Subgraphs: 6, Replicas: 2},
		{Seed: 1, Levels: 5, Impls: 5, Subgraphs: 10, Replicas: 2},
	} {
		p := p
		t.Run(fmt.Sprintf("D=%d_M=%d_S=%d", p.Levels, p.Impls, p.Subgraphs), func(t *testing.T) {
			sg := generatePathoSupergraph(t, p)
			h, err := hypergraph.Build(sg.DataSources, scaleHypergraphCfg())
			if err != nil {
				t.Fatalf("hypergraph.Build: %v", err)
			}
			if h == nil {
				t.Fatal("hypergraph.Build: nil H, no error")
			}
			t.Logf("D=%d M=%d S=%d R=%d: SDLBytes=%d TypeCount=%d |V|=%d |E|=%d",
				p.Levels, p.Impls, p.Subgraphs, p.Replicas, sg.SDLBytes, sg.TypeCount, h.NumNodes(), h.NumEdges())
		})
	}
}

// TestPathoSanityPlan: planv2 plans all three operation families on a moderate shape, with at
// least one fetch each.
func TestPathoSanityPlan(t *testing.T) {
	p := pathoGenParams{Seed: 1, Levels: 3, Impls: 4, Subgraphs: 6, Replicas: 2}
	sg := generatePathoSupergraph(t, p)
	pl, err := planv2.NewPlanner(planConfig(sg.DataSources))
	if err != nil {
		t.Fatalf("planv2.NewPlanner: %v", err)
	}
	schemaSDL := pathoSchemaSDL(p)
	for _, op := range pathoOps(p) {
		op := op
		t.Run(op.Name, func(t *testing.T) {
			operation, def, report := parseAndNormalize(t, schemaSDL, op.Op)
			out := pl.Plan(operation, def, "", report)
			if report.HasErrors() {
				t.Fatalf("%s: Plan: %s", op.Name, report.Error())
			}
			if out == nil {
				t.Fatalf("%s: Plan returned nil with no report error", op.Name)
			}
			n := fetchCount(out)
			if n == 0 {
				t.Errorf("%s: zero fetches in a non-empty plan", op.Name)
			}
			t.Logf("%s: fetches=%d", op.Name, n)
		})
	}
}
