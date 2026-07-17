package testdata

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// TypeCycleConfig is the SELF-NESTED ENTITY regression shape (M2 requires-chain wave addendum): a
// schema TYPE CYCLE V -> M -> P -> E -> V where the entity V re-appears nested inside itself ~5 levels
// deep, with V split across two subgraphs and a @requires field on the second. The deep re-entry
// makes a goal's chain-layered-repaired walk visit the (V,a) object node TWICE (root and deep
// positions collapse onto one graph node), so the head-keyed producedBy union maps (V,a) to the
// DEEP descent -- a CYCLIC producer map (V,a)->(E,a)->(P,a)->(M,a)->(V,a). Any producer-chain walk over
// that map that is not grounded in the finite query structure loops forever (the customer-corpus
// stack overflow in lower.groupForObject).
//
//	a: Query.v(id); V @key(id) { id m w t }; M { p }; P { edges: [E] }; E { node: V }; T { s }
//	b: V @key(id) { id score @requires("t { s }") }, t/T.s @external
func TypeCycleConfig() []plan.DataSource {
	const schemaA = `
type Query { v(id: String!): V }
type V @key(fields: "id") {
  id: String!
  w: String
  m: M
  t: T
}
type M { p: P }
type P { edges: [E] }
type E { node: V }
type T { s: String }
`
	const schemaB = `
type V @key(fields: "id") {
  id: String!
  t: T
  score: Int
}
type T { s: String }
`
	a := newDataSource("a", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"v"}},
			{TypeName: "V", FieldNames: []string{"id", "w", "m", "t"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "M", FieldNames: []string{"p"}},
			{TypeName: "P", FieldNames: []string{"edges"}},
			{TypeName: "E", FieldNames: []string{"node"}},
			{TypeName: "T", FieldNames: []string{"s"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: "V", SelectionSet: "id"}},
		},
	})
	b := newDataSource("b", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "V", FieldNames: []string{"id", "score"}, ExternalFieldNames: []string{"t"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "T", ExternalFieldNames: []string{"s"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: "V", SelectionSet: "id"}},
			Requires: plan.FederationFieldConfigurations{
				{TypeName: "V", FieldName: "score", SelectionSet: "t { s }"},
			},
		},
	})
	return []plan.DataSource{a, b}
}
