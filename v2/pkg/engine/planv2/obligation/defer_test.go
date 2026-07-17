package obligation

import (
	"reflect"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
)

// deferUserSchema is the composed client schema of hypergraph/testdata DeferUserSingleConfig.
const deferUserSchema = `
schema { query: Query mutation: Mutation }
type Query { user: User }
type Mutation { updateUser: User }
type User { id: ID! name: String! title: String! description: String! info: Info }
type Info { email: String phone: String }
`

func buildDeferUserH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	h, err := hypergraph.Build(hgtestdata.DeferUserSingleConfig(), hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation"},
	})
	if err != nil {
		t.Fatalf("build hypergraph: %v", err)
	}
	return h
}

// parseOpDefer normalizes with the engine's defer expansion enabled -- the D11.13 input contract
// (@defer fragments rewritten into per-field @__defer_internal stamps).
func parseOpDefer(t *testing.T, schema, query string) (*ast.Document, *ast.Document) {
	t.Helper()
	return parseOpWith(t, schema, query, astnormalization.NewWithOpts(
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithEnableDefer(),
	))
}

// TestBuild_DeferScopeOnObligations pins the D3 defer-scope carry: each field obligation records
// the @__defer_internal id it is stamped with (0 when unstamped), and nested fields inside a
// deferred fragment carry the innermost enclosing fragment's id (the normalization stamps them
// recursively).
func TestBuild_DeferScopeOnObligations(t *testing.T) {
	h := buildDeferUserH(t)
	op, def := parseOpDefer(t, deferUserSchema,
		`query User { user { name ... @defer { title info { email } } } }`)
	tree, err := Build(op, def, "User", h)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{ // Type.field -> defer scope
		"Query.user": 0,
		"User.name":  0,
		"User.title": 1,
		"User.info":  1,
		"Info.email": 1,
	}
	seen := map[string]int{}
	for _, ob := range tree.Obligations() {
		if ob.Kind != Field {
			continue
		}
		seen[ob.Type+"."+ob.Field] = ob.DeferID
	}
	if !reflect.DeepEqual(want, seen) {
		t.Fatalf("defer scopes wrong:\n want %v\n got  %v", want, seen)
	}
}

// TestBuild_DeferDescriptors pins the descriptor record: one per distinct id, with the parent id
// from the directive and the mount path being the enclosing selection set's field response keys.
func TestBuild_DeferDescriptors(t *testing.T) {
	h := buildDeferUserH(t)
	op, def := parseOpDefer(t, deferUserSchema,
		`query User { user { name ... @defer(label: "t") { title ... @defer { description } } } }`)
	tree, err := Build(op, def, "User", h)
	if err != nil {
		t.Fatal(err)
	}
	defers := tree.Defers()
	if len(defers) != 2 {
		t.Fatalf("want 2 defer records, got %+v", defers)
	}
	d1, d2 := defers[1], defers[2]
	if d1.ID != 1 || d1.ParentID != 0 || d1.Label != "t" || !reflect.DeepEqual(d1.Path, []string{"user"}) {
		t.Fatalf("descriptor 1 wrong: %+v", d1)
	}
	if d2.ID != 2 || d2.ParentID != 1 || d2.Label != "" || !reflect.DeepEqual(d2.Path, []string{"user"}) {
		t.Fatalf("descriptor 2 wrong: %+v", d2)
	}
}

// TestBuild_DeferIgnoredOnMutation pins the FS-DEF-7/design-parity gate: only query operations
// record defer scopes and descriptors; a mutation carrying @__defer_internal stamps builds a
// scope-0 tree with no defer records (the plan flattens, conforming per FS-DEF-1).
func TestBuild_DeferIgnoredOnMutation(t *testing.T) {
	h := buildDeferUserH(t)
	op, def := parseOpDefer(t, deferUserSchema,
		`mutation Update { updateUser { name ... @defer { title } } }`)
	tree, err := Build(op, def, "Update", h)
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.Defers(); len(got) != 0 {
		t.Fatalf("mutation must record no defers, got %+v", got)
	}
	for _, ob := range tree.Obligations() {
		if ob.DeferID != 0 {
			t.Fatalf("mutation obligation %s.%s must have scope 0, got %d", ob.Type, ob.Field, ob.DeferID)
		}
	}
}
