package bench

// fixtures_test.go builds the M1 Task-13 benchmark corpus: a HAND-DUPLICATED subset of the
// differential harness's agreed fixtures (entity-jump, cross-subgraph-roots, requires-chain,
// multi-hop-entity -- all cases where BOTH v1 and planv2 plan) plus the partial-union fixture
// (planv2-only: v1 deadlocks at PLANNING time on it -- see differential.KnownDivergences and
// TestV1PartialUnionCorroboration in pkg/engine/planv2/differential), plus three larger SYNTHETIC
// cases sized for the M1 scaling story (deep nesting ~10 levels, wide selection ~50 fields, an
// 8-subgraph fan-out).
//
// Duplication note: the differential package's schema/op/subgraph literals and its buildConfig/
// dataSource helpers are unexported, so there is no import seam to reuse them from this package --
// this file re-declares the small subset this benchmark needs rather than exporting differential's
// internals purely for a second consumer. The literal text is copied verbatim from
// pkg/engine/planv2/differential/differential_test.go and fixtures_task11_test.go so the two corpora
// stay in sync by inspection.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// subgraph bundles one federation subgraph's SDL + capability metadata, mirroring
// differential.subgraph (unexported there, so re-declared here).
type subgraph struct {
	name string
	sdl  string
	meta *plan.DataSourceMetadata
}

// corpusCase is one benchmark fixture: a fixed plan.Configuration plus a fresh-parse closure (both
// planners mutate their operation/definition documents during normalization, so every timed
// iteration needs its own copy -- see planOld/planNew in bench_test.go). V1Plannable is false for
// fixtures where v1 cannot produce ANY plan (a planning-time error, not a divergence) -- those are
// benchmarked planv2-only and noted as such in BenchmarkPlanningOldVsNew.
type corpusCase struct {
	Name        string
	Schema      string
	Op          string
	Config      plan.Configuration
	V1Plannable bool
}

// parse re-parses and re-normalizes the case's (schema, op) pair, returning a fresh
// operation/definition pair every call -- the same pinned pipeline planv2.Planner.Plan documents
// (astnormalization with ExtractVariables/InlineFragmentSpreads/RemoveFragmentDefinitions/
// RemoveUnusedVariables) and the differential harness runs.
func (c corpusCase) parse(tb testing.TB) (*ast.Document, *ast.Document, string, *operationreport.Report) {
	tb.Helper()
	def := unsafeparser.ParseGraphqlDocumentString(c.Schema)
	if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
		tb.Fatalf("%s: merge base schema: %v", c.Name, err)
	}
	operation := unsafeparser.ParseGraphqlDocumentString(c.Op)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
	).NormalizeOperation(&operation, &def, report)
	if report.HasErrors() {
		tb.Fatalf("%s: normalize: %s", c.Name, report.Error())
	}
	return &operation, &def, "", report
}

// buildCase constructs a corpusCase's fixed Configuration from subgraph fixtures.
func buildCase(tb testing.TB, name, schema, op string, subgraphs []subgraph, v1Plannable bool) corpusCase {
	tb.Helper()
	ds := make([]plan.DataSource, 0, len(subgraphs))
	for _, sg := range subgraphs {
		ds = append(ds, dataSource(tb, sg))
	}
	return corpusCase{
		Name:   name,
		Schema: schema,
		Op:     op,
		Config: plan.Configuration{
			DataSources:                  ds,
			DisableResolveFieldPositions: true,
		},
		V1Plannable: v1Plannable,
	}
}

func dataSource(tb testing.TB, sg subgraph) plan.DataSource {
	tb.Helper()
	schemaCfg, err := graphql_datasource.NewSchemaConfiguration(sg.sdl, &graphql_datasource.FederationConfiguration{
		Enabled:    true,
		ServiceSDL: sg.sdl,
	})
	if err != nil {
		tb.Fatalf("schema configuration for %s: %v", sg.name, err)
	}
	cfg, err := graphql_datasource.NewConfiguration(graphql_datasource.ConfigurationInput{
		Fetch:               &graphql_datasource.FetchConfiguration{URL: "http://" + sg.name},
		SchemaConfiguration: schemaCfg,
	})
	if err != nil {
		tb.Fatalf("configuration for %s: %v", sg.name, err)
	}
	dsCfg, err := plan.NewDataSourceConfiguration[graphql_datasource.Configuration](
		sg.name, &graphql_datasource.Factory[graphql_datasource.Configuration]{}, sg.meta, cfg)
	if err != nil {
		tb.Fatalf("datasource configuration for %s: %v", sg.name, err)
	}
	return dsCfg
}

// differentialCorpus is the head-to-head subset: fixtures where planv2 must match v1's ability to
// plan at all. entity-jump/cross-subgraph-roots are the differential harness's base agreed cases;
// requires-chain/multi-hop-entity are two of its Task-11 fixtures (a @requires jump and a
// three-subgraph chained entity jump) chosen to exercise deeper fetch trees than the base cases.
// partial-union is v1-UNPLANNABLE (planning-time deadlock, corroborated in the differential
// package) and is benchmarked planv2-only, explicitly noted in BenchmarkPlanningOldVsNew.
func differentialCorpus(tb testing.TB) []corpusCase {
	tb.Helper()
	return []corpusCase{
		buildCase(tb, "entity-jump", entityJumpSchema, entityJumpOp, entityJumpSubgraphs(), true),
		buildCase(tb, "cross-subgraph-roots", crossRootsSchema, crossRootsOp, crossRootsSubgraphs(), true),
		buildCase(tb, "requires-chain", requiresChainSchema, requiresChainOp, requiresChainSubgraphs(), true),
		buildCase(tb, "multi-hop-entity", multiHopSchema, multiHopOp, multiHopSubgraphs(), true),
		buildCase(tb, "partial-union", partialUnionSchema, partialUnionOp, partialUnionSubgraphs(), false),
	}
}

// syntheticCorpus is the three larger, generated cases the M1 report's scaling story needs: deep
// nesting (~10 levels), wide selection (~50 sibling fields), and an 8-subgraph fan-out. All three
// are single-hop (no entity jumps) so both planners plan them without any M1 feature gaps.
func syntheticCorpus(tb testing.TB) []corpusCase {
	tb.Helper()
	const (
		nestDepth = 10
		wideCount = 50
		fanOut    = 8
	)
	return []corpusCase{
		buildCase(tb, "deep-nesting-10", deepNestingSchema(nestDepth), deepNestingOp(nestDepth), deepNestingSubgraphs(nestDepth), true),
		buildCase(tb, "wide-selection-50", wideSelectionSchema(wideCount), wideSelectionOp(wideCount), wideSelectionSubgraphs(wideCount), true),
		buildCase(tb, "multi-subgraph-fanout-8", fanOutSchema(fanOut), fanOutOp(fanOut), fanOutSubgraphs(fanOut), true),
	}
}

// --- differential-corpus fixtures (duplicated from pkg/engine/planv2/differential) -----------

const entityJumpSchema = `
schema { query: Query }
type Query { me: User }
type User { id: ID! name: String age: Int }
`
const entityJumpOp = `{ me { name age } }`

func entityJumpSubgraphs() []subgraph {
	return []subgraph{
		{
			name: "users",
			sdl: `
type Query { me: User }
type User @key(fields: "id") { id: ID! name: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"me"}},
					{TypeName: "User", FieldNames: []string{"id", "name"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "ages",
			sdl: `
type Query { _dummy: String }
type User @key(fields: "id") { id: ID! age: Int }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"_dummy"}},
					{TypeName: "User", FieldNames: []string{"id", "age"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
	}
}

const crossRootsSchema = `
schema { query: Query }
type Query { hello: String world: String }
`
const crossRootsOp = `{ hello world }`

func crossRootsSubgraphs() []subgraph {
	return []subgraph{
		{
			name: "a",
			sdl:  `type Query { hello: String }`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"hello"}}},
			},
		},
		{
			name: "b",
			sdl:  `type Query { world: String }`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"world"}}},
			},
		},
	}
}

const requiresChainSchema = `
schema { query: Query }
type Query { product: Product }
type Product { id: ID! dimensions: Dimensions shippingEstimate: Float }
type Dimensions { length: Float width: Float height: Float }
`
const requiresChainOp = `{ product { shippingEstimate } }`

func requiresChainSubgraphs() []subgraph {
	return []subgraph{
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
	}
}

const multiHopSchema = `
schema { query: Query }
type Query { me: User }
type User { id: ID! name: String hobby: String score: Int }
`
const multiHopOp = `{ me { name hobby score } }`

func multiHopSubgraphs() []subgraph {
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
	return []subgraph{
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
			sdl:  `type User @key(fields: "id") { id: ID! hobby: String }`,
			meta: userMeta([]string{"hobby"}, []string{"id"}, false),
		},
		{
			name: "c",
			sdl:  `type User @key(fields: "id") { id: ID! score: Int }`,
			meta: userMeta([]string{"score"}, []string{"id"}, false),
		},
	}
}

// partialUnionSchema/Op/Subgraphs match the spec's partial-union example (see
// differential.PartialUnionSchema/Op) -- v1
// deadlocks at planning time when both mutually-exclusive union members are co-selected
// (TestV1PartialUnionCorroboration), so this fixture is benchmarked planv2-only.
const partialUnionSchema = `
schema { query: Query }
type Query { wrapper: Wrapper }
type Wrapper { id: ID! action: Action }
union Action = Common | OnlyA | OnlyB
type Common { c: String }
type OnlyA { a: String }
type OnlyB { b: String }
`
const partialUnionOp = `{ wrapper { action { __typename ... on Common { c } ... on OnlyA { a } ... on OnlyB { b } } } }`

func partialUnionSubgraphs() []subgraph {
	return []subgraph{
		{
			name: "A",
			sdl: `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyA
type Common { c: String }
type OnlyA { a: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"wrapper"}},
					{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Common", FieldNames: []string{"c"}},
					{TypeName: "OnlyA", FieldNames: []string{"a"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Wrapper", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "B",
			sdl: `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyB
type Common { c: String }
type OnlyB { b: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"wrapper"}},
					{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Common", FieldNames: []string{"c"}},
					{TypeName: "OnlyB", FieldNames: []string{"b"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Wrapper", SelectionSet: "id"}},
				},
			},
		},
	}
}

// --- synthetic scaling fixtures ----------------------------------------------------------------

// deepNestingSchema builds `type Level1 { value1: String next: Level2 } ... type LevelN { valueN: String }`
// -- a single-subgraph n-level nesting chain.
func deepNestingSchema(n int) string {
	var b strings.Builder
	b.WriteString("schema { query: Query }\n")
	b.WriteString("type Query { level1: Level1 }\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "type Level%d { value%d: String", i, i)
		if i < n {
			fmt.Fprintf(&b, " next: Level%d", i+1)
		}
		b.WriteString(" }\n")
	}
	return b.String()
}

// deepNestingOp builds `{ level1 { value1 next { value2 next { ... valueN } ... } } }`.
func deepNestingOp(n int) string {
	var open strings.Builder
	open.WriteString("{ level1 { ")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&open, "value%d ", i)
		if i < n {
			open.WriteString("next { ")
		}
	}
	// n opens after the root ("level1 {" counts as 1, plus n-1 "next {"): n closing braces, plus
	// the outer root "{".
	for i := 0; i < n; i++ {
		open.WriteString("} ")
	}
	open.WriteString("}")
	return open.String()
}

func deepNestingSubgraphs(n int) []subgraph {
	rootFields := plan.TypeFields{{TypeName: "Query", FieldNames: []string{"level1"}}}
	child := make(plan.TypeFields, 0, n)
	for i := 1; i <= n; i++ {
		fields := []string{fmt.Sprintf("value%d", i)}
		if i < n {
			fields = append(fields, "next")
		}
		child = append(child, plan.TypeField{TypeName: fmt.Sprintf("Level%d", i), FieldNames: fields})
	}
	return []subgraph{
		{
			name: "levels",
			sdl:  deepNestingSchema(n),
			meta: &plan.DataSourceMetadata{RootNodes: rootFields, ChildNodes: child},
		},
	}
}

// wideSelectionSchema builds `type Wide { f0: String ... f(m-1): String }` -- a single object with m
// sibling scalar fields.
func wideSelectionSchema(m int) string {
	var b strings.Builder
	b.WriteString("schema { query: Query }\n")
	b.WriteString("type Query { wide: Wide }\n")
	b.WriteString("type Wide {")
	for i := 0; i < m; i++ {
		fmt.Fprintf(&b, " f%d: String", i)
	}
	b.WriteString(" }\n")
	return b.String()
}

func wideSelectionOp(m int) string {
	var b strings.Builder
	b.WriteString("{ wide {")
	for i := 0; i < m; i++ {
		fmt.Fprintf(&b, " f%d", i)
	}
	b.WriteString(" } }")
	return b.String()
}

func wideSelectionSubgraphs(m int) []subgraph {
	fields := make([]string, m)
	for i := 0; i < m; i++ {
		fields[i] = fmt.Sprintf("f%d", i)
	}
	return []subgraph{
		{
			name: "wide",
			sdl:  wideSelectionSchema(m),
			meta: &plan.DataSourceMetadata{
				RootNodes:  plan.TypeFields{{TypeName: "Query", FieldNames: []string{"wide"}}},
				ChildNodes: plan.TypeFields{{TypeName: "Wide", FieldNames: fields}},
			},
		},
	}
}

// fanOutSchema/Op/Subgraphs build k INDEPENDENT single-field subgraphs (`type Query { fieldI: String }`
// each), composed into `type Query { field0: String ... field(k-1): String }` -- a multi-subgraph
// fan-out with no entity jumps, exercising cross-subgraph root distribution at k-way scale.
func fanOutSchema(k int) string {
	var b strings.Builder
	b.WriteString("schema { query: Query }\ntype Query {")
	for i := 0; i < k; i++ {
		fmt.Fprintf(&b, " field%d: String", i)
	}
	b.WriteString(" }\n")
	return b.String()
}

func fanOutOp(k int) string {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < k; i++ {
		fmt.Fprintf(&b, " field%d", i)
	}
	b.WriteString(" }")
	return b.String()
}

func fanOutSubgraphs(k int) []subgraph {
	out := make([]subgraph, 0, k)
	for i := 0; i < k; i++ {
		field := fmt.Sprintf("field%d", i)
		out = append(out, subgraph{
			name: fmt.Sprintf("fanout%d", i),
			sdl:  fmt.Sprintf("type Query { %s: String }", field),
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{field}}},
			},
		})
	}
	return out
}
