package differential

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
)

// TestDefer_BothPlannersProduceDeferPlans is the vacuity guard for the defer fixtures: a MATCH is
// meaningless if @defer was silently stripped and both planners produced flattened synchronous
// plans, so this pins that BOTH planners emit *plan.DeferResponsePlan on every defer fixture (the
// oracle then compares descriptors/partition/stamps, never the trivial sync path).
func TestDefer_BothPlannersProduceDeferPlans(t *testing.T) {
	for _, c := range deferCases() {
		t.Run(c.name, func(t *testing.T) {
			cfg := buildConfig(t, c.subgraphs, c.fields...)

			oldPlanner, err := plan.NewPlanner(cfg)
			if err != nil {
				t.Fatalf("old planner: %v", err)
			}
			newPlanner, err := planv2.NewPlanner(cfg)
			if err != nil {
				t.Fatalf("planv2 planner: %v", err)
			}

			oldOp, oldDef, oldReport := parseAndNormalizeOpts(t, c.schema, c.op, true)
			oldPlan := oldPlanner.Plan(oldOp, oldDef, "", oldReport)
			if oldReport.HasErrors() {
				t.Fatalf("old planner failed: %s", oldReport.Error())
			}
			if _, ok := oldPlan.(*plan.DeferResponsePlan); !ok {
				t.Fatalf("v1 must produce a DeferResponsePlan, got %T", oldPlan)
			}

			newOp, newDef, newReport := parseAndNormalizeOpts(t, c.schema, c.op, true)
			newPlan := newPlanner.Plan(newOp, newDef, "", newReport)
			if newReport.HasErrors() {
				t.Fatalf("planv2 failed: %s", newReport.Error())
			}
			if _, ok := newPlan.(*plan.DeferResponsePlan); !ok {
				t.Fatalf("planv2 must produce a DeferResponsePlan, got %T", newPlan)
			}
		})
	}
}

// deferCases pin D11.13 against v1 (v1 has FULL incremental-delivery support -- the differential
// oracle for defer): both planners normalize with WithEnableDefer and must agree on the defer
// descriptors (ids, parent chain, labels, mount paths), the increment partition (which defer
// scopes own fetches), and the DeferField-stamped response shape, under every datasource ordering
// (CompareDeferPlans). The three fixtures are the three anchor classes of v1's defer plan suite:
// root re-walk (same-subgraph defer), nested defer (parent/child scopes), and entity re-entry
// (deferred field behind an entity jump, keys in the parent scope).
func deferCases() []differentialCase {
	singleUser := []subgraph{
		{
			name: "first",
			sdl: `
type Query { user: User }
type User { id: ID! name: String! title: String! description: String! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"user"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "User", FieldNames: []string{"id", "name", "title", "description"}},
				},
			},
		},
	}
	const singleUserSchema = `
schema { query: Query }
type Query { user: User }
type User { id: ID! name: String! title: String! description: String! }
`

	entityUser := []subgraph{
		{
			name: "first",
			sdl: `
type Query { user: User }
type User @key(fields: "id") { id: ID! title: String! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"user"}},
					{TypeName: "User", FieldNames: []string{"id", "title"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "second",
			sdl: `
type User @key(fields: "id") { id: ID! firstName: String! lastName: String! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "User", FieldNames: []string{"id", "firstName", "lastName"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
	}
	const entityUserSchema = `
schema { query: Query }
type Query { user: User }
type User { id: ID! title: String! firstName: String! lastName: String! }
`

	return []differentialCase{
		{
			name:      "defer-root-rewalk",
			schema:    singleUserSchema,
			op:        `query User { user { name ... @defer { title } } }`,
			subgraphs: singleUser,
			deferOp:   true,
		},
		{
			name:      "defer-nested",
			schema:    singleUserSchema,
			op:        `query User { user { name ... @defer(label: "outer") { title ... @defer { description } } } }`,
			subgraphs: singleUser,
			deferOp:   true,
		},
		{
			name:      "defer-entity-jump",
			schema:    entityUserSchema,
			op:        `query User { user { title firstName ... @defer { lastName } } }`,
			subgraphs: entityUser,
			deferOp:   true,
		},
	}
}
