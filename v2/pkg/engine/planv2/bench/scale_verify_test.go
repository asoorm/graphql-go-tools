package bench

// scale_verify_test.go is the M15 scale-proof benchmark's CORRECTNESS gate: it proves the
// generateScaleSupergraph shape (scale_gen_test.go) actually composes into a valid H
// (hypergraph.Build runs clean) and actually PLANS (planv2 end to end, including the sprinkled
// multi-key/composite-key/@requires capabilities, not just the plain ring). These run under
// `go test` (no -bench needed) and are the -race target; the scale_bench_test.go benchmarks reuse
// the same generator at larger sizes without -race (timing noise).
//
// Sizes here are deliberately small/moderate (not the BENCHMARKS.md S=200 headline size) so this
// stays fast enough for a normal `go test ./...` run -- the expensive, large-FieldsPerEntity S=200
// numbers live only in the -bench=. path (scale_bench_test.go), run on demand.

import (
	"fmt"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// scaleHypergraphCfg mirrors planv2.NewPlanner's BuildConfig exactly (see planv2.go) so the verify
// tests and benchmarks exercise the same compile path a real planv2.Planner would.
func scaleHypergraphCfg() hypergraph.BuildConfig {
	return hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation"},
	}
}

// TestScaleSupergraphComposes is the "builder runs clean" bar: hypergraph.Build must succeed (no
// error) for a spread of S, including one deliberately at the 200-subgraph headline scale (kept fast
// here with a SMALL FieldsPerEntity -- the byte-footprint-heavy version is the benchmark-only path).
func TestScaleSupergraphComposes(t *testing.T) {
	for _, s := range []int{2, 8, 30, 200} {
		s := s
		t.Run(fmt.Sprintf("S=%d", s), func(t *testing.T) {
			sg := generateScaleSupergraph(t, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: 3})
			h, err := hypergraph.Build(sg.DataSources, scaleHypergraphCfg())
			if err != nil {
				t.Fatalf("hypergraph.Build(S=%d): %v", s, err)
			}
			if h == nil {
				t.Fatalf("hypergraph.Build(S=%d): nil H, no error", s)
			}
			t.Logf("S=%d: SDLBytes=%d TypeCount=%d", s, sg.SDLBytes, sg.TypeCount)
			if s == 200 && sg.TypeCount < 500 {
				t.Errorf("S=200: TypeCount=%d, want >= 500 (M15 scale target)", sg.TypeCount)
			}
		})
	}
}

// parseAndNormalize is the pinned planv2 input pipeline (see planv2.go's package doc / the
// differential harness / fixtures_test.go's corpusCase.parse) -- every caller needs its own fresh
// (operation, definition) pair since normalization mutates both documents.
func parseAndNormalize(tb testing.TB, schemaSDL, opSDL string) (*ast.Document, *ast.Document, *operationreport.Report) {
	tb.Helper()
	def := unsafeparser.ParseGraphqlDocumentString(schemaSDL)
	if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
		tb.Fatalf("merge base schema: %v", err)
	}
	op := unsafeparser.ParseGraphqlDocumentString(opSDL)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
	).NormalizeOperation(&op, &def, report)
	if report.HasErrors() {
		tb.Fatalf("normalize: %s", report.Error())
	}
	return &op, &def, report
}

// TestScaleSanityPlan is the "sanity plan succeeds" bar: planv2.NewPlanner+Plan must succeed on the
// ring topology for a spread of spans, at a moderate S so this stays -race-fast.
func TestScaleSanityPlan(t *testing.T) {
	const s = 20
	sg := generateScaleSupergraph(t, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: 3})
	pl, err := planv2.NewPlanner(planConfig(sg.DataSources))
	if err != nil {
		t.Fatalf("planv2.NewPlanner: %v", err)
	}
	schemaSDL := scaleSchemaSDL(s)
	for _, span := range []int{1, 2, 5, 10, s} {
		span := span
		t.Run(fmt.Sprintf("span=%d", span), func(t *testing.T) {
			op, def, report := parseAndNormalize(t, schemaSDL, spanOperation(span))
			out := pl.Plan(op, def, "", report)
			if report.HasErrors() {
				t.Fatalf("span=%d: Plan: %s", span, report.Error())
			}
			if out == nil {
				t.Fatalf("span=%d: Plan returned nil with no report error", span)
			}
			n := fetchCount(out)
			if n == 0 {
				t.Errorf("span=%d: zero fetches in a non-empty plan", span)
			}
			// NOT asserted as an exact/lower-bound invariant: most hops are forced entity jumps
			// (attr0 never lives in the previous hop's stub), but every scaleProvidesEvery-th
			// subgraph's `next` field carries @provides(fields:"attr0"), which
			// lets the planner skip the jump for that hop entirely -- fetch count is therefore
			// span-minus-however-many-provides-hops-fall-in-range, not a fixed function of span
			// alone. Logged for the BENCHMARKS.md scale story, not gated.
			t.Logf("span=%d: fetches=%d", span, n)
		})
	}
}

// TestScaleMixedCapabilitiesPlan proves the sprinkled multi-key / composite-key / @requires
// capabilities are not just inert SDL text: at least one of them is actually reachable and
// plannable, for every role residue present at a moderate S.
func TestScaleMixedCapabilitiesPlan(t *testing.T) {
	const s = 20 // >= scaleRoleCycle*2 so all three sprinkle roles are guaranteed to appear
	sg := generateScaleSupergraph(t, scaleGenParams{Seed: 1, Subgraphs: s, FieldsPerEntity: 2})
	pl, err := planv2.NewPlanner(planConfig(sg.DataSources))
	if err != nil {
		t.Fatalf("planv2.NewPlanner: %v", err)
	}
	schemaSDL := scaleSchemaSDL(s)

	tested := map[int]bool{}
	for i := 0; i < s; i++ {
		role := roleOf((i + 1) % s)
		if role == 0 || tested[role] {
			continue
		}
		var field, label string
		switch role {
		case scaleRoleMultiKey:
			field, label = "sku", "multi-key"
		case scaleRoleComposite:
			field, label = "region { code }", "composite-key"
		case scaleRoleRequires:
			field, label = "weight", "requires"
		}
		op := fmt.Sprintf("{ start %s }", pathTo(i, field))
		t.Run(label, func(t *testing.T) {
			operation, def, report := parseAndNormalize(t, schemaSDL, op)
			out := pl.Plan(operation, def, "", report)
			if report.HasErrors() {
				t.Fatalf("%s (entity index %d): Plan: %s", label, i, report.Error())
			}
			if out == nil || fetchCount(out) == 0 {
				t.Fatalf("%s (entity index %d): Plan produced zero fetches", label, i)
			}
			t.Logf("%s: entity index %d, fetches=%d", label, i, fetchCount(out))
		})
		tested[role] = true
	}
	if len(tested) != 3 {
		t.Fatalf("S=%d: only found %d/3 sprinkle roles present (scaleRoleCycle=%d) -- widen S", s, len(tested), scaleRoleCycle)
	}
}
