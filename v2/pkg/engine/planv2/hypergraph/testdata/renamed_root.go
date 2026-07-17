package testdata

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// RenamedRootConfig covers a subgraph that renames its root operation types with a `schema { query: ... }`
// definition (here AcmeQuery/AcmeMutation), while the router's node metadata still lists the root
// fields under the composed name (Query/Mutation). Widget can only be reached through the root field
// `widget`, so if the builder looked up that field's output type under the composed name "Query"
// (which isn't in this SDL) it would drop the descent and leave Widget unreachable. The builder must
// resolve it under the subgraph's real root type name instead.
func RenamedRootConfig() []plan.DataSource {
	const sdl = `
schema { query: AcmeQuery mutation: AcmeMutation }
type AcmeQuery { widget: Widget }
type AcmeMutation { makeWidget: Widget }
type Widget { id: ID! name: String }
`
	ds := newDataSource("acme", sdl, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"widget"}},
			{TypeName: "Mutation", FieldNames: []string{"makeWidget"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Widget", FieldNames: []string{"id", "name"}},
		},
	})
	return []plan.DataSource{ds}
}
