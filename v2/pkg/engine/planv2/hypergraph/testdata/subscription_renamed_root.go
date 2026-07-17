package testdata

// subscription_renamed_root.go -- fixtures for the D5pp-analog SUBSCRIPTION root-resolution classes
// (customer-sweep "obligation 0 unreachable" family): a subscription whose payload types are pure
// subscription payloads (no query field returns them, no keys), so the ONLY route to every leaf is
// the subscription root chain in the owner subgraph. If the builder cannot tie the metadata's root
// type name to the owner's SDL, the whole payload subtree is unreachable.

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// SubscriptionRenamedRootConfig is the FIXED class: the owner subgraph renames its subscription
// root (`schema { subscription: SubscriptionRoot }`) and the node metadata lists the subscription
// root field under that renamed name. The builder's per-subgraph subscription root view must map
// the renamed name onto the subscription root node. Subgraph B declares no Subscription at all
// (the subset-declaration shape of the multi-subgraph customer config).
func SubscriptionRenamedRootConfig() []plan.DataSource {
	const schemaA = `
schema { query: Query subscription: SubscriptionRoot }
type Query { ping: String }
type SubscriptionRoot { productUpdated(upc: String!): Product }
type Product {
  id: ID!
  name: String
  price: Float
}
`
	const schemaB = `
type Query { other: String }
`
	a := newDataSource("A", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"ping"}},
			{TypeName: "SubscriptionRoot", FieldNames: []string{"productUpdated"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Product", FieldNames: []string{"id", "name", "price"}},
		},
	})
	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"other"}},
		},
	})
	return []plan.DataSource{a, b}
}

// SubscriptionMetadataOnlyConfig is the RESIDUAL class, pinned fail-loud: the metadata declares the
// subscription root field but the owner's SDL carries NO Subscription type at all (schema drift, or
// a field actually owned by a non-GraphQL/pubsub trigger datasource the harness skips). No source
// available to the builder can resolve the root field's output type, so the payload subtree is
// unreachable -- the planner must fail with the typed no-valid-plan error, never a silent wrong plan.
func SubscriptionMetadataOnlyConfig() []plan.DataSource {
	const schemaA = `
type Query { ping: String }
type Product {
  id: ID!
  name: String
  price: Float
}
`
	const schemaB = `
type Query { other: String }
`
	a := newDataSource("A", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"ping"}},
			{TypeName: "Subscription", FieldNames: []string{"productUpdated"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Product", FieldNames: []string{"id", "name", "price"}},
		},
	})
	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"other"}},
		},
	})
	return []plan.DataSource{a, b}
}
