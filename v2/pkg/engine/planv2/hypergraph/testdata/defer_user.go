package testdata

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// DeferUserSingleConfig is the single-subgraph defer fixture, mirroring v1's
// graphql_datasource_defer_test "on root query node" / "nested defer on single subgraph" cases:
// one subgraph resolves Query.user and every User field (plus a nested Info object for the
// all-deferred placeholder class). A deferred field here has no entity anchor, so its scope
// variant must re-walk from the operation root (FORMAL_SPEC D11.13 root re-walk anchoring).
func DeferUserSingleConfig() []plan.DataSource {
	const schema = `
type Query { user: User }
type Mutation { updateUser: User }
type User {
  id: ID!
  name: String!
  title: String!
  description: String!
  info: Info
}
type Info { email: String phone: String }
`
	ds := newDataSource("first", schema, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"user"}},
			{TypeName: "Mutation", FieldNames: []string{"updateUser"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "User", FieldNames: []string{"id", "name", "title", "description", "info"}},
			{TypeName: "Info", FieldNames: []string{"email", "phone"}},
		},
	})
	return []plan.DataSource{ds}
}

// DeferUserEntityConfig is the two-subgraph defer fixture, mirroring v1's "on entity from other
// subgraph" case: subgraph first resolves Query.user and User{id,title}; subgraph second owns
// User{firstName,lastName} behind @key(fields: "id"). A deferred field on second re-enters through
// the same entity jump (D11.13 entity re-entry anchoring) with the key fetched in the parent
// scope, so the initial set grows by exactly `__typename id` on the root fetch.
func DeferUserEntityConfig() []plan.DataSource {
	const schemaFirst = `
type Query { user: User }
type User @key(fields: "id") {
  id: ID!
  title: String!
}
`
	const schemaSecond = `
type User @key(fields: "id") {
  id: ID!
  firstName: String!
  lastName: String!
}
`
	first := newDataSource("first", schemaFirst, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"user"}},
			{TypeName: "User", FieldNames: []string{"id", "title"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "User", SelectionSet: "id"},
			},
		},
	})
	second := newDataSource("second", schemaSecond, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "User", FieldNames: []string{"id", "firstName", "lastName"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{
				{TypeName: "User", SelectionSet: "id"},
			},
		},
	})
	return []plan.DataSource{first, second}
}
