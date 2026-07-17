package testdata

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// InterfaceRefinementConfig covers member fallback for interface refinement: a root field returns an
// interface `Node`, which is refined to a second "mixin" interface `Detail` that is never returned
// directly, so Detail itself is unreachable in the graph. The field `title` on Detail therefore has
// to resolve on the concrete member the parent instance actually is. Article is a real cross-subgraph
// entity (@key id in both A and B), so narrowing does NOT apply here -- without the fallback this is a
// hard ErrNoValidPlan. It mirrors a blocker shape seen in the private corpus.
//
//   - A: `Query { item: Node }`, interfaces Node/Detail, `Article implements Node & Detail` with
//     `title` -- Article is the sole member, and an entity.
//   - B: `Article @key(id) { id }` -- makes Article a jump target, so it is a modelled entity.
func InterfaceRefinementConfig() []plan.DataSource {
	const schemaA = `
type Query { item: Node }
interface Node { id: ID! }
interface Detail { title: String }
union _Entity = Article
type Article implements Node & Detail { id: ID! title: String }
`
	const schemaB = `
union _Entity = Article
type Article { id: ID! }
`
	a := newDataSource("A", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"item"}},
			{TypeName: "Article", FieldNames: []string{"id", "title"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Node", FieldNames: []string{"id"}},
			{TypeName: "Detail", FieldNames: []string{"title"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Article", SelectionSet: "id"},
			},
		},
	})
	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Article", FieldNames: []string{"id"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Article", SelectionSet: "id"},
			},
		},
	})
	return []plan.DataSource{a, b}
}
