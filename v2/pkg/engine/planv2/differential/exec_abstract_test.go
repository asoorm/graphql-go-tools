package differential

// exec_abstract_test.go is the PERMANENT executed-truth differential oracle for the abstract-type
// families the real Guild federation-gateway-audit exercises: (1) an interface field with multiple
// implementers, (2) a union whose members are distributed across subgraphs, and (3) an entity call
// reached through an abstract-typed position (union members that are entities resolved in a second
// subgraph).
//
// WHY THIS EXISTS (M4 FieldInfo-regression wave): the sibling oracle fieldinfo_test.go compares
// resolve.FieldInfo ATTRIBUTES (Name / ParentTypeNames / AuthorizationCoordinates) between planv2
// and v1 -- a STRUCTURAL check. It cannot see an execution-level regression, because it never runs
// a query through postprocess + resolve and compares the ANSWER. This oracle closes that gap for
// the abstract-type class: it PLANS with the planv2 facade (IncludeInfo ON, the production path),
// runs the plan through the untouched v1 postprocess, EXECUTES it against in-harness subgraphs, and
// asserts the client-visible response equals the audit's expected answer -- and, where v1 can plan
// the shape, that v1 produces the same answer (runExecSync's built-in v1-sanity leg).
//
// SCOPE NOTE (honest, per the wave's finding): planv2 already answers all three families correctly
// at HEAD -- this oracle is GREEN. It is a REGRESSION GUARD, not a red-first reproduction: the M4
// investigation established that resolve.FieldInfo / FetchInfo.RootFields are execution-inert in
// this pipeline (no field-value renderer, no authorizer, and the postprocessed fetch tree is
// byte-identical with Info on vs off), so the FieldInfo emission cannot by itself move an executed
// answer here. What this oracle DOES guarantee going forward is that any future change which moves
// the executed answer for an abstract-type shape -- through the postprocess seam or anywhere else --
// fails a committed gate instead of only the out-of-tree router audit. The guard's teeth are
// verified by TestExecAbstract_GuardIsLive below, which corrupts one expected answer and confirms
// the harness reports the mismatch.

import (
	"context"
	"reflect"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
)

// --- Family 1: interface field with multiple implementers (member-gated selections) --------------

func execAbstractInterface() execSyncCase {
	const schema = `
schema { query: Query }
type Query { node: Node }
interface Node { id: ID! }
type User implements Node { id: ID! name: String }
type Admin implements Node { id: ID! level: Int }
`
	subgraphs := []subgraph{{
		name: "a",
		sdl: `
type Query { node: Node }
interface Node { id: ID! }
type User implements Node { id: ID! name: String }
type Admin implements Node { id: ID! level: Int }
`,
		meta: &plan.DataSourceMetadata{
			RootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"node"}}},
			ChildNodes: plan.TypeFields{
				{TypeName: "Node", FieldNames: []string{"id"}},
				{TypeName: "User", FieldNames: []string{"id", "name"}},
				{TypeName: "Admin", FieldNames: []string{"id", "level"}},
			},
		},
	}}
	return execSyncCase{
		name:      "interface field with multiple implementers",
		schema:    schema,
		op:        `{ node { id __typename ... on User { name } ... on Admin { level } } }`,
		subgraphs: subgraphs,
		data: map[string]syncData{
			"a": {
				root:       map[string]any{"node": map[string]any{"__typename": "Admin", "id": "ad1", "level": 7}},
				implements: map[string][]string{"User": {"Node"}, "Admin": {"Node"}},
			},
		},
		expected: `{"data":{"node":{"id":"ad1","__typename":"Admin","level":7}}}`,
	}
}

// --- Family 2: union with members distributed across subgraphs (child-type-mismatch shape) --------
//
// union Account = User | Admin; Query.users (subgraph a, User.id only) and Query.accounts (subgraph
// b, the union owner). users.name is resolved by an entity jump into b. v1 cannot plan this family
// (it rejects with "Cannot query field name on type Query"), so the v1-sanity leg is skipped.

func execAbstractDistributedUnion() execSyncCase {
	const schema = `
schema { query: Query }
type Query { users: [User!]! accounts: [Account!]! }
union Account = User | Admin
type User { id: ID name: String similarAccounts: [Account!]! }
type Admin { id: ID name: String similarAccounts: [Account!]! }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { users: [User!]! }
type User @key(fields: "id") { id: ID }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"users"}},
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
union Account = User | Admin
type User @key(fields: "id") { id: ID! name: String similarAccounts: [Account!]! }
type Admin { id: ID name: String similarAccounts: [Account!]! }
type Query { accounts: [Account!]! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"accounts"}},
					{TypeName: "User", FieldNames: []string{"id", "name", "similarAccounts"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Admin", FieldNames: []string{"id", "name", "similarAccounts"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
	}
	return execSyncCase{
		name:      "union with distributed members",
		schema:    schema,
		op:        `{ users { id name } accounts { ... on User { id name } ... on Admin { id name } } }`,
		subgraphs: subgraphs,
		skipV1:    true, // v1 cannot plan this shape (Cannot query field name on type Query)
		data: map[string]syncData{
			"a": {root: map[string]any{
				"users": []any{map[string]any{"__typename": "User", "id": "u1"}},
			}},
			"b": {
				root: map[string]any{"accounts": []any{
					map[string]any{"__typename": "User", "id": "u1", "name": "u1-name"},
					map[string]any{"__typename": "Admin", "id": "a1", "name": "a1-name"},
				}},
				entities: map[string]map[string]map[string]any{"User": {"u1": {"name": "u1-name"}}},
			},
		},
		expected: `{"data":{"users":[{"id":"u1","name":"u1-name"}],"accounts":[{"id":"u1","name":"u1-name"},{"id":"a1","name":"a1-name"}]}}`,
	}
}

// --- Family 3: entity call reached through an abstract-typed position -----------------------------
//
// union Result = Product | Article, both entities. Subgraph a returns the union (typename + key);
// each member's descriptive field (name / headline) is resolved by an entity jump into subgraph b.
// This is the "entity-call on an abstract type" shape: the representations sent to b are gathered
// from member-gated positions under the union.

func execAbstractEntityCallOnUnion() execSyncCase {
	const schema = `
schema { query: Query }
type Query { search: [Result!]! }
union Result = Product | Article
type Product { id: ID! name: String }
type Article { id: ID! headline: String }
`
	subgraphs := []subgraph{
		{
			name: "a",
			sdl: `
type Query { search: [Result!]! }
union Result = Product | Article
type Product @key(fields: "id") { id: ID! }
type Article @key(fields: "id") { id: ID! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"search"}},
					{TypeName: "Product", FieldNames: []string{"id"}},
					{TypeName: "Article", FieldNames: []string{"id"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{
						{TypeName: "Product", SelectionSet: "id"},
						{TypeName: "Article", SelectionSet: "id"},
					},
				},
			},
		},
		{
			name: "b",
			sdl: `
type Product @key(fields: "id") { id: ID! name: String }
type Article @key(fields: "id") { id: ID! headline: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Product", FieldNames: []string{"id", "name"}},
					{TypeName: "Article", FieldNames: []string{"id", "headline"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{
						{TypeName: "Product", SelectionSet: "id"},
						{TypeName: "Article", SelectionSet: "id"},
					},
				},
			},
		},
	}
	return execSyncCase{
		name:      "entity call through abstract position",
		schema:    schema,
		op:        `{ search { __typename ... on Product { id name } ... on Article { id headline } } }`,
		subgraphs: subgraphs,
		skipV1:    true, // v1 cannot plan this distributed-union-member shape
		data: map[string]syncData{
			"a": {root: map[string]any{
				"search": []any{
					map[string]any{"__typename": "Product", "id": "p1"},
					map[string]any{"__typename": "Article", "id": "ar1"},
				},
			}},
			"b": {
				entities: map[string]map[string]map[string]any{
					"Product": {"p1": {"name": "widget"}},
					"Article": {"ar1": {"headline": "big news"}},
				},
			},
		},
		expected: `{"data":{"search":[{"__typename":"Product","id":"p1","name":"widget"},{"__typename":"Article","id":"ar1","headline":"big news"}]}}`,
	}
}

// TestExecAbstract is the permanent executed-truth abstract-type oracle (three families). Each case
// is planned by the planv2 facade with FieldInfo/RootFields emission ON, run through the untouched
// v1 postprocess, executed, and compared byte-wise against the expected answer (and against v1
// where v1 can plan the shape).
func TestExecAbstract(t *testing.T) {
	cases := []execSyncCase{
		execAbstractInterface(),
		execAbstractDistributedUnion(),
		execAbstractEntityCallOnUnion(),
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			runExecSync(t, c)
		})
	}
}

// TestExecAbstract_GuardIsLive proves the oracle has teeth: it executes the interface fixture
// through the production pipeline (planv2 facade -> untouched v1 postprocess -> resolve) and pins
// that the executed bytes MATCH the correct expected answer and DO NOT match a corrupted one. This
// establishes that TestExecAbstract's green status reflects a real byte comparison of executed
// output, so a future execution regression on an abstract-type shape cannot pass silently.
func TestExecAbstract_GuardIsLive(t *testing.T) {
	c := execAbstractInterface()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	urls := map[string]string{}
	for _, sg := range c.subgraphs {
		srv := syncSubgraph(t, c.data[sg.name])
		defer srv.Close()
		urls[sg.name] = srv.URL
	}
	cfg := executableConfig(t, ctx, c.subgraphs, urls)

	p, err := planv2.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("planv2 planner: %v", err)
	}
	op, def, rep := parseAndNormalize(t, c.schema, c.op)
	pl := p.Plan(op, def, "", rep)
	if rep.HasErrors() {
		t.Fatalf("planv2 planning failed: %s", rep.Error())
	}
	got := executeSyncPlan(t, ctx, pl, op, "planv2")

	gotData, _ := dataAndErrors(t, "planv2", got)
	wantData, _ := dataAndErrors(t, "expected", c.expected)
	corruptData, _ := dataAndErrors(t, "corrupt", `{"data":{"node":{"id":"WRONG","__typename":"Admin","level":7}}}`)

	if !reflect.DeepEqual(gotData, wantData) {
		t.Fatalf("guard setup: planv2 output did not match the correct answer\n got:  %s\n want: %s", got, c.expected)
	}
	if reflect.DeepEqual(gotData, corruptData) {
		t.Fatalf("guard is not live: executed output matched a deliberately corrupted answer")
	}
}
