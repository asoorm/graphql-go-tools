// Package testdata provides hand-written supergraph configurations for the hypergraph builder tests,
// transcribed from the worked examples in the spec. They use the real plan.DataSource types, so the
// builder is exercised against production input rather than a bespoke fake. Spec: FORMAL_SPEC Section 7.
package testdata

import (
	"context"

	"github.com/jensneuse/abstractlogger"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// schemaFactory is a minimal plan.PlannerFactory whose only job is to hand the builder a subgraph's
// parsed schema. The planner half of the interface is never called by Build, so it returns zero
// values.
type schemaFactory struct {
	schema *ast.Document
}

func (f *schemaFactory) UpstreamSchema(_ plan.DataSourceConfiguration[struct{}]) (*ast.Document, bool) {
	return f.schema, true
}

func (f *schemaFactory) PlanningBehavior() plan.DataSourcePlanningBehavior {
	return plan.DataSourcePlanningBehavior{}
}

func (f *schemaFactory) Planner(_ abstractlogger.Logger) plan.DataSourcePlanner[struct{}] {
	return nil
}

func (f *schemaFactory) Context() context.Context {
	return context.Background()
}

// newDataSource builds a real plan.DataSource from an SDL string and metadata, running the same
// initialization the production loader runs (index + key parsing).
func newDataSource(name, sdl string, meta *plan.DataSourceMetadata) plan.DataSource {
	doc := unsafeparser.ParseGraphqlDocumentString(sdl)
	ds, err := plan.NewDataSourceConfigurationWithName[struct{}](
		name, name, &schemaFactory{schema: &doc}, meta, struct{}{},
	)
	if err != nil {
		panic(err)
	}
	return ds
}

// PartialUnionConfig is the partial-union worked example: entity Wrapper @key(id) in both A and B, a shareable
// Wrapper.action: Action, and a partial union -- `union Action = Common | OnlyA` in A and
// `Action = Common | OnlyB` in B. Common/OnlyA/OnlyB have no key, so they get no entity jump.
func PartialUnionConfig() []plan.DataSource {
	// `union _Entity` mirrors what a production subgraph schema contains: the merged federation schema
	// includes a meta union of all entities. The builder must skip it (the reserved "_" prefix) -- meta
	// types get no member edges.
	const schemaA = `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyA
union _Entity = Wrapper
type Common { c: String }
type OnlyA { a: String }
`
	const schemaB = `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyB
union _Entity = Wrapper
type Common { c: String }
type OnlyB { b: String }
`
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
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Wrapper", SelectionSet: "id"},
			},
		},
	})

	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"wrapper"}},
			{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Common", FieldNames: []string{"c"}},
			{TypeName: "OnlyB", FieldNames: []string{"b"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Wrapper", SelectionSet: "id"},
			},
		},
	})

	return []plan.DataSource{a, b}
}

// PartialUnionUnreachableParentConfig is a route-scoping counterexample for narrowing: the abstract
// parent field is @shareable across two subgraphs, but one subgraph's copy of the parent object is
// unreachable -- it has no root path and its type is not an entity, so nothing in the graph produces
// it. Mirrors the audit's partial-union/case-02.
//
//   - Subgraph A: `Query { getResponse: Response }`, `Response { actions: [Action!]! }`,
//     `union Action = Alpha | Beta` -- the full member set, reachable via A's root.
//   - Subgraph B: `Response @shareable { actions: [Action!]! }` (no Query, no @key),
//     `union Action = Alpha` -- a partial member set on a parent object nothing can reach.
//
// If narrowing looked only at which subgraphs DECLARE Response.actions it would see {A, B}, intersect
// their members down to {Alpha}, and wrongly drop Beta -- even though the only usable route (through A)
// can resolve it. Scoping to the reachable route instead leaves only {A}, keeps both members, and Beta
// is correctly covered. Alpha/Beta have no key, so the entity gate doesn't apply.
func PartialUnionUnreachableParentConfig() []plan.DataSource {
	const schemaA = `
type Query { getResponse: Response }
type Response { actions: [Action!]! }
union Action = Alpha | Beta
union _Entity = Response
type Alpha { x: String }
type Beta { name: String }
`
	const schemaB = `
type Response { actions: [Action!]! }
union Action = Alpha
union _Entity = Response
type Alpha { x: String }
`
	a := newDataSource("A", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"getResponse"}},
			{TypeName: "Response", FieldNames: []string{"actions"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Alpha", FieldNames: []string{"x"}},
			{TypeName: "Beta", FieldNames: []string{"name"}},
		},
	})
	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		// Response.actions is declared (shareable) but B's Response has no producing edge: no Query root
		// reaches it and Response is not an entity, so it's an unreachable node in the graph.
		RootNodes: plan.TypeFields{
			{TypeName: "Response", FieldNames: []string{"actions"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Alpha", FieldNames: []string{"x"}},
		},
	})
	return []plan.DataSource{a, b}
}

// PartialUnionEntityKeyConfig covers how a resolvable:false key affects narrowing. Like
// PartialUnionConfig, but OnlyA carries a key "oid" in subgraph A whose entity resolver is turned on
// or off by the parameter, and subgraph B also carries OnlyA.oid so the key can be supplied from B.
// With resolvable=true the builder emits a B->A entity jump into OnlyA, making OnlyA an entity, which
// stops it being narrowed away. With resolvable=false (@key(resolvable: false)) that key emits no
// jump, so OnlyA is an entity nowhere and can be narrowed away just like a keyless member.
func PartialUnionEntityKeyConfig(resolvable bool) []plan.DataSource {
	const schemaA = `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyA
union _Entity = Wrapper | OnlyA
type Common { c: String }
type OnlyA @key(fields: "oid") { oid: ID! a: String }
`
	const schemaB = `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyB
union _Entity = Wrapper
type Common { c: String }
type OnlyB { b: String }
type OnlyA { oid: ID! }
`
	a := newDataSource("A", schemaA, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"wrapper"}},
			{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
			{TypeName: "OnlyA", FieldNames: []string{"oid", "a"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Common", FieldNames: []string{"c"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Wrapper", SelectionSet: "id"},
				{TypeName: "OnlyA", SelectionSet: "oid", DisableEntityResolver: !resolvable},
			},
		},
	})

	b := newDataSource("B", schemaB, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"wrapper"}},
			{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Common", FieldNames: []string{"c"}},
			{TypeName: "OnlyB", FieldNames: []string{"b"}},
			{TypeName: "OnlyA", FieldNames: []string{"oid"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "Wrapper", SelectionSet: "id"},
			},
		},
	})

	return []plan.DataSource{a, b}
}
