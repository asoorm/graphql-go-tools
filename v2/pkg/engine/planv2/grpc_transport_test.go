package planv2

// grpc_transport_test.go is the M4.3 EXECUTED-TRUTH guard for gRPC/ConnectRPC transport emission, in
// the spirit of TestNewPlanner_AcceptsRealRouterDefaults but stronger: plan-shape acceptance alone is
// insufficient (the M4.1 over-refusal that disabled planv2 for real configs was invisible to shape-only
// checks). This test drives the gRPC fetch planv2 emits through the grpc_datasource execution harness --
// a real bufconn gRPC server serving grpctest.MockService -- and asserts REAL response bytes, exactly as
// the grpc_datasource package's own Load tests do.
//
// What this proves in-repo: a plan.Configuration carrying a gRPC-configured graphql datasource is
// ACCEPTED at NewPlanner (the former ErrGRPCDatasourceNotSupported refusal is gone), and the fetch it
// lowers carries an EXECUTABLE grpc_datasource.DataSource (not a shape-only stub) whose Load returns the
// mock service's data. The live RPC transport is injected via Config.GRPCTransports (a bufconn client),
// standing in for the router's real gRPC client connection -- which is a Factory-level runtime dependency
// unreachable through plan.Configuration (see transport.go). A live upstream gRPC server / the
// coordinator real-router check remains out of repo scope; this bufconn harness is the in-repo executed
// truth.

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	grpcdatasource "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/grpc_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/grpctest"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/grpctest/productv1"
)

// categoriesMapping is the minimal GRPC<->GraphQL mapping the `{ categories { id name } }` executed-truth
// query needs: the categories query RPC plus the Category id/name field mappings.
func categoriesMapping() *grpcdatasource.GRPCMapping {
	return &grpcdatasource.GRPCMapping{
		Service: "Products",
		QueryRPCs: grpcdatasource.RPCConfigMap[grpcdatasource.RPCConfig]{
			"categories": {
				RPC:      "QueryCategories",
				Request:  "QueryCategoriesRequest",
				Response: "QueryCategoriesResponse",
			},
		},
		Fields: map[string]grpcdatasource.FieldMap{
			"Query": {
				"categories": {TargetName: "categories"},
			},
			"Category": {
				"id":   {TargetName: "id"},
				"name": {TargetName: "name"},
			},
		},
	}
}

// setupBufconnProducts starts an in-process gRPC server serving grpctest.MockService and returns an
// RPCTransport bound to it (mirrors grpc_datasource's setupTestGRPCServer). The transport is what
// Config.GRPCTransports injects.
func setupBufconnProducts(t *testing.T) grpcdatasource.RPCTransport {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	productv1.RegisterProductServiceServer(server, &grpctest.MockService{})
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Errorf("failed to serve: %v", err)
		}
	}()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithLocalDNSResolution(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		conn.Close()
		server.Stop()
		lis.Close()
	})
	return grpcdatasource.NewGRPCTransport(conn)
}

// grpcProductsConfig builds a single-subgraph plan.Configuration whose datasource is a gRPC-configured
// graphql datasource for the products schema (upstream SDL + gRPC mapping/compiler). Subgraph name
// "Products" matches the mapping Service and the Config.GRPCTransports key.
func grpcProductsConfig(t *testing.T) plan.Configuration {
	t.Helper()
	schemaCfg, err := graphql_datasource.NewSchemaConfiguration(grpctest.MustSchemaSDL(t), nil)
	require.NoError(t, err)
	compiler, err := grpcdatasource.NewProtoCompiler(grpctest.MustProtoSchema(t), categoriesMapping())
	require.NoError(t, err)
	cfg, err := graphql_datasource.NewConfiguration(graphql_datasource.ConfigurationInput{
		SchemaConfiguration: schemaCfg,
		GRPC: &grpcdatasource.GRPCConfiguration{
			Mapping:  categoriesMapping(),
			Compiler: compiler,
		},
	})
	require.NoError(t, err)
	ds, err := plan.NewDataSourceConfiguration[graphql_datasource.Configuration](
		"Products",
		&graphql_datasource.Factory[graphql_datasource.Configuration]{},
		grpctest.GetDataSourceMetadata(),
		cfg,
	)
	require.NoError(t, err)
	return plan.Configuration{DataSources: []plan.DataSource{ds}}
}

// grpcRootFetch pulls the single root SingleFetch out of a lowered synchronous plan.
func grpcRootFetch(t *testing.T, p plan.Plan) *resolve.SingleFetch {
	t.Helper()
	sp, ok := p.(*plan.SynchronousResponsePlan)
	require.Truef(t, ok, "expected *plan.SynchronousResponsePlan, got %T", p)
	require.Len(t, sp.Response.RawFetches, 1, "expected exactly one root fetch")
	sf, ok := sp.Response.RawFetches[0].Fetch.(*resolve.SingleFetch)
	require.Truef(t, ok, "root fetch is not a SingleFetch: %T", sp.Response.RawFetches[0].Fetch)
	return sf
}

// TestNewPlanner_GRPCFetchExecutesRealOutput is the executed-truth guard: plan a gRPC query, then drive
// the emitted fetch's DataSource through Load against a real bufconn server and assert the mock's data.
func TestNewPlanner_GRPCFetchExecutesRealOutput(t *testing.T) {
	transport := setupBufconnProducts(t)
	config := grpcProductsConfig(t)

	p, err := NewPlannerWithConfig(config, Config{
		GRPCTransports: map[string]grpcdatasource.RPCTransport{"Products": transport},
	})
	require.NoError(t, err, "NewPlanner must ACCEPT a gRPC-configured datasource (M4.3)")

	op, def, report := parseAndNormalize(t, grpctest.MustSchemaSDL(t), `{ categories { id name } }`)
	result := p.Plan(op, def, "", report)
	require.False(t, report.HasErrors(), "plan reported errors: %s", report.Error())

	sf := grpcRootFetch(t, result)
	require.NotNil(t, sf.FetchConfiguration.DataSource,
		"the gRPC fetch must carry an executable DataSource, not a shape-only stub")

	// EXECUTED TRUTH: run the emitted DataSource through the grpc_datasource harness. The fetch Input is
	// the body envelope the resolver would hand Load (no client-argument placeholders for this query).
	out, err := sf.FetchConfiguration.DataSource.Load(context.Background(), nil, []byte(sf.FetchConfiguration.Input))
	require.NoError(t, err)

	var resp struct {
		Data struct {
			Categories []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"categories"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(out, &resp), "raw output: %s", string(out))
	require.Empty(t, resp.Errors, "raw output: %s", string(out))
	require.NotEmpty(t, resp.Data.Categories, "raw output: %s", string(out))
	// grpctest.MockService.QueryCategories returns four categories, category-1..4.
	require.Equal(t, "category-1", resp.Data.Categories[0].ID)
	require.Equal(t, "CATEGORY_KIND_BOOK Category", resp.Data.Categories[0].Name)
	require.Len(t, resp.Data.Categories, 4)
}

// TestNewPlanner_GRPCFetchNoTransportLoadsError proves the honest degradation: with NO transport injected
// (the RPC client is unreachable from plan.Configuration), the fetch STILL carries a real gRPC DataSource
// (executable machinery, RPC plan compiled) -- not a shape-only stub -- and its Load surfaces the typed
// "requires an rpc transport" error loudly rather than silently returning empty. This is the red half of
// the executed-truth witness: it is the transport injection that turns the same plan executable.
func TestNewPlanner_GRPCFetchNoTransportLoadsError(t *testing.T) {
	config := grpcProductsConfig(t)

	p, err := NewPlanner(config) // no GRPCTransports
	require.NoError(t, err)

	op, def, report := parseAndNormalize(t, grpctest.MustSchemaSDL(t), `{ categories { id name } }`)
	result := p.Plan(op, def, "", report)
	require.False(t, report.HasErrors(), "plan reported errors: %s", report.Error())

	sf := grpcRootFetch(t, result)
	require.NotNil(t, sf.FetchConfiguration.DataSource,
		"a gRPC fetch is executable machinery even without a transport (not a shape-only stub)")

	_, err = sf.FetchConfiguration.DataSource.Load(context.Background(), nil, []byte(sf.FetchConfiguration.Input))
	require.Error(t, err, "Load without an injected RPC transport must fail loudly, not silently succeed")
}
