package differential

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// task11DifferentialFixtures is the Task-10-review-mandated differential set, run head-to-head
// (v1 vs planv2) through the response-shape oracle under every datasource ordering. Each fixture
// targets a shape dimension the base cases did not exercise:
//
//   - requires-chain      : Section 7.2 @requires -- a dependent field fetched via an entity jump whose
//     representation carries the @requires selection (client never sees it).
//   - multi-hop-entity     : an entity resolved across THREE subgraphs (A->B->C), two chained jumps.
//   - multi-key-entity     : an entity with two distinct @key directives, hopped via the non-id key.
//   - interface-selection  : an interface field with inline `... on` member fragments + __typename.
//   - list-field           : list-of-objects and list-of-scalars -- exercises the list-lowering fix
//     (resolve.Array) end-to-end through both planners and the oracle's Array recursion.
//   - aliased-collision     : the same field selected under two different response aliases.
//
// All six are cases where the v1 planner is correct, so planv2 must be response-shape equivalent.
func task11DifferentialFixtures() []differentialCase {
	return []differentialCase{
		requiresChainFixture(),
		multiHopEntityFixture(),
		multiKeyEntityFixture(),
		interfaceSelectionFixture(),
		listFieldFixture(),
		aliasedCollisionFixture(),
	}
}

// requiresChainFixture -- Section 7.2 @requires shape. Product @key(id) with dimensions in A;
// shippingEstimate in B @requires(dimensions{...}). The client asks only for shippingEstimate; the
// planner must jump A->B carrying dimensions in the representation, and the response shape is just
// `{ product { shippingEstimate } }`.
func requiresChainFixture() differentialCase {
	return differentialCase{
		name: "requires-chain",
		schema: `
schema { query: Query }
type Query { product: Product }
type Product { id: ID! dimensions: Dimensions shippingEstimate: Float }
type Dimensions { length: Float width: Float height: Float }
`,
		op: `{ product { shippingEstimate } }`,
		subgraphs: []subgraph{
			{
				name: "a",
				sdl: `
type Query { product: Product }
type Product @key(fields: "id") { id: ID! dimensions: Dimensions }
type Dimensions { length: Float width: Float height: Float }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{TypeName: "Query", FieldNames: []string{"product"}},
						{TypeName: "Product", FieldNames: []string{"id", "dimensions"}},
					},
					ChildNodes: plan.TypeFields{
						{TypeName: "Dimensions", FieldNames: []string{"length", "width", "height"}},
					},
					FederationMetaData: plan.FederationMetaData{
						Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
					},
				},
			},
			{
				name: "b",
				sdl: `
type Product @key(fields: "id") { id: ID! dimensions: Dimensions shippingEstimate: Float }
type Dimensions { length: Float width: Float height: Float }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{
							TypeName:           "Product",
							FieldNames:         []string{"shippingEstimate"},
							ExternalFieldNames: []string{"id", "dimensions"},
						},
					},
					ChildNodes: plan.TypeFields{
						{TypeName: "Dimensions", ExternalFieldNames: []string{"length", "width", "height"}},
					},
					FederationMetaData: plan.FederationMetaData{
						Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
						Requires: plan.FederationFieldConfigurations{
							{TypeName: "Product", FieldName: "shippingEstimate", SelectionSet: "dimensions { length width height }"},
						},
					},
				},
			},
		},
	}
}

// multiHopEntityFixture -- a User entity @key(id) resolved across THREE subgraphs: a owns id+name,
// b owns hobby, c owns score. `{ me { name hobby score } }` forces two chained entity jumps.
func multiHopEntityFixture() differentialCase {
	userMeta := func(fields []string, external []string, root bool) *plan.DataSourceMetadata {
		root2 := plan.TypeFields{}
		if root {
			root2 = append(root2, plan.TypeField{TypeName: "Query", FieldNames: []string{"me"}})
		}
		root2 = append(root2, plan.TypeField{TypeName: "User", FieldNames: fields, ExternalFieldNames: external})
		return &plan.DataSourceMetadata{
			RootNodes: root2,
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
			},
		}
	}
	return differentialCase{
		name: "multi-hop-entity",
		schema: `
schema { query: Query }
type Query { me: User }
type User { id: ID! name: String hobby: String score: Int }
`,
		op: `{ me { name hobby score } }`,
		subgraphs: []subgraph{
			{
				name: "a",
				sdl: `
type Query { me: User }
type User @key(fields: "id") { id: ID! name: String }
`,
				meta: userMeta([]string{"id", "name"}, nil, true),
			},
			{
				name: "b",
				sdl: `
type User @key(fields: "id") { id: ID! hobby: String }
`,
				meta: userMeta([]string{"hobby"}, []string{"id"}, false),
			},
			{
				name: "c",
				sdl: `
type User @key(fields: "id") { id: ID! score: Int }
`,
				meta: userMeta([]string{"score"}, []string{"id"}, false),
			},
		},
	}
}

// multiKeyEntityFixture -- a User entity with two @key directives: subgraph a resolves by id and
// owns id+email; subgraph b declares @key(email) and owns nickname. The jump a->b must use the email
// key (id is not a key in b). `{ user { id nickname } }`.
func multiKeyEntityFixture() differentialCase {
	return differentialCase{
		name: "multi-key-entity",
		schema: `
schema { query: Query }
type Query { user: User }
type User { id: ID! email: String! nickname: String! }
`,
		op: `{ user { id nickname } }`,
		subgraphs: []subgraph{
			{
				name: "email",
				sdl: `
type Query { user: User }
type User @key(fields: "id") @key(fields: "email") { id: ID! email: String! }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{TypeName: "Query", FieldNames: []string{"user"}},
						{TypeName: "User", FieldNames: []string{"id", "email"}},
					},
					FederationMetaData: plan.FederationMetaData{
						Keys: plan.FederationFieldConfigurations{
							{TypeName: "User", SelectionSet: "id"},
							{TypeName: "User", SelectionSet: "email"},
						},
					},
				},
			},
			{
				name: "nickname",
				sdl: `
type User @key(fields: "email") { email: String! nickname: String! }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{
							TypeName:           "User",
							FieldNames:         []string{"nickname"},
							ExternalFieldNames: []string{"email"},
						},
					},
					FederationMetaData: plan.FederationMetaData{
						Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "email"}},
					},
				},
			},
		},
	}
}

// interfaceSelectionFixture -- a single-subgraph interface field with inline member fragments.
// `{ node { __typename ... on Book { title } ... on Movie { runtime } } }`.
func interfaceSelectionFixture() differentialCase {
	return differentialCase{
		name: "interface-selection",
		schema: `
schema { query: Query }
type Query { node: Node }
interface Node { id: ID! }
type Book implements Node { id: ID! title: String }
type Movie implements Node { id: ID! runtime: Int }
`,
		op: `{ node { __typename ... on Book { title } ... on Movie { runtime } } }`,
		subgraphs: []subgraph{
			{
				name: "content",
				sdl: `
type Query { node: Node }
interface Node { id: ID! }
type Book implements Node { id: ID! title: String }
type Movie implements Node { id: ID! runtime: Int }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{TypeName: "Query", FieldNames: []string{"node"}},
					},
					ChildNodes: plan.TypeFields{
						{TypeName: "Node", FieldNames: []string{"id"}},
						{TypeName: "Book", FieldNames: []string{"id", "title"}},
						{TypeName: "Movie", FieldNames: []string{"id", "runtime"}},
					},
				},
			},
		},
	}
}

// listFieldFixture -- list-of-objects and list-of-scalars, single subgraph. Exercises the
// list-lowering fix (resolve.Array) through both planners and the oracle's Array recursion.
func listFieldFixture() differentialCase {
	return differentialCase{
		name: "list-field",
		schema: `
schema { query: Query }
type Query { users: [User!] }
type User { id: ID! name: String tags: [String] }
`,
		op: `{ users { id name tags } }`,
		subgraphs: []subgraph{
			{
				name: "users",
				sdl: `
type Query { users: [User!] }
type User { id: ID! name: String tags: [String] }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{TypeName: "Query", FieldNames: []string{"users"}},
					},
					ChildNodes: plan.TypeFields{
						{TypeName: "User", FieldNames: []string{"id", "name", "tags"}},
					},
				},
			},
		},
	}
}

// aliasedCollisionFixture -- the same field selected under two different response aliases, single
// subgraph. Both plans must render two distinct client keys (a, b) reading the same underlying field.
func aliasedCollisionFixture() differentialCase {
	return differentialCase{
		name: "aliased-collision",
		schema: `
schema { query: Query }
type Query { me: User }
type User { id: ID! name: String }
`,
		op: `{ me { a: name b: name } }`,
		subgraphs: []subgraph{
			{
				name: "users",
				sdl: `
type Query { me: User }
type User { id: ID! name: String }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{TypeName: "Query", FieldNames: []string{"me"}},
					},
					ChildNodes: plan.TypeFields{
						{TypeName: "User", FieldNames: []string{"id", "name"}},
					},
				},
			},
		},
	}
}
