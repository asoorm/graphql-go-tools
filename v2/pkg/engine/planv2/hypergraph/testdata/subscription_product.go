package testdata

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// SubscriptionProductConfig is the subscription worked shape (FORMAL_SPEC D11.12): subgraph A owns
// the Subscription root field `productUpdated(upc: String!): Product` plus Product's key and `name`;
// subgraph B owns `Product.price` behind the @key(id) entity jump. A subscription selecting `price`
// therefore lowers to a trigger against A plus one per-event `_entities` fetch against B -- the
// trigger/response split with a real cross-subgraph jump below the root.
func SubscriptionProductConfig() []plan.DataSource {
	const schemaA = `
type Query { product: Product }
type Subscription { productUpdated(upc: String!): Product }
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
	a := newDataSource("A", schemaA, &plan.DataSourceMetadata{
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

	return []plan.DataSource{a, b}
}
