package lower

// transport.go carries the M1.5 TRANSPORT LOWERING contract: the per-subgraph executable transport
// that turns a shape-correct planv2 plan into an EXECUTABLE one.
//
// Background (m15-router-report DEFECT 2). lower emits the fetch TREE -- documents, representations,
// response shape, dependency order -- but until this wave it attached no transport: every fetch left
// FetchConfiguration.DataSource nil and emitted Input as `{"body":{"query":...}}` with no url/method.
// The resolve loader panics on the nil DataSource (loader.go source.Load, source == nil) and, past
// that, has no URL to dial. v1 obtains both inside the graphql_datasource planner's ConfigureFetch:
// `dataSource = &Source{httpClient: p.fetchClient}` plus httpclient.SetInputURL/Method/Header over the
// body envelope. plan.Configuration is consumed by hypergraph.Build and not retained, so planv2 must
// carry the same facts forward itself -- this file is that carrier, and buildFetches / buildRawFetches
// consult it to attach DataSource + wire the Input on EVERY fetch (root and entity).
//
// The table is OPTIONAL: a nil/empty TransportTable leaves fetches transport-free -- the exact
// pre-M1.5 output the plan-level audit/differential harnesses compare against. Only the executable
// callers (planv2.Plan, wired from a real plan.Configuration) populate it.

import (
	"encoding/json"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// Transport is one subgraph's executable transport: the resolve.DataSource that performs the HTTP
// round-trip and the wire fields (url/method/header) the Input envelope must carry -- the fields
// resolve's httpclient.Do reads. Header is a pre-marshaled JSON object (http.Header shape) or nil.
// Subscription, when non-nil, is the subgraph's SUBSCRIPTION transport (D11.12) -- consulted only by
// the subscription trigger, never by sync fetches.
type Transport struct {
	DataSource resolve.DataSource
	URL        string
	Method     string
	Header     json.RawMessage
	// ID is the datasource's configured identifier (ds.Id()), carried for FetchInfo (the query-plan
	// printer's SubgraphID); it never affects the wire and does not make an otherwise-empty
	// Transport attach.
	ID string

	Subscription *SubscriptionTransport

	// GRPC, when non-nil, is the subgraph's gRPC/ConnectRPC fetch factory (M4.3). Unlike the HTTP
	// DataSource -- one stateless Source that serves every fetch, the query travelling in the Input
	// body -- a gRPC DataSource compiles ITS OWN operation into an RPC execution plan at construction,
	// so a distinct DataSource must be built per fetch from that fetch's subgraph operation. attach
	// invokes the factory with the fetch's rendered operation (fc.QueryPlan.Query) to build it. Set
	// only for gRPC-configured subgraphs; mutually exclusive with the HTTP wire fields (DataSource/
	// URL/Method/Header), which a gRPC fetch never carries (no url/method/header envelope -- the
	// transport is the pre-bound RPC client, and Load reads body.variables plus the resolve headers).
	GRPC GRPCFetchFactory
}

// GRPCFetchFactory builds the per-fetch executable gRPC/ConnectRPC DataSource from the fetch's subgraph
// operation document (the GraphQL query string lowering already renders onto fc.QueryPlan.Query). It is
// implemented in the planv2 facade (transport.go), which owns the grpc_datasource dependency and the
// per-subgraph mapping/compiler/definition/RPC-transport it captures; keeping the constructor behind an
// interface holds the grpc_datasource import out of lower, exactly as SubscriptionTransport holds the
// graphql_datasource subscription client out of lower.
type GRPCFetchFactory interface {
	// DataSourceForOperation parses the subgraph operation and builds its executable gRPC DataSource,
	// or returns an error the lowering path surfaces (never a silent shape-only fallback).
	DataSourceForOperation(query string) (resolve.DataSource, error)
}

// SubscriptionTransport is one subgraph's executable SUBSCRIPTION transport (FORMAL_SPEC D11.12
// trigger transport): the resolve.SubscriptionDataSource that opens the upstream connection and the
// wire fields the trigger Input envelope carries -- the graphql_datasource GraphQLSubscriptionOptions
// keys (url/header/use_sse/sse_method_post/ws_sub_protocol/forwarded client headers). `method` is an
// HTTP-fetch field and never appears on a trigger. Header and the two Forwarded* fields are
// pre-marshaled JSON (or nil), mirroring Transport.Header, so this package stays free of the
// graphql_datasource dependency.
type SubscriptionTransport struct {
	Source        resolve.SubscriptionDataSource
	URL           string
	Header        json.RawMessage
	UseSSE        bool
	SSEMethodPost bool
	WsSubProtocol string
	// ForwardedClientHeaderNames / ForwardedClientHeaderRegularExpressions: pre-marshaled JSON arrays
	// (the wire values of the corresponding GraphQLSubscriptionOptions fields), or nil.
	ForwardedClientHeaderNames              json.RawMessage
	ForwardedClientHeaderRegularExpressions json.RawMessage
	// SourceID names the datasource for the trigger's SourceID (v1 sets both from the datasource
	// config); the trigger's SourceName is the subgraph name lowering already has.
	SourceID string
}

// empty reports whether this Transport carries nothing to attach (the zero value): a shape-only
// lowering leaves such fetches untouched, preserving byte-identical pre-transport output. A gRPC
// factory is transport (a fetch with only GRPC set is NOT empty), so gRPC fetches attach an executable
// DataSource rather than lowering shape-only.
func (t Transport) empty() bool {
	return t.DataSource == nil && t.GRPC == nil && t.URL == "" && t.Method == "" && len(t.Header) == 0
}

// TransportTable maps a subgraph NAME (== the fetch's DataSourceIdentifier, == h.SubgraphName) to its
// executable transport. A nil table (or a name with no entry) yields shape-only fetches.
type TransportTable map[string]Transport

// lookup returns the transport for a subgraph name, or the zero Transport when the table is nil / the
// name is absent (shape-only fetch).
func (tt TransportTable) lookup(subgraph string) Transport {
	if tt == nil {
		return Transport{}
	}
	return tt[subgraph]
}

// attach wires a fetch's executable transport in place: it sets FetchConfiguration.DataSource and
// splices the url/method/header wire fields into the Input envelope. A no-op for an empty Transport
// (shape-only lowering) so the pre-M1.5 Input/DataSource are preserved untouched.
//
// The Input is assembled by STRING splicing rather than a JSON set: an entity fetch's Input carries
// the `$$0$$` representations placeholder (and forwarded `$$N$$` context variables), which is not
// valid JSON, so a JSON-aware setter (sjson) cannot round-trip it. Every planv2 Input begins with the
// literal `{"body":` prefix, so injecting the wire fields as leading top-level keys is exact and
// leaves the body -- representations template included -- byte-identical. Key order is irrelevant:
// httpclient.Do reads url/method/header/body by key path.
//
// gRPC/ConnectRPC (t.GRPC != nil) diverges from HTTP: it builds this fetch's own DataSource from the
// fetch's rendered operation (fc.QueryPlan.Query) and attaches ONLY that DataSource -- no url/method/
// header is spliced (a gRPC fetch has no HTTP wire envelope; the DataSource reads body.variables and
// the resolve headers, and dials the pre-bound RPC transport the factory captured). The body Input --
// `{"body":{"query":...,"variables":...}}` -- is left byte-identical; Load ignores body.query (the
// operation is already compiled into the plan) and reads body.variables. A construction error is
// returned, never swallowed.
func (t Transport) attach(fc *resolve.FetchConfiguration) error {
	if t.empty() {
		return nil
	}
	if t.GRPC != nil {
		ds, err := t.GRPC.DataSourceForOperation(fc.QueryPlan.Query)
		if err != nil {
			return err
		}
		fc.DataSource = ds
		return nil
	}
	fc.DataSource = t.DataSource
	fc.Input = wrapInputTransport(fc.Input, t)
	return nil
}

// wrapTriggerInput splices the SUBSCRIPTION wire fields as leading top-level keys of a trigger's
// body envelope (`{"body":{...}}`), producing the graphql_datasource GraphQLSubscriptionOptions
// wire shape, e.g. `{"url":"ws://...","ws_sub_protocol":"graphql-transport-ws","body":{...}}`.
// String splicing for the same reason as wrapInputTransport: the body's `$$N$$` variable segments
// are not valid JSON. A nil SubscriptionTransport returns the input untouched (shape-only trigger).
// False flags and empty strings are omitted, matching v1's ConfigureSubscription (SetInputFlag only
// on true, SetInputWSSubprotocol no-op on empty).
func wrapTriggerInput(input string, s *SubscriptionTransport) string {
	if s == nil {
		return input
	}
	if !strings.HasPrefix(input, "{") {
		return input // defensive: not the expected envelope -- leave untouched
	}
	var b strings.Builder
	b.Grow(len(input) + len(s.URL) + len(s.Header) + 96)
	b.WriteByte('{')
	if s.URL != "" {
		b.WriteString(`"url":`)
		b.Write(jsonQuote(s.URL))
		b.WriteByte(',')
	}
	if s.UseSSE {
		b.WriteString(`"use_sse":true,`)
	}
	if s.SSEMethodPost {
		b.WriteString(`"sse_method_post":true,`)
	}
	if s.WsSubProtocol != "" {
		b.WriteString(`"ws_sub_protocol":`)
		b.Write(jsonQuote(s.WsSubProtocol))
		b.WriteByte(',')
	}
	if len(s.ForwardedClientHeaderNames) > 0 {
		b.WriteString(`"forwarded_client_header_names":`)
		b.Write(s.ForwardedClientHeaderNames)
		b.WriteByte(',')
	}
	if len(s.ForwardedClientHeaderRegularExpressions) > 0 {
		b.WriteString(`"forwarded_client_header_regular_expressions":`)
		b.Write(s.ForwardedClientHeaderRegularExpressions)
		b.WriteByte(',')
	}
	if len(s.Header) > 0 {
		b.WriteString(`"header":`)
		b.Write(s.Header)
		b.WriteByte(',')
	}
	b.WriteString(input[1:]) // the original body envelope, minus its leading '{'
	return b.String()
}

// wrapInputTransport splices the transport wire fields as leading top-level keys of a planv2 Input
// envelope (which always starts with `{"body":`), producing e.g.
// `{"url":"http://...","method":"POST","header":{...},"body":{...}}`.
func wrapInputTransport(input string, t Transport) string {
	if !strings.HasPrefix(input, "{") {
		return input // defensive: not the expected envelope -- leave untouched
	}
	var b strings.Builder
	b.Grow(len(input) + len(t.URL) + len(t.Method) + len(t.Header) + 32)
	b.WriteByte('{')
	if t.URL != "" {
		b.WriteString(`"url":`)
		b.Write(jsonQuote(t.URL))
		b.WriteByte(',')
	}
	if t.Method != "" {
		b.WriteString(`"method":`)
		b.Write(jsonQuote(t.Method))
		b.WriteByte(',')
	}
	if len(t.Header) > 0 {
		b.WriteString(`"header":`)
		b.Write(t.Header)
		b.WriteByte(',')
	}
	b.WriteString(input[1:]) // the original body (and any other keys), minus its leading '{'
	return b.String()
}

// jsonQuote JSON-encodes a string value (quotes + escapes) for splicing into the Input envelope.
func jsonQuote(s string) []byte {
	out, err := json.Marshal(s)
	if err != nil { // json.Marshal of a string never errors, but stay defensive
		return []byte(`""`)
	}
	return out
}
