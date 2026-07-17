package testdata

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// DistributedKeyConfig mirrors the audit's `complex-entity-call` suite: composite @keys that NO
// single source subgraph supplies in full (FORMAL_SPEC D7ppp -- distributed @key).
//
//	link:     Product @key(id) @key(id pid)                        (pid owner via id)
//	list:     ProductList @key(products{id pid}); Product @key(id pid)
//	price:    ProductList @key(products{id pid category{id tag}} selected{id})   <- distributed
//	          Product @key(id pid category{id tag})                              <- distributed
//	products: Query.topProducts; ProductList @key(products{id});
//	          Product @extends @key(id) (id @external); Category (id tag mainProduct)
//
// price's Product key needs pid (link/list only) and category (products only) -- no single source.
// price's ProductList key additionally needs selected (list only). list's ProductList key
// products{id pid} IS fully supplied by price -- the D7ppp gate must leave it on base D7.
func DistributedKeyConfig() []plan.DataSource {
	const schemaLink = `
type Product @key(fields: "id") @key(fields: "id pid") {
  id: String!
  pid: String!
}
`
	const schemaList = `
type ProductList @key(fields: "products{id pid}") {
  products: [Product!]!
  first: Product
  selected: Product
}
type Product @key(fields: "id pid") {
  id: String!
  pid: String
}
`
	const schemaPrice = `
type ProductList @key(fields: "products{id pid category{id tag}} selected{id}") {
  products: [Product!]!
  first: Product
  selected: Product
}
type Product @key(fields: "id pid category{id tag}") {
  id: String!
  price: Price
  pid: String
  category: Category
}
type Category @key(fields: "id tag") {
  id: String!
  tag: String
}
type Price {
  price: Float!
}
`
	const schemaProducts = `
type Query {
  topProducts: ProductList!
}
type ProductList @key(fields: "products{id}") {
  products: [Product!]!
}
type Product @key(fields: "id") {
  id: String!
  category: Category
}
type Category @key(fields: "id") {
  mainProduct: Product!
  id: String!
  tag: String
}
`
	link := newDataSource("link", schemaLink, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Product", FieldNames: []string{"id", "pid"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Product", SelectionSet: "id"},
				{TypeName: "Product", SelectionSet: "id pid"},
			},
		},
	})
	list := newDataSource("list", schemaList, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "ProductList", FieldNames: []string{"products", "first", "selected"}},
			{TypeName: "Product", FieldNames: []string{"id", "pid"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "ProductList", SelectionSet: "products{id pid}"},
				{TypeName: "Product", SelectionSet: "id pid"},
			},
		},
	})
	price := newDataSource("price", schemaPrice, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "ProductList", FieldNames: []string{"products", "first", "selected"}},
			{TypeName: "Product", FieldNames: []string{"id", "price", "pid", "category"}},
			{TypeName: "Category", FieldNames: []string{"id", "tag"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Price", FieldNames: []string{"price"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "ProductList", SelectionSet: "products{id pid category{id tag}} selected{id}"},
				{TypeName: "Product", SelectionSet: "id pid category{id tag}"},
				{TypeName: "Category", SelectionSet: "id tag"},
			},
		},
	})
	products := newDataSource("products", schemaProducts, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"topProducts"}},
			{TypeName: "ProductList", FieldNames: []string{"products"}},
			{TypeName: "Product", FieldNames: []string{"category"}, ExternalFieldNames: []string{"id"}},
			{TypeName: "Category", FieldNames: []string{"mainProduct", "id", "tag"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "ProductList", SelectionSet: "products{id}"},
				{TypeName: "Product", SelectionSet: "id"},
				{TypeName: "Category", SelectionSet: "id"},
			},
		},
	})
	return []plan.DataSource{link, list, price, products}
}
