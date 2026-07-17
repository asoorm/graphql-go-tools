package differential

// opscoped_equality_test.go -- the differential harness's mode-equality pass for the operation-
// scoped search (FORMAL_SPEC Section 6.5). This is planv2-vs-planv2: every registered differential
// fixture (the partial-union headline case plus the full agreed set -- sync, subscription, and
// defer) is planned in both modes and the plans must be byte-identical on the canonical fetch
// rendering AND equivalent under the same semantic oracles the v1 comparison uses
// (CompareResponseShapes / CompareSubscriptionPlans / CompareDeferPlans). Run on the base
// datasource order; the C.4 determinism obligations (and the conformance determinism probe, which
// runs under both modes via RunWithPlannerConfig) cover order-permutation invariance separately.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

func TestDifferential_OpScopedModeEquality(t *testing.T) {
	all := append([]differentialCase{
		{name: "partial-union", schema: PartialUnionSchema, op: PartialUnionOp, subgraphs: partialUnionSubgraphs()},
	}, agreedCases()...)
	for _, c := range all {
		c := c
		t.Run(c.name, func(t *testing.T) {
			cfg := buildConfig(t, c.subgraphs, c.fields...)

			plannerU, err := planv2.NewPlannerWithConfig(cfg, planv2.Config{})
			if err != nil {
				t.Fatalf("unscoped NewPlanner: %v", err)
			}
			plannerS, err := planv2.NewPlannerWithConfig(cfg, planv2.Config{OperationScopedSearch: true})
			if err != nil {
				t.Fatalf("scoped NewPlanner: %v", err)
			}

			opU, defU, repU := parseAndNormalizeOpts(t, c.schema, c.op, c.deferOp)
			planU := plannerU.Plan(opU, defU, "", repU)
			opS, defS, repS := parseAndNormalizeOpts(t, c.schema, c.op, c.deferOp)
			planS := plannerS.Plan(opS, defS, "", repS)

			if repU.HasErrors() != repS.HasErrors() {
				t.Fatalf("mode error divergence: unscoped=%v scoped=%v", repU.Error(), repS.Error())
			}
			if repU.HasErrors() {
				if repU.Error() != repS.Error() {
					t.Fatalf("different plan errors between modes:\n unscoped: %s\n scoped:   %s", repU.Error(), repS.Error())
				}
				return
			}

			canonU := canonFetches(t, planU)
			canonS := canonFetches(t, planS)
			if canonU != canonS {
				t.Fatalf("canonical plans differ between modes:\n--- unscoped ---\n%s\n--- scoped ---\n%s", canonU, canonS)
			}

			// Same oracle dispatch runBothOpts uses for the v1 comparison, here mode-vs-mode.
			var div *Divergence
			_, uIsDefer := planU.(*plan.DeferResponsePlan)
			_, sIsDefer := planS.(*plan.DeferResponsePlan)
			if _, isSub := planS.(*plan.SubscriptionResponsePlan); isSub {
				div = CompareSubscriptionPlans(planU, planS)
			} else if uIsDefer || sIsDefer {
				div = CompareDeferPlans(planU, planS)
			} else {
				div = CompareResponseShapes(planU, planS)
			}
			if div != nil {
				t.Fatalf("semantic divergence between modes: %s", div)
			}
		})
	}
}

// canonFetches renders a plan's fetch tree canonically for the byte-equality comparison, across
// the three plan kinds the facade emits (mirrors the audit gate's renderer).
func canonFetches(t *testing.T, p plan.Plan) string {
	t.Helper()
	var trigger *resolve.GraphQLSubscriptionTrigger
	var fetches []*resolve.FetchItem
	switch pl := p.(type) {
	case *plan.SynchronousResponsePlan:
		if pl == nil || pl.Response == nil {
			t.Fatalf("nil SynchronousResponsePlan")
		}
		fetches = pl.Response.RawFetches
	case *plan.SubscriptionResponsePlan:
		if pl == nil || pl.Response == nil || pl.Response.Response == nil {
			t.Fatalf("nil SubscriptionResponsePlan")
		}
		trigger = &pl.Response.Trigger
		fetches = pl.Response.Response.RawFetches
	case *plan.DeferResponsePlan:
		if pl == nil || pl.Response == nil || pl.Response.Response == nil {
			t.Fatalf("nil DeferResponsePlan")
		}
		fetches = pl.Response.Response.RawFetches
	default:
		t.Fatalf("unexpected plan type %T", p)
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
		doc := ""
		if sf.QueryPlan != nil {
			doc = sf.QueryPlan.Query
		}
		fmt.Fprintf(&b, "fetch %d sg=%s deps=%v defer=%d path=%s doc=%s\n",
			i, sf.DataSourceIdentifier, sf.DependsOnFetchIDs, sf.DeferID, item.ResponsePath, doc)
	}
	return b.String()
}
