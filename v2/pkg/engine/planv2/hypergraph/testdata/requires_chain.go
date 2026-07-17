package testdata

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// RequiresChainConfig mirrors the audit's `requires-requires` suite: a NESTED @requires chain whose
// inputs live in different subgraphs, so no single source supplies all of a target's requires.
//
//	a: Product { id price }                                  (price owner)
//	b: Query.product; Product { id hasDiscount }             (root + hasDiscount owner)
//	c: Product { id isExpensive @requires(price)             (requires level 1, two independent requires)
//	             isExpensiveWithDiscount @requires(hasDiscount) }
//	d: Product { id canAfford @requires(isExpensive)         (requires level 2 -- requires a requires)
//	             canAffordWithDiscount @requires(isExpensiveWithDiscount) }
//
// Under base D7 (ride-along, single-source requires) subgraph c had NO jump in at all: no source
// resolves both price and hasDiscount. Spec: FORMAL_SPEC D7pp.
func RequiresChainConfig() []plan.DataSource {
	const schemaA = `
type Product @key(fields: "id") {
  id: ID!
  price: Float!
}
`
	const schemaB = `
type Query { product: Product }
type Product @key(fields: "id") {
  id: ID!
  hasDiscount: Boolean!
}
`
	const schemaC = `
type Product @key(fields: "id") {
  id: ID!
  price: Float!
  isExpensive: Boolean!
  hasDiscount: Boolean!
  isExpensiveWithDiscount: Boolean!
}
`
	const schemaD = `
type Product @key(fields: "id") {
  id: ID!
  isExpensive: Boolean!
  canAfford: Boolean!
  isExpensiveWithDiscount: Boolean!
  canAffordWithDiscount: Boolean!
}
`
	a := newDataSource("a", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Product", FieldNames: []string{"id", "price"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
		},
	})
	b := newDataSource("b", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"product"}},
			{TypeName: "Product", FieldNames: []string{"id", "hasDiscount"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
		},
	})
	c := newDataSource("c", schemaC, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{
				TypeName:           "Product",
				FieldNames:         []string{"id", "isExpensive", "isExpensiveWithDiscount"},
				ExternalFieldNames: []string{"price", "hasDiscount"},
			},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
			Requires: plan.FederationFieldConfigurations{
				{TypeName: "Product", FieldName: "isExpensive", SelectionSet: "price"},
				{TypeName: "Product", FieldName: "isExpensiveWithDiscount", SelectionSet: "hasDiscount"},
			},
		},
	})
	d := newDataSource("d", schemaD, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{
				TypeName:           "Product",
				FieldNames:         []string{"id", "canAfford", "canAffordWithDiscount"},
				ExternalFieldNames: []string{"isExpensive", "isExpensiveWithDiscount"},
			},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
			Requires: plan.FederationFieldConfigurations{
				{TypeName: "Product", FieldName: "canAfford", SelectionSet: "isExpensive"},
				{TypeName: "Product", FieldName: "canAffordWithDiscount", SelectionSet: "isExpensiveWithDiscount"},
			},
		},
	})
	return []plan.DataSource{a, b, c, d}
}

// RequiresDistributedConfig mirrors the audit's `requires-circular` suite: a @requires selection
// whose PATH field lives in the target subgraph itself and whose LEAF lives in a third subgraph --
// no source resolves the whole selection, and the correct plan is v1's relay (root a -> b for
// author{id} -> a for yearsOfExperience -> back into b with the gathered representation).
//
//	a: Query.feed; Post { id byExpert @requires(byNovice) }; Author { id name yearsOfExperience }
//	b: Post { id author byNovice @requires(author { yearsOfExperience }) }; Author { id }
func RequiresDistributedConfig() []plan.DataSource {
	const schemaA = `
type Query { feed: [Post] }
type Post @key(fields: "id") {
  id: ID!
  byNovice: Boolean!
  byExpert: Boolean!
}
type Author @key(fields: "id") {
  id: ID!
  name: String!
  yearsOfExperience: Int!
}
`
	const schemaB = `
type Post @key(fields: "id") {
  id: ID!
  author: Author!
  byNovice: Boolean!
}
type Author @key(fields: "id") {
  id: ID!
  yearsOfExperience: Int!
}
`
	a := newDataSource("a", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"feed"}},
			{TypeName: "Post", FieldNames: []string{"id", "byExpert"}, ExternalFieldNames: []string{"byNovice"}},
			{TypeName: "Author", FieldNames: []string{"id", "name", "yearsOfExperience"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Post", SelectionSet: "id"},
				{TypeName: "Author", SelectionSet: "id"},
			},
			Requires: plan.FederationFieldConfigurations{
				{TypeName: "Post", FieldName: "byExpert", SelectionSet: "byNovice"},
			},
		},
	})
	b := newDataSource("b", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Post", FieldNames: []string{"id", "author", "byNovice"}},
			{TypeName: "Author", FieldNames: []string{"id"}, ExternalFieldNames: []string{"yearsOfExperience"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Post", SelectionSet: "id"},
				{TypeName: "Author", SelectionSet: "id"},
			},
			Requires: plan.FederationFieldConfigurations{
				{TypeName: "Post", FieldName: "byNovice", SelectionSet: "author { yearsOfExperience }"},
			},
		},
	})
	return []plan.DataSource{a, b}
}
