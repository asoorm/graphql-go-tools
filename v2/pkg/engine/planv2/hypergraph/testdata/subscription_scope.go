package testdata

// subscription_scope.go -- fixtures for the D11.12 operation-kind root-scoping regression: a schema
// that merely DECLARES a Subscription type (SDL + RootNodes metadata) must not perturb QUERY/MUTATION
// operation planning -- not the plan bytes and not the D10 RouteFallbacks. The customer-corpus shape
// this pins: subscription root nodes/edges participating in (optimistic) reachability and fallback
// routing of query operations.

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// EntityJumpConfigWithSubscription is EntityJumpConfig with subgraph A additionally declaring
// `type Subscription { <field>: Product }` -- the mirror-name / early-sorting-name shapes.
func EntityJumpConfigWithSubscription(field string) []plan.DataSource {
	schemaA := `
type Query { product: Product }
type Subscription { ` + field + `: Product }
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
			{TypeName: "Subscription", FieldNames: []string{field}},
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
				TypeName:           "Product",
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

// NarrowedUnionConfig is the D6p route-scoping shape: subgraph A owns the query root and the
// partial union member OnlyA; subgraph B owns OnlyB -- but B's Wrapper key is NON-RESOLVABLE
// (`@key(resolvable: false)`), so B's copy of `Wrapper.action` has no producing route and D6p
// route-scoping narrows OnlyB to a response-only null. withSubscription additionally declares
// `type Subscription { wrapperUpdated: Wrapper }` ON B: pre-fix, that subscription root made B's
// region optimistically reachable for a QUERY operation too -- un-narrowing OnlyB and driving its
// goal into the D10 fallback path (fetch perturbation + Warn diagnostics on a pure query op).
func NarrowedUnionConfig(withSubscription bool) []plan.DataSource {
	const schemaA = `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyA
type Common { c: String }
type OnlyA { a: String }
`
	schemaB := `
type Wrapper @key(fields: "id", resolvable: false) { id: ID! action: Action }
union Action = Common | OnlyB
type Common { c: String }
type OnlyB { b: String }
`
	bRoots := plan.TypeFields{
		{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
	}
	if withSubscription {
		schemaB = `
type Subscription { wrapperUpdated: Wrapper }
` + schemaB
		bRoots = append(plan.TypeFields{
			{TypeName: "Subscription", FieldNames: []string{"wrapperUpdated"}},
		}, bRoots...)
	}

	a := newDataSource("A", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"wrapper"}},
			{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Common", FieldNames: []string{"c"}},
			{TypeName: "OnlyA", FieldNames: []string{"a"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: "Wrapper", SelectionSet: "id"}},
		},
	})
	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		RootNodes: bRoots,
		ChildNodes: plan.TypeFields{
			{TypeName: "Common", FieldNames: []string{"c"}},
			{TypeName: "OnlyB", FieldNames: []string{"b"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Wrapper", SelectionSet: "id", DisableEntityResolver: true},
			},
		},
	})
	return []plan.DataSource{a, b}
}
