package external

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
)

// This file is the EXECUTION-CONFIG ADAPTER: it parses a Cosmo router execution-config.json
// (engineConfig.datasourceConfigurations[] + engineConfig.graphqlSchema) directly into the same
// []plan.DataSource + client schema that the v1 plan.Planner and the planv2 facade consume. This is
// the SAME structure Cosmo's router feeds the v1 planner in production -- the protojson field names
// mirror plan.DataSourceMetadata / plan.FederationMetaData closely -- so a snapshot of a live graph's
// config plans here exactly as it would in the router, without going back through SDL derivation
// (audit.BuildDataSources' path). No customer data lives in THIS file: it is a generic format
// decoder, exercised only by a synthetic fixture (execconfig_test.go). Corpus content is read at run
// time by the env-gated sweep and never embedded here or in any committed artifact.
//
// Only the fields the planner needs are decoded; unknown JSON fields are ignored. Every string field
// is camelCase to match protojson output (the on-disk form of nodev1.RouterConfig).

// ExecutionConfig is the top-level execution-config.json shape (only the decoded subset).
type ExecutionConfig struct {
	EngineConfig EngineConfig `json:"engineConfig"`
}

// EngineConfig mirrors nodev1.EngineConfiguration (decoded subset).
type EngineConfig struct {
	// GraphQLSchema is the router's internal planning schema (the composed supergraph with
	// federation directives) -- the `definition` both planners plan against. GraphQLClientSchema is
	// the narrower client-facing schema; it is decoded for completeness but planning uses
	// GraphQLSchema, matching the router.
	GraphQLSchema       internedString     `json:"graphqlSchema"`
	GraphQLClientSchema internedString     `json:"graphqlClientSchema"`
	DataSourceConfigs   []DataSourceConfig `json:"datasourceConfigurations"`
	FieldConfigs        []FieldConfigJSON  `json:"fieldConfigurations"`

	// StringStorage is the router's content-addressed string pool. Large SDL blobs (notably each
	// datasource's upstream schema) are stored here once and referenced by key elsewhere in the
	// config ({"key": "<hash>"}), so any interned field must be resolved through this map. See
	// internedString.
	StringStorage map[string]string `json:"stringStorage"`
}

// internedString decodes a Cosmo InternedString field, which appears in the JSON as EITHER a plain
// string (the literal value inline) OR an object {"key": "<hash>"} referencing engineConfig
// .stringStorage. resolve() returns the effective value given the pool.
type internedString struct {
	literal string
	key     string
	isRef   bool
}

func (s *internedString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &s.literal)
	}
	var ref struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		return err
	}
	s.key, s.isRef = ref.Key, true
	return nil
}

func (s internedString) resolve(pool map[string]string) string {
	if s.isRef {
		return pool[s.key]
	}
	return s.literal
}

// DataSourceConfig mirrors nodev1.DataSourceConfiguration (decoded subset).
type DataSourceConfig struct {
	ID               string                `json:"id"`
	Kind             string                `json:"kind"` // GRAPHQL, PUBSUB, STATIC, ...
	RootNodes        []TypeFieldJSON       `json:"rootNodes"`
	ChildNodes       []TypeFieldJSON       `json:"childNodes"`
	Keys             []FederationFieldJSON `json:"keys"`
	Provides         []FederationFieldJSON `json:"provides"`
	Requires         []FederationFieldJSON `json:"requires"`
	EntityInterfaces []EntityInterfaceJSON `json:"entityInterfaces"`
	InterfaceObjects []EntityInterfaceJSON `json:"interfaceObjects"`
	CustomGraphQL    *CustomGraphQLJSON    `json:"customGraphql"`
}

// FieldConfigJSON mirrors nodev1.FieldConfiguration -- the per-field argument-source configuration the
// router feeds both planners so a field's arguments (e.g. product(id:)) are propagated to the correct
// subgraph fetch. Only the argument mapping is decoded (path/authorization/subscription-filter are
// not needed for plan-shape comparison).
type FieldConfigJSON struct {
	TypeName  string               `json:"typeName"`
	FieldName string               `json:"fieldName"`
	Arguments []ArgumentConfigJSON `json:"argumentsConfiguration"`
}

// ArgumentConfigJSON mirrors nodev1.ArgumentConfiguration.
type ArgumentConfigJSON struct {
	Name       string   `json:"name"`
	SourceType string   `json:"sourceType"` // FIELD_ARGUMENT | OBJECT_FIELD
	SourcePath []string `json:"sourcePath"`
}

// TypeFieldJSON mirrors nodev1.TypeField.
type TypeFieldJSON struct {
	TypeName           string   `json:"typeName"`
	FieldNames         []string `json:"fieldNames"`
	ExternalFieldNames []string `json:"externalFieldNames"`
}

// FederationFieldJSON mirrors nodev1.FederationFieldConfiguration (keys / requires / provides).
type FederationFieldJSON struct {
	TypeName              string             `json:"typeName"`
	FieldName             string             `json:"fieldName"`
	SelectionSet          string             `json:"selectionSet"`
	DisableEntityResolver bool               `json:"disableEntityResolver"`
	Conditions            []KeyConditionJSON `json:"conditions"`
}

// KeyConditionJSON mirrors nodev1.KeyCondition (implicit-key conditions).
type KeyConditionJSON struct {
	Coordinates []FieldCoordinateJSON `json:"coordinates"`
	FieldPath   []string              `json:"fieldPath"`
}

// FieldCoordinateJSON mirrors nodev1.FieldCoordinates.
type FieldCoordinateJSON struct {
	TypeName  string `json:"typeName"`
	FieldName string `json:"fieldName"`
}

// EntityInterfaceJSON mirrors nodev1.EntityInterfaceConfiguration (entityInterfaces / interfaceObjects).
type EntityInterfaceJSON struct {
	InterfaceTypeName string   `json:"interfaceTypeName"`
	ConcreteTypeNames []string `json:"concreteTypeNames"`
}

// CustomGraphQLJSON mirrors nodev1.DataSourceCustom_GraphQL -- the GraphQL-datasource-specific block
// carrying the upstream (subgraph) schema and its federation service SDL. UpstreamSchema is
// (usually) interned into stringStorage; ServiceSDL is (usually) inline. Both are decoded as
// internedString to handle either form. The fetch block (upstream URL/headers/body) is deliberately
// NOT decoded: it has no bearing on planning or response shape, and its fields are themselves
// interned -- the datasource id is used to synthesize a stable placeholder URL instead. The
// SUBSCRIPTION block IS decoded (D11.12): its url/protocol/subprotocol shape the trigger envelope
// both planners emit, and v1's ConfigureSubscription hard-errors without a subscription
// configuration ("subscription configuration is empty" -- the sweep's v1-error class).
type CustomGraphQLJSON struct {
	UpstreamSchema internedString       `json:"upstreamSchema"`
	Federation     *FederationCfgJSON   `json:"federation"`
	Subscription   *SubscriptionCfgJSON `json:"subscription"`
}

// SubscriptionCfgJSON mirrors nodev1.GraphQLSubscriptionConfiguration (decoded subset). URL is an
// InternedString like the other endpoint fields; Protocol/WebsocketSubprotocol are protojson enum
// names.
type SubscriptionCfgJSON struct {
	Enabled              bool           `json:"enabled"`
	URL                  internedString `json:"url"`
	Protocol             string         `json:"protocol"`             // GRAPHQL_SUBSCRIPTION_PROTOCOL_WS | _SSE | _SSE_POST
	WebsocketSubprotocol string         `json:"websocketSubprotocol"` // GRAPHQL_WEBSOCKET_SUBPROTOCOL_AUTO | _WS | _TRANSPORT_WS
}

// FederationCfgJSON mirrors nodev1.GraphQLFederationConfiguration.
type FederationCfgJSON struct {
	Enabled    bool           `json:"enabled"`
	ServiceSDL internedString `json:"serviceSdl"`
}

// LoadExecutionConfig reads and decodes an execution-config.json file. It decodes only the subset the
// planner needs; unknown fields are ignored.
func LoadExecutionConfig(path string) (*ExecutionConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read execution config %q: %w", path, err)
	}
	var ec ExecutionConfig
	if err := json.Unmarshal(data, &ec); err != nil {
		return nil, fmt.Errorf("decode execution config %q: %w", path, err)
	}
	return &ec, nil
}

// ClientSchema returns the router's internal planning schema (engineConfig.graphqlSchema) -- the
// `definition` both the v1 planner and the planv2 facade plan against -- resolving it through the
// string pool if it is interned.
func (ec *ExecutionConfig) ClientSchema() string {
	return ec.EngineConfig.GraphQLSchema.resolve(ec.EngineConfig.StringStorage)
}

// FieldConfigurations maps engineConfig.fieldConfigurations into plan.FieldConfigurations -- the
// argument-source table both the v1 planner and planv2 consult to route a field's arguments to the
// right subgraph fetch. Without it, operations that pass required arguments (product(id:$id)) fail
// upstream-document validation ("argument id is required but missing"), exactly as they would in a
// mis-configured router.
func (ec *ExecutionConfig) FieldConfigurations() plan.FieldConfigurations {
	if len(ec.EngineConfig.FieldConfigs) == 0 {
		return nil
	}
	out := make(plan.FieldConfigurations, 0, len(ec.EngineConfig.FieldConfigs))
	for _, fc := range ec.EngineConfig.FieldConfigs {
		cfg := plan.FieldConfiguration{TypeName: fc.TypeName, FieldName: fc.FieldName}
		for _, a := range fc.Arguments {
			cfg.Arguments = append(cfg.Arguments, plan.ArgumentConfiguration{
				Name:       a.Name,
				SourceType: argumentSourceType(a.SourceType),
				SourcePath: a.SourcePath,
			})
		}
		out = append(out, cfg)
	}
	return out
}

// argumentSourceType maps the protojson enum name to the plan SourceType constant.
func argumentSourceType(s string) plan.SourceType {
	switch s {
	case "OBJECT_FIELD":
		return plan.ObjectFieldSource
	default: // FIELD_ARGUMENT (and unspecified)
		return plan.FieldArgumentSource
	}
}

// BuildDataSources maps every GRAPHQL datasource in the execution config into a plan.DataSource,
// building a real graphql_datasource.Configuration (federation enabled where the config says so) with
// the subgraph's upstream schema straight from the config -- the production router's own path,
// bypassing SDL-based metadata derivation (audit.BuildDataSources). Non-GraphQL datasources (PUBSUB,
// etc.) are skipped: planner-v2 plans GraphQL subgraphs only, and the audit/differential machinery
// this harness reuses is GraphQL-only.
//
// The returned data sources are ready to pass as plan.Configuration.DataSources to both
// plan.NewPlanner (v1) and planv2.NewPlanner.
func (ec *ExecutionConfig) BuildDataSources() ([]plan.DataSource, error) {
	pool := ec.EngineConfig.StringStorage
	out := make([]plan.DataSource, 0, len(ec.EngineConfig.DataSourceConfigs))
	for i := range ec.EngineConfig.DataSourceConfigs {
		dsc := &ec.EngineConfig.DataSourceConfigs[i]
		if dsc.Kind != "GRAPHQL" {
			continue // planner-v2 and this harness are GraphQL-only
		}
		if dsc.CustomGraphQL == nil || dsc.CustomGraphQL.UpstreamSchema.resolve(pool) == "" {
			return nil, fmt.Errorf("datasource %q: GRAPHQL kind without customGraphql.upstreamSchema", dsc.ID)
		}
		ds, err := buildGraphQLDataSource(dsc, pool)
		if err != nil {
			return nil, err
		}
		out = append(out, ds)
	}
	return out, nil
}

func buildGraphQLDataSource(dsc *DataSourceConfig, pool map[string]string) (plan.DataSource, error) {
	meta := buildMetadata(dsc)

	var fedCfg *graphql_datasource.FederationConfiguration
	if f := dsc.CustomGraphQL.Federation; f != nil && f.Enabled {
		fedCfg = &graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: f.ServiceSDL.resolve(pool)}
	}

	schemaCfg, err := graphql_datasource.NewSchemaConfiguration(dsc.CustomGraphQL.UpstreamSchema.resolve(pool), fedCfg)
	if err != nil {
		return nil, fmt.Errorf("datasource %q: schema config: %w", dsc.ID, err)
	}

	cfg, err := graphql_datasource.NewConfiguration(graphql_datasource.ConfigurationInput{
		Fetch: &graphql_datasource.FetchConfiguration{URL: "http://" + dsc.ID},
		// Subscription transport (D11.12): decoded from the config's subscription block when
		// present, defaulted otherwise. Always populated for a GRAPHQL datasource -- v1's
		// ConfigureSubscription hard-errors on a nil subscription configuration, and planv2's
		// trigger would otherwise lower hollow (a body-only envelope with no url/source). An empty
		// URL inherits the fetch URL inside NewConfiguration, mirroring the router's default.
		Subscription:        subscriptionConfiguration(dsc, pool),
		SchemaConfiguration: schemaCfg,
	})
	if err != nil {
		return nil, fmt.Errorf("datasource %q: config: %w", dsc.ID, err)
	}

	ds, err := plan.NewDataSourceConfiguration[graphql_datasource.Configuration](
		dsc.ID, &graphql_datasource.Factory[graphql_datasource.Configuration]{}, meta, cfg)
	if err != nil {
		return nil, fmt.Errorf("datasource %q: %w", dsc.ID, err)
	}
	return ds, nil
}

// subscriptionConfiguration maps a datasource's customGraphql.subscription block into the
// graphql_datasource subscription transport: protocol -> UseSSE/SSEMethodPost, websocketSubprotocol ->
// the wire subprotocol name ("" = auto-negotiate). A missing block yields the router's defaults
// (WS transport on the fetch URL).
func subscriptionConfiguration(dsc *DataSourceConfig, pool map[string]string) *graphql_datasource.SubscriptionConfiguration {
	out := &graphql_datasource.SubscriptionConfiguration{}
	sub := dsc.CustomGraphQL.Subscription
	if sub == nil {
		return out
	}
	out.URL = sub.URL.resolve(pool)
	switch sub.Protocol {
	case "GRAPHQL_SUBSCRIPTION_PROTOCOL_SSE":
		out.UseSSE = true
	case "GRAPHQL_SUBSCRIPTION_PROTOCOL_SSE_POST":
		out.UseSSE = true
		out.SSEMethodPost = true
	default: // GRAPHQL_SUBSCRIPTION_PROTOCOL_WS (and unspecified)
	}
	switch sub.WebsocketSubprotocol {
	case "GRAPHQL_WEBSOCKET_SUBPROTOCOL_WS":
		out.WsSubProtocol = "graphql-ws"
	case "GRAPHQL_WEBSOCKET_SUBPROTOCOL_TRANSPORT_WS":
		out.WsSubProtocol = "graphql-transport-ws"
	default: // GRAPHQL_WEBSOCKET_SUBPROTOCOL_AUTO (and unspecified): "" = negotiate
	}
	return out
}

// buildMetadata translates a datasource's node/federation JSON directly into a
// plan.DataSourceMetadata -- the same struct audit.BuildDataSources derives from SDL, but taken
// verbatim from the config the router already computed at composition time.
func buildMetadata(dsc *DataSourceConfig) *plan.DataSourceMetadata {
	return &plan.DataSourceMetadata{
		RootNodes:  toTypeFields(dsc.RootNodes),
		ChildNodes: toTypeFields(dsc.ChildNodes),
		FederationMetaData: plan.FederationMetaData{
			Keys:             toFederationFields(dsc.Keys),
			Requires:         toFederationFields(dsc.Requires),
			Provides:         toFederationFields(dsc.Provides),
			EntityInterfaces: toEntityInterfaces(dsc.EntityInterfaces),
			InterfaceObjects: toEntityInterfaces(dsc.InterfaceObjects),
		},
	}
}

func toTypeFields(in []TypeFieldJSON) plan.TypeFields {
	if len(in) == 0 {
		return nil
	}
	out := make(plan.TypeFields, 0, len(in))
	for _, tf := range in {
		out = append(out, plan.TypeField{
			TypeName:           tf.TypeName,
			FieldNames:         tf.FieldNames,
			ExternalFieldNames: tf.ExternalFieldNames,
		})
	}
	return out
}

func toFederationFields(in []FederationFieldJSON) plan.FederationFieldConfigurations {
	if len(in) == 0 {
		return nil
	}
	out := make(plan.FederationFieldConfigurations, 0, len(in))
	for _, f := range in {
		out = append(out, plan.FederationFieldConfiguration{
			TypeName:              f.TypeName,
			FieldName:             f.FieldName,
			SelectionSet:          f.SelectionSet,
			DisableEntityResolver: f.DisableEntityResolver,
			Conditions:            toKeyConditions(f.Conditions),
		})
	}
	return out
}

func toKeyConditions(in []KeyConditionJSON) []plan.KeyCondition {
	if len(in) == 0 {
		return nil
	}
	out := make([]plan.KeyCondition, 0, len(in))
	for _, c := range in {
		coords := make([]plan.FieldCoordinate, 0, len(c.Coordinates))
		for _, co := range c.Coordinates {
			coords = append(coords, plan.FieldCoordinate{TypeName: co.TypeName, FieldName: co.FieldName})
		}
		out = append(out, plan.KeyCondition{Coordinates: coords, FieldPath: c.FieldPath})
	}
	return out
}

func toEntityInterfaces(in []EntityInterfaceJSON) []plan.EntityInterfaceConfiguration {
	if len(in) == 0 {
		return nil
	}
	out := make([]plan.EntityInterfaceConfiguration, 0, len(in))
	for _, e := range in {
		out = append(out, plan.EntityInterfaceConfiguration{
			InterfaceTypeName: e.InterfaceTypeName,
			ConcreteTypeNames: e.ConcreteTypeNames,
		})
	}
	return out
}
