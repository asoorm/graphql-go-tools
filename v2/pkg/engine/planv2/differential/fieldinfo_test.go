package differential

// fieldinfo_test.go is the FieldInfo / authorization-coordinate parity oracle (M4.1 adoption
// safety): both planners plan identical fixtures, both plans run the untouched v1 postprocess, and
// the oracle compares (1) the derived resolve.AuthorizationCoordinates EXACTLY -- the set postprocess
// hands the BatchAuthorizer, i.e. the security surface -- and (2) the per-field resolve.FieldInfo
// attributes v1 emits (Name, ExactParentTypeName, ParentTypeNames, NamedType, HasAuthorizationRule)
// at every matched response position, with Source compared as a non-empty-subset (planv2 attributes
// each field to the ONE route the search chose; v1 lists every planner that touched the field --
// a documented superset).
//
// Scope note (mirrors the package doc): the response-tree walk matches fields by (client key, gate)
// like the shape oracle; positions only one plan carries are already divergences under the shape
// oracle and are not re-reported here.

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/testing/permutations"
)

// fieldInfoFixture is one (schema, op, subgraphs, field configs) case for the parity oracle.
type fieldInfoFixture struct {
	name      string
	schema    string
	op        string
	subgraphs []subgraph
	fields    plan.FieldConfigurations
}

// fieldInfoFixtures: a handful of shapes covering the consumers -- a root field with an auth rule,
// an entity-jump field with an auth rule (coordinate must name the jump's datasource), an
// interface-typed position (ParentTypeNames must include implementers), and a member-refined field.
func fieldInfoFixtures() []fieldInfoFixture {
	usersReviews := []subgraph{
		{
			name: "users",
			sdl: `
type Query { me: User }
type User @key(fields: "id") { id: ID! name: String secret: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"me"}},
					{TypeName: "User", FieldNames: []string{"id", "name", "secret"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "reviews",
			sdl: `
type User @key(fields: "id") { id: ID! reviews: [Review] }
type Review { body: String stars: Int }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "User", FieldNames: []string{"id", "reviews"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Review", FieldNames: []string{"body", "stars"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
	}
	usersReviewsSchema := `
schema { query: Query }
type Query { me: User }
type User { id: ID! name: String secret: String reviews: [Review] }
type Review { body: String stars: Int }
`

	ifaceSubgraphs := []subgraph{
		{
			name: "nodes",
			sdl: `
type Query { node: Node }
interface Node { id: ID! }
type A implements Node { id: ID! a: String }
type B implements Node { id: ID! b: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"node"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Node", FieldNames: []string{"id"}},
					{TypeName: "A", FieldNames: []string{"id", "a"}},
					{TypeName: "B", FieldNames: []string{"id", "b"}},
				},
			},
		},
	}
	ifaceSchema := `
schema { query: Query }
type Query { node: Node }
interface Node { id: ID! }
type A implements Node { id: ID! a: String }
type B implements Node { id: ID! b: String }
`

	return []fieldInfoFixture{
		{
			name:      "auth rule on root and entity-jump fields",
			schema:    usersReviewsSchema,
			op:        `{ me { name secret reviews { body stars } } }`,
			subgraphs: usersReviews,
			fields: plan.FieldConfigurations{
				{TypeName: "Query", FieldName: "me", HasAuthorizationRule: true},
				{TypeName: "User", FieldName: "secret", HasAuthorizationRule: true},
				{TypeName: "Review", FieldName: "body", HasAuthorizationRule: true},
			},
		},
		{
			name:      "no auth rules -- empty coordinate list on both sides",
			schema:    usersReviewsSchema,
			op:        `{ me { name } }`,
			subgraphs: usersReviews,
		},
		{
			name:      "interface position: ParentTypeNames carry implementers",
			schema:    ifaceSchema,
			op:        `{ node { id __typename ... on A { a } } }`,
			subgraphs: ifaceSubgraphs,
			fields: plan.FieldConfigurations{
				{TypeName: "A", FieldName: "a", HasAuthorizationRule: true},
			},
		},
	}
}

// TestFieldInfoParity is the differential FieldInfo oracle described in the file doc.
func TestFieldInfoParity(t *testing.T) {
	for _, fx := range fieldInfoFixtures() {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			baseCfg := buildConfig(t, fx.subgraphs, fx.fields...)
			for _, perm := range permutations.Generate(baseCfg.DataSources) {
				cfg := baseCfg
				cfg.DataSources = perm.DataSources

				oldPlanner, err := plan.NewPlanner(cfg)
				if err != nil {
					t.Fatalf("old planner (order %v): %v", perm.Order, err)
				}
				newPlanner, err := planv2.NewPlanner(cfg)
				if err != nil {
					t.Fatalf("planv2 planner (order %v): %v", perm.Order, err)
				}

				oldOp, oldDef, oldReport := parseAndNormalize(t, fx.schema, fx.op)
				oldPlan := oldPlanner.Plan(oldOp, oldDef, "", oldReport)
				if oldReport.HasErrors() {
					t.Fatalf("old planner failed (order %v): %s", perm.Order, oldReport.Error())
				}
				newOp, newDef, newReport := parseAndNormalize(t, fx.schema, fx.op)
				newPlan := newPlanner.Plan(newOp, newDef, "", newReport)
				if newReport.HasErrors() {
					t.Fatalf("planv2 failed (order %v): %s", perm.Order, newReport.Error())
				}

				postprocess.NewProcessor().Process(oldPlan)
				postprocess.NewProcessor().Process(newPlan)

				oldSp := oldPlan.(*plan.SynchronousResponsePlan)
				newSp := newPlan.(*plan.SynchronousResponsePlan)

				// (1) The security surface: identical authorization coordinate sets.
				oldCoords := canonAuthCoordinates(oldSp.Response.Info.AuthorizationCoordinates)
				newCoords := canonAuthCoordinates(newSp.Response.Info.AuthorizationCoordinates)
				if oldCoords != newCoords {
					t.Fatalf("authorization coordinates diverge (order %v):\n old: %s\n new: %s",
						perm.Order, oldCoords, newCoords)
				}

				// (2) FieldInfo attributes at every matched response position.
				if div := diffFieldInfo("", oldSp.Response.Data, newSp.Response.Data); div != "" {
					t.Fatalf("FieldInfo diverges (order %v): %s", perm.Order, div)
				}
			}
		})
	}
}

// canonAuthCoordinates renders a coordinate list canonically: "dsID:Type.field" sorted, joined.
func canonAuthCoordinates(coords []resolve.AuthorizationCoordinate) string {
	parts := make([]string, 0, len(coords))
	for _, c := range coords {
		parts = append(parts, fmt.Sprintf("%s:%s.%s", c.DataSourceID, c.Coordinate.TypeName, c.Coordinate.FieldName))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// diffFieldInfo walks both response trees by (client key, gate) -- the shape oracle's identity --
// and compares FieldInfo attributes on every position matched in both. Returns "" on parity or a
// description of the first divergence.
func diffFieldInfo(path string, oldObj, newObj *resolve.Object) string {
	oldShapes := fieldInfoShapes(oldObj)
	newShapes := fieldInfoShapes(newObj)
	keys := make([]string, 0, len(oldShapes))
	for k := range oldShapes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		of := oldShapes[k]
		nf, ok := newShapes[k]
		if !ok {
			continue // presence divergences belong to the shape oracle
		}
		p := join(path, string(of.Name))
		oi, ni := of.Info, nf.Info
		if oi == nil && ni == nil {
			// both Info-free (e.g. static __typename on the root) -- fine
		} else if oi == nil || ni == nil {
			return fmt.Sprintf("%s: FieldInfo present on one side only (old=%v new=%v)", p, oi != nil, ni != nil)
		} else {
			if oi.Name != ni.Name {
				return fmt.Sprintf("%s: Name differs: old=%q new=%q", p, oi.Name, ni.Name)
			}
			if oi.ExactParentTypeName != ni.ExactParentTypeName {
				return fmt.Sprintf("%s: ExactParentTypeName differs: old=%q new=%q", p, oi.ExactParentTypeName, ni.ExactParentTypeName)
			}
			if canonStrings(oi.ParentTypeNames) != canonStrings(ni.ParentTypeNames) {
				return fmt.Sprintf("%s: ParentTypeNames differ: old=%v new=%v", p, oi.ParentTypeNames, ni.ParentTypeNames)
			}
			if oi.NamedType != ni.NamedType {
				return fmt.Sprintf("%s: NamedType differs: old=%q new=%q", p, oi.NamedType, ni.NamedType)
			}
			if oi.HasAuthorizationRule != ni.HasAuthorizationRule {
				return fmt.Sprintf("%s: HasAuthorizationRule differs: old=%v new=%v", p, oi.HasAuthorizationRule, ni.HasAuthorizationRule)
			}
			// Source: planv2 attributes the ONE chosen route; v1 lists every planner that touched
			// the field. Non-empty subset is the parity contract (documented in the file doc).
			if len(ni.Source.IDs) == 0 {
				return fmt.Sprintf("%s: planv2 Source.IDs empty (v1: %v)", p, oi.Source.IDs)
			}
			for _, id := range ni.Source.IDs {
				if !containsString(oi.Source.IDs, id) {
					return fmt.Sprintf("%s: planv2 Source.IDs %v not a subset of v1 %v", p, ni.Source.IDs, oi.Source.IDs)
				}
			}
		}
		// Recurse into matched composite positions.
		oldChild := childObject(of.Value)
		newChild := childObject(nf.Value)
		if oldChild != nil && newChild != nil {
			if div := diffFieldInfo(join(path, string(of.Name)), oldChild, newChild); div != "" {
				return div
			}
		}
	}
	return ""
}

func fieldInfoShapes(obj *resolve.Object) map[string]*resolve.Field {
	out := map[string]*resolve.Field{}
	if obj == nil {
		return out
	}
	for _, f := range obj.Fields {
		out[string(f.Name)+gateString(f.OnTypeNames)] = f
	}
	return out
}

func childObject(v resolve.Node) *resolve.Object {
	for {
		arr, ok := v.(*resolve.Array)
		if !ok {
			break
		}
		v = arr.Item
	}
	obj, _ := v.(*resolve.Object)
	return obj
}

func canonStrings(s []string) string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return strings.Join(c, "|")
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
