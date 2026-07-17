package planv2

// subscription_scope_test.go pins the D11.12 operation-kind ROOT-SCOPING guarantee: a schema that
// merely DECLARES a Subscription type must not perturb query/mutation planning -- byte-identical
// plans AND byte-identical D10 RouteFallbacks with and without the declaration. The audit corpus
// cannot catch this (it carries no subscription-declaring schemas); these synthetic shapes must.

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
)

// planAndFallbacks plans op through the facade and returns the plan's JSON rendering plus a
// deterministic rendering of the D10 route-fallback diagnostics.
func planAndFallbacks(t *testing.T, ds []plan.DataSource, schema, op string) (string, string) {
	t.Helper()
	p := mustPlanner(t, ds)
	opDoc, defDoc, report := parseAndNormalize(t, schema, op)
	pl, diags := p.PlanWithDiagnostics(opDoc, defDoc, "", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	fb := fmt.Sprintf("n=%d", len(diags.RouteFallbacks))
	for _, f := range diags.RouteFallbacks {
		fb += fmt.Sprintf(" [%s %s rf=%s sg=%s]", f.Kind, f.Coordinate, f.RootField, f.Subgraph)
	}
	return canonPlan(pl), fb
}

// canonPlan renders a plan into a deterministic byte string for exact comparison: structs/slices/
// maps recursively, []byte as strings, map keys sorted, func/chan fields skipped (encoding/json
// rejects resolve.SkipArrayItem and friends outright). Pointer identity is never printed, so two
// separately planned identical plans render identically.
func canonPlan(v any) string {
	var b strings.Builder
	writeCanon(&b, reflect.ValueOf(v))
	return b.String()
}

func writeCanon(b *strings.Builder, v reflect.Value) {
	if !v.IsValid() {
		b.WriteString("nil")
		return
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		writeCanon(b, v.Elem())
	case reflect.Struct:
		t := v.Type()
		b.WriteString(t.Name())
		b.WriteByte('{')
		for i := 0; i < t.NumField(); i++ {
			fv := v.Field(i)
			if k := fv.Kind(); k == reflect.Func || k == reflect.Chan {
				continue // non-comparable plumbing (e.g. resolve.Array.SkipItem)
			}
			b.WriteString(t.Field(i).Name)
			b.WriteByte(':')
			writeCanon(b, fv)
			b.WriteByte(' ')
		}
		b.WriteByte('}')
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			fmt.Fprintf(b, "%q", v.Bytes())
			return
		}
		b.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			writeCanon(b, v.Index(i))
			b.WriteByte(' ')
		}
		b.WriteByte(']')
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		b.WriteByte('{')
		for _, k := range keys {
			fmt.Fprintf(b, "%v:", k)
			writeCanon(b, v.MapIndex(k))
			b.WriteByte(' ')
		}
		b.WriteByte('}')
	case reflect.Func, reflect.Chan:
		// unreachable (skipped at the struct level); keep output total just in case
		b.WriteString("fn")
	default:
		fmt.Fprintf(b, "%v", v)
	}
}

// assertUnperturbed plans the same query op against the base config and the subscription-declaring
// config and requires byte-identical plans and byte-identical fallbacks.
func assertUnperturbed(t *testing.T, base, withSub []plan.DataSource, schema, op string) {
	t.Helper()
	planA, fbA := planAndFallbacks(t, base, schema, op)
	planB, fbB := planAndFallbacks(t, withSub, schema, op)
	if fbA != fbB {
		t.Errorf("RouteFallbacks perturbed by a declared Subscription type:\n without: %s\n with:    %s", fbA, fbB)
	}
	if planA != planB {
		t.Errorf("query plan perturbed by a declared Subscription type:\n without: %s\n with:    %s", planA, planB)
	}
}

const narrowedUnionSupergraph = `
schema { query: Query }
type Query { wrapper: Wrapper }
type Wrapper { id: ID! action: Action }
union Action = Common | OnlyA | OnlyB
type Common { c: String }
type OnlyA { a: String }
type OnlyB { b: String }
`

// TestPlanner_SubscriptionRootDoesNotPerturbNarrowing is the D6p route-scoping shape from the
// customer sweep's fallback-perturbation class: subgraph B's entity copy is unreachable (its only
// key is resolvable:false), so the OnlyB member is narrowed to a response-only null. Declaring
// `type Subscription { wrapperUpdated: Wrapper }` on B must NOT make B's region count as reachable
// for a QUERY operation -- pre-fix it did, un-narrowing OnlyB and driving its goal through the D10
// fallback (a Warn diagnostic and a perturbed fetch tree on a pure query op).
func TestPlanner_SubscriptionRootDoesNotPerturbNarrowing(t *testing.T) {
	assertUnperturbed(t,
		hgtestdata.NarrowedUnionConfig(false),
		hgtestdata.NarrowedUnionConfig(true),
		narrowedUnionSupergraph,
		`{ wrapper { action { __typename ... on Common { c } ... on OnlyA { a } ... on OnlyB { b } } } }`)
}

// TestPlanner_SubscriptionRootDoesNotPerturbEntityJump covers the routing side on the Section 7.2
// entity-jump/@requires shape: a Subscription mirroring the query root field's name (the unmasked
// same-name foreign-root hazard) and one sorting lexicographically before it (the tie-order hazard)
// must both leave the query plan and its fallbacks byte-identical.
func TestPlanner_SubscriptionRootDoesNotPerturbEntityJump(t *testing.T) {
	op := `{ product { shippingEstimate } }`
	for _, field := range []string{"product", "aProductUpdated"} {
		t.Run(field, func(t *testing.T) {
			assertUnperturbed(t,
				hgtestdata.EntityJumpConfig(),
				hgtestdata.EntityJumpConfigWithSubscription(field),
				entityJumpSupergraph, op)
		})
	}
}

const renamedRootSupergraph = `
schema { query: Query subscription: SubscriptionRoot }
type Query { ping: String other: String }
type SubscriptionRoot { productUpdated(upc: String!): Product }
type Product { id: ID! name: String price: Float }
`

// TestPlanner_SubscriptionRenamedRoot pins the D5pp-analog fix (customer-sweep "obligation 0
// unreachable" family): the owner subgraph renames its subscription root and the metadata lists the
// root field under the renamed name; the payload types are pure subscription payloads, so without
// the per-subgraph subscription root view every leaf was unreachable. The subscription must plan --
// a trigger against A selecting the whole payload locally.
func TestPlanner_SubscriptionRenamedRoot(t *testing.T) {
	p := mustPlanner(t, hgtestdata.SubscriptionRenamedRootConfig())
	op, def, report := parseAndNormalize(t, renamedRootSupergraph,
		`subscription($upc: String!) { productUpdated(upc: $upc) { id name price } }`)

	result := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("renamed-subscription-root operation must plan, got: %s", report.Error())
	}
	sub, ok := result.(*plan.SubscriptionResponsePlan)
	if !ok {
		t.Fatalf("want *plan.SubscriptionResponsePlan, got %T", result)
	}
	doc := sub.Response.Trigger.QueryPlan.Query
	for _, want := range []string{"subscription", "productUpdated(upc: $upc)", "name", "price"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("trigger document missing %q: %s", want, doc)
		}
	}
	if got := len(sub.Response.Response.RawFetches); got != 0 {
		t.Fatalf("payload is owner-local; want 0 per-event fetches, got %d", got)
	}
}

// TestPlanner_SubscriptionMetadataOnlyFailsLoud pins the residual class: metadata declares the
// subscription root field but no SDL available to the builder can resolve its output type (schema
// drift / pubsub-owned field). The planner must fail with the typed no-valid-plan error -- loud,
// never a silent wrong plan (Section 6.3).
func TestPlanner_SubscriptionMetadataOnlyFailsLoud(t *testing.T) {
	p := mustPlanner(t, hgtestdata.SubscriptionMetadataOnlyConfig())
	op, def, report := parseAndNormalize(t, subscriptionProductSupergraph,
		`subscription UpdatePrice($upc: String!) { productUpdated(upc: $upc) { name price } }`)

	result := p.Plan(op, def, "UpdatePrice", report)
	if result != nil {
		t.Fatalf("metadata-only subscription root must not plan, got %T", result)
	}
	if !report.HasErrors() || !strings.Contains(report.Error(), "no valid plan") {
		t.Fatalf("want the typed no-valid-plan error, got %q", report.Error())
	}
}

const billingSupergraph = `
schema { query: Query mutation: Mutation }
type Query { currentPlan: Subscription }
type Mutation { planCancel(id: ID!): Subscription }
type Subscription { id: ID! endsAt: String region: String holder: Holder }
type Holder { id: ID! name: String }
`

const billingRenamedSupergraph = `
schema { query: Query mutation: Mutation }
type Query { currentPlan: BillingSubscription }
type Mutation { planCancel(id: ID!): BillingSubscription }
type BillingSubscription { id: ID! endsAt: String region: String holder: Holder }
type Holder { id: ID! name: String }
`

const billingWithRealtimeSupergraph = `
schema { query: Query mutation: Mutation subscription: RealtimeSubscription }
type Query { currentPlan: Subscription ping: String }
type Mutation { planCancel(id: ID!): Subscription }
type Subscription { id: ID! endsAt: String region: String holder: Holder }
type Holder { id: ID! name: String }
type RealtimeSubscription { tick: Tick }
type Tick { id: ID! value: Float }
`

// billingOps are the query and mutation shapes of the re-sweep regression: both traverse the
// billing entity named `Subscription` (owner fields + a cross-subgraph entity jump to holder).
var billingOps = []struct{ name, op string }{
	{"query", `{ currentPlan { endsAt region holder { name } } }`},
	{"mutation", `mutation Cancel($id: ID!) { planCancel(id: $id) { endsAt holder { name } } }`},
}

// TestPlanner_DataTypeNamedSubscription pins the re-sweep regression class: an ENTITY literally
// named `Subscription` (billing/commerce) with NO subscription operation roots anywhere. It is
// ordinary data: queries and mutations traversing it must plan exactly as they would if the type
// were named `BillingSubscription` (compared modulo the type name in the rendered plan), with zero
// route fallbacks -- never be re-tailed onto the subscription operation root and masked out of
// query/mutation routing (the 49-op ErrNoValidPlan regression).
func TestPlanner_DataTypeNamedSubscription(t *testing.T) {
	for _, tc := range billingOps {
		t.Run(tc.name, func(t *testing.T) {
			planA, fbA := planAndFallbacks(t, hgtestdata.BillingSubscriptionConfig("Subscription"), billingSupergraph, tc.op)
			opRenamed := tc.op // the ops name no types, so they run unchanged against the twin
			planB, fbB := planAndFallbacks(t, hgtestdata.BillingSubscriptionConfig("BillingSubscription"), billingRenamedSupergraph, opRenamed)
			// canonPlan renders []byte fields as plain strings, so one type-name replacement
			// normalizes the twin everywhere it can appear (documents, fragments, OnTypeNames).
			planBNormalized := strings.ReplaceAll(planB, "BillingSubscription", "Subscription")
			if fbA != fbB {
				t.Errorf("fallbacks differ from the renamed twin:\n Subscription:        %s\n BillingSubscription: %s", fbA, fbB)
			}
			if fbA != "n=0" {
				t.Errorf("billing traversal must not fall back, got %s", fbA)
			}
			if planA != planBNormalized {
				t.Errorf("plan differs from the renamed twin (modulo type name):\n Subscription:        %s\n BillingSubscription: %s", planA, planBNormalized)
			}
		})
	}
}

// TestPlanner_DataTypeNamedSubscriptionWithRealtimeRoots is the sharp sub-case: the same billing
// entity named `Subscription` PLUS a third subgraph declaring a genuine subscription operation
// root (renamed, `schema { subscription: RealtimeSubscription }`). Per subgraph, the same-named
// type stays plain data in billing/holders; queries/mutations must plan byte-identically to the
// realtime-free config, and the genuine subscription root must still work.
func TestPlanner_DataTypeNamedSubscriptionWithRealtimeRoots(t *testing.T) {
	for _, tc := range billingOps {
		t.Run(tc.name, func(t *testing.T) {
			planA, fbA := planAndFallbacks(t, hgtestdata.BillingSubscriptionConfig("Subscription"), billingSupergraph, tc.op)
			planB, fbB := planAndFallbacks(t, hgtestdata.BillingSubscriptionWithRealtimeConfig(), billingWithRealtimeSupergraph, tc.op)
			if fbA != fbB {
				t.Errorf("fallbacks perturbed by the realtime subgraph:\n without: %s\n with:    %s", fbA, fbB)
			}
			if planA != planB {
				t.Errorf("query/mutation plan perturbed by the realtime subgraph:\n without: %s\n with:    %s", planA, planB)
			}
		})
	}

	// The genuine (renamed) subscription root still plans: trigger against realtime.
	t.Run("realtime-subscription", func(t *testing.T) {
		p := mustPlanner(t, hgtestdata.BillingSubscriptionWithRealtimeConfig())
		op, def, report := parseAndNormalize(t, billingWithRealtimeSupergraph, `subscription { tick { id value } }`)
		result := p.Plan(op, def, "", report)
		if report.HasErrors() {
			t.Fatalf("realtime subscription must plan: %s", report.Error())
		}
		sub, ok := result.(*plan.SubscriptionResponsePlan)
		if !ok {
			t.Fatalf("want *plan.SubscriptionResponsePlan, got %T", result)
		}
		if doc := sub.Response.Trigger.QueryPlan.Query; !strings.Contains(doc, "tick") {
			t.Fatalf("trigger must select tick: %s", doc)
		}
	})
}

const billingRootNodesOnlySupergraph = `
schema { query: Query mutation: Mutation }
type Query { currentPlan: Subscription other: String }
type Mutation { planCancel(id: ID!): PlanCancelPayload }
type PlanCancelPayload { subscription: Subscription }
type Subscription { id: ID! availablePlans: [String] queueSize: Int endsAt: String region: String holder: Holder }
type Holder { id: ID! name: String }
`

const billingRootNodesOnlyRenamedSupergraph = `
schema { query: Query mutation: Mutation }
type Query { currentPlan: BillingSubscription other: String }
type Mutation { planCancel(id: ID!): PlanCancelPayload }
type PlanCancelPayload { subscription: BillingSubscription }
type BillingSubscription { id: ID! availablePlans: [String] queueSize: Int endsAt: String region: String holder: Holder }
type Holder { id: ID! name: String }
`

// TestPlanner_DataTypeNamedSubscriptionRootNodesOnly pins the VERIFIED customer-config shape the
// keys/child evidence cannot decide (the re-sweep's remaining 39-op case): the `Subscription`-named
// data type sits in rootNodes with its full field list, has zero keys, zero childNodes entries,
// and no SDL schema block -- the only data-ness evidence is the SDL referencing the type as an
// OUTPUT TYPE (Query.currentPlan, PlanCancelPayload.subscription). Its fields must
// object-tail; the regression's query and mutation shapes must plan identically to the renamed
// twin with zero fallbacks.
func TestPlanner_DataTypeNamedSubscriptionRootNodesOnly(t *testing.T) {
	ops := []struct{ name, op string }{
		{"query", `{ currentPlan { endsAt region holder { name } } }`},
		{"mutation", `mutation Cancel($id: ID!) { planCancel(id: $id) { subscription { endsAt queueSize availablePlans } } }`},
	}
	for _, tc := range ops {
		t.Run(tc.name, func(t *testing.T) {
			planA, fbA := planAndFallbacks(t, hgtestdata.BillingRootNodesOnlyConfig("Subscription"), billingRootNodesOnlySupergraph, tc.op)
			planB, fbB := planAndFallbacks(t, hgtestdata.BillingRootNodesOnlyConfig("BillingSubscription"), billingRootNodesOnlyRenamedSupergraph, tc.op)
			planBNormalized := strings.ReplaceAll(planB, "BillingSubscription", "Subscription")
			if fbA != fbB {
				t.Errorf("fallbacks differ from the renamed twin:\n Subscription:        %s\n BillingSubscription: %s", fbA, fbB)
			}
			if fbA != "n=0" {
				t.Errorf("billing traversal must not fall back, got %s", fbA)
			}
			if planA != planBNormalized {
				t.Errorf("plan differs from the renamed twin (modulo type name):\n Subscription:        %s\n BillingSubscription: %s", planA, planBNormalized)
			}
		})
	}
}

const mergedDualRoleSupergraph = `
schema { query: Query mutation: Mutation subscription: Subscription }
type Query { currentPlan: Subscription }
type Mutation { planCancel(id: ID!): PlanCancelPayload }
type PlanCancelPayload { subscription: Subscription }
type Subscription { id: ID! availablePlans: [String] queueSize: Int endsAt: String region: String holder: Holder realtimeTick: Tick }
type Holder { id: ID! name: String }
type Tick { id: ID! value: Float }
`

// TestPlanner_MergedDualRoleSubscriptionType pins the GROUND-TRUTH customer shape: one composed
// type named `Subscription` that is SIMULTANEOUSLY the subscription operation root (the realtime
// field, anchored via the billing SDL's explicit `schema { subscription: Subscription }` block and
// the realtime subgraph's default naming) AND a data object (the billing payload fields,
// referenced by `Query.currentPlan` / `PlanCancelPayload.subscription`). Under the
// dual-role model the type's field edges are ALWAYS data-routable (object-tailed) for every
// operation kind, and the subscription ANCHOR exists in parallel, kind-masked -- so the query and
// mutation shapes plan with zero fallbacks AND a subscription on the realtime field plans via the
// anchor.
func TestPlanner_MergedDualRoleSubscriptionType(t *testing.T) {
	ops := []struct{ name, op string }{
		{"query", `{ currentPlan { endsAt region holder { name } } }`},
		{"mutation", `mutation Cancel($id: ID!) { planCancel(id: $id) { subscription { endsAt queueSize availablePlans } } }`},
	}
	for _, tc := range ops {
		t.Run(tc.name, func(t *testing.T) {
			_, fb := planAndFallbacks(t, hgtestdata.MergedDualRoleConfig(), mergedDualRoleSupergraph, tc.op)
			if fb != "n=0" {
				t.Errorf("dual-role data traversal must not fall back, got %s", fb)
			}
		})
	}

	t.Run("subscription", func(t *testing.T) {
		p := mustPlanner(t, hgtestdata.MergedDualRoleConfig())
		op, def, report := parseAndNormalize(t, mergedDualRoleSupergraph, `subscription { realtimeTick { id value } }`)
		result := p.Plan(op, def, "", report)
		if report.HasErrors() {
			t.Fatalf("realtime subscription on the merged type must plan via the anchor: %s", report.Error())
		}
		sub, ok := result.(*plan.SubscriptionResponsePlan)
		if !ok {
			t.Fatalf("want *plan.SubscriptionResponsePlan, got %T", result)
		}
		doc := sub.Response.Trigger.QueryPlan.Query
		if !strings.HasPrefix(doc, "subscription") || !strings.Contains(doc, "realtimeTick") {
			t.Fatalf("trigger must anchor the realtime field: %s", doc)
		}
		if string(sub.Response.Trigger.SourceName) != "realtime" {
			t.Fatalf("trigger must target the realtime subgraph, got %q", sub.Response.Trigger.SourceName)
		}
	})
}
