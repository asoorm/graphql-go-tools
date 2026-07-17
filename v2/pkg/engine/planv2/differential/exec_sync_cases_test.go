package differential

// exec_sync_cases_test.go carries the executed-truth fixtures for the runtime-encoding defect
// classes the real Guild federation-gateway-audit exposed (m3-real-audit-report Section d). One test per
// class; each fixture is the audit's failing shape reduced to its smallest in-repo reproduction,
// with v1 as the sanity oracle (v1 passes all of these through the same harness).

import (
	"sync"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
)

// TestExecSync_SubgraphErrorPropagation is the expected-subgraph-error class (audit:
// enum-intersection_1, union-interface-distributed_7/8, corrupted-supergraph-node-id_0/5,
// non-resolvable-interface-object_3): a subgraph responds with a GraphQL error entry, and the
// client response must carry an `errors` entry -- v1 selects the subgraph response's `errors` path
// on every fetch (graphql_datasource PostProcessing SelectResponseErrorsPath), planv2's encoding
// omitted it, so the error silently vanished and the audit's should-error cases resolved leniently.
func TestExecSync_SubgraphErrorPropagation(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { user: User boom: String }
type User { id: ID! name: String }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { user: User boom: String }
type User @key(fields: "id") { id: ID! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"user", "boom"}},
					{TypeName: "User", FieldNames: []string{"id"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type User @key(fields: "id") { id: ID! name: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{{TypeName: "User", FieldNames: []string{"id", "name"}}},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
	}

	t.Run("root fetch error", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			name:      "root fetch error",
			schema:    schema,
			op:        `{ boom }`,
			subgraphs: subgraphs,
			data: map[string]syncData{
				"a": {fieldErrors: map[string]string{"boom": "boom failed"}},
				"b": {},
			},
			expected: `{"errors":[{"message":"boom failed"}],"data":{"boom":null}}`,
		})
	})

	t.Run("entity fetch error", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			name:      "entity fetch error",
			schema:    schema,
			op:        `{ user { id name } }`,
			subgraphs: subgraphs,
			data: map[string]syncData{
				"a": {root: map[string]any{
					"user": map[string]any{"__typename": "User", "id": "u1"},
				}},
				"b": {fieldErrors: map[string]string{"_entities": "entity resolution failed"}},
			},
			expected: `{"errors":[{"message":"entity resolution failed"}],"data":{"user":{"id":"u1","name":null}}}`,
		})
	})
}

// TestExecSync_AliasedTypename is the aliased-__typename class (audit: typename_0..3): a client
// alias of the meta-field (`typename: __typename`, plain or member-gated) must materialize under
// the alias key. planv2's document encoding collapsed every __typename selection onto the bare
// meta-field, so the fetch never returned the alias key and the response read null.
func TestExecSync_AliasedTypename(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { fruit: Fruit iface: Node }
union Fruit = Apple | Banana
interface Node { id: ID! }
type Apple { name: String }
type Banana { name: String }
type Oven implements Node { id: ID! }
type Toaster implements Node { id: ID! }
`
	subgraphs := []subgraph{{
		name: "a",
		sdl: `
type Query { fruit: Fruit iface: Node }
union Fruit = Apple | Banana
interface Node { id: ID! }
type Apple { name: String }
type Banana { name: String }
type Oven implements Node { id: ID! }
type Toaster implements Node { id: ID! }
`,
		meta: &plan.DataSourceMetadata{
			RootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"fruit", "iface"}}},
			ChildNodes: plan.TypeFields{
				{TypeName: "Apple", FieldNames: []string{"name"}},
				{TypeName: "Banana", FieldNames: []string{"name"}},
				{TypeName: "Node", FieldNames: []string{"id"}},
				{TypeName: "Oven", FieldNames: []string{"id"}},
				{TypeName: "Toaster", FieldNames: []string{"id"}},
			},
		},
	}}
	data := map[string]syncData{
		"a": {
			root: map[string]any{
				"fruit": map[string]any{"__typename": "Apple", "name": "gala"},
				"iface": map[string]any{"__typename": "Toaster", "id": "t1"},
			},
			implements: map[string][]string{"Oven": {"Node"}, "Toaster": {"Node"}},
		},
	}

	t.Run("plain alias on union", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ fruit { __typename typename: __typename t: __typename } }`,
			subgraphs: subgraphs,
			data:      data,
			expected:  `{"data":{"fruit":{"__typename":"Apple","typename":"Apple","t":"Apple"}}}`,
		})
	})

	t.Run("member-gated alias", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ fruit { __typename ... on Apple { typename: __typename } ... on Banana { typename: __typename } } }`,
			subgraphs: subgraphs,
			data:      data,
			expected:  `{"data":{"fruit":{"__typename":"Apple","typename":"Apple"}}}`,
		})
	})

	t.Run("interface alias", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ iface { id __typename typename: __typename t: __typename } }`,
			subgraphs: subgraphs,
			data:      data,
			expected:  `{"data":{"iface":{"id":"t1","__typename":"Toaster","typename":"Toaster","t":"Toaster"}}}`,
		})
	})
}

// TestExecSync_SerialMutation is the serial-root-mutation class (audit: mutations_2): the GraphQL
// spec mandates root mutation fields execute in document order, one after another. Three counters
// operations spread over three subgraphs observe call order through shared state: parallel (or
// mis-grouped: five+twelve in ONE fetch to c) execution produces 5,14,7,0 instead of 5,10,12,12.
func TestExecSync_SerialMutation(t *testing.T) {
	const schema = `
schema { query: Query mutation: Mutation }
type Query { ping: String }
type Mutation { add(num: Int!): Int! multiply(by: Int!): Int! delete: Int! }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { ping: String }
type Mutation { multiply(by: Int!): Int! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"ping"}},
					{TypeName: "Mutation", FieldNames: []string{"multiply"}},
				},
			},
		},
		{
			name: "b",
			sdl:  `type Mutation { delete: Int! }`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{{TypeName: "Mutation", FieldNames: []string{"delete"}}},
			},
		},
		{
			name: "c",
			sdl:  `type Mutation { add(num: Int!): Int! }`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{{TypeName: "Mutation", FieldNames: []string{"add"}}},
			},
		},
	}

	var mu sync.Mutex
	counter := 0
	add := func(args map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		counter += int(args["num"].(float64))
		return counter
	}
	multiply := func(args map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		counter *= int(args["by"].(float64))
		return counter
	}
	del := func(map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		old := counter
		counter = 0
		return old
	}

	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `mutation { five: add(num: 5) ten: multiply(by: 2) twelve: add(num: 2) final: delete }`,
		subgraphs: subgraphs,
		data: map[string]syncData{
			"a": {calls: map[string]func(map[string]any) any{"multiply": multiply}},
			"b": {calls: map[string]func(map[string]any) any{"delete": del}},
			"c": {calls: map[string]func(map[string]any) any{"add": add}},
		},
		fields: []plan.FieldConfiguration{
			{TypeName: "Mutation", FieldName: "add",
				Arguments: plan.ArgumentsConfigurations{{Name: "num", SourceType: plan.FieldArgumentSource}}},
			{TypeName: "Mutation", FieldName: "multiply",
				Arguments: plan.ArgumentsConfigurations{{Name: "by", SourceType: plan.FieldArgumentSource}}},
		},
		expected: `{"data":{"five":5,"ten":10,"twelve":12,"final":12}}`,
		reset: func() {
			mu.Lock()
			defer mu.Unlock()
			counter = 0
		},
	})
}

// TestExecSync_RequiresOnRootEntity is the root-returned-entity @requires class (audit:
// mutations_0/_1 isExpensive-null): a @requires field on the entity object RETURNED BY a root
// field (query or mutation) must receive the gathered value in its representation. The subgraph
// computes the answer FROM the representation's price -- a representation arriving without it
// yields the observable null.
func TestExecSync_RequiresOnRootEntity(t *testing.T) {
	const schema = `
schema { query: Query mutation: Mutation }
type Query { product(id: ID!): Product }
type Mutation { addProduct(name: String!): Product! }
type Product { id: ID! name: String! price: Float! isExpensive: Boolean! isAvailable: Boolean! }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { product(id: ID!): Product }
type Mutation { addProduct(name: String!): Product! }
type Product @key(fields: "id") { id: ID! name: String! price: Float! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"product"}},
					{TypeName: "Mutation", FieldNames: []string{"addProduct"}},
					{TypeName: "Product", FieldNames: []string{"id", "name", "price"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Product @key(fields: "id") { id: ID! price: Float! @external isExpensive: Boolean! @requires(fields: "price") isAvailable: Boolean! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Product", FieldNames: []string{"id", "isExpensive", "isAvailable"}, ExternalFieldNames: []string{"price"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
					Requires: plan.FederationFieldConfigurations{
						{TypeName: "Product", FieldName: "isExpensive", SelectionSet: "price"},
					},
				},
			},
		},
	}
	product := map[string]any{"__typename": "Product", "id": "p1", "name": "p1-name", "price": 599.99}
	data := map[string]syncData{
		"a": {
			root:  map[string]any{"product": product},
			calls: map[string]func(map[string]any) any{"addProduct": func(map[string]any) any { return product }},
		},
		"b": {
			entityCalls: map[string]func(map[string]any) map[string]any{
				"Product": func(rep map[string]any) map[string]any {
					price, ok := rep["price"].(float64)
					if !ok {
						return map[string]any{"isExpensive": nil, "isAvailable": true} // gather value never arrived
					}
					return map[string]any{"isExpensive": price > 100, "isAvailable": true}
				},
			},
		},
	}
	fields := []plan.FieldConfiguration{
		{TypeName: "Mutation", FieldName: "addProduct",
			Arguments: plan.ArgumentsConfigurations{{Name: "name", SourceType: plan.FieldArgumentSource}}},
		{TypeName: "Query", FieldName: "product",
			Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
	}

	t.Run("query root", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ product(id: "p1") { id name price isExpensive isAvailable } }`,
			subgraphs: subgraphs,
			data:      data,
			fields:    fields,
			expected:  `{"data":{"product":{"id":"p1","name":"p1-name","price":599.99,"isExpensive":true,"isAvailable":true}}}`,
		})
	})

	t.Run("mutation root", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `mutation { addProduct(name: "new") { name price isExpensive isAvailable } }`,
			subgraphs: subgraphs,
			data:      data,
			fields:    fields,
			expected:  `{"data":{"addProduct":{"name":"p1-name","price":599.99,"isExpensive":true,"isAvailable":true}}}`,
		})
	})
}

// TestExecSync_ListValuedRequiresGather is the list-valued @requires representation class (audit:
// requires-with-argument_1..4; DIVERGENCES register "list-valued key path representation VALUE"):
// a @requires selection over a LIST field (`comments(limit: 3) { authorId }`) must render into the
// representation as a JSON array of projected items -- the name-keyed object builder rendered it as
// a single object, the walker refused the array value, the representation rendered null and the
// requiring field resolved null. The aliased variant additionally reads the gather from the
// argument-conflict alias position (`_planv2req_comments_N`).
func TestExecSync_ListValuedRequiresGather(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { feed: [Post] }
type Post { id: ID! author: Author comments(limit: Int!): [Comment] }
type Comment { id: ID! authorId: ID }
type Author { id: ID! }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { feed: [Post] }
type Post @key(fields: "id") { id: ID! comments(limit: Int!): [Comment] }
type Comment { id: ID! authorId: ID }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"feed"}},
					{TypeName: "Post", FieldNames: []string{"id", "comments"}},
				},
				ChildNodes: plan.TypeFields{{TypeName: "Comment", FieldNames: []string{"id", "authorId"}}},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Post", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Post @key(fields: "id") { id: ID! author: Author @requires(fields: "comments(limit: 3) { authorId }") comments(limit: Int!): [Comment] @external }
type Comment { id: ID! authorId: ID @external }
type Author { id: ID! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Post", FieldNames: []string{"id", "author"}, ExternalFieldNames: []string{"comments"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Comment", ExternalFieldNames: []string{"id", "authorId"}},
					{TypeName: "Author", FieldNames: []string{"id"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Post", SelectionSet: "id"}},
					Requires: plan.FederationFieldConfigurations{
						{TypeName: "Post", FieldName: "author", SelectionSet: "comments(limit: 3) { authorId }"},
					},
				},
			},
		},
	}
	data := map[string]syncData{
		"a": {root: map[string]any{
			"feed": []any{map[string]any{
				"__typename": "Post", "id": "p1",
				"comments": []any{
					map[string]any{"__typename": "Comment", "id": "c1", "authorId": "a9"},
					map[string]any{"__typename": "Comment", "id": "c2", "authorId": "a7"},
				},
			}},
		}},
		"b": {entityCalls: map[string]func(map[string]any) map[string]any{
			"Post": func(rep map[string]any) map[string]any {
				comments, ok := rep["comments"].([]any)
				if !ok || len(comments) == 0 {
					return map[string]any{"author": nil} // gather list never arrived
				}
				first, _ := comments[0].(map[string]any)
				authorID, _ := first["authorId"].(string)
				if authorID == "" {
					return map[string]any{"author": nil}
				}
				return map[string]any{"author": map[string]any{"__typename": "Author", "id": authorID}}
			},
		}},
	}
	fields := []plan.FieldConfiguration{
		{TypeName: "Post", FieldName: "comments",
			Arguments: plan.ArgumentsConfigurations{{Name: "limit", SourceType: plan.FieldArgumentSource}}},
	}

	t.Run("plain gather", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ feed { author { id } } }`,
			subgraphs: subgraphs,
			data:      data,
			fields:    fields,
			expected:  `{"data":{"feed":[{"author":{"id":"a9"}}]}}`,
		})
	})

	t.Run("aliased gather under client argument conflict", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ feed { author { id } comments(limit: 1) { id } } }`,
			subgraphs: subgraphs,
			data:      data,
			fields:    fields,
			expected:  `{"data":{"feed":[{"author":{"id":"a9"},"comments":[{"id":"c1"},{"id":"c2"}]}]}}`,
		})
	})
}

// TestExecSync_FragmentConditionedRequiresValue is the requires-fragment representation-value class
// (audit: requires-with-fragments_5; DIVERGENCES register "requires-fragment representation
// value"): a fragment-conditioned @requires coordinate (`data { foo ... on Qux { qux } }`) must
// ride the representation VALUE gated on the member type -- the name-keyed builder dropped the
// fragment branches, so the target's resolver never saw the conditional input.
func TestExecSync_FragmentConditionedRequiresValue(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { e: Entity }
type Entity { id: ID! data: Foo requirer: String }
interface Foo { foo: String }
type Qux implements Foo { foo: String qux: String }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { e: Entity }
type Entity @key(fields: "id") { id: ID! data: Foo }
interface Foo { foo: String }
type Qux implements Foo { foo: String qux: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"e"}},
					{TypeName: "Entity", FieldNames: []string{"id", "data"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Foo", FieldNames: []string{"foo"}},
					{TypeName: "Qux", FieldNames: []string{"foo", "qux"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Entity", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Entity @key(fields: "id") { id: ID! data: Foo @external requirer: String @requires(fields: "data { foo ... on Qux { qux } }") }
interface Foo { foo: String }
type Qux implements Foo { foo: String qux: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Entity", FieldNames: []string{"id", "requirer"}, ExternalFieldNames: []string{"data"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Foo", ExternalFieldNames: []string{"foo"}},
					{TypeName: "Qux", ExternalFieldNames: []string{"foo", "qux"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Entity", SelectionSet: "id"}},
					Requires: plan.FederationFieldConfigurations{
						{TypeName: "Entity", FieldName: "requirer", SelectionSet: "data { foo ... on Qux { qux } }"},
					},
				},
			},
		},
	}
	data := map[string]syncData{
		"a": {
			root: map[string]any{
				"e": map[string]any{
					"__typename": "Entity", "id": "e1",
					"data": map[string]any{"__typename": "Qux", "foo": "f", "qux": "q"},
				},
			},
			implements: map[string][]string{"Qux": {"Foo"}},
		},
		"b": {entityCalls: map[string]func(map[string]any) map[string]any{
			"Entity": func(rep map[string]any) map[string]any {
				d, _ := rep["data"].(map[string]any)
				foo, _ := d["foo"].(string)
				qux, ok := d["qux"].(string)
				if !ok {
					return map[string]any{"requirer": nil} // conditional input never arrived
				}
				return map[string]any{"requirer": foo + "_" + qux}
			},
		}},
	}

	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `{ e { requirer } }`,
		subgraphs: subgraphs,
		data:      data,
		expected:  `{"data":{"e":{"requirer":"f_q"}}}`,
	})
}

// TestExecSync_InaccessibleMemberNulls is the possible-types completion class (audit:
// requires-with-fragments_0/_4): an abstract position whose runtime __typename is NOT a member of
// the CLIENT schema (an @inaccessible concrete type, or a corrupted subgraph value) must complete
// to null -- v1 stamps every composite response object with TypeName/PossibleTypes from the
// composed definition and the resolver nulls the mismatch; planv2's shape carried no
// PossibleTypes, so the inaccessible object leaked to the client.
func TestExecSync_InaccessibleMemberNulls(t *testing.T) {
	// The CLIENT schema knows only Qux; the subgraph additionally serves Baz (inaccessible).
	const schema = `
schema { query: Query }
type Query { e: Entity }
type Entity { id: ID! data: Foo }
interface Foo { foo: String }
type Qux implements Foo { foo: String qux: String }
`
	subgraphs := []subgraph{{
		name: "a",
		sdl: `
type Query { e: Entity }
type Entity @key(fields: "id") { id: ID! data: Foo }
interface Foo { foo: String }
type Qux implements Foo { foo: String qux: String }
type Baz implements Foo @inaccessible { foo: String baz: String }
`,
		meta: &plan.DataSourceMetadata{
			RootNodes: plan.TypeFields{
				{TypeName: "Query", FieldNames: []string{"e"}},
				{TypeName: "Entity", FieldNames: []string{"id", "data"}},
			},
			ChildNodes: plan.TypeFields{
				{TypeName: "Foo", FieldNames: []string{"foo"}},
				{TypeName: "Qux", FieldNames: []string{"foo", "qux"}},
				{TypeName: "Baz", FieldNames: []string{"foo", "baz"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{{TypeName: "Entity", SelectionSet: "id"}},
			},
		},
	}}
	data := map[string]syncData{
		"a": {
			root: map[string]any{
				"e": map[string]any{
					"__typename": "Entity", "id": "e1",
					"data": map[string]any{"__typename": "Baz", "foo": "f", "baz": "z"},
				},
			},
			implements: map[string][]string{"Qux": {"Foo"}, "Baz": {"Foo"}},
		},
	}

	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `{ e { data { __typename } } }`,
		subgraphs: subgraphs,
		data:      data,
		expected:  `{"errors":[{"message":"invalid __typename"}],"data":{"e":{"data":null}}}`,
	})
}

// TestExecSync_InterfaceObject is the C-disc interface-object runtime class (audit:
// simple-interface-object_1/2/4/5, interface-object-with-requires -- the register's C-disc
// residual). Subgraph a declares the entity interface (`interface NodeWithName @key` with concrete
// User); subgraph b models it as `type NodeWithName @key @interfaceObject`. Three runtime contracts:
// a jump INTO b presents the INTERFACE name as the representation __typename (b declares no
// concrete types) with the gate accepting the concrete source typename; a jump FROM b's
// interface-named data INTO a accepts the interface name on a concrete-head representation and
// re-discriminates by selecting __typename in the entity fragment (the concrete name merges over
// the interface name, so member gates apply); and fetches into b never select __typename inside
// the entity fragment (the interface name must not clobber the concrete discriminator).
func TestExecSync_InterfaceObject(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { users: [NodeWithName!]! anotherUsers: [NodeWithName] }
interface NodeWithName { id: ID! name: String username: String }
type User implements NodeWithName { id: ID! name: String age: Int username: String }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { users: [NodeWithName!]! }
interface NodeWithName @key(fields: "id") { id: ID! name: String }
type User implements NodeWithName @key(fields: "id") { id: ID! name: String age: Int }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"users"}},
					{TypeName: "NodeWithName", FieldNames: []string{"id", "name"}},
					{TypeName: "User", FieldNames: []string{"id", "name", "age"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{
						{TypeName: "NodeWithName", SelectionSet: "id"},
						{TypeName: "User", SelectionSet: "id"},
					},
					EntityInterfaces: []plan.EntityInterfaceConfiguration{
						{InterfaceTypeName: "NodeWithName", ConcreteTypeNames: []string{"User"}},
					},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Query { anotherUsers: [NodeWithName] }
type NodeWithName @key(fields: "id") @interfaceObject { id: ID! username: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"anotherUsers"}},
					{TypeName: "NodeWithName", FieldNames: []string{"id", "username"}},
					{TypeName: "User", FieldNames: []string{"id", "username"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{
						{TypeName: "NodeWithName", SelectionSet: "id"},
						{TypeName: "User", SelectionSet: "id"},
					},
					InterfaceObjects: []plan.EntityInterfaceConfiguration{
						{InterfaceTypeName: "NodeWithName", ConcreteTypeNames: []string{"User"}},
					},
				},
			},
		},
	}
	user1a := map[string]any{"__typename": "User", "id": "u1", "name": "u1-name", "age": 11}
	data := map[string]syncData{
		"a": {
			root:       map[string]any{"users": []any{user1a}},
			implements: map[string][]string{"User": {"NodeWithName"}},
			entities: map[string]map[string]map[string]any{
				// a resolves references typed on the interface AND on the concrete member; either
				// way the returned object is the CONCRETE User (schema-computed __typename).
				"NodeWithName": {"u1": user1a},
				"User":         {"u1": user1a},
			},
		},
		"b": {
			// b's schema models NodeWithName as an OBJECT: every __typename it reports is the
			// interface name (the audit's subgraph even returns a deliberately wrong resolver value
			// to prove the gateway must not trust it).
			root: map[string]any{"anotherUsers": []any{
				map[string]any{"__typename": "NodeWithName", "id": "u1", "username": "u1-username"},
			}},
			entities: map[string]map[string]map[string]any{
				// Registered ONLY under the interface name: a representation arriving with
				// __typename "User" (concrete) finds nothing -- exactly a graphql-js subgraph
				// erroring "no object type User". The echo leaves __typename "NodeWithName".
				"NodeWithName": {"u1": {"username": "u1-username"}},
			},
		},
	}

	t.Run("into interface object", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ users { id name username } }`,
			subgraphs: subgraphs,
			data:      data,
			expected:  `{"data":{"users":[{"id":"u1","name":"u1-name","username":"u1-username"}]}}`,
		})
	})

	t.Run("member gate from interface object", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ anotherUsers { ... on User { age } } }`,
			subgraphs: subgraphs,
			data:      data,
			expected:  `{"data":{"anotherUsers":[{"age":11}]}}`,
		})
	})

	t.Run("concrete typename needs synthesized discriminator", func(t *testing.T) {
		// `anotherUsers { __typename username }` resolves ENTIRELY in b (the @interfaceObject
		// subgraph) -- no client field forces a fetch to a -- yet the client-visible __typename must
		// be the CONCRETE member name. The plan must synthesize the discriminator jump to the
		// interface-declaring subgraph a (fetch __typename through the entity interface).
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ anotherUsers { __typename username } }`,
			subgraphs: subgraphs,
			data:      data,
			expected:  `{"data":{"anotherUsers":[{"__typename":"User","username":"u1-username"}]}}`,
		})
	})

	t.Run("member gate only needs synthesized discriminator", func(t *testing.T) {
		// `anotherUsers { ... on User { __typename } }` -- the member gate itself is the only
		// concrete-type observation; nothing else leaves b.
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ anotherUsers { ... on User { __typename } } }`,
			subgraphs: subgraphs,
			data:      data,
			expected:  `{"data":{"anotherUsers":[{"__typename":"User"}]}}`,
		})
	})

	t.Run("both directions with member gate", func(t *testing.T) {
		runExecSync(t, execSyncCase{
			schema:    schema,
			op:        `{ users { ... on User { age id name username } id name } }`,
			subgraphs: subgraphs,
			data:      data,
			expected:  `{"data":{"users":[{"age":11,"id":"u1","name":"u1-name","username":"u1-username"}]}}`,
		})
	})
}

// TestExecSync_NullKeyRepresentationSkipped is the null-key representation class (audit:
// null-keys_0): a key-relay entity that the intermediate subgraph cannot resolve (b returns a null
// entity for upc b3) leaves the NEXT hop's key value null -- that representation item must be
// dropped from the batch (v1: the representation leaf carries the key field's REAL schema
// nullability, so a null `id: ID!` errors the item render and SkipErrItems drops it), leaving the
// sibling entities resolved and the null-keyed one completing to null without failing the whole
// fetch. planv2's always-nullable representation leaves sent `"id":null` upstream instead.
func TestExecSync_NullKeyRepresentationSkipped(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { bookContainers: [BookContainer] }
type BookContainer { book: Book }
type Book { upc: ID! id: ID! author: Author }
type Author { id: ID! name: String }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { bookContainers: [BookContainer] }
type BookContainer { book: Book }
type Book @key(fields: "upc") { upc: ID! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"bookContainers"}},
					{TypeName: "Book", FieldNames: []string{"upc"}},
				},
				ChildNodes: plan.TypeFields{{TypeName: "BookContainer", FieldNames: []string{"book"}}},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Book", SelectionSet: "upc"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Book @key(fields: "id") @key(fields: "upc") { id: ID! upc: ID! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{{TypeName: "Book", FieldNames: []string{"id", "upc"}}},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{
						{TypeName: "Book", SelectionSet: "id"},
						{TypeName: "Book", SelectionSet: "upc"},
					},
				},
			},
		},
		{
			name: "c",
			sdl: `
type Book @key(fields: "id") { id: ID! author: Author }
type Author { id: ID! name: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes:  plan.TypeFields{{TypeName: "Book", FieldNames: []string{"id", "author"}}},
				ChildNodes: plan.TypeFields{{TypeName: "Author", FieldNames: []string{"id", "name"}}},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Book", SelectionSet: "id"}},
				},
			},
		},
	}
	upcToID := map[string]string{"b1": "id1", "b2": "id2"}
	authors := map[string]map[string]any{
		"id1": {"__typename": "Author", "id": "a1", "name": "Alice"},
		"id2": {"__typename": "Author", "id": "a2", "name": "Bob"},
	}
	data := map[string]syncData{
		"a": {root: map[string]any{
			"bookContainers": []any{
				map[string]any{"book": map[string]any{"__typename": "Book", "upc": "b1"}},
				map[string]any{"book": map[string]any{"__typename": "Book", "upc": "b2"}},
				map[string]any{"book": map[string]any{"__typename": "Book", "upc": "b3"}},
			},
		}},
		"b": {entityCalls: map[string]func(map[string]any) map[string]any{
			"Book": func(rep map[string]any) map[string]any {
				upc, _ := rep["upc"].(string)
				id, ok := upcToID[upc]
				if !ok {
					return nil
				}
				return map[string]any{"id": id, "upc": upc}
			},
		}},
		"c": {entityCalls: map[string]func(map[string]any) map[string]any{
			"Book": func(rep map[string]any) map[string]any {
				id, _ := rep["id"].(string)
				author, ok := authors[id]
				if !ok {
					return nil
				}
				return map[string]any{"author": author}
			},
		}},
	}

	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `{ bookContainers { book { upc author { name } } } }`,
		subgraphs: subgraphs,
		data:      data,
		expected:  `{"data":{"bookContainers":[{"book":{"upc":"b1","author":{"name":"Alice"}}},{"book":{"upc":"b2","author":{"name":"Bob"}}},{"book":{"upc":"b3","author":null}}]}}`,
	})
}

// TestExecSync_DistributedUnionMemberAtForeignPosition is the distributed abstract-member class
// (audit: partial-union-complex_4): subgraphs a and b declare DIFFERENT member sets for one union
// (a: Common|OnlyA, b: Common|OnlyB), and the queried position (rootA.bWrapper.actions) is
// reachable ONLY through b (bWrapper exists only there). The D6 narrowing verdict must judge
// member possibility against the subgraphs that can supply THIS POSITION's parent instance ({b}),
// not every subgraph declaring Wrapper.actions -- the position-blind set wrongly intersected OnlyB
// away (impossible in a) and the field resolved to a response-only null.
func TestExecSync_DistributedUnionMemberAtForeignPosition(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { rootA: Container }
type Container { id: ID! aWrapper: Wrapper bWrapper: Wrapper }
type Wrapper { actions: [Action] }
union Action = Common | OnlyA | OnlyB
type Common { label: String }
type OnlyA { a: String }
type OnlyB { b: String }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { rootA: Container }
type Container @key(fields: "id") { id: ID! aWrapper: Wrapper }
type Wrapper { actions: [Action] }
union Action = Common | OnlyA
type Common { label: String }
type OnlyA { a: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"rootA"}},
					{TypeName: "Container", FieldNames: []string{"id", "aWrapper"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Wrapper", FieldNames: []string{"actions"}},
					{TypeName: "Common", FieldNames: []string{"label"}},
					{TypeName: "OnlyA", FieldNames: []string{"a"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Container", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Container @key(fields: "id") { id: ID! bWrapper: Wrapper }
type Wrapper { actions: [Action] }
union Action = Common | OnlyB
type Common { label: String }
type OnlyB { b: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Container", FieldNames: []string{"id", "bWrapper"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Wrapper", FieldNames: []string{"actions"}},
					{TypeName: "Common", FieldNames: []string{"label"}},
					{TypeName: "OnlyB", FieldNames: []string{"b"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Container", SelectionSet: "id"}},
				},
			},
		},
	}
	data := map[string]syncData{
		"a": {root: map[string]any{
			"rootA": map[string]any{"__typename": "Container", "id": "c1"},
		}},
		"b": {entities: map[string]map[string]map[string]any{
			"Container": {"c1": {
				"bWrapper": map[string]any{
					"__typename": "Wrapper",
					"actions": []any{
						map[string]any{"__typename": "Common", "label": "common label"},
						map[string]any{"__typename": "OnlyB", "b": "only b"},
					},
				},
			}},
		}},
	}

	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `{ rootA { bWrapper { actions { __typename ... on Common { label } ... on OnlyA { a } ... on OnlyB { b } } } } }`,
		subgraphs: subgraphs,
		data:      data,
		expected:  `{"data":{"rootA":{"bWrapper":{"actions":[{"__typename":"Common","label":"common label"},{"__typename":"OnlyB","b":"only b"}]}}}}`,
	})
}

// TestExecSync_InterfaceFragmentLocalMembership is the distributed interface-membership class
// (audit: union-interface-distributed_0): the composed schema has Oven and Toaster implementing
// Node, but subgraph a's OWN schema only declares `Toaster implements Node` (b contributes Oven's
// membership). Printing `... on Node { id }` into a's document makes a resolve id for toasters
// ONLY -- the ovens' ids silently null although a resolves Oven.id (it is Oven's @key). The
// fragment must expand per concrete member against a subgraph whose local membership differs from
// the composed one (v1's abstract selection rewriter).
func TestExecSync_InterfaceFragmentLocalMembership(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { products: [Product] }
union Product = Oven | Toaster
interface Node { id: ID! }
type Oven implements Node { id: ID! }
type Toaster implements Node { id: ID! warranty: Int }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { products: [Product] }
union Product = Oven | Toaster
interface Node { id: ID! }
type Oven @key(fields: "id") { id: ID! }
type Toaster implements Node @key(fields: "id") { id: ID! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"products"}},
					{TypeName: "Oven", FieldNames: []string{"id"}},
					{TypeName: "Toaster", FieldNames: []string{"id"}},
				},
				ChildNodes: plan.TypeFields{{TypeName: "Node", FieldNames: []string{"id"}}},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{
						{TypeName: "Oven", SelectionSet: "id"},
						{TypeName: "Toaster", SelectionSet: "id"},
					},
				},
			},
		},
		{
			name: "b",
			sdl: `
interface Node { id: ID! }
type Oven implements Node @key(fields: "id") { id: ID! warranty: Int }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes:  plan.TypeFields{{TypeName: "Oven", FieldNames: []string{"id", "warranty"}}},
				ChildNodes: plan.TypeFields{{TypeName: "Node", FieldNames: []string{"id"}}},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Oven", SelectionSet: "id"}},
				},
			},
		},
	}
	data := map[string]syncData{
		"a": {
			root: map[string]any{"products": []any{
				map[string]any{"__typename": "Oven", "id": "oven1"},
				map[string]any{"__typename": "Toaster", "id": "toaster1"},
			}},
			// a's LOCAL membership: only Toaster implements Node (mirrors the subgraph SDL -- the
			// semantic resolver must not apply `... on Node` to Oven values in a).
			implements: map[string][]string{"Toaster": {"Node"}},
		},
		"b": {
			implements: map[string][]string{"Oven": {"Node"}},
		},
	}

	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `{ products { ... on Node { id } } }`,
		subgraphs: subgraphs,
		data:      data,
		expected:  `{"data":{"products":[{"id":"oven1"},{"id":"toaster1"}]}}`,
	})
}

// TestExecSync_RequiresOnRootEntityWgcMetadata is TestExecSync_RequiresOnRootEntity with the
// metadata shape the REAL wgc-composed router config carries (m3 encoding wave, live-router
// diagnosis): the requiring subgraph's node metadata does NOT list the @requires input as an
// external field -- `price` appears nowhere in b's rootNodes -- only the `requires` configuration
// names it. The audit-deriver shape (ExternalFieldNames: ["price"]) passed while the router
// failed; this pins the wgc shape in-repo.
func TestExecSync_RequiresOnRootEntityWgcMetadata(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { product(id: ID!): Product }
type Product { id: ID! name: String! price: Float! isExpensive: Boolean! isAvailable: Boolean! }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { product(id: ID!): Product }
type Product @key(fields: "id") { id: ID! name: String! price: Float! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"product"}},
					{TypeName: "Product", FieldNames: []string{"id", "name", "price"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Product @key(fields: "id") { id: ID! price: Float! @external isExpensive: Boolean! @requires(fields: "price") isAvailable: Boolean! }
`,
			meta: &plan.DataSourceMetadata{
				// wgc shape: NO ExternalFieldNames -- price is absent from the node metadata.
				RootNodes: plan.TypeFields{
					{TypeName: "Product", FieldNames: []string{"id", "isExpensive", "isAvailable"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
					Requires: plan.FederationFieldConfigurations{
						{TypeName: "Product", FieldName: "isExpensive", SelectionSet: "price"},
					},
				},
			},
		},
	}
	product := map[string]any{"__typename": "Product", "id": "p1", "name": "p1-name", "price": 599.99}
	data := map[string]syncData{
		"a": {root: map[string]any{"product": product}},
		"b": {
			entityCalls: map[string]func(map[string]any) map[string]any{
				"Product": func(rep map[string]any) map[string]any {
					price, ok := rep["price"].(float64)
					if !ok {
						return map[string]any{"isExpensive": nil, "isAvailable": true}
					}
					return map[string]any{"isExpensive": price > 100, "isAvailable": true}
				},
			},
		},
	}
	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `{ product(id: "p1") { id name price isExpensive isAvailable } }`,
		subgraphs: subgraphs,
		data:      data,
		fields: []plan.FieldConfiguration{
			{TypeName: "Query", FieldName: "product",
				Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
		},
		expected: `{"data":{"product":{"id":"p1","name":"p1-name","price":599.99,"isExpensive":true,"isAvailable":true}}}`,
	})
}

// TestExecSync_ScopedUnscopedTwinSingleFetch is the scoped/unscoped-twin co-location residual made
// OBSERVABLE (audit: mutations_0/_1): b's reference resolver consumes the entity on first
// resolution (the audit's deleteProduct-in-__resolveReference trap), so a plan that splits the
// @requires field (isExpensive, requires price) and its plain sibling (isAvailable) into TWO
// entity fetches to b sees the second one resolve null. v1 co-locates them into ONE fetch whose
// representation carries the key AND the @requires value.
func TestExecSync_ScopedUnscopedTwinSingleFetch(t *testing.T) {
	const schema = `
schema { query: Query }
type Query { product(id: ID!): Product }
type Product { id: ID! name: String! price: Float! isExpensive: Boolean! isAvailable: Boolean! }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { product(id: ID!): Product }
type Product @key(fields: "id") { id: ID! name: String! price: Float! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"product"}},
					{TypeName: "Product", FieldNames: []string{"id", "name", "price"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Product @key(fields: "id") { id: ID! price: Float! @external isExpensive: Boolean! @requires(fields: "price") isAvailable: Boolean! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Product", FieldNames: []string{"id", "isExpensive", "isAvailable"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
					Requires: plan.FederationFieldConfigurations{
						{TypeName: "Product", FieldName: "isExpensive", SelectionSet: "price"},
					},
				},
			},
		},
	}
	var mu sync.Mutex
	resolved := false // b consumes the entity on first reference resolution
	data := map[string]syncData{
		"a": {root: map[string]any{
			"product": map[string]any{"__typename": "Product", "id": "p1", "name": "p1-name", "price": 599.99},
		}},
		"b": {
			entityCalls: map[string]func(map[string]any) map[string]any{
				"Product": func(rep map[string]any) map[string]any {
					mu.Lock()
					defer mu.Unlock()
					if resolved {
						return nil // the entity was consumed by the previous fetch
					}
					resolved = true
					out := map[string]any{"isAvailable": true}
					if price, ok := rep["price"].(float64); ok {
						out["isExpensive"] = price > 100
					}
					return out
				},
			},
		},
	}
	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `{ product(id: "p1") { id name price isExpensive isAvailable } }`,
		subgraphs: subgraphs,
		data:      data,
		fields: []plan.FieldConfiguration{
			{TypeName: "Query", FieldName: "product",
				Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
		},
		expected: `{"data":{"product":{"id":"p1","name":"p1-name","price":599.99,"isExpensive":true,"isAvailable":true}}}`,
		reset: func() {
			mu.Lock()
			defer mu.Unlock()
			resolved = false
		},
	})
}

// TestExecSync_ShareableMutationRootSingleExecution is the shared-root mutation split class (real
// Guild audit: mutations_3; DIVERGENCES executed-truth residual entry 1 -- the silent-wrong-effect
// blocker): Mutation.submitReport is shareable in subgraphs a and b, and its payload entity Report
// carries alpha only in a and beta only in b. Per-goal cheapest routing resolves alpha through a's
// root and beta through b's root -- TWO root fetches, each executing the side effect (the audit's
// live capture: b's copy succeeds, a's fails with "already added"; with non-idempotent resolvers
// both apply). The obligated shape (FS-ROOT-6, GraphQL Section 6.2.2 execute-once; v1 parity): the
// root field's ENTIRE selection enters through ONE subgraph's root fetch, and the cross-subgraph
// field arrives via an entity jump off the payload. The fixture counts executions through shared
// state -- the executed truth this test pins is EXACTLY ONE submission per leg.
func TestExecSync_ShareableMutationRootSingleExecution(t *testing.T) {
	const schema = `
schema { query: Query mutation: Mutation }
type Query { report: Report }
type Mutation { submitReport: Report! }
type Report { id: ID! alpha: String! beta: String! }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { report: Report }
type Mutation { submitReport: Report! }
type Report @key(fields: "id") { id: ID! alpha: String! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"report"}},
					{TypeName: "Mutation", FieldNames: []string{"submitReport"}},
					{TypeName: "Report", FieldNames: []string{"id", "alpha"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Report", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Mutation { submitReport: Report! }
type Report @key(fields: "id") { id: ID! beta: String! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Mutation", FieldNames: []string{"submitReport"}},
					{TypeName: "Report", FieldNames: []string{"id", "beta"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Report", SelectionSet: "id"}},
				},
			},
		},
	}

	var mu sync.Mutex
	execA, execB := 0, 0
	v1Executions := -1 // captured by reset (which runs between the v1 leg and the planv2 leg)
	data := map[string]syncData{
		"a": {
			calls: map[string]func(map[string]any) any{
				"submitReport": func(map[string]any) any {
					mu.Lock()
					defer mu.Unlock()
					execA++
					return map[string]any{"__typename": "Report", "id": "r1", "alpha": "from-a"}
				},
			},
			entities: map[string]map[string]map[string]any{
				"Report": {"r1": {"alpha": "from-a"}},
			},
		},
		"b": {
			calls: map[string]func(map[string]any) any{
				"submitReport": func(map[string]any) any {
					mu.Lock()
					defer mu.Unlock()
					execB++
					return map[string]any{"__typename": "Report", "id": "r1", "beta": "from-b"}
				},
			},
			entities: map[string]map[string]map[string]any{
				"Report": {"r1": {"beta": "from-b"}},
			},
		},
	}

	runExecSync(t, execSyncCase{
		schema:    schema,
		op:        `mutation { submitReport { id alpha beta } }`,
		subgraphs: subgraphs,
		data:      data,
		expected:  `{"data":{"submitReport":{"id":"r1","alpha":"from-a","beta":"from-b"}}}`,
		reset: func() {
			mu.Lock()
			defer mu.Unlock()
			v1Executions = execA + execB
			execA, execB = 0, 0
		},
	})

	// Executed-truth assertions: the side effect ran EXACTLY ONCE per leg. A split plan produces two
	// root fetches (one per subgraph), each executing the mutation -- the merged DATA can still come
	// out byte-correct, which is exactly what makes the defect silent; only the execution count
	// observes it.
	mu.Lock()
	defer mu.Unlock()
	if v1Executions != 1 {
		t.Fatalf("fixture sanity: v1 executed the mutation %d times, want exactly 1", v1Executions)
	}
	if got := execA + execB; got != 1 {
		t.Fatalf("planv2 executed the mutation %d times (a=%d, b=%d), want exactly 1 "+
			"(shareable mutation root split -- the mutations_3 double-execution)", got, execA, execB)
	}
}
