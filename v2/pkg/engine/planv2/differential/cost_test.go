package differential

// cost_test.go is the IBM @cost / ComputeCosts parity oracle (M4.5): both planners plan an identical
// (schema, operation, per-DS CostConfig) fixture with plan.Configuration.ComputeCosts set, and the
// oracle compares the CostCalculator each plan carries -- EstimateCost over identical variables must
// agree exactly. Cost is POST-PLAN (the calculator is attached to the finished plan; it does not steer
// plan selection), so this oracle rides the same normalized-input pipeline as the shape oracle and
// asserts the calculator, never the fetch tree.
//
// A nil calculator on a ComputeCosts:true plan is the defect this oracle exists to catch: it would
// mean the router's ValidateSliceArguments / cost-limit enforcement silently does nothing (see
// execution_engine.go getCachedPlan -- a nil calculator skips ValidateSliceArguments and cost
// computation). Both planners MUST hand back a working calculator.

import (
	"testing"

	"github.com/wundergraph/astjson"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/testing/permutations"
)

func costVars(t *testing.T, json string) resolve.VariablesView {
	t.Helper()
	if json == "" {
		json = "{}"
	}
	return resolve.NewVariablesView(astjson.MustParseBytes([]byte(json)), nil)
}

// planWithCost plans (schema, op) with the given planner-building closures under ComputeCosts, and
// returns the attached CostCalculator. A nil calculator is reported by the caller as the M4.5 defect.
func planV1Cost(t *testing.T, cfg plan.Configuration, schema, op string) *plan.CostCalculator {
	t.Helper()
	p, err := plan.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("v1 NewPlanner: %v", err)
	}
	o, d, r := parseAndNormalize(t, schema, op)
	res := p.Plan(o, d, "", r)
	if r.HasErrors() {
		t.Fatalf("v1 plan: %s", r.Error())
	}
	return res.GetCostCalculator()
}

func planV2Cost(t *testing.T, cfg plan.Configuration, schema, op string) *plan.CostCalculator {
	t.Helper()
	p, err := planv2.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("planv2 NewPlanner (ComputeCosts): %v", err)
	}
	o, d, r := parseAndNormalize(t, schema, op)
	res := p.Plan(o, d, "", r)
	if r.HasErrors() {
		t.Fatalf("planv2 plan: %s", r.Error())
	}
	if res == nil {
		t.Fatalf("planv2 returned nil plan")
	}
	return res.GetCostCalculator()
}

// TestCost_Witness_SingleSubgraph is the red-first witness: a single subgraph declares a field weight
// (Hero.name = 17); the query { hero { name } } costs Hero (default object weight 1) + name (17) = 18.
// planv2 must attach a calculator that reports 18 -- identical to v1.
func TestCost_Witness_SingleSubgraph(t *testing.T) {
	const schema = `schema { query: Query } type Query { hero: Hero } type Hero { name: String primaryFunction: String }`
	sg := subgraph{
		name: "catalog",
		sdl:  `type Query { hero: Hero } type Hero { name: String primaryFunction: String }`,
		meta: &plan.DataSourceMetadata{
			RootNodes: plan.TypeFields{
				{TypeName: "Query", FieldNames: []string{"hero"}},
				{TypeName: "Hero", FieldNames: []string{"name", "primaryFunction"}},
			},
			CostConfig: &plan.DataSourceCostConfig{
				Weights: map[plan.FieldCoordinate]*plan.FieldCost{
					{TypeName: "Hero", FieldName: "name"}: {HasWeight: true, Weight: 17},
				},
			},
		},
	}
	cfg := buildConfig(t, []subgraph{sg})
	cfg.ComputeCosts = true

	const op = `{ hero { name primaryFunction } }`

	v1Calc := planV1Cost(t, cfg, schema, op)
	v2Calc := planV2Cost(t, cfg, schema, op)

	if v1Calc == nil {
		t.Fatalf("v1 calculator nil (fixture bug)")
	}
	if v2Calc == nil {
		t.Fatalf("planv2 attached a NIL CostCalculator with ComputeCosts:true -- cost limits would silently NOT be enforced (M4.5 defect)")
	}
	vars := costVars(t, "{}")
	if got := v1Calc.EstimateCost(vars); got != 18 {
		t.Fatalf("v1 estimate = %d, want 18 (fixture bug)", got)
	}
	if got := v2Calc.EstimateCost(vars); got != 18 {
		t.Fatalf("planv2 estimate = %d, want 18 (Hero=1 + name=17)", got)
	}
}

// TestCost_DifferentialParity plans several single-subgraph shapes with ComputeCosts under EVERY
// datasource ordering and asserts planv2's EstimateCost matches v1's exactly. Single-subgraph shapes
// isolate the cost machinery from the field->datasource attribution subset (DV-012): every field
// resolves on the one subgraph, so both planners draw from the same cost config.
func TestCost_DifferentialParity(t *testing.T) {
	cases := []struct {
		name       string
		schema     string
		sdl        string
		rootNodes  plan.TypeFields
		childNodes plan.TypeFields
		cost       *plan.DataSourceCostConfig
		op         string
		vars       string
	}{
		{
			name:      "field weights and default object weight",
			schema:    `schema { query: Query } type Query { hero: Hero } type Hero { name: String rank: Int }`,
			sdl:       `type Query { hero: Hero } type Hero { name: String rank: Int }`,
			rootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"hero"}}, {TypeName: "Hero", FieldNames: []string{"name", "rank"}}},
			cost: &plan.DataSourceCostConfig{
				Weights: map[plan.FieldCoordinate]*plan.FieldCost{
					{TypeName: "Hero", FieldName: "name"}: {HasWeight: true, Weight: 5},
					{TypeName: "Hero", FieldName: "rank"}: {HasWeight: true, Weight: 3},
				},
			},
			op: `{ hero { name rank } }`,
		},
		{
			name:      "type weight on returned object",
			schema:    `schema { query: Query } type Query { hero: Hero } type Hero { name: String }`,
			sdl:       `type Query { hero: Hero } type Hero { name: String }`,
			rootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"hero"}}, {TypeName: "Hero", FieldNames: []string{"name"}}},
			cost: &plan.DataSourceCostConfig{
				Types: map[string]int{"Hero": 9},
			},
			op: `{ hero { name } }`,
		},
		{
			name:      "list field with listSize assumed size",
			schema:    `schema { query: Query } type Query { heroes: [Hero] } type Hero { name: String }`,
			sdl:       `type Query { heroes: [Hero] } type Hero { name: String }`,
			rootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"heroes"}}, {TypeName: "Hero", FieldNames: []string{"name"}}},
			cost: &plan.DataSourceCostConfig{
				Weights: map[plan.FieldCoordinate]*plan.FieldCost{
					{TypeName: "Hero", FieldName: "name"}: {HasWeight: true, Weight: 2},
				},
				ListSizes: map[plan.FieldCoordinate]*plan.FieldListSize{
					{TypeName: "Query", FieldName: "heroes"}: {AssumedSize: 10},
				},
			},
			op: `{ heroes { name } }`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sg := subgraph{
				name: "catalog",
				sdl:  tc.sdl,
				meta: &plan.DataSourceMetadata{
					RootNodes:  tc.rootNodes,
					ChildNodes: tc.childNodes,
					CostConfig: tc.cost,
				},
			}
			baseCfg := buildConfig(t, []subgraph{sg})
			baseCfg.ComputeCosts = true
			vars := costVars(t, tc.vars)

			for _, perm := range permutations.Generate(baseCfg.DataSources) {
				cfg := baseCfg
				cfg.DataSources = perm.DataSources

				v1Calc := planV1Cost(t, cfg, tc.schema, tc.op)
				v2Calc := planV2Cost(t, cfg, tc.schema, tc.op)
				if v2Calc == nil {
					t.Fatalf("order %v: planv2 attached a nil calculator with ComputeCosts:true", perm.Order)
				}
				want := v1Calc.EstimateCost(vars)
				got := v2Calc.EstimateCost(vars)
				if got != want {
					t.Fatalf("order %v: planv2 estimate = %d, v1 = %d", perm.Order, got, want)
				}
			}
		})
	}
}

// TestCost_DifferentialParity_Federated is the DV-012 divergence WITNESS: a shared federation entity
// (User, resolvable on BOTH subgraphs) is where planv2's single-route cost attribution diverges from
// v1's sum-over-all-planners. The client selects `me` (a User) once; v1 charges the User object
// weight ONCE PER subgraph that can resolve the entity (users + reviews = 2), while planv2 charges the
// one route the search chose (1). Every explicitly-weighted leaf (name=2 on users, body=4 on reviews)
// agrees; the whole delta is the shared-entity default object weight double-count. This test pins
// BOTH numbers so the divergence stays visible and bounded (see DIVERGENCES.md DV-012): planv2 is the
// well-defined single-route cost, v1 is higher by exactly the extra shared-entity object weight.
func TestCost_DifferentialParity_Federated(t *testing.T) {
	users := subgraph{
		name: "users",
		sdl:  `type Query { me: User } type User @key(fields: "id") { id: ID! name: String }`,
		meta: &plan.DataSourceMetadata{
			RootNodes: plan.TypeFields{
				{TypeName: "Query", FieldNames: []string{"me"}},
				{TypeName: "User", FieldNames: []string{"id", "name"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
			},
			CostConfig: &plan.DataSourceCostConfig{
				Weights: map[plan.FieldCoordinate]*plan.FieldCost{
					{TypeName: "User", FieldName: "name"}: {HasWeight: true, Weight: 2},
				},
			},
		},
	}
	reviews := subgraph{
		name: "reviews",
		sdl:  `type User @key(fields: "id") { id: ID! reviews: [Review] } type Review { body: String stars: Int }`,
		meta: &plan.DataSourceMetadata{
			RootNodes: plan.TypeFields{{TypeName: "User", FieldNames: []string{"id", "reviews"}}},
			ChildNodes: plan.TypeFields{
				{TypeName: "Review", FieldNames: []string{"body", "stars"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
			},
			CostConfig: &plan.DataSourceCostConfig{
				Weights: map[plan.FieldCoordinate]*plan.FieldCost{
					{TypeName: "Review", FieldName: "body"}: {HasWeight: true, Weight: 4},
				},
			},
		},
	}
	const schema = `schema { query: Query }
type Query { me: User }
type User { id: ID! name: String reviews: [Review] }
type Review { body: String stars: Int }`
	const op = `{ me { name reviews { body } } }`

	baseCfg := buildConfig(t, []subgraph{users, reviews})
	baseCfg.ComputeCosts = true
	vars := costVars(t, "{}")

	// planv2 (single route): me User=1 + name=2 + reviews [Review] field=1 (default list, size 1) +
	//   body=4  => 8.
	// v1 (sum over planners): the shared User entity adds a second default object weight (+1)  => 9.
	const wantV2 = 8
	const wantV1 = 9

	for _, perm := range permutations.Generate(baseCfg.DataSources) {
		cfg := baseCfg
		cfg.DataSources = perm.DataSources

		v1Calc := planV1Cost(t, cfg, schema, op)
		v2Calc := planV2Cost(t, cfg, schema, op)
		if v2Calc == nil {
			t.Fatalf("order %v: planv2 attached a nil calculator with ComputeCosts:true", perm.Order)
		}
		gotV1 := v1Calc.EstimateCost(vars)
		gotV2 := v2Calc.EstimateCost(vars)
		if gotV2 != wantV2 {
			t.Fatalf("order %v: planv2 estimate = %d, want %d (single-route cost)", perm.Order, gotV2, wantV2)
		}
		if gotV1 != wantV1 {
			t.Fatalf("order %v: v1 estimate = %d, want %d (sum-over-planners; DV-012 witness stale?)", perm.Order, gotV1, wantV1)
		}
		if gotV2 > gotV1 {
			t.Fatalf("order %v: planv2 single-route cost %d exceeds v1 sum-over-planners %d -- DV-012 says planv2 is a subset", perm.Order, gotV2, gotV1)
		}
	}
}
