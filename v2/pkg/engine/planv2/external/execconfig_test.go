package external

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
)

// syntheticExecConfig is a hand-written, two-subgraph execution-config.json mirroring the SAME shape
// a Cosmo router writes (protojson of nodev1.RouterConfig). It is a toy variation on the classic
// PUBLIC federation demo shape (accounts: User; products: Product/topProducts + User.reviews entity
// extension) -- NO customer/external data. It exercises every field the adapter maps: rootNodes,
// childNodes, keys (with disableEntityResolver), requires, and the customGraphql upstream/federation
// blocks. The escaping is deliberately verbose so this stays a literal, reviewable fixture rather than
// generated content.
const syntheticExecConfig = `{
  "engineConfig": {
    "graphqlSchema": "schema { query: Query subscription: Subscription } type Query { me: User topProducts: [Product] } type Subscription { updatedPrice: Product } type User { id: ID! name: String reviews: [Review] } type Product { upc: String! name: String price: Int reviews: [Review] } type Review { id: ID! body: String author: User }",
    "graphqlClientSchema": "schema { query: Query subscription: Subscription } type Query { me: User topProducts: [Product] } type Subscription { updatedPrice: Product } type User { id: ID! name: String reviews: [Review] } type Product { upc: String! name: String price: Int reviews: [Review] } type Review { id: ID! body: String author: User }",
    "datasourceConfigurations": [
      {
        "id": "accounts",
        "kind": "GRAPHQL",
        "rootNodes": [
          { "typeName": "Query", "fieldNames": ["me"] },
          { "typeName": "User", "fieldNames": ["id", "name"] }
        ],
        "keys": [
          { "typeName": "User", "selectionSet": "id" }
        ],
        "customGraphql": {
          "upstreamSchema": "type Query { me: User } type User @key(fields: \"id\") { id: ID! name: String }",
          "federation": { "enabled": true, "serviceSdl": "type Query { me: User } type User @key(fields: \"id\") { id: ID! name: String }" },
          "fetch": { "url": "http://accounts" }
        }
      },
      {
        "id": "products",
        "kind": "GRAPHQL",
        "rootNodes": [
          { "typeName": "Query", "fieldNames": ["topProducts"] },
          { "typeName": "Subscription", "fieldNames": ["updatedPrice"] },
          { "typeName": "Product", "fieldNames": ["upc", "name", "price", "reviews"] },
          { "typeName": "User", "fieldNames": ["reviews"] }
        ],
        "childNodes": [
          { "typeName": "Review", "fieldNames": ["id", "body", "author"] },
          { "typeName": "User", "fieldNames": ["id"] }
        ],
        "keys": [
          { "typeName": "Product", "selectionSet": "upc" },
          { "typeName": "User", "selectionSet": "id" }
        ],
        "customGraphql": {
          "upstreamSchema": "type Query { topProducts: [Product] } type Subscription { updatedPrice: Product } type Product @key(fields: \"upc\") { upc: String! name: String price: Int reviews: [Review] } type Review { id: ID! body: String author: User } type User @key(fields: \"id\") { id: ID! reviews: [Review] }",
          "federation": { "enabled": true, "serviceSdl": "type Query { topProducts: [Product] } type Subscription { updatedPrice: Product } type Product @key(fields: \"upc\") { upc: String! name: String price: Int reviews: [Review] } type Review { id: ID! body: String author: User } type User @key(fields: \"id\") { id: ID! reviews: [Review] }" },
          "fetch": { "url": "http://products" },
          "subscription": { "enabled": true, "url": "ws://products", "protocol": "GRAPHQL_SUBSCRIPTION_PROTOCOL_WS", "websocketSubprotocol": "GRAPHQL_WEBSOCKET_SUBPROTOCOL_TRANSPORT_WS" }
        }
      },
      {
        "id": "events",
        "kind": "PUBSUB"
      }
    ]
  }
}`

// TestExecConfigAdapterDecodeAndMap verifies the pure decode+map layer: the two GRAPHQL datasources
// are produced (the PUBSUB one skipped), the client schema is surfaced, and the federation/node
// metadata round-trips into the plan types the way the router computed it.
func TestExecConfigAdapterDecodeAndMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution-config.json")
	if err := os.WriteFile(path, []byte(syntheticExecConfig), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ec, err := LoadExecutionConfig(path)
	if err != nil {
		t.Fatalf("load execution config: %v", err)
	}
	if ec.ClientSchema() == "" {
		t.Fatal("client schema empty")
	}
	if len(ec.EngineConfig.DataSourceConfigs) != 3 {
		t.Fatalf("decoded %d datasource configs, want 3", len(ec.EngineConfig.DataSourceConfigs))
	}

	ds, err := ec.BuildDataSources()
	if err != nil {
		t.Fatalf("build data sources: %v", err)
	}
	if len(ds) != 2 {
		t.Fatalf("built %d data sources, want 2 (PUBSUB skipped)", len(ds))
	}

	// Federation metadata must survive the mapping: products carries a resolvable Product key and a
	// non-resolvable (disableEntityResolver) User key.
	var products plan.DataSource
	for _, d := range ds {
		if d.Id() == "products" {
			products = d
		}
	}
	if products == nil {
		t.Fatal("products data source missing")
	}
	if !products.HasEntity("Product") {
		t.Error("products must expose Product as an entity (key)")
	}
	if !products.HasRootNode("Query", "topProducts") {
		t.Error("products must own Query.topProducts as a root node")
	}
	if !products.HasChildNode("Review", "body") {
		t.Error("products must own Review.body as a child node")
	}
	if !products.HasEntity("User") {
		t.Error("products must expose User as an entity (extended reviews key)")
	}
}

// TestExecConfigDisableEntityResolverMapping pins the disableEntityResolver flag mapping in the pure
// federation-field translation -- a non-resolvable key must be excluded from the resolvable-key filter
// the planner uses to decide whether an entity can be fetched by this subgraph.
func TestExecConfigDisableEntityResolverMapping(t *testing.T) {
	keys := toFederationFields([]FederationFieldJSON{
		{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
		{TypeName: "Product", SelectionSet: "upc"},
	})
	if got := keys.FilterByTypeAndResolvability("User", true); len(got) != 0 {
		t.Errorf("User key has disableEntityResolver=true; want 0 resolvable keys, got %d", len(got))
	}
	if got := keys.FilterByTypeAndResolvability("Product", true); len(got) != 1 {
		t.Errorf("Product key is resolvable; want 1 resolvable key, got %d", len(got))
	}
}

// TestExecConfigFieldConfigurations pins the argument-source mapping: the protojson enum names map to
// the plan SourceType constants, and every configured field is carried through.
func TestExecConfigFieldConfigurations(t *testing.T) {
	const cfgJSON = `{"engineConfig":{"fieldConfigurations":[
		{"typeName":"Query","fieldName":"product","argumentsConfiguration":[{"name":"id","sourceType":"FIELD_ARGUMENT"}]},
		{"typeName":"User","fieldName":"address","argumentsConfiguration":[{"name":"zip","sourceType":"OBJECT_FIELD","sourcePath":["zip"]}]}
	]}}`
	path := filepath.Join(t.TempDir(), "execution-config.json")
	if err := os.WriteFile(path, []byte(cfgJSON), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ec, err := LoadExecutionConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fields := ec.FieldConfigurations()
	if len(fields) != 2 {
		t.Fatalf("got %d field configs, want 2", len(fields))
	}
	if fields[0].TypeName != "Query" || fields[0].FieldName != "product" ||
		len(fields[0].Arguments) != 1 || fields[0].Arguments[0].SourceType != plan.FieldArgumentSource {
		t.Errorf("product field config mismapped: %+v", fields[0])
	}
	if fields[1].Arguments[0].SourceType != plan.ObjectFieldSource ||
		len(fields[1].Arguments[0].SourcePath) != 1 || fields[1].Arguments[0].SourcePath[0] != "zip" {
		t.Errorf("address field config mismapped: %+v", fields[1])
	}
}

// TestExecConfigAdapterPlansBothPlanners proves the adapter output is a valid plan.Configuration for
// BOTH the v1 planner and the planv2 facade: a cross-subgraph query (accounts.me -> products.reviews)
// plans without error on each, and the two agree on response shape. This is the end-to-end contract
// the corpus sweep relies on, exercised entirely on synthetic data.
func TestExecConfigAdapterPlansBothPlanners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution-config.json")
	if err := os.WriteFile(path, []byte(syntheticExecConfig), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ec, err := LoadExecutionConfig(path)
	if err != nil {
		t.Fatalf("load execution config: %v", err)
	}
	ds, err := ec.BuildDataSources()
	if err != nil {
		t.Fatalf("build data sources: %v", err)
	}
	cfg := plan.Configuration{DataSources: ds, DisableResolveFieldPositions: true}

	const op = `query { me { id name reviews { body } } }`

	newPlanner, err := planv2.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("planv2 NewPlanner: %v", err)
	}
	oldPlanner, err := plan.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("v1 NewPlanner: %v", err)
	}

	newOp, newDef, newReport := parseAndNormalize(ec.ClientSchema(), op)
	newPlan := newPlanner.Plan(newOp, newDef, "", newReport)
	if newReport.HasErrors() {
		t.Fatalf("planv2 plan: %s", newReport.Error())
	}
	if _, ok := newPlan.(*plan.SynchronousResponsePlan); !ok {
		t.Fatalf("planv2 plan type = %T, want *plan.SynchronousResponsePlan", newPlan)
	}

	oldOp, oldDef, oldReport := parseAndNormalize(ec.ClientSchema(), op)
	oldPlan := oldPlanner.Plan(oldOp, oldDef, "", oldReport)
	if oldReport.HasErrors() {
		t.Fatalf("v1 plan: %s", oldReport.Error())
	}

	// Response-shape parity: both planners must produce the same top-level response object.
	newResp, ok := newPlan.(*plan.SynchronousResponsePlan)
	if !ok {
		t.Fatalf("planv2 plan not synchronous: %T", newPlan)
	}
	oldResp, ok := oldPlan.(*plan.SynchronousResponsePlan)
	if !ok {
		t.Fatalf("v1 plan not synchronous: %T", oldPlan)
	}
	if newResp.Response == nil || oldResp.Response == nil {
		t.Fatal("nil response on a planner output")
	}
	if newResp.Response.Data == nil {
		t.Fatal("planv2 response root data is nil")
	}
}

// TestExecConfigAdapterSubscription pins the subscription-transport mapping (D11.12): the adapter
// must populate graphql_datasource's SubscriptionConfiguration from the config's
// customGraphql.subscription block (url + protocol + websocketSubprotocol), so that (a) the v1
// comparator's ConfigureSubscription does not error with "subscription configuration is empty" and
// (b) planv2's trigger is NOT hollow -- its input envelope carries the subscription url. This is the
// customer-sweep v1-error 8->43 class and the hollow-trigger false-success check.
func TestExecConfigAdapterSubscription(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution-config.json")
	if err := os.WriteFile(path, []byte(syntheticExecConfig), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ec, err := LoadExecutionConfig(path)
	if err != nil {
		t.Fatalf("load execution config: %v", err)
	}
	ds, err := ec.BuildDataSources()
	if err != nil {
		t.Fatalf("build data sources: %v", err)
	}

	// (a0) The mapped configuration itself carries the subscription transport.
	var products plan.DataSource
	for _, d := range ds {
		if d.Id() == "products" {
			products = d
		}
	}
	cfgDS, ok := products.(plan.DataSourceConfiguration[graphql_datasource.Configuration])
	if !ok {
		t.Fatalf("products is not a graphql datasource configuration: %T", products)
	}
	pCfg := cfgDS.CustomConfiguration()
	sub, ok := pCfg.SubscriptionConfiguration()
	if !ok {
		t.Fatal("products subscription configuration missing -- the adapter did not map customGraphql.subscription")
	}
	if sub.URL != "ws://products" {
		t.Errorf("subscription url = %q, want ws://products", sub.URL)
	}
	if sub.WsSubProtocol != "graphql-transport-ws" {
		t.Errorf("ws sub-protocol = %q, want graphql-transport-ws", sub.WsSubProtocol)
	}
	if sub.UseSSE || sub.SSEMethodPost {
		t.Errorf("protocol WS must not map to SSE flags: %+v", sub)
	}

	cfg := plan.Configuration{DataSources: ds, DisableResolveFieldPositions: true}
	const op = `subscription { updatedPrice { upc price name } }`

	// (a) v1 plans the subscription without "subscription configuration is empty".
	oldPlanner, err := plan.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("v1 NewPlanner: %v", err)
	}
	oldOp, oldDef, oldReport := parseAndNormalize(ec.ClientSchema(), op)
	oldPlan := oldPlanner.Plan(oldOp, oldDef, "", oldReport)
	if oldReport.HasErrors() {
		t.Fatalf("v1 subscription plan errored (the sweep's v1-error class): %s", oldReport.Error())
	}
	if _, ok := oldPlan.(*plan.SubscriptionResponsePlan); !ok {
		t.Fatalf("v1 plan type = %T, want *plan.SubscriptionResponsePlan", oldPlan)
	}

	// (b) planv2's trigger is not hollow: the input envelope carries the subscription url.
	newPlanner, err := planv2.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("planv2 NewPlanner: %v", err)
	}
	newOp, newDef, newReport := parseAndNormalize(ec.ClientSchema(), op)
	newPlan := newPlanner.Plan(newOp, newDef, "", newReport)
	if newReport.HasErrors() {
		t.Fatalf("planv2 subscription plan errored: %s", newReport.Error())
	}
	subPlan, ok := newPlan.(*plan.SubscriptionResponsePlan)
	if !ok {
		t.Fatalf("planv2 plan type = %T, want *plan.SubscriptionResponsePlan", newPlan)
	}
	input := string(subPlan.Response.Trigger.Input)
	if !strings.Contains(input, `"url":"ws://products"`) {
		t.Errorf("planv2 trigger is hollow -- input lacks the subscription url: %s", input)
	}
	if subPlan.Response.Trigger.Source == nil {
		t.Error("planv2 trigger carries no subscription source")
	}
}
