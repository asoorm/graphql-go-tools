package planv2

// diagnostics_test.go pins the facade half of the D10 typed-loud fall-back (FORMAL_SPEC D10
// amendment): PlanWithDiagnostics threads search.Result.RouteFallbacks through to callers, renders
// one Warn diagnostic per event by default, and a clean plan carries none. The end-to-end firing
// against a real federation config is diagnostics_external_test.go; the audit runner asserts zero
// fallbacks on every PASS case.

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// TestPlanWithDiagnostics_CleanPlanHasNone: a plan that never falls back must carry no route
// fallbacks and no warnings, and PlanWithDiagnostics must produce the same plan Plan does.
func TestPlanWithDiagnostics_CleanPlanHasNone(t *testing.T) {
	p := mustPlanner(t, hgtestdata.EntityJumpConfig())
	op, def, report := parseAndNormalize(t, entityJumpSupergraph, `{ product { shippingEstimate } }`)

	result, diags := p.PlanWithDiagnostics(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	if _, ok := result.(*plan.SynchronousResponsePlan); !ok {
		t.Fatalf("want *plan.SynchronousResponsePlan, got %T", result)
	}
	if len(diags.RouteFallbacks) != 0 || len(diags.Warnings) != 0 {
		t.Fatalf("clean plan must carry no diagnostics, got %+v", diags)
	}
}

// TestRouteFallbackDiagnostics_Rendering: each typed RouteFallback renders exactly one Warn-level
// diagnostic with the stable code and a message naming the goal, the branch, and the subgraph --
// the "loud" half of typed-and-loud.
func TestRouteFallbackDiagnostics_Rendering(t *testing.T) {
	fallbacks := []search.RouteFallback{
		{Kind: search.RouteFallbackRootPin, Coordinate: "Media.title", RootField: "aMedia", Subgraph: "b"},
		{Kind: search.RouteFallbackScopedWalk, Coordinate: "Media.title", RootField: "aMedia", Subgraph: "b"},
	}
	diags := routeFallbackDiagnostics(fallbacks)
	if len(diags.RouteFallbacks) != 2 || len(diags.Warnings) != 2 {
		t.Fatalf("want the typed record plus one warning per event, got %+v", diags)
	}
	for i, w := range diags.Warnings {
		if w.Severity != SeverityWarn {
			t.Errorf("warning %d: want severity %q, got %q", i, SeverityWarn, w.Severity)
		}
		if w.Code != DiagRouteFallback {
			t.Errorf("warning %d: want code %q, got %q", i, DiagRouteFallback, w.Code)
		}
		for _, part := range []string{"Media.title", "aMedia", string(fallbacks[i].Kind), `"b"`} {
			if !strings.Contains(w.Message, part) {
				t.Errorf("warning %d message must name %q, got %q", i, part, w.Message)
			}
		}
	}
}
