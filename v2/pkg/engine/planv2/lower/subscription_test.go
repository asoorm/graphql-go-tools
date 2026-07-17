package lower

// subscription_test.go pins D11.12 at the lowering layer: the trigger/response split at the root
// position, trigger input correctness (variables/arguments on the root field), the per-event entity
// fetch below the root, the single-root-field precondition, and the trigger transport attach.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cespare/xxhash/v2"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/httpclient"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

const subscriptionSupergraph = `
schema { query: Query subscription: Subscription }
type Query { product: Product }
type Subscription { productUpdated(upc: String!): Product }
type Product { id: ID! name: String price: Float }
`

// buildHSub is buildH with the subscription root registered -- the facade's RootType map once
// subscriptions are in scope.
func buildHSub(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	h, err := hypergraph.Build(hgtestdata.SubscriptionProductConfig(), hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation", "subscription": "Subscription"},
	})
	if err != nil {
		t.Fatalf("build H: %v", err)
	}
	return h
}

func planSubscription(t *testing.T, op string) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	h := buildHSub(t)
	o, opDoc, defDoc := buildTree(t, h, subscriptionSupergraph, op)
	res := runSearch(t, h, o)
	return h, o, res, opDoc, defDoc
}

// TestLowerSubscription_TriggerResponseSplit pins the split: the root group becomes the trigger
// (subscription keyword, no fetch entry), the entity jump below the root stays an ordinary query
// fetch depending on the trigger's id slot, and the response tree carries the full client selection.
func TestLowerSubscription_TriggerResponseSplit(t *testing.T) {
	h, o, res, opDoc, defDoc := planSubscription(t,
		`subscription($upc: String!) { productUpdated(upc: $upc) { name price } }`)

	p, err := LowerSubscription(h, o, res, opDoc, defDoc)
	if err != nil {
		t.Fatalf("LowerSubscription: %v", err)
	}
	sub := p.Response

	// Trigger document: subscription keyword, root field with its argument, A-local selection.
	doc := sub.Trigger.QueryPlan.Query
	if !strings.HasPrefix(doc, "subscription($upc: String!)") {
		t.Fatalf("trigger document must declare the forwarded variable on the subscription keyword: %s", doc)
	}
	if !strings.Contains(doc, "productUpdated(upc: $upc)") || !strings.Contains(doc, "name") {
		t.Fatalf("trigger document must select the root field with arguments: %s", doc)
	}
	if strings.Contains(doc, "price") {
		t.Fatalf("price is owned by B and must not leak into the trigger document: %s", doc)
	}

	// Trigger input: shape-only (nil transport) -- body-only envelope with the $$0$$ context variable.
	input := string(sub.Trigger.Input)
	if !strings.HasPrefix(input, `{"body":{"query":`) {
		t.Fatalf("shape-only trigger input must be the body-only envelope: %s", input)
	}
	if !strings.Contains(input, `"variables":{"upc":$$0$$}`) {
		t.Fatalf("trigger input must forward $upc as $$0$$: %s", input)
	}
	if len(sub.Trigger.Variables) != 1 {
		t.Fatalf("trigger must carry exactly one forwarded variable, got %d", len(sub.Trigger.Variables))
	}
	if sub.Trigger.Source != nil {
		t.Fatalf("shape-only lowering must not attach a subscription source, got %T", sub.Trigger.Source)
	}
	// v1 trigger post-processing contract: data + errors paths.
	if got := sub.Trigger.PostProcessing; len(got.SelectResponseDataPath) != 1 || got.SelectResponseDataPath[0] != "data" ||
		len(got.SelectResponseErrorsPath) != 1 || got.SelectResponseErrorsPath[0] != "errors" {
		t.Fatalf("trigger post-processing must select data+errors, got %+v", got)
	}

	// Response: one per-event entity fetch (price via B), a query, depending on the trigger's slot.
	resp := sub.Response
	if resp.Info == nil || resp.Info.OperationType != ast.OperationTypeSubscription {
		t.Fatalf("response Info.OperationType must be subscription, got %+v", resp.Info)
	}
	if len(resp.RawFetches) != 1 {
		t.Fatalf("want 1 per-event entity fetch, got %d", len(resp.RawFetches))
	}
	sf := resp.RawFetches[0].Fetch.(*resolve.SingleFetch)
	if !sf.RequiresEntityFetch && !sf.RequiresEntityBatchFetch {
		t.Fatal("the fetch below the root must be an entity fetch")
	}
	if !strings.HasPrefix(sf.FetchConfiguration.QueryPlan.Query, "query") {
		t.Fatalf("per-event fetches stay query-shaped (FS-SUB-4): %s", sf.FetchConfiguration.QueryPlan.Query)
	}
	if len(sf.DependsOnFetchIDs) != 1 || sf.DependsOnFetchIDs[0] != 0 {
		t.Fatalf("the entity fetch must depend on the trigger's id slot 0, got %v", sf.DependsOnFetchIDs)
	}
	if sf.FetchDependencies.FetchID == 0 {
		t.Fatal("the trigger occupies fetch id 0; the entity fetch must keep its own id")
	}

	// Response shape (I4): the client selection tree, root field included.
	if got := string(resp.Data.Fields[0].Name); got != "productUpdated" {
		t.Fatalf("response root field must be productUpdated, got %q", got)
	}
	rootObj, ok := resp.Data.Fields[0].Value.(*resolve.Object)
	if !ok {
		t.Fatalf("root field value must be an object, got %T", resp.Data.Fields[0].Value)
	}
	var keys []string
	for _, f := range rootObj.Fields {
		keys = append(keys, string(f.Name))
	}
	if strings.Join(keys, " ") != "name price" {
		t.Fatalf("per-event response must select name price, got %v", keys)
	}
}

// TestLowerSubscription_SingleSubgraph pins the trigger-only plan: a subscription whose whole
// selection is served by the root subgraph lowers to a trigger and ZERO per-event fetches.
func TestLowerSubscription_SingleSubgraph(t *testing.T) {
	h, o, res, opDoc, defDoc := planSubscription(t,
		`subscription($upc: String!) { productUpdated(upc: $upc) { id name } }`)

	p, err := LowerSubscription(h, o, res, opDoc, defDoc)
	if err != nil {
		t.Fatalf("LowerSubscription: %v", err)
	}
	if got := len(p.Response.Response.RawFetches); got != 0 {
		t.Fatalf("trigger-only subscription must emit no per-event fetches, got %d", got)
	}
	doc := p.Response.Trigger.QueryPlan.Query
	for _, want := range []string{"subscription", "id", "name"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("trigger document missing %q: %s", want, doc)
		}
	}
}

// TestLowerSubscription_SingleRootFieldPrecondition pins the D11.12 precondition: more than one
// top-level field is a typed error, never a mis-lowered plan.
func TestLowerSubscription_SingleRootFieldPrecondition(t *testing.T) {
	h, o, res, opDoc, defDoc := planSubscription(t,
		`subscription($a: String! $b: String!) { x: productUpdated(upc: $a) { name } y: productUpdated(upc: $b) { name } }`)

	_, err := LowerSubscription(h, o, res, opDoc, defDoc)
	if err == nil {
		t.Fatal("multi-root subscription must fail with the typed single-root-field error")
	}
	if !strings.Contains(err.Error(), "exactly one root field") {
		t.Fatalf("want the single-root-field error, got: %v", err)
	}
}

// stubSubscriptionSource is a test double for the trigger's resolve.SubscriptionDataSource.
type stubSubscriptionSource struct{}

func (stubSubscriptionSource) Start(_ *resolve.Context, _ http.Header, _ []byte, _ resolve.SubscriptionUpdater) error {
	return nil
}
func (stubSubscriptionSource) HashTriggerInput(input []byte, xxh *xxhash.Digest) error {
	_, err := xxh.Write(input)
	return err
}

// TestLowerSubscriptionExecutable_TransportAttach pins the D11.12 trigger transport: the trigger
// input gains the subscription wire fields (url/ws flags/header -- never `method`), the trigger
// source is the subscription source, and per-event entity fetches keep the ordinary HTTP transport.
func TestLowerSubscriptionExecutable_TransportAttach(t *testing.T) {
	h, o, res, opDoc, defDoc := planSubscription(t,
		`subscription($upc: String!) { productUpdated(upc: $upc) { name price } }`)

	src := stubSubscriptionSource{}
	table := TransportTable{
		"A": {
			DataSource: stubDataSource{}, URL: "http://a/graphql", Method: "POST",
			Subscription: &SubscriptionTransport{
				Source:        src,
				URL:           "ws://a/graphql",
				WsSubProtocol: "graphql-transport-ws",
			},
		},
		"B": {DataSource: stubDataSource{}, URL: "http://b/graphql", Method: "POST"},
	}

	p, err := LowerSubscriptionExecutable(h, o, res, opDoc, defDoc, table)
	if err != nil {
		t.Fatalf("LowerSubscriptionExecutable: %v", err)
	}
	sub := p.Response

	input := string(sub.Trigger.Input)
	if !strings.Contains(input, `"url":"ws://a/graphql"`) {
		t.Fatalf("trigger input must carry the subscription url: %s", input)
	}
	if !strings.Contains(input, `"ws_sub_protocol":"graphql-transport-ws"`) {
		t.Fatalf("trigger input must carry the ws sub-protocol: %s", input)
	}
	if strings.Contains(input, `"method"`) {
		t.Fatalf("method is an HTTP-fetch field and must not appear on a trigger input: %s", input)
	}
	if sub.Trigger.Source != src {
		t.Fatalf("trigger source must be the subscription source, got %T", sub.Trigger.Source)
	}
	if sub.Trigger.SourceName != "A" {
		t.Fatalf("trigger source name must be the root subgraph, got %q", sub.Trigger.SourceName)
	}

	// The per-event entity fetch keeps the ordinary HTTP transport attach (D11.5).
	sf := sub.Response.RawFetches[0].Fetch.(*resolve.SingleFetch)
	if !strings.Contains(sf.FetchConfiguration.Input, `"url":"http://b/graphql"`) {
		t.Fatalf("entity fetch must carry B's HTTP transport: %s", sf.FetchConfiguration.Input)
	}
	if sf.FetchConfiguration.DataSource == nil {
		t.Fatal("entity fetch must carry its HTTP data source")
	}
}

// stubDataSource is a minimal resolve.DataSource for transport-attach assertions.
type stubDataSource struct{}

func (stubDataSource) Load(_ context.Context, _ http.Header, _ []byte) ([]byte, error) {
	panic("not used")
}
func (stubDataSource) LoadWithFiles(_ context.Context, _ http.Header, _ []byte, _ []*httpclient.FileUpload) ([]byte, error) {
	panic("not used")
}
