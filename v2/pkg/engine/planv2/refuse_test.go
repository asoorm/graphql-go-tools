package planv2

// refuse_test.go pins the M4.1 typed-loud sweep (PARITY.md Section 2/Section 3): every plan.Configuration
// surface planv2 cannot honor yet must REFUSE with a typed sentinel at NewPlanner -- never be
// silently ignored. Each error is errors.Is-matchable so the router seam can catch the refusal and
// fall back to v1 (the adoption contract for MISSING-LOUD rows).

import (
	"errors"
	"testing"

	"github.com/wundergraph/astjson"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	grpcdatasource "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/grpc_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

func TestNewPlanner_RefusesUnsupportedConfig(t *testing.T) {
	base := func() plan.Configuration {
		return plan.Configuration{DataSources: hgtestdata.EntityJumpConfig()}
	}
	cases := []struct {
		name   string
		mutate func(*plan.Configuration)
		want   error
	}{
		{
			name:   "type renames (Types)",
			mutate: func(c *plan.Configuration) { c.Types = plan.TypeConfigurations{{TypeName: "A", RenameTo: "B"}} },
			want:   ErrTypeRenamesNotSupported,
		},
		{
			name: "custom resolve map",
			mutate: func(c *plan.Configuration) {
				c.CustomResolveMap = map[string]resolve.CustomResolve{"Custom": nil}
			},
			want: ErrCustomResolveMapNotSupported,
		},
		{
			name:   "fetch reasons",
			mutate: func(c *plan.Configuration) { c.BuildFetchReasons = true },
			want:   ErrFetchReasonsNotSupported,
		},
		{
			name:   "validate required external fields",
			mutate: func(c *plan.Configuration) { c.ValidateRequiredExternalFields = true },
			want:   ErrFetchReasonsNotSupported,
		},
		{
			name: "field path mapping",
			mutate: func(c *plan.Configuration) {
				c.Fields = plan.FieldConfigurations{{TypeName: "Product", FieldName: "id", Path: []string{"identifier"}}}
			},
			want: ErrFieldPathMappingNotSupported,
		},
		{
			name: "unescape response json",
			mutate: func(c *plan.Configuration) {
				c.Fields = plan.FieldConfigurations{{TypeName: "Product", FieldName: "id", UnescapeResponseJson: true}}
			},
			want: ErrUnescapeResponseJSONNotSupported,
		},
		{
			name: "argument render config",
			mutate: func(c *plan.Configuration) {
				c.Fields = plan.FieldConfigurations{{TypeName: "Query", FieldName: "product",
					Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource,
						RenderConfig: plan.RenderArgumentAsArrayCSV}}}}
			},
			want: ErrArgumentRenderingNotSupported,
		},
		{
			name: "argument rename type to",
			mutate: func(c *plan.Configuration) {
				c.Fields = plan.FieldConfigurations{{TypeName: "Query", FieldName: "product",
					Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource,
						RenameTypeTo: "OtherID"}}}}
			},
			want: ErrArgumentRenderingNotSupported,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			_, err := NewPlanner(cfg)
			if err == nil {
				t.Fatalf("NewPlanner accepted a config carrying an unsupported capability -- silent ignore")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want typed error %v, got %v", tc.want, err)
			}
		})
	}
}

// TestNewPlanner_AcceptsRealRouterDefaults is the red-first guard for the M4.1 over-refusal:
// refuse.go was validated only against harness configs that never set Cosmo router DEFAULTS. The
// router sets MinifySubgraphOperations (envDefault:"true") in EVERY default deployment, so a refusal
// on that flag refuses EVERY real config and disables planv2 for 100% of operations. Minification is
// a non-semantic subgraph-query-string optimization; planv2 emitting an unminified but semantically
// identical document yields identical responses (proven at b7b8e1f0: planv2 planned WITH
// MinifySubgraphOperations set and scored 188). EnableOperationNamePropagation only sets the subgraph
// operation NAME (observability/tracing; results identical). A config mirroring real router defaults
// MUST be ACCEPTED (planv2 plans; results-identical, superset-safe).
func TestNewPlanner_AcceptsRealRouterDefaults(t *testing.T) {
	cfg := plan.Configuration{
		DataSources: hgtestdata.EntityJumpConfig(),
		// Cosmo router default (config.go: envDefault:"true"): ON in every default deployment.
		MinifySubgraphOperations: true,
		// Router observability flag: sets the subgraph operation name only; results identical.
		EnableOperationNamePropagation: true,
	}
	if _, err := NewPlanner(cfg); err != nil {
		t.Fatalf("NewPlanner refused a real-router-defaults config (disables planv2 for 100%% of ops): %v", err)
	}
	if _, err := NewPlannerWithConfig(cfg, Config{}); err != nil {
		t.Fatalf("NewPlannerWithConfig refused a real-router-defaults config: %v", err)
	}
}

// TestNewPlanner_AcceptsComputeCosts is the M4.5 guard, in the spirit of AcceptsRealRouterDefaults:
// a real router that configures IBM cost sets plan.Configuration.ComputeCosts (plus the tuning knobs
// and per-DS CostConfig). Before M4.5 this REFUSED at NewPlanner (ErrComputeCostsNotSupported), so
// planv2 fell back to v1 for every cost-configured router. Cost is now honored, so a ComputeCosts
// config MUST be accepted AND the emitted plan MUST carry a working (non-nil) CostCalculator --
// a nil calculator would make the engine skip ValidateSliceArguments and every cost-limit check
// silently (execution_engine.go getCachedPlan), the security-grade defect this wave closes.
func TestNewPlanner_AcceptsComputeCosts(t *testing.T) {
	cfg := plan.Configuration{
		DataSources:  hgtestdata.EntityJumpConfig(),
		ComputeCosts: true,
	}
	p, err := NewPlanner(cfg)
	if err != nil {
		t.Fatalf("NewPlanner refused a ComputeCosts config (disables planv2 for every cost-configured router): %v", err)
	}
	op, def, report := parseAndNormalize(t, entityJumpSupergraph, `{ product { shippingEstimate } }`)
	result := p.Plan(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sp, ok := result.(*plan.SynchronousResponsePlan)
	if !ok {
		t.Fatalf("expected *plan.SynchronousResponsePlan, got %T", result)
	}
	calc := sp.GetCostCalculator()
	if calc == nil {
		t.Fatalf("ComputeCosts:true plan carries a NIL CostCalculator -- cost limits would silently NOT be enforced")
	}
	// The calculator must be usable: EstimateCost over empty variables computes without panicking and
	// is well-defined (>= 0) for this weight-free fixture (default object weights only).
	if got := calc.EstimateCost(resolve.NewVariablesView(astjson.MustParseBytes([]byte("{}")), nil)); got < 0 {
		t.Fatalf("EstimateCost returned a negative cost %d", got)
	}
}

// TestNewPlanner_AcceptsSubscriptionFilter is the M4.4 guard, in the spirit of AcceptsComputeCosts:
// before M4.4 a config carrying a SubscriptionFilterCondition REFUSED at NewPlanner
// (ErrSubscriptionFilterNotSupported), so planv2 fell back to v1 for every subscription-filter router.
// The filter is now honored, so such a config MUST be ACCEPTED AND the emitted subscription plan MUST
// carry a non-nil resolve.SubscriptionFilter on the GraphQLSubscription -- a nil filter would deliver
// events the condition says to skip (a data-exposure defect). The EXECUTED-TRUTH guard (drive
// SkipEvent, prove the skip actually happens, compare to v1) lives in differential/exec_subfilter_test.go;
// plan-shape acceptance alone is insufficient (the M4.1 over-refusal lesson).
func TestNewPlanner_AcceptsSubscriptionFilter(t *testing.T) {
	cfg := plan.Configuration{
		DataSources: hgtestdata.SubscriptionProductConfig(),
		Fields: plan.FieldConfigurations{
			{
				TypeName:  "Subscription",
				FieldName: "productUpdated",
				Arguments: plan.ArgumentsConfigurations{
					{Name: "upc", SourceType: plan.FieldArgumentSource},
				},
				SubscriptionFilterCondition: &plan.SubscriptionFilterCondition{
					In: &plan.SubscriptionFieldCondition{
						FieldPath: []string{"upc"},
						Values:    []string{"{{ args.upc }}"},
					},
				},
			},
		},
	}
	p, err := NewPlanner(cfg)
	if err != nil {
		t.Fatalf("NewPlanner refused a SubscriptionFilterCondition config (disables planv2 for every subscription-filter router): %v", err)
	}
	op, def, report := parseAndNormalize(t, subscriptionProductSupergraph,
		`subscription UpdatePrice($upc: String!) { productUpdated(upc: $upc) { name price } }`)
	result := p.Plan(op, def, "UpdatePrice", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sub, ok := result.(*plan.SubscriptionResponsePlan)
	if !ok {
		t.Fatalf("expected *plan.SubscriptionResponsePlan, got %T", result)
	}
	if sub.Response.Filter == nil {
		t.Fatalf("SubscriptionFilterCondition config produced a plan with a NIL Filter -- events that must be skipped would be delivered (data exposure)")
	}
	if sub.Response.Filter.In == nil || len(sub.Response.Filter.In.FieldPath) != 1 || sub.Response.Filter.In.FieldPath[0] != "upc" {
		t.Fatalf("emitted filter must carry the IN condition on FieldPath [upc], got %+v", sub.Response.Filter)
	}
}

// TestNewPlanner_AcceptsGRPCDatasource is the M4.3 inversion of the former refusal guard: a
// gRPC/ConnectRPC-configured datasource must now be ACCEPTED at NewPlanner (its fetches build a
// per-operation executable grpc_datasource.DataSource -- see transport.go). This case pins acceptance
// only; the EXECUTED-TRUTH guard (plan a gRPC query, pull the fetch's DataSource, drive it through the
// grpc_datasource harness against a real bufconn server, assert real output) lives in
// grpc_transport_test.go -- plan-shape acceptance alone is insufficient (the M4.1 over-refusal lesson).
func TestNewPlanner_AcceptsGRPCDatasource(t *testing.T) {
	schemaCfg, err := graphql_datasource.NewSchemaConfiguration(`type Query { hello: String }`, nil)
	if err != nil {
		t.Fatalf("schema configuration: %v", err)
	}
	cfg, err := graphql_datasource.NewConfiguration(graphql_datasource.ConfigurationInput{
		SchemaConfiguration: schemaCfg,
		GRPC:                &grpcdatasource.GRPCConfiguration{},
	})
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	ds, err := plan.NewDataSourceConfiguration[graphql_datasource.Configuration](
		"grpc-ds", &graphql_datasource.Factory[graphql_datasource.Configuration]{},
		&plan.DataSourceMetadata{
			RootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"hello"}}},
		}, cfg)
	if err != nil {
		t.Fatalf("datasource configuration: %v", err)
	}
	if _, err = NewPlanner(plan.Configuration{DataSources: []plan.DataSource{ds}}); err != nil {
		t.Fatalf("NewPlanner refused a gRPC-configured datasource (M4.3 accepts them): %v", err)
	}
}

// TestNewPlanner_AcceptsSupportedConfig guards the refusal sweep's precision: the surfaces planv2
// DOES honor (auth rules via the M4.1 Info wave; FIELD_ARGUMENT and OBJECT_FIELD argument configs --
// the latter deliberately unrefused, see PARITY.md Section 3 corpus-compat note; default render configs;
// DisableIncludeInfo/DisableResolveFieldPositions) must not trip a refusal.
func TestNewPlanner_AcceptsSupportedConfig(t *testing.T) {
	cfg := plan.Configuration{
		DataSources: hgtestdata.EntityJumpConfig(),
		Fields: plan.FieldConfigurations{
			{TypeName: "Product", FieldName: "shippingEstimate", HasAuthorizationRule: true},
			{TypeName: "Query", FieldName: "product",
				Arguments: plan.ArgumentsConfigurations{
					{Name: "id", SourceType: plan.FieldArgumentSource},
					{Name: "ref", SourceType: plan.ObjectFieldSource, SourcePath: []string{"obj", "field"}},
				}},
		},
		DisableResolveFieldPositions: true,
		DisableIncludeInfo:           true,
	}
	if _, err := NewPlanner(cfg); err != nil {
		t.Fatalf("NewPlanner refused a supported config: %v", err)
	}
}
