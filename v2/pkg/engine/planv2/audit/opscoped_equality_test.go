package audit

// opscoped_equality_test.go -- the audit-corpus dual-mode gate for the operation-scoped search
// (FORMAL_SPEC Section 6.5). ONE committed test runs the FULL corpus under both modes and asserts PLAN
// EQUALITY per case, not merely both-pass:
//
//   - Outcome equality: identical status AND reason (a scoped-mode SKIP where the default PASSes
//     would hide a divergence behind classification);
//   - D10 register equality: the RouteFallback records are verbatim-identical per case, so the
//     corpus fall-back register (fallback_register_test.go) is mode-identical by construction;
//   - plan byte-equality: for every case both modes can plan, the canonicalized fetch tree
//     (documents, targets, dependencies, response paths, trigger) is byte-identical and the
//     response shapes agree under the differential package's semantic oracle.
//
// SettleStats and other resource-guard observables are exempt (Section 6.5 "what may legitimately
// differ"); nothing plan-observable is.

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/differential"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

var opScopedOpts = planv2.Config{OperationScopedSearch: true}

func TestAudit_OpScopedModeEquality(t *testing.T) {
	cases, err := loadCorpus("testdata")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("empty corpus")
	}
	var planned, compared int
	for _, c := range cases {
		name := c.Suite + "/" + c.Name

		// 1. Outcome equality (runs SKIP/GAP/FAIL cases too -- classification must not move).
		outU := RunWithPlannerConfig(c, planv2.Config{})
		outS := RunWithPlannerConfig(c, opScopedOpts)
		if outU.Status != outS.Status || outU.Reason != outS.Reason {
			t.Errorf("%s: outcome differs between modes:\n unscoped: %s (%s)\n scoped:   %s (%s)",
				name, outU.Status, outU.Reason, outS.Status, outS.Reason)
			continue
		}
		// 2. D10 register equality, verbatim.
		if !reflect.DeepEqual(outU.RouteFallbacks, outS.RouteFallbacks) {
			t.Errorf("%s: D10 fall-back records differ between modes (register must be mode-identical):\n unscoped: %+v\n scoped:   %+v",
				name, outU.RouteFallbacks, outS.RouteFallbacks)
			continue
		}
		if c.Skip != "" {
			continue
		}

		// 3. Plan byte-equality for cases that plan.
		planU, canonU, errU := planForModes(c, planv2.Config{})
		planS, canonS, errS := planForModes(c, opScopedOpts)
		if (errU == nil) != (errS == nil) {
			t.Errorf("%s: plan error divergence between modes: unscoped=%v scoped=%v", name, errU, errS)
			continue
		}
		if errU != nil {
			if errU.Error() != errS.Error() {
				t.Errorf("%s: different plan errors between modes:\n unscoped: %v\n scoped:   %v", name, errU, errS)
			}
			planned++
			continue
		}
		planned++
		if canonU != canonS {
			t.Errorf("%s: canonical plans differ between modes:\n--- unscoped ---\n%s\n--- scoped ---\n%s", name, canonU, canonS)
			continue
		}
		if div := differential.CompareResponseShapes(planU, planS); div != nil {
			t.Errorf("%s: response shapes differ between modes: %s", name, div)
			continue
		}
		compared++
	}
	t.Logf("op-scoped mode equality: %d cases planned in both modes, %d byte-identical plan comparisons", planned, compared)
	if compared == 0 {
		t.Fatal("no case produced comparable plans -- the gate is vacuous")
	}
}

// planForModes plans one case through the planv2 facade with the given options and renders the
// canonical plan text (fetch documents, targets, dependency ids, response paths, and the
// subscription trigger when present). Returns the raw plan too, for the semantic shape oracle.
func planForModes(c Case, opts planv2.Config) (plan.Plan, string, error) {
	dataSources, _, err := BuildDataSources(c)
	if err != nil {
		return nil, "", fmt.Errorf("config: %w", err)
	}
	planner, err := planv2.NewPlannerWithConfig(plan.Configuration{DataSources: dataSources, DisableResolveFieldPositions: true}, opts)
	if err != nil {
		return nil, "", fmt.Errorf("NewPlanner: %w", err)
	}
	op, def, report := parseAndNormalize(c.Definition, c.Operation)
	if report.HasErrors() {
		return nil, "", fmt.Errorf("normalize: %s", report.Error())
	}
	p, _ := planner.PlanWithDiagnostics(op, def, "", report)
	if report.HasErrors() {
		return nil, "", fmt.Errorf("plan: %s", report.Error())
	}
	canon, err := canonicalPlan(p)
	if err != nil {
		return nil, "", err
	}
	return p, canon, nil
}

// canonicalPlan renders a plan's fetch tree canonically -- the same observables the conformance
// runner's planCanon reads (documents, subgraph targets, dependencies, defer ids, response paths,
// trigger), across all three plan kinds the facade emits.
func canonicalPlan(p plan.Plan) (string, error) {
	var trigger *resolve.GraphQLSubscriptionTrigger
	var fetches []*resolve.FetchItem
	switch pl := p.(type) {
	case *plan.SynchronousResponsePlan:
		if pl == nil || pl.Response == nil {
			return "", fmt.Errorf("nil SynchronousResponsePlan")
		}
		fetches = pl.Response.RawFetches
	case *plan.SubscriptionResponsePlan:
		if pl == nil || pl.Response == nil || pl.Response.Response == nil {
			return "", fmt.Errorf("nil SubscriptionResponsePlan")
		}
		trigger = &pl.Response.Trigger
		fetches = pl.Response.Response.RawFetches
	case *plan.DeferResponsePlan:
		if pl == nil || pl.Response == nil || pl.Response.Response == nil {
			return "", fmt.Errorf("nil DeferResponsePlan")
		}
		fetches = pl.Response.Response.RawFetches
	default:
		return "", fmt.Errorf("unexpected plan type %T", p)
	}
	var b strings.Builder
	if trigger != nil {
		doc := ""
		if trigger.QueryPlan != nil {
			doc = trigger.QueryPlan.Query
		}
		fmt.Fprintf(&b, "trigger %s %s\n", trigger.SourceName, doc)
	}
	for i, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok {
			fmt.Fprintf(&b, "fetch %d (%T)\n", i, item.Fetch)
			continue
		}
		fmt.Fprintf(&b, "fetch %d sg=%s deps=%v defer=%d path=%s doc=%s\n",
			i, sf.DataSourceIdentifier, sf.DependsOnFetchIDs, sf.DeferID, item.ResponsePath, fetchQuery(sf))
	}
	return b.String(), nil
}
