package planv2

// authz_info_test.go pins the M4.1 adoption-safety wave (PARITY.md Section 6): planv2 must emit
// resolve.FieldInfo on the response tree and FetchInfo.RootFields on fetches so that postprocess's
// collectAuthorizationCoordinates finds every @authenticated/@requiresScopes coordinate. Before the
// wave, planv2 emitted neither -- a protected field resolved UNAUTHORIZED silently (the register's
// loudest row). These tests are the facade-level red-first assertions; the differential package
// carries the v1-parity oracle on the same surfaces.

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// TestPlanner_AuthorizationCoordinates plans an operation whose two fields carry authorization
// rules (the compiled form of @authenticated/@requiresScopes: FieldConfiguration.HasAuthorizationRule)
// through the facade and asserts that the untouched v1 postprocess pipeline derives BOTH
// authorization coordinates -- one via FetchInfo.RootFields (the root fetch), one via the entity
// fetch's RootFields and the field's FieldInfo. An empty coordinate list means the BatchAuthorizer
// is never consulted and the protected fields resolve unauthorized -- the silent security degrade
// this wave closes.
func TestPlanner_AuthorizationCoordinates(t *testing.T) {
	cfg := plan.Configuration{
		DataSources: hgtestdata.EntityJumpConfig(),
		Fields: plan.FieldConfigurations{
			{TypeName: "Query", FieldName: "product", HasAuthorizationRule: true},
			{TypeName: "Product", FieldName: "shippingEstimate", HasAuthorizationRule: true},
		},
	}
	p, err := NewPlanner(cfg)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	op, def, report := parseAndNormalize(t, entityJumpSupergraph, `{ product { shippingEstimate } }`)
	result := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sp := result.(*plan.SynchronousResponsePlan)

	postprocess.NewProcessor().Process(sp)

	coords := sp.Response.Info.AuthorizationCoordinates
	want := map[resolve.GraphCoordinate]bool{
		{TypeName: "Query", FieldName: "product"}:            false,
		{TypeName: "Product", FieldName: "shippingEstimate"}: false,
	}
	for _, c := range coords {
		key := resolve.GraphCoordinate{TypeName: c.Coordinate.TypeName, FieldName: c.Coordinate.FieldName}
		if _, ok := want[key]; ok {
			want[key] = true
			if c.DataSourceID == "" {
				t.Errorf("coordinate %s.%s carries an empty DataSourceID", key.TypeName, key.FieldName)
			}
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("authorization coordinate %s.%s not collected (got %v) -- the BatchAuthorizer "+
				"would never be consulted for it", key.TypeName, key.FieldName, coords)
		}
	}
}

// TestPlanner_FieldInfoEmission asserts the response tree carries per-field resolve.FieldInfo (the
// v1 visitor contract: Name, ExactParentTypeName, ParentTypeNames, NamedType, Source IDs/Names,
// HasAuthorizationRule) -- the surface the router's schema-usage telemetry and the per-field
// authorization reader consume.
func TestPlanner_FieldInfoEmission(t *testing.T) {
	cfg := plan.Configuration{
		DataSources: hgtestdata.EntityJumpConfig(),
		Fields: plan.FieldConfigurations{
			{TypeName: "Product", FieldName: "shippingEstimate", HasAuthorizationRule: true},
		},
	}
	p, err := NewPlanner(cfg)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	op, def, report := parseAndNormalize(t, entityJumpSupergraph, `{ product { id shippingEstimate } }`)
	result := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sp := result.(*plan.SynchronousResponsePlan)

	product := sp.Response.Data.Fields[0]
	if product.Info == nil {
		t.Fatalf("Query.product carries no FieldInfo")
	}
	if product.Info.Name != "product" || product.Info.ExactParentTypeName != "Query" {
		t.Errorf("Query.product FieldInfo wrong: %+v", product.Info)
	}
	if product.Info.NamedType != "Product" {
		t.Errorf("Query.product NamedType: want Product, got %q", product.Info.NamedType)
	}
	if len(product.Info.Source.IDs) == 0 || len(product.Info.Source.Names) == 0 {
		t.Errorf("Query.product FieldInfo has no Source attribution: %+v", product.Info.Source)
	}

	for _, f := range product.Value.(*resolve.Object).Fields {
		if f.Info == nil {
			t.Fatalf("Product.%s carries no FieldInfo", string(f.Name))
		}
		if f.Info.ExactParentTypeName != "Product" {
			t.Errorf("Product.%s ExactParentTypeName: want Product, got %q", string(f.Name), f.Info.ExactParentTypeName)
		}
		if string(f.Name) == "shippingEstimate" {
			if !f.Info.HasAuthorizationRule {
				t.Errorf("Product.shippingEstimate FieldInfo must carry HasAuthorizationRule")
			}
			if f.Info.NamedType != "Float" {
				t.Errorf("Product.shippingEstimate NamedType: want Float, got %q", f.Info.NamedType)
			}
		}
	}
}

// TestPlanner_FieldInfoDisabled pins the DisableIncludeInfo contract (v1 parity, PARITY.md Section 2): a
// config that disables Info emission must produce a response tree with NO FieldInfo and fetches
// without RootFields. planv2 keeps its minimal FetchInfo record (DataSourceID/Name/OperationType/
// QueryPlan) even then -- the query-plan printer dereferences Info unconditionally, a documented,
// strictly-additive divergence from v1's nil.
func TestPlanner_FieldInfoDisabled(t *testing.T) {
	cfg := plan.Configuration{
		DataSources:        hgtestdata.EntityJumpConfig(),
		DisableIncludeInfo: true,
	}
	p, err := NewPlanner(cfg)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	op, def, report := parseAndNormalize(t, entityJumpSupergraph, `{ product { shippingEstimate } }`)
	sp := p.Plan(op, def, "", report).(*plan.SynchronousResponsePlan)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	product := sp.Response.Data.Fields[0]
	if product.Info != nil {
		t.Errorf("DisableIncludeInfo must suppress FieldInfo, got %+v", product.Info)
	}
	for _, item := range sp.Response.RawFetches {
		sf := item.Fetch.(*resolve.SingleFetch)
		if sf.Info == nil {
			t.Fatalf("planv2 keeps the minimal FetchInfo even under DisableIncludeInfo (printer nil-safety)")
		}
		if len(sf.Info.RootFields) != 0 {
			t.Errorf("DisableIncludeInfo must suppress RootFields, got %v", sf.Info.RootFields)
		}
	}
}

// TestPlanner_SubscriptionRootFieldInfo pins the postprocess trigger contract: the subscription
// root field's FieldInfo must carry a non-empty Source (postprocess createFetchTree reads
// Source.IDs[0]/Names[0] to build the trigger node) -- and the postprocessed subscription plan
// must carry the trigger fetch node v1 plans get.
func TestPlanner_SubscriptionRootFieldInfo(t *testing.T) {
	p := mustPlanner(t, hgtestdata.EntityJumpConfigWithSubscription("productUpdated"))
	op, def, report := parseAndNormalize(t,
		`schema { query: Query subscription: Subscription }
		type Query { product: Product }
		type Subscription { productUpdated: Product }
		type Product { id: ID! organization: Organization dimensions: Dimensions shippingEstimate: Float }
		type Organization { id: ID! }
		type Dimensions { length: Float width: Float height: Float }`,
		`subscription { productUpdated { id } }`)
	result := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sub, ok := result.(*plan.SubscriptionResponsePlan)
	if !ok {
		t.Fatalf("want *plan.SubscriptionResponsePlan, got %T", result)
	}
	root := sub.Response.Response.Data.Fields[0]
	if root.Info == nil {
		t.Fatalf("subscription root field carries no FieldInfo")
	}
	if len(root.Info.Source.IDs) == 0 || len(root.Info.Source.Names) == 0 {
		t.Fatalf("subscription root FieldInfo must carry Source attribution (postprocess trigger reads IDs[0]): %+v", root.Info.Source)
	}
	// The postprocess pipeline must run unchanged and build the trigger node off that Info.
	postprocess.NewProcessor().Process(sub)
	if sub.Response.Response.Fetches == nil || sub.Response.Response.Fetches.Trigger == nil {
		t.Fatalf("postprocess did not build the subscription trigger node from the root FieldInfo")
	}
}
