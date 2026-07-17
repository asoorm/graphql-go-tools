package planv2

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

const entityJumpSupergraph = `
schema { query: Query }
type Query { product: Product }
type Product { id: ID! organization: Organization dimensions: Dimensions shippingEstimate: Float }
type Organization { id: ID! }
type Dimensions { length: Float width: Float height: Float }
`

const partialUnionSupergraph = `
schema { query: Query }
type Query { wrapper: Wrapper }
type Wrapper { id: ID! action: Action }
union Action = Common | OnlyA | OnlyB
type Common { c: String }
type OnlyA { a: String }
type OnlyB { b: String }
`

// TestPlanner_EntityJump drives Section 7.2 end-to-end THROUGH the facade: NewPlanner compiles H once, Plan
// lowers `{ product { shippingEstimate } }` to a two-fetch plan (root A + _entities B), and the plan
// survives the untouched postprocess pipeline (L14b). It also pins the typed-leaf carry-forward:
// shippingEstimate is resolve.Float, not resolve.String.
func TestPlanner_EntityJump(t *testing.T) {
	p := mustPlanner(t, hgtestdata.EntityJumpConfig())
	op, def, report := parseAndNormalize(t, entityJumpSupergraph, `{ product { shippingEstimate } }`)

	result := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sp, ok := result.(*plan.SynchronousResponsePlan)
	if !ok {
		t.Fatalf("want *plan.SynchronousResponsePlan, got %T", result)
	}

	// Two fetches (D11.1): one root, one _entities jump.
	if got := len(sp.Response.RawFetches); got != 2 {
		t.Fatalf("want 2 fetches, got %d", got)
	}
	var jump, root int
	for _, f := range sp.Response.RawFetches {
		if f.Fetch.(*resolve.SingleFetch).RequiresEntityFetch {
			jump++
		} else {
			root++
		}
	}
	if jump != 1 || root != 1 {
		t.Fatalf("want one root + one jump fetch, got root=%d jump=%d", root, jump)
	}

	// Typed leaf: shippingEstimate: Float must lower to resolve.Float (else walkFloat's value would
	// be routed through walkString and hard-error at execution).
	product := sp.Response.Data.Fields[0]
	shipping := product.Value.(*resolve.Object).Fields[0]
	if shipping.Value.NodeKind() != resolve.NodeKindFloat {
		t.Fatalf("shippingEstimate must be resolve.Float, got %v", shipping.Value.NodeKind())
	}

	// L14b: the emitted plan must process without panic through the untouched postprocess pipeline.
	postprocess.NewProcessor().Process(sp)
}

// TestPlanner_PartialUnion drives Section 7.1 through the facade: the exclusive members OnlyA/OnlyB are
// D6-narrowed to response-only nulls, so the response keeps a/b gated on __typename with no
// producing fetch -- the response shape the client sees is preserved (I4).
func TestPlanner_PartialUnion(t *testing.T) {
	p := mustPlanner(t, hgtestdata.PartialUnionConfig())
	op, def, report := parseAndNormalize(t, partialUnionSupergraph,
		`{ wrapper { action { __typename ... on Common { c } ... on OnlyA { a } ... on OnlyB { b } } } }`)

	result := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sp := result.(*plan.SynchronousResponsePlan)

	// Response shape preserved: action selects __typename + all three members' fields, each gated.
	shape := canonResolveFields(sp.Response.Data.Fields)
	for _, want := range []string{"__typename", "c@Common", "a@OnlyA", "b@OnlyB"} {
		if !strings.Contains(shape, want) {
			t.Fatalf("response shape missing %q: %s", want, shape)
		}
	}
	// Narrowed members are response-only nulls: no fetch document resolves OnlyA/OnlyB.
	for _, f := range sp.Response.RawFetches {
		doc := f.Fetch.(*resolve.SingleFetch).FetchConfiguration.QueryPlan.Query
		if strings.Contains(doc, "on OnlyA") || strings.Contains(doc, "on OnlyB") {
			t.Fatalf("narrowed member must not appear in any fetch document: %q", doc)
		}
	}
}

const subscriptionProductSupergraph = `
schema { query: Query subscription: Subscription }
type Query { product: Product }
type Subscription { productUpdated(upc: String!): Product }
type Product { id: ID! name: String price: Float }
`

// TestPlanner_Subscription drives D11.12 through the facade: a subscription operation produces a
// plan.SubscriptionResponsePlan whose trigger carries the `subscription` document against the root
// subgraph (A) and whose response tree carries the per-event `_entities` jump (B) -- and the plan
// survives the untouched postprocess pipeline (which compiles the trigger's input template).
func TestPlanner_Subscription(t *testing.T) {
	p := mustPlanner(t, hgtestdata.SubscriptionProductConfig())
	op, def, report := parseAndNormalize(t, subscriptionProductSupergraph,
		`subscription UpdatePrice($upc: String!) { productUpdated(upc: $upc) { name price } }`)

	result := p.Plan(op, def, "UpdatePrice", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sub, ok := result.(*plan.SubscriptionResponsePlan)
	if !ok {
		t.Fatalf("want *plan.SubscriptionResponsePlan, got %T", result)
	}

	// Trigger: the subscription document against A, with the client variable forwarded at $$0$$.
	trigger := sub.Response.Trigger
	if trigger.QueryPlan == nil || !strings.HasPrefix(trigger.QueryPlan.Query, "subscription") {
		t.Fatalf("trigger document must use the subscription keyword, got %+v", trigger.QueryPlan)
	}
	for _, want := range []string{"productUpdated(upc: $upc)", "name"} {
		if !strings.Contains(trigger.QueryPlan.Query, want) {
			t.Fatalf("trigger document missing %q: %s", want, trigger.QueryPlan.Query)
		}
	}
	if !strings.Contains(string(trigger.Input), `"variables":{"upc":$$0$$}`) {
		t.Fatalf("trigger input must forward $upc as $$0$$: %s", trigger.Input)
	}
	if len(trigger.Variables) != 1 {
		t.Fatalf("trigger must carry exactly the forwarded context variable, got %d", len(trigger.Variables))
	}

	// Response: exactly one per-event entity fetch (price via B), depending on the trigger's id slot.
	resp := sub.Response.Response
	if resp == nil || resp.Info == nil || resp.Info.OperationType != ast.OperationTypeSubscription {
		t.Fatalf("response Info must carry OperationType subscription, got %+v", resp.Info)
	}
	if len(resp.RawFetches) != 1 {
		t.Fatalf("want exactly 1 per-event entity fetch, got %d", len(resp.RawFetches))
	}
	sf := resp.RawFetches[0].Fetch.(*resolve.SingleFetch)
	if !sf.RequiresEntityFetch && !sf.RequiresEntityBatchFetch {
		t.Fatal("the per-event fetch below the root must be an entity fetch")
	}
	if !strings.Contains(sf.FetchConfiguration.QueryPlan.Query, "price") {
		t.Fatalf("entity fetch must resolve price: %s", sf.FetchConfiguration.QueryPlan.Query)
	}
	if !strings.HasPrefix(sf.FetchConfiguration.QueryPlan.Query, "query") {
		t.Fatalf("entity fetches stay `query` regardless of operation type: %s", sf.FetchConfiguration.QueryPlan.Query)
	}

	// Response shape: the client selection tree, root field included (I4).
	if got := string(resp.Data.Fields[0].Name); got != "productUpdated" {
		t.Fatalf("response root field must be productUpdated, got %q", got)
	}

	// L14b: the plan must process through the untouched postprocess pipeline (trigger template compile
	// included) without panic.
	postprocess.NewProcessor().Process(sub)
	if trigger := &sub.Response.Trigger; trigger.InputTemplate.Segments == nil {
		t.Fatal("postprocess must compile the trigger input template")
	}
}

const edfsSubscriptionSupergraph = `
schema { query: Query subscription: Subscription }
type Query { product: Product }
type Subscription { productUpdated(id: ID!): Product }
type Product { id: ID! name: String price: Float }
`

// TestPlanner_EDFSSubscription drives the M4.2 EDFS worked example (FEDERATION_SEMANTICS FS-EDFS,
// FORMAL_SPEC D5-EDFS / D11.12-EDFS) at the facade: an EDFS event-source subscription root (subgraph
// P owns `productUpdated` as a NATS `@edfs__natsSubscribe` field; its SDL is the composed subgraph
// SDL the router emits) plans as a first-class root entrance. The trigger targets the event source
// P, and the payload's cross-subgraph field (`price`, owned by the HTTP subgraph B) rides a
// per-event `_entities` query fetch -- byte-identical plan SHAPE to the ordinary-subscription twin
// (TestPlanner_Subscription); only the trigger TRANSPORT (a broker binding vs WebSocket/SSE) differs
// at lowering, which the planner/search layer never sees (FS-EDFS-2: below the root is FS-SUB-3).
// This pins the (a)+(b) baseline -- the common Cosmo shape where the event source contributes SDL.
// The SDL-less PUBSUB direction (composed-schema-only payload typing) is the residual DV-011.
func TestPlanner_EDFSSubscription(t *testing.T) {
	p := mustPlanner(t, hgtestdata.EDFSSubscriptionConfig())
	op, def, report := parseAndNormalize(t, edfsSubscriptionSupergraph,
		`subscription UpdateProduct($id: ID!) { productUpdated(id: $id) { name price } }`)

	result := p.Plan(op, def, "UpdateProduct", report)
	if report.HasErrors() {
		t.Fatalf("EDFS subscription must plan (first-class root entrance): %s", report.Error())
	}
	sub, ok := result.(*plan.SubscriptionResponsePlan)
	if !ok {
		t.Fatalf("want *plan.SubscriptionResponsePlan, got %T", result)
	}

	// Trigger: the subscription document against the event source P, with the client id forwarded.
	trigger := sub.Response.Trigger
	if trigger.QueryPlan == nil || !strings.HasPrefix(trigger.QueryPlan.Query, "subscription") {
		t.Fatalf("EDFS trigger must use the subscription keyword, got %+v", trigger.QueryPlan)
	}
	if string(trigger.SourceName) != "P" {
		t.Fatalf("EDFS trigger must target the event source P, got %q", trigger.SourceName)
	}
	for _, want := range []string{"productUpdated(id: $id)", "name"} {
		if !strings.Contains(trigger.QueryPlan.Query, want) {
			t.Fatalf("EDFS trigger document missing %q: %s", want, trigger.QueryPlan.Query)
		}
	}

	// Response: exactly one per-event entity fetch for `price` (owned by the HTTP subgraph B),
	// a `query` regardless of the subscription operation type (FS-SUB-4).
	resp := sub.Response.Response
	if len(resp.RawFetches) != 1 {
		t.Fatalf("want exactly 1 per-event entity fetch (price via B), got %d", len(resp.RawFetches))
	}
	sf := resp.RawFetches[0].Fetch.(*resolve.SingleFetch)
	if !sf.RequiresEntityFetch && !sf.RequiresEntityBatchFetch {
		t.Fatal("the per-event fetch below the EDFS root must be an entity fetch")
	}
	if string(sf.DataSourceIdentifier) != "B" {
		t.Fatalf("the per-event entity fetch must target subgraph B, got %q", sf.DataSourceIdentifier)
	}
	if !strings.HasPrefix(sf.FetchConfiguration.QueryPlan.Query, "query") ||
		!strings.Contains(sf.FetchConfiguration.QueryPlan.Query, "price") {
		t.Fatalf("entity fetch must be a `query` resolving price: %s", sf.FetchConfiguration.QueryPlan.Query)
	}

	// The plan survives the untouched postprocess pipeline (trigger template compile included).
	postprocess.NewProcessor().Process(sub)
	if sub.Response.Trigger.InputTemplate.Segments == nil {
		t.Fatal("postprocess must compile the EDFS trigger input template")
	}
}

// TestPlanner_SubscriptionSingleRootField pins the D11.12 precondition: a subscription with more
// than one root field is rejected with a typed error (GraphQL spec Section 5.2.3.1), never mis-lowered.
func TestPlanner_SubscriptionSingleRootField(t *testing.T) {
	p := mustPlanner(t, hgtestdata.SubscriptionProductConfig())
	op, def, report := parseAndNormalize(t, subscriptionProductSupergraph,
		`subscription Two($a: String! $b: String!) { x: productUpdated(upc: $a) { name } y: productUpdated(upc: $b) { name } }`)

	result := p.Plan(op, def, "Two", report)
	if result != nil {
		t.Fatalf("multi-root subscription must return a nil plan, got %T", result)
	}
	if !report.HasErrors() || !strings.Contains(report.Error(), "exactly one root field") {
		t.Fatalf("multi-root subscription must record the typed single-root-field error, got %q", report.Error())
	}
}

// TestPlanner_OptsAccepted pins carry-forward #4: Plan accepts the variadic plan.Opts (including
// IncludeQueryPlanInResponse) for drop-in compatibility and plans successfully; M1 honors none of
// them yet (documented no-op), so the plan is identical to the no-opts case.
func TestPlanner_OptsAccepted(t *testing.T) {
	p := mustPlanner(t, hgtestdata.EntityJumpConfig())
	op, def, report := parseAndNormalize(t, entityJumpSupergraph, `{ product { shippingEstimate } }`)

	result := p.Plan(op, def, "", report, plan.IncludeQueryPlanInResponse())
	if report.HasErrors() {
		t.Fatalf("plan with opts reported errors: %s", report.Error())
	}
	if result == nil {
		t.Fatal("plan with opts must not be nil")
	}
}

// --- helpers ---------------------------------------------------------------------------------

func mustPlanner(t *testing.T, ds []plan.DataSource) *Planner {
	t.Helper()
	p, err := NewPlanner(plan.Configuration{DataSources: ds})
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	return p
}

// parseAndNormalize runs the exact pipeline the facade pins (and the differential harness reuses):
// merge the definition with the base schema, then normalize the operation with the v1 option set.
func parseAndNormalize(t *testing.T, schema, operation string) (*ast.Document, *ast.Document, *operationreport.Report) {
	t.Helper()
	def := unsafeparser.ParseGraphqlDocumentString(schema)
	if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
		t.Fatalf("merge base schema: %v", err)
	}
	op := unsafeparser.ParseGraphqlDocumentString(operation)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
	).NormalizeOperation(&op, &def, report)
	if report.HasErrors() {
		t.Fatalf("normalize: %s", report.Error())
	}
	return &op, &def, report
}

// canonResolveFields renders a resolve field list as `key@Gate{children}` (gate omitted when
// ungated, braces omitted for leaves) -- enough to assert response-shape keys and __typename gates.
func canonResolveFields(fields []*resolve.Field) string {
	var parts []string
	for _, f := range fields {
		s := string(f.Name)
		if len(f.OnTypeNames) > 0 {
			names := make([]string, len(f.OnTypeNames))
			for i, n := range f.OnTypeNames {
				names[i] = string(n)
			}
			s += "@" + strings.Join(names, "|")
		}
		if obj, ok := f.Value.(*resolve.Object); ok {
			s += "{" + canonResolveFields(obj.Fields) + "}"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
