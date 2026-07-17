package testdata

// edfs_subscription.go -- fixture for the M4.2 EDFS (event-driven federated subscriptions) worked
// example (FEDERATION_SEMANTICS.md FS-EDFS, FORMAL_SPEC.md D5-EDFS / D11.12-EDFS). Subgraph P is a
// NATS EDFS event source: its subscription root field carries an `@edfs__natsSubscribe` directive
// and its payload is the entity `Product` (declared with a `@key` so it resolves per event). This
// is the common Cosmo shape where the composed subgraph SDL for the event source IS present in the
// config (the router composes it) -- the event source contributes its type shape like any subgraph;
// only its transport (a broker binding, D11.12-EDFS) differs, which is a lowering concern the
// planner/search layer never sees. Subgraph B (an ordinary HTTP subgraph) owns `Product.price`
// behind the `@key(id)` entity jump. A subscription selecting `price` therefore plans as a trigger
// against P plus one per-event `_entities` fetch against B -- the FS-SUB trigger/response split with
// a real cross-subgraph jump below an EDFS root.
//
// The pure SDL-less direction (a PUBSUB datasource whose config carries `customEvents` and NO
// upstream schema, so the payload type comes only from the composed schema, D5-EDFS) is the
// residual that needs the real-router config shape -- see DIVERGENCES.md DV-011.

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// EDFSSubscriptionConfig is the EDFS event-source subscription shape: subgraph P owns the
// `@edfs__natsSubscribe` root field `productUpdated(id: ID!): Product` plus Product's key and
// `name`; subgraph B owns `Product.price` behind the entity jump. Parity with SubscriptionProductConfig
// (the ordinary-subscription twin) except that P's root field is a NATS event source rather than an
// HTTP subscription -- the plan shape is identical; only the trigger transport differs at lowering.
func EDFSSubscriptionConfig() []plan.DataSource {
	const schemaP = `
type Query { product: Product }
type Subscription { productUpdated(id: ID!): Product @edfs__natsSubscribe(subjects: ["products.{{ args.id }}"]) }
type Product @key(fields: "id") {
  id: ID!
  name: String
}
`
	const schemaB = `
type Product @key(fields: "id") {
  id: ID!
  price: Float
}
`
	p := newDataSource("P", schemaP, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"product"}},
			{TypeName: "Subscription", FieldNames: []string{"productUpdated"}},
			{TypeName: "Product", FieldNames: []string{"id", "name"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Product", SelectionSet: "id"},
			},
		},
	})

	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Product", FieldNames: []string{"id", "price"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Product", SelectionSet: "id"},
			},
		},
	})

	return []plan.DataSource{p, b}
}
