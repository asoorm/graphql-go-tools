package differential

// exec_subfilter_test.go is the EXECUTED-TRUTH witness for @openfed__subscriptionFilter (M4.4). A
// plan-shape assertion ("the Filter field is populated") is necessary but NOT sufficient: the
// security property is that an event the filter says to SKIP is actually skipped and an event that
// PASSES is actually delivered. This test builds the SAME plan.Configuration through BOTH planners,
// extracts the resolve.SubscriptionFilter each emits, and drives resolve.SubscriptionFilter.SkipEvent
// -- the exact predicate the resolver runs per event (resolve.go evalFilter) -- proving planv2 skips
// the must-skip event, delivers the must-pass event, and agrees with v1 on every event. Pre-fix (the
// refusal removed but no filter built) planv2's Filter is nil and SkipEvent returns false for every
// event -- the must-skip event is DELIVERED, the data-exposure defect this witness exists to catch.

import (
	"testing"

	"github.com/wundergraph/astjson"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// subFilterFixture is the shared subscription-filter case: an IN filter delivering only events whose
// price equals the forwarded `min` argument. Numeric (Int) to keep SkipEvent's type handling
// deterministic across the string-marshal path.
const (
	subFilterSchema = `
schema { query: Query subscription: Subscription }
type Query { productUpdates(min: Int!): Product }
type Subscription { productUpdates(min: Int!): Product }
type Product { id: ID! price: Int }
`
	subFilterOp = `subscription($min: Int!) { productUpdates(min: $min) { id price } }`
)

func subFilterSubgraphs() []subgraph {
	return []subgraph{
		{
			name: "products",
			sdl: `
type Query { productUpdates(min: Int!): Product }
type Subscription { productUpdates(min: Int!): Product }
type Product { id: ID! price: Int }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"productUpdates"}},
					{TypeName: "Subscription", FieldNames: []string{"productUpdates"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Product", FieldNames: []string{"id", "price"}},
				},
			},
		},
	}
}

func subFilterFields() plan.FieldConfigurations {
	return plan.FieldConfigurations{
		{
			TypeName:  "Subscription",
			FieldName: "productUpdates",
			Arguments: plan.ArgumentsConfigurations{
				{Name: "min", SourceType: plan.FieldArgumentSource},
			},
			SubscriptionFilterCondition: &plan.SubscriptionFilterCondition{
				In: &plan.SubscriptionFieldCondition{
					FieldPath: []string{"price"},
					Values:    []string{"{{ args.min }}"},
				},
			},
		},
	}
}

// planSubscriptionFilter plans subFilterOp with the requested planner and returns the built filter.
func planV2SubscriptionFilter(t *testing.T) *resolve.SubscriptionFilter {
	t.Helper()
	cfg := buildConfig(t, subFilterSubgraphs(), subFilterFields()...)
	p, err := planv2.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("planv2.NewPlanner must ACCEPT a config carrying a SubscriptionFilterCondition: %v", err)
	}
	op, def, report := parseAndNormalize(t, subFilterSchema, subFilterOp)
	out := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("planv2 planning failed: %s", report.Error())
	}
	sub, ok := out.(*plan.SubscriptionResponsePlan)
	if !ok {
		t.Fatalf("planv2 must produce a SubscriptionResponsePlan, got %T", out)
	}
	return sub.Response.Filter
}

func planV1SubscriptionFilter(t *testing.T) *resolve.SubscriptionFilter {
	t.Helper()
	cfg := buildConfig(t, subFilterSubgraphs(), subFilterFields()...)
	p, err := plan.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("v1 NewPlanner: %v", err)
	}
	op, def, report := parseAndNormalize(t, subFilterSchema, subFilterOp)
	out := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("v1 planning failed: %s", report.Error())
	}
	sub, ok := out.(*plan.SubscriptionResponsePlan)
	if !ok {
		t.Fatalf("v1 must produce a SubscriptionResponsePlan, got %T", out)
	}
	return sub.Response.Filter
}

// TestSubscriptionFilter_ExecutedTruth is the core security witness: planv2's emitted filter must
// SKIP the must-skip event and DELIVER the must-pass event, and its decision must equal v1's on
// every event.
func TestSubscriptionFilter_ExecutedTruth(t *testing.T) {
	v2Filter := planV2SubscriptionFilter(t)
	if v2Filter == nil {
		t.Fatal("planv2 emitted a nil subscription filter -- events that must be skipped would be delivered (data exposure)")
	}
	v1Filter := planV1SubscriptionFilter(t)
	if v1Filter == nil {
		t.Fatal("v1 emitted a nil subscription filter (fixture setup error)")
	}

	// ctx.Variables carries the forwarded argument min=100; the IN filter delivers only price==100.
	newCtx := func() *resolve.Context {
		return &resolve.Context{Variables: astjson.MustParseBytes([]byte(`{"min":100}`))}
	}

	cases := []struct {
		name     string
		event    string
		wantSkip bool // true = filter says skip (must NOT be delivered)
	}{
		{name: "pass: price equals min", event: `{"id":"p1","price":100}`, wantSkip: false},
		{name: "skip: price differs from min", event: `{"id":"p2","price":200}`, wantSkip: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v2Skip, err := v2Filter.SkipEvent(newCtx(), []byte(c.event))
			if err != nil {
				t.Fatalf("planv2 filter SkipEvent error: %v", err)
			}
			if v2Skip != c.wantSkip {
				t.Fatalf("planv2 filter: event %s: skip=%v, want %v (a wrong decision here is a data-exposure or dropped-event defect)",
					c.event, v2Skip, c.wantSkip)
			}
			// Parity with v1's runtime decision on the identical event.
			v1Skip, err := v1Filter.SkipEvent(newCtx(), []byte(c.event))
			if err != nil {
				t.Fatalf("v1 filter SkipEvent error: %v", err)
			}
			if v2Skip != v1Skip {
				t.Fatalf("planv2 and v1 disagree on event %s: planv2 skip=%v, v1 skip=%v", c.event, v2Skip, v1Skip)
			}
		})
	}
}

// TestSubscriptionFilter_V1Parity_Structure pins that planv2 builds the SAME filter STRUCTURE v1
// builds (FieldPath + rendered segments), independent of the executed-truth check -- a structural
// regression net alongside the behavioral one.
func TestSubscriptionFilter_V1Parity_Structure(t *testing.T) {
	if v2, v1 := canonFilter(planV2SubscriptionFilter(t)), canonFilter(planV1SubscriptionFilter(t)); v2 != v1 {
		t.Fatalf("planv2 filter structure diverges from v1:\n planv2: %s\n v1:    %s", v2, v1)
	}
}
