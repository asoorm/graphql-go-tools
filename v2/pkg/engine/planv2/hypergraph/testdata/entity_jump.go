package testdata

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// EntityJumpConfig is the worked example from the spec: Product is an entity with a nested key
// @key(fields: "id organization { id }"). Subgraph A resolves the key fields plus dimensions;
// subgraph B resolves shippingEstimate, which @requires(fields: "dimensions { length width height }").
// So the entity jump into Product-in-B needs a tail combining both the nested key and the @requires
// selection. In B, id/organization/dimensions are @external inputs -- B reads them but can't resolve
// them, so they produce no fetchable field edge. Spec: FORMAL_SPEC Section 7.2.
func EntityJumpConfig() []plan.DataSource {
	const schemaA = `
type Query { product: Product }
type Product @key(fields: "id organization { id }") {
  id: ID!
  organization: Organization
  dimensions: Dimensions
}
type Organization { id: ID! }
type Dimensions { length: Float width: Float height: Float }
`
	const schemaB = `
type Product @key(fields: "id organization { id }") {
  id: ID!
  organization: Organization
  dimensions: Dimensions
  shippingEstimate: Float
}
type Organization { id: ID! }
type Dimensions { length: Float width: Float height: Float }
`
	a := newDataSource("A", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"product"}},
			{TypeName: "Product", FieldNames: []string{"id", "organization", "dimensions"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Organization", FieldNames: []string{"id"}},
			{TypeName: "Dimensions", FieldNames: []string{"length", "width", "height"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Product", SelectionSet: "id organization { id }"},
			},
		},
	})

	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{
				TypeName: "Product",
				// "dimensions" is listed in both FieldNames and ExternalFieldNames -- the shape the
				// config loader produces when an @external field is also a node. This exercises the
				// skip branch: external wins, so no fetchable field edge is emitted for it.
				FieldNames:         []string{"shippingEstimate", "dimensions"},
				ExternalFieldNames: []string{"id", "organization", "dimensions"},
			},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Organization", ExternalFieldNames: []string{"id"}},
			{TypeName: "Dimensions", ExternalFieldNames: []string{"length", "width", "height"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Product", SelectionSet: "id organization { id }"},
			},
			Requires: plan.FederationFieldConfigurations{
				{TypeName: "Product", FieldName: "shippingEstimate", SelectionSet: "dimensions { length width height }"},
			},
		},
	})

	return []plan.DataSource{a, b}
}
