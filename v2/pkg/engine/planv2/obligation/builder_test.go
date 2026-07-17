package obligation

import (
	"reflect"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// partialUnionSchema is the composed (client-facing) schema for FORMAL_SPEC.md Section 7.1 -- the union
// of what subgraphs A and B individually declare (see hypergraph/testdata/partial_union.go's
// PartialUnionConfig, whose per-subgraph schemas are the D6 partial-member inputs H is built
// from). Normalizing/walking the client operation needs the full composed type, independent of
// which subgraph knows which member.
const partialUnionSchema = `
schema { query: Query }
type Query { wrapper: Wrapper }
type Wrapper { id: ID! action: Action }
union Action = Common | OnlyA | OnlyB
type Common { c: String }
type OnlyA { a: String }
type OnlyB { b: String }
`

const partialUnionOperation = `{ wrapper { action {
	__typename ... on Common { c } ... on OnlyA { a } ... on OnlyB { b } } } }`

func TestBuild_PartialUnionObligationTree(t *testing.T) {
	h := buildPartialUnionH(t) // reuses hypergraph testdata
	op, def := parseOp(t, partialUnionSchema, partialUnionOperation)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}
	// D3: refinement obligations o2/o3/o4 for Common/OnlyA/OnlyB under Action.
	refines := refinementConcretes(tree)
	assertEqualStrings(t, refines, []string{"Common", "OnlyA", "OnlyB"})
	// Goal set G(O) is exactly the real (non-__typename) refinement leaves c/a/b: __typename is
	// satisfied at lowering with no hypergraph node of its own, so SETTLE never sees it
	// (tla/PlannerSearch.tla; R3) and it must not appear as a goal.
	assertEqualStrings(t, goalLabels(tree), []string{"Common.c", "OnlyA.a", "OnlyB.b"})
	// cand(<Common.c>) has one field node per candidate subgraph.
	g := goalFor(t, tree, "Common", "c")
	if len(tree.Cand(g)) == 0 {
		t.Fatal("cand(Common.c) must be non-empty")
	}
}

// TestBuild_ObligationTreeShape checks the D3 tree structure against Section 7.1's worked example
// (o0 Query.wrapper -> o1 Wrapper.action -> {refine Common, refine OnlyA, refine OnlyB}, each
// parenting its single selected field) rather than only the goal-set summary above.
func TestBuild_ObligationTreeShape(t *testing.T) {
	h := buildPartialUnionH(t)
	op, def := parseOp(t, partialUnionSchema, partialUnionOperation)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}

	obs := tree.Obligations()

	o0 := obs[0] // <Query.wrapper>, a top-level obligation: Parent is the reserved NoParent sentinel
	if o0.Kind != Field || o0.Type != "Query" || o0.Field != "wrapper" || o0.Parent != NoParent {
		t.Fatalf("o0 mismatch: %+v", o0)
	}

	o1 := findField(t, obs, "Wrapper", "action")
	if o1.Parent != o0.ID {
		t.Fatalf("o1 parent = %d, want %d (o0)", o1.Parent, o0.ID)
	}

	typenameOb := findByKindField(t, obs, Typename, "Action", typenameField)
	if typenameOb.Parent != o1.ID {
		t.Fatalf("__typename parent = %d, want %d (o1)", typenameOb.Parent, o1.ID)
	}

	for _, tc := range []struct{ concrete, field string }{
		{"Common", "c"}, {"OnlyA", "a"}, {"OnlyB", "b"},
	} {
		refine := findRefine(t, obs, "Action", tc.concrete)
		if refine.Parent != o1.ID {
			t.Fatalf("refine(%s) parent = %d, want %d (o1)", tc.concrete, refine.Parent, o1.ID)
		}
		leaf := findField(t, obs, tc.concrete, tc.field)
		if leaf.Parent != refine.ID {
			t.Fatalf("leaf(%s.%s) parent = %d, want %d (refine %s)", tc.concrete, tc.field, leaf.Parent, refine.ID, tc.concrete)
		}
	}
}

// TestBuild_NamedFragmentEquivalence: the Section 7.1 operation written with a named fragment
// (`...CommonFrag` + `fragment CommonFrag on Common { c }`) must produce EXACTLY the same
// obligation tree and goal set as the inline-fragment version. The named-fragment document is
// deliberately normalized WITHOUT WithRemoveFragmentDefinitions (plain-NormalizeOperation
// semantics: the spread is inlined but the FragmentDefinition RootNode stays behind):
// Walker.Walk visits ALL RootNodes, so without Build's EnterFragmentDefinition SkipNode guard
// the leftover fragment body would be walked a second time -- after the obligation stack has
// drained -- emitting a spurious root-parented Common.c obligation and goal. This test pins the
// guard.
func TestBuild_NamedFragmentEquivalence(t *testing.T) {
	h := buildPartialUnionH(t)

	opInline, defInline := parseOp(t, partialUnionSchema, partialUnionOperation)
	want, err := Build(opInline, defInline, "", h)
	if err != nil {
		t.Fatal(err)
	}

	const namedFragmentOperation = `{ wrapper { action {
		__typename ...CommonFrag ... on OnlyA { a } ... on OnlyB { b } } } }
	fragment CommonFrag on Common { c }`
	opNamed, defNamed := parseOpKeepFragments(t, partialUnionSchema, namedFragmentOperation)
	got, err := Build(opNamed, defNamed, "", h)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got.Obligations(), want.Obligations()) {
		t.Fatalf("obligation tree mismatch:\n got %+v\nwant %+v", got.Obligations(), want.Obligations())
	}
	assertEqualStrings(t, goalLabels(got), goalLabels(want))
	assertEqualStrings(t, goalLabels(got), []string{"Common.c", "OnlyA.a", "OnlyB.b"})
}

// TestBuild_TypenameIsNotAGoal: __typename stays in the obligation tree (Kind Typename, for
// lowering's response-shape bookkeeping per D11.3/I4) but is excluded from Goals() -- SETTLE
// never sees it (tla/PlannerSearch.tla; R3 assigns __typename gating to Task 8 lowering).
func TestBuild_TypenameIsNotAGoal(t *testing.T) {
	h := buildPartialUnionH(t)
	op, def := parseOp(t, partialUnionSchema, partialUnionOperation)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}

	// Present in the tree, with the dedicated kind and its response key preserved.
	tn := findByKindField(t, tree.Obligations(), Typename, "Action", typenameField)
	if tn.RespKey != typenameField {
		t.Fatalf("__typename RespKey = %q, want %q", tn.RespKey, typenameField)
	}

	// Never a goal.
	for _, g := range tree.Goals() {
		if ob := tree.Ob(g); ob.Kind == Typename || ob.Field == typenameField {
			t.Fatalf("__typename must not be a goal, but goal %d maps to %+v", g, ob)
		}
	}
}

// --- helpers ---------------------------------------------------------------------------

// TestBuild_TypenameTerminalGoal_D3Prime pins the D3p amendment: a Field obligation whose entire
// subtree is __typename meta-fields (no resolvable field beneath) is a RESOLUTION LEAF and must be a
// goal, so search covers it and lowering can emit `field { __typename }`. Under the pre-D3p plain-leaf
// rule such a composite had children and was never a goal, so the whole __typename-only subtree
// dropped from every fetch (assertion-6 leaf-coverage gap). candFor resolves it to the field's own
// field-resolution nodes exactly as for a leaf field.
func TestBuild_TypenameTerminalGoal_D3Prime(t *testing.T) {
	h := buildPartialUnionH(t)

	// action's only child is __typename -> action is a resolution leaf goal; wrapper has a field
	// descendant (action) so it is NOT a goal (its coverage rides on action's).
	t.Run("composite with only __typename child becomes a goal", func(t *testing.T) {
		op, def := parseOp(t, partialUnionSchema, `{ wrapper { action { __typename } } }`)
		tree, err := Build(op, def, "", h)
		if err != nil {
			t.Fatal(err)
		}
		assertEqualStrings(t, goalLabels(tree), []string{"Wrapper.action"})
		g := goalFor(t, tree, "Wrapper", "action")
		if len(tree.Cand(g)) == 0 {
			t.Fatal("cand(Wrapper.action) must be non-empty (the field's own resolution nodes)")
		}
	})

	// The root field itself is typename-terminal -> the root field is the goal.
	t.Run("root field with only __typename child becomes a goal", func(t *testing.T) {
		op, def := parseOp(t, partialUnionSchema, `{ wrapper { __typename } }`)
		tree, err := Build(op, def, "", h)
		if err != nil {
			t.Fatal(err)
		}
		assertEqualStrings(t, goalLabels(tree), []string{"Query.wrapper"})
	})

	// A __typename-only inline refinement beneath a union is still typename-terminal: no member field
	// is resolvable, so the enclosing field (action) is the resolution-leaf goal, NOT the refinement.
	t.Run("refinement with only __typename is typename-terminal at the enclosing field", func(t *testing.T) {
		op, def := parseOp(t, partialUnionSchema, `{ wrapper { action { ... on Common { __typename } } } }`)
		tree, err := Build(op, def, "", h)
		if err != nil {
			t.Fatal(err)
		}
		assertEqualStrings(t, goalLabels(tree), []string{"Wrapper.action"})
	})

	// A refinement with a REAL field beneath keeps the pre-D3p behavior exactly: the member field is
	// the goal, the enclosing composite is not (regression guard for the additive-only claim).
	t.Run("refinement with a real field keeps the member-field goal", func(t *testing.T) {
		op, def := parseOp(t, partialUnionSchema, `{ wrapper { action { ... on Common { c } } } }`)
		tree, err := Build(op, def, "", h)
		if err != nil {
			t.Fatal(err)
		}
		assertEqualStrings(t, goalLabels(tree), []string{"Common.c"})
	})
}

func buildPartialUnionH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	h, err := hypergraph.Build(hgtestdata.PartialUnionConfig(), hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query"},
	})
	if err != nil {
		t.Fatalf("build hypergraph: %v", err)
	}
	return h
}

// parseOp parses and normalizes with the repo's safe precedent (datasourcetesting.go): fragment
// spreads inlined AND fragment definitions removed.
func parseOp(t *testing.T, schema, query string) (*ast.Document, *ast.Document) {
	t.Helper()
	return parseOpWith(t, schema, query, astnormalization.NewWithOpts(
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
	))
}

// parseOpKeepFragments normalizes with plain-NormalizeOperation semantics: spreads are inlined
// but the FragmentDefinition RootNodes are LEFT in the document -- the input shape Build's
// EnterFragmentDefinition guard exists for (see TestBuild_NamedFragmentEquivalence).
func parseOpKeepFragments(t *testing.T, schema, query string) (*ast.Document, *ast.Document) {
	t.Helper()
	return parseOpWith(t, schema, query, astnormalization.NewWithOpts(
		astnormalization.WithInlineFragmentSpreads(),
	))
}

func parseOpWith(t *testing.T, schema, query string, norm *astnormalization.OperationNormalizer) (*ast.Document, *ast.Document) {
	t.Helper()
	def := unsafeparser.ParseGraphqlDocumentStringWithBaseSchema(schema)
	op := unsafeparser.ParseGraphqlDocumentString(query)
	report := &operationreport.Report{}
	norm.NormalizeOperation(&op, &def, report)
	if report.HasErrors() {
		t.Fatalf("normalize operation: %s", report.Error())
	}
	return &op, &def
}

func refinementConcretes(tree *Tree) []string {
	var out []string
	for _, ob := range tree.Obligations() {
		if ob.Kind == Refine {
			out = append(out, ob.Concrete)
		}
	}
	return out
}

// goalLabels renders G(O) as "Type.field" strings in goal order, for exact goal-set assertions.
func goalLabels(tree *Tree) []string {
	var out []string
	for _, g := range tree.Goals() {
		ob := tree.Ob(g)
		out = append(out, ob.Type+"."+ob.Field)
	}
	return out
}

func goalFor(t *testing.T, tree *Tree, typeName, field string) GoalID {
	t.Helper()
	for _, g := range tree.Goals() {
		ob := tree.Ob(g)
		if ob.Kind == Field && ob.Type == typeName && ob.Field == field {
			return g
		}
	}
	t.Fatalf("no goal for %s.%s", typeName, field)
	return 0
}

func findField(t *testing.T, obs []Obligation, typeName, field string) Obligation {
	t.Helper()
	return findByKindField(t, obs, Field, typeName, field)
}

func findByKindField(t *testing.T, obs []Obligation, kind Kind, typeName, field string) Obligation {
	t.Helper()
	for _, ob := range obs {
		if ob.Kind == kind && ob.Type == typeName && ob.Field == field {
			return ob
		}
	}
	t.Fatalf("no kind-%d obligation for %s.%s", kind, typeName, field)
	return Obligation{}
}

func findRefine(t *testing.T, obs []Obligation, typeName, concrete string) Obligation {
	t.Helper()
	for _, ob := range obs {
		if ob.Kind == Refine && ob.Type == typeName && ob.Concrete == concrete {
			return ob
		}
	}
	t.Fatalf("no refine obligation for %s |> %s", typeName, concrete)
	return Obligation{}
}

func assertEqualStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("mismatch at %d: got %v want %v", i, got, want)
		}
	}
}
