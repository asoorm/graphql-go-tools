package bench

// scale_v1_test.go pins the v1 comparison leg of the M15 scale-proof benchmark. Discovered while
// building the ring-topology generator (scale_gen_test.go): v1 (plan.NewPlanner+Plan) cannot plan
// ANY operation that crosses into a second subgraph on this topology -- a HARD planning-time error,
// not a timeout or exponential blowup, and NOT scale-dependent (it reproduces identically at S=2).
// planv2 plans every span/S combination in this suite successfully (TestScaleSanityPlan,
// TestScaleMixedCapabilitiesPlan). This is reported per the M15 brief's explicit instruction: measure
// and report an error honestly, without editorializing.
//
// ROOT CAUSE (isolated below, TestScaleV1FailsOnPureReferenceStub): the ring's "next" field needs
// Entity_(i+1)'s output type to resolve in subgraph i's SDL, so subgraph i declares a bare reference
// stub `type Entity_(i+1) @key(fields: "id") { id: ID! }` carrying ONLY the key field -- a standard,
// realistic federation pattern (a subgraph referencing an entity it doesn't own, contributing zero
// fields of its own). v1's nodesResolvableVisitor cannot disambiguate which datasource resolves
// `Entity_(i+1).id` when one subgraph owns the type fully and the other's ENTIRE declaration for
// that type is external -- `internal: could not select the datasource to resolve <Type>.id`. This is
// DIFFERENT from entity_jump.go/multiHopSubgraphs' pattern (both v1-plannable in the existing
// differential corpus, see fixtures_test.go's V1Plannable:true fixtures): those referencing
// subgraphs always carry at least one genuinely-OWNED field alongside the external key
// (shippingEstimate/hobby/score) -- never a 100%-external, zero-owned-field stub. A pure reference
// stub is what breaks v1's heuristic; planv2's hypergraph builder has no such gap.
//
// This is a NEW v1 failure mode, distinct from the already-registered partial-union planning-time
// deadlock (differential.TestV1PartialUnionCorroboration) -- a different code path
// (nodesResolvableVisitor vs the union-exclusivity planner), a different symptom (fast typed error
// vs. deadlock), and a different trigger (pure reference stubs vs. mutually-exclusive union members).

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
)

// TestScaleV1FailsOnPureReferenceStub is the minimal (S=2) isolation of the v1 divergence: even a
// bare `{ start { id next { id } } }` -- no filler fields, no sprinkled capabilities, just the ring's
// `next` hop and a KEY field on both ends -- fails v1 planning. Pinned so a future v1 fix (or a
// future generator change that accidentally "fixes" this by giving stubs an owned field) is a
// visible test change, not a silent drift.
func TestScaleV1FailsOnPureReferenceStub(t *testing.T) {
	sg := generateScaleSupergraph(t, scaleGenParams{Seed: 1, Subgraphs: 2, FieldsPerEntity: 0})
	schemaSDL := scaleSchemaSDL(2)

	pl, err := plan.NewPlanner(planConfig(sg.DataSources))
	if err != nil {
		t.Fatalf("v1 NewPlanner: %v (want success -- construction succeeds; only Plan fails)", err)
	}
	op, def, report := parseAndNormalize(t, schemaSDL, "{ start { id next { id } } }")
	out := pl.Plan(op, def, "", report)
	if !report.HasErrors() {
		t.Fatalf("v1 Plan unexpectedly SUCCEEDED (fetches=%d) -- the pure-reference-stub divergence "+
			"this test pins may have been fixed upstream; if so, update this test's expectation and "+
			"BENCHMARKS.md's Scale section rather than deleting the coverage", fetchCount(out))
	}
	const wantSubstr = "could not select the datasource to resolve"
	if !strings.Contains(report.Error(), wantSubstr) {
		t.Errorf("v1 Plan error changed shape -- got %q, want it to contain %q (BENCHMARKS.md quotes "+
			"this exact error; update both together if this is an intentional v1 change)",
			report.Error(), wantSubstr)
	}
	t.Logf("v1 Plan error (expected divergence): %s", report.Error())
}

// TestScaleV1PlansSingleSubgraphSpan confirms the ONE v1-plannable point in this topology: span=1
// never crosses into a second subgraph (no reference stub involved), so v1 plans it like any other
// single-subgraph query. This is the only apples-to-apples v1-vs-planv2 planning-time comparison the
// ring topology supports; BenchmarkScaleV1PlanningSpan1 in scale_bench_test.go times it.
func TestScaleV1PlansSingleSubgraphSpan(t *testing.T) {
	sg := generateScaleSupergraph(t, scaleGenParams{Seed: 1, Subgraphs: 20, FieldsPerEntity: 3})
	schemaSDL := scaleSchemaSDL(20)

	pl, err := plan.NewPlanner(planConfig(sg.DataSources))
	if err != nil {
		t.Fatalf("v1 NewPlanner: %v", err)
	}
	op, def, report := parseAndNormalize(t, schemaSDL, spanOperation(1))
	out := pl.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("v1 Plan(span=1): %s", report.Error())
	}
	if fetchCount(out) == 0 {
		t.Fatal("v1 Plan(span=1): zero fetches")
	}
}
