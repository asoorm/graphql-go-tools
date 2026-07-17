package planv2

// transport.go builds the per-subgraph transport table -- the HTTP details each fetch needs, plus the
// subscription transport a trigger needs -- from the plan.Configuration the facade gets at NewPlanner.
// This is the one place planv2 reads datasource-specific config: the facade is the only component
// holding the Configuration, and the transport (which URL to hit, with what method and headers; which
// URL/protocol to subscribe on) is the only per-subgraph fact that lowering can't work out from the
// graph alone.
//
// v1 gets the same facts inside the graphql_datasource planner while configuring a fetch: the HTTP
// source object plus the fetch URL, method, and headers (ConfigureFetch), and the subscription wire
// options plus the subscription source (ConfigureSubscription). planv2 never runs that planner, so it
// reads the fields off the datasource's exported config accessors and rebuilds the same sources
// through the graphql_datasource.NewSource / NewSubscriptionSource constructors -- no client is
// reimplemented.
//
// Two honest limitations, both scoped to the executable clients:
//   - The http.Client a datasource's fetches actually run on is held in an unexported field of the v1
//     datasource and can't be reached through the plan.DataSource interface planv2 is handed; reaching
//     it would mean editing the v1 plan package, which is out of scope. So planv2 builds each source
//     on http.DefaultClient. For the localhost federation test harness that is behaviorally exact;
//     wiring a configured client (timeouts, custom transport, mTLS) is a follow-up.
//   - The GraphQLSubscriptionClient likewise lives in the datasource factory's unexported field, and
//     its constructor wants a lifecycle context the facade doesn't have (NewPlanner is context-free
//     and the facade must spawn no goroutines -- the goleak gate). So the trigger's source builds a
//     fresh client per subscription START, scoped to that subscription's own context: every goroutine
//     the client spawns (ws ping loop, read loop) dies with the subscription. The cost is upstream
//     connection multiplexing across concurrent identical subscriptions (v1 shares one client);
//     correctness is unaffected. Wiring a shared, engine-lifecycle client is the same follow-up as
//     the http.Client above.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cespare/xxhash/v2"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	grpcdatasource "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/grpc_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/lower"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// buildTransportTable builds the transport table from the supergraph's datasources: for every graphql
// datasource that has an HTTP fetch config, it records the subgraph's HTTP source and the
// url/method/header fields; for every graphql datasource that has a subscription config, it records
// the subscription wire fields and a per-start subscription source (D11.12). Keyed by subgraph name
// (the same key lowering and the graph use). Datasources that aren't graphql, or have neither config
// (gRPC), get no entry and their fetches lower to shape only. Returns nil when no subgraph has any
// transport, so the plan stays byte-identical to shape-only lowering.
func buildTransportTable(dataSources []plan.DataSource, grpcTransports map[string]grpcdatasource.RPCTransport) lower.TransportTable {
	tt := make(lower.TransportTable, len(dataSources))
	client := http.DefaultClient // see the client limitation in the package doc
	for _, ds := range dataSources {
		cfgDS, ok := ds.(plan.DataSourceConfiguration[graphql_datasource.Configuration])
		if !ok {
			continue // not a graphql datasource: no HTTP transport
		}
		cfg := cfgDS.CustomConfiguration()
		var entry lower.Transport
		entry.ID = ds.Id() // FetchInfo identifier; wire-inert
		hasAny := false
		// gRPC/ConnectRPC (M4.3): a gRPC-configured graphql datasource carries no HTTP fetch/subscription
		// config; instead its fetches build a per-operation grpc_datasource.DataSource. The subgraph's
		// static planning inputs (upstream SDL, mapping, compiler, disabled flag, entity key/requires
		// federation configs) are read from the config here -- the one place planv2 reads datasource
		// config -- and captured in a fetch factory lower invokes per fetch. The RPC transport (the live
		// client connection) is NOT part of plan.Configuration -- it lives in the datasource Factory,
		// unreachable through the plan.DataSource interface -- exactly the limitation the http.Client and
		// subscription client have above. It is supplied by the embedder via Config.GRPCTransports (keyed
		// by subgraph name); an absent transport still builds an executable DataSource (the RPC plan is
		// compiled), whose Load then returns the typed "requires an rpc transport" error rather than a
		// silent shape-only stub -- see the package doc.
		if cfg.IsGRPC() {
			if factory := newGRPCFetchFactory(ds, cfg, grpcTransports[ds.Name()]); factory != nil {
				entry.GRPC = factory
				hasAny = true
			}
		}
		if fetch, ok := cfg.FetchConfiguration(); ok {
			entry.DataSource = graphql_datasource.NewSource(client)
			entry.URL = fetch.URL
			entry.Method = fetch.Method
			entry.Header = marshalNonNull(fetch.Header)
			hasAny = true
		}
		if sub, ok := cfg.SubscriptionConfiguration(); ok {
			entry.Subscription = &lower.SubscriptionTransport{
				Source:                                  &perStartSubscriptionSource{hooks: sub.StartupHooks},
				URL:                                     sub.URL,
				Header:                                  marshalNonNull(sub.Header),
				UseSSE:                                  sub.UseSSE,
				SSEMethodPost:                           sub.SSEMethodPost,
				WsSubProtocol:                           sub.WsSubProtocol,
				ForwardedClientHeaderNames:              marshalNonNull(sub.ForwardedClientHeaderNames),
				ForwardedClientHeaderRegularExpressions: marshalNonNull(sub.ForwardedClientHeaderRegularExpressions),
				SourceID:                                ds.Id(),
			}
			hasAny = true
		}
		if hasAny {
			tt[ds.Name()] = entry
		}
	}
	if len(tt) == 0 {
		return nil
	}
	return tt
}

// grpcFetchFactory is the facade's lower.GRPCFetchFactory: it holds one subgraph's static gRPC
// planning inputs (captured once at NewPlanner) and, per fetch, parses that fetch's rendered subgraph
// operation and builds the executable grpc_datasource.DataSource via grpc_datasource.NewDataSource --
// the same constructor v1's graphql_datasource ConfigureFetch calls, reusing the transport unchanged.
// One factory is shared across a subgraph's fetches (the static inputs are read-only); only the
// per-fetch operation differs, so the factory carries no per-fetch or per-query state and is safe for
// concurrent Plan calls, matching the HTTP source.
type grpcFetchFactory struct {
	transport         grpcdatasource.RPCTransport // may be nil (see buildTransportTable); Load errors typed
	definition        *ast.Document               // the subgraph's upstream SDL AST (parsed once)
	mapping           *grpcdatasource.GRPCMapping
	compiler          *grpcdatasource.RPCCompiler
	federationConfigs plan.FederationFieldConfigurations // entity @key/@requires selections (per-subgraph)
	subgraphName      string
	disabled          bool
}

// newGRPCFetchFactory captures a gRPC subgraph's static planning inputs, or returns nil when the
// upstream SDL / gRPC config is unavailable (the fetch then lowers shape-only rather than panicking --
// a malformed gRPC datasource is not made executable). The federation configs are the datasource's
// entity key + requires selections (FederationMetaData), which the grpc planner consults to resolve
// _entities fetches; v1 threads the per-fetch RequiredFields, a subset -- the full per-subgraph set is
// a sound superset for telling the planner which selections are federation-required.
func newGRPCFetchFactory(ds plan.DataSource, cfg graphql_datasource.Configuration, transport grpcdatasource.RPCTransport) *grpcFetchFactory {
	grpc, ok := cfg.GRPCConfiguration()
	if !ok {
		return nil
	}
	def, err := cfg.UpstreamSchema()
	if err != nil || def == nil {
		return nil
	}
	fed := ds.FederationConfiguration()
	federationConfigs := make(plan.FederationFieldConfigurations, 0, len(fed.Keys)+len(fed.Requires))
	federationConfigs = append(federationConfigs, fed.Keys...)
	federationConfigs = append(federationConfigs, fed.Requires...)
	return &grpcFetchFactory{
		transport:         transport,
		definition:        def,
		mapping:           grpc.Mapping,
		compiler:          grpc.Compiler,
		federationConfigs: federationConfigs,
		subgraphName:      ds.Name(),
		disabled:          grpc.Disabled,
	}
}

// DataSourceForOperation parses the fetch's subgraph operation and builds its executable gRPC
// DataSource, mirroring v1 ConfigureFetch (parse the operation, then grpc_datasource.NewDataSource with
// the subgraph's mapping/compiler/definition/federation-configs). NewDataSource plans the operation at
// construction; the RPC transport is used only at Load.
func (f *grpcFetchFactory) DataSourceForOperation(query string) (resolve.DataSource, error) {
	opDoc, report := astparser.ParseGraphqlDocumentString(query)
	if report.HasErrors() {
		return nil, fmt.Errorf("planv2 grpc: parse subgraph operation for %s: %w", f.subgraphName, report)
	}
	ds, err := grpcdatasource.NewDataSource(f.transport, grpcdatasource.DataSourceConfig{
		Operation:         &opDoc,
		Definition:        f.definition,
		Mapping:           f.mapping,
		Compiler:          f.compiler,
		Disabled:          f.disabled,
		FederationConfigs: f.federationConfigs,
		SubgraphName:      f.subgraphName,
	})
	if err != nil {
		return nil, fmt.Errorf("planv2 grpc: build datasource for %s: %w", f.subgraphName, err)
	}
	return ds, nil
}

// marshalNonNull JSON-encodes v, returning nil for an empty/null encoding -- the "field absent" wire
// form the input envelopes splice conditionally.
func marshalNonNull(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil || bytes.Equal(b, []byte("null")) || bytes.Equal(b, []byte("[]")) || bytes.Equal(b, []byte("{}")) {
		return nil
	}
	return b
}

// perStartSubscriptionSource is the facade's resolve.SubscriptionDataSource: it defers building the
// graphql_datasource subscription client to each subscription START, scoped to that subscription's
// context (see the package doc's second limitation -- the facade must spawn no goroutines, and the
// factory's shared client is unreachable). Start and the trigger-input hash otherwise delegate to the
// production SubscriptionSource byte-for-byte.
type perStartSubscriptionSource struct {
	hooks []graphql_datasource.SubscriptionOnStartFn
}

func (s *perStartSubscriptionSource) Start(ctx *resolve.Context, headers http.Header, input []byte, updater resolve.SubscriptionUpdater) error {
	client := graphql_datasource.NewGraphQLSubscriptionClient(ctx.Context())
	return graphql_datasource.NewSubscriptionSource(client, s.hooks...).Start(ctx, headers, input, updater)
}

// HashTriggerInput matches SubscriptionSource.HashTriggerInput (the input alone identifies the
// trigger; the resolver appends the header hash itself).
func (s *perStartSubscriptionSource) HashTriggerInput(input []byte, xxh *xxhash.Digest) error {
	_, err := xxh.Write(input)
	return err
}

// SubscriptionOnStart implements resolve.HookableSubscriptionDataSource, mirroring
// SubscriptionSource.SubscriptionOnStart: hooks run sequentially, short-circuiting on error.
func (s *perStartSubscriptionSource) SubscriptionOnStart(ctx resolve.StartupHookContext, input []byte) error {
	for _, fn := range s.hooks {
		if err := fn(ctx, input); err != nil {
			return err
		}
	}
	return nil
}
