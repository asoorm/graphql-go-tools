package planv2

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// The composed client schemas of the defer fixtures (hypergraph/testdata/defer_user.go).
const deferUserSingleSupergraph = `
schema { query: Query mutation: Mutation }
type Query { user: User }
type Mutation { updateUser: User }
type User { id: ID! name: String! title: String! description: String! info: Info }
type Info { email: String phone: String }
`

const deferUserEntitySupergraph = `
schema { query: Query }
type Query { user: User }
type User { id: ID! title: String! firstName: String! lastName: String! }
`

// parseAndNormalizeDefer is parseAndNormalize with the engine's defer normalization enabled
// (WithEnableDefer) -- the exact input contract of FORMAL_SPEC D11.13: @defer fragments rewritten
// into per-field @__defer_internal stamps before the planner runs.
func parseAndNormalizeDefer(t *testing.T, schema, operation string) (*ast.Document, *ast.Document, *operationreport.Report) {
	t.Helper()
	def := unsafeparser.ParseGraphqlDocumentString(schema)
	if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
		t.Fatalf("merge base schema: %v", err)
	}
	op := unsafeparser.ParseGraphqlDocumentString(operation)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
		astnormalization.WithEnableDefer(),
	).NormalizeOperation(&op, &def, report)
	if report.HasErrors() {
		t.Fatalf("normalize: %s", report.Error())
	}
	return &op, &def, report
}

// deferFetch is the flattened view of one raw fetch this file asserts on.
type deferFetch struct {
	deferID   int
	entity    bool
	query     string
	dependsOn []int
	fetchID   int
}

func rawDeferFetches(t *testing.T, resp *resolve.GraphQLResponse) []deferFetch {
	t.Helper()
	out := make([]deferFetch, 0, len(resp.RawFetches))
	for _, item := range resp.RawFetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok {
			t.Fatalf("raw fetch is not a SingleFetch: %T", item.Fetch)
		}
		q := ""
		if sf.QueryPlan != nil {
			q = sf.QueryPlan.Query
		}
		out = append(out, deferFetch{
			deferID:   sf.DeferID,
			entity:    sf.RequiresEntityFetch || sf.RequiresEntityBatchFetch,
			query:     q,
			dependsOn: sf.DependsOnFetchIDs,
			fetchID:   sf.FetchID,
		})
	}
	return out
}

// fieldByName walks an object's fields for the client key.
func fieldByName(t *testing.T, obj *resolve.Object, name string) *resolve.Field {
	t.Helper()
	for _, f := range obj.Fields {
		if string(f.Name) == name {
			return f
		}
	}
	t.Fatalf("field %q not found in response object", name)
	return nil
}

// TestPlanner_DeferRoot is the D11.13 root re-walk anchor class (v1: "defer on root query node"):
// `user { name ... @defer { title } }` on one subgraph must produce a DeferResponsePlan whose
// primary fetch selects name (not title) and whose scope-1 variant re-walks the user chain
// selecting title (not name), with the descriptor mounted at ["user"] and title stamped
// DeferField{1} in the (full, I4) response tree.
func TestPlanner_DeferRoot(t *testing.T) {
	p := mustPlanner(t, hgtestdata.DeferUserSingleConfig())
	op, def, report := parseAndNormalizeDefer(t, deferUserSingleSupergraph,
		`query User { user { name ... @defer { title } } }`)

	result := p.Plan(op, def, "User", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	dp, ok := result.(*plan.DeferResponsePlan)
	if !ok {
		t.Fatalf("want *plan.DeferResponsePlan, got %T", result)
	}

	// Descriptors: exactly one, id 1, top-level, mounted at ["user"].
	if len(dp.Response.DeferDescriptors) != 1 {
		t.Fatalf("want 1 defer descriptor, got %d: %+v", len(dp.Response.DeferDescriptors), dp.Response.DeferDescriptors)
	}
	d := dp.Response.DeferDescriptors[1]
	if d.ID != 1 || d.ParentID != 0 || d.Label != "" || len(d.Path) != 1 || d.Path[0] != "user" {
		t.Fatalf("descriptor 1 wrong: %+v", d)
	}

	// Fetch partition (FS-DEF-2): primary fetch has name, not title; deferred fetch re-walks user
	// and has title, not name.
	fetches := rawDeferFetches(t, dp.Response.Response)
	if len(fetches) != 2 {
		t.Fatalf("want 2 fetches (primary + scope-1), got %d: %+v", len(fetches), fetches)
	}
	var primary, deferred *deferFetch
	for i := range fetches {
		switch fetches[i].deferID {
		case 0:
			primary = &fetches[i]
		case 1:
			deferred = &fetches[i]
		}
	}
	if primary == nil || deferred == nil {
		t.Fatalf("want one scope-0 and one scope-1 fetch, got %+v", fetches)
	}
	if !strings.Contains(primary.query, "name") || strings.Contains(primary.query, "title") {
		t.Fatalf("primary fetch must select name and not title, got %q", primary.query)
	}
	if !strings.Contains(deferred.query, "user") || !strings.Contains(deferred.query, "title") || strings.Contains(deferred.query, "name") {
		t.Fatalf("deferred fetch must re-walk user and select title only, got %q", deferred.query)
	}
	if len(deferred.dependsOn) != 0 {
		t.Fatalf("deferred root re-walk must not depend on other fetches, got %v", deferred.dependsOn)
	}

	// Response tree (I4): full client shape; title stamped, name unstamped.
	user := fieldByName(t, dp.Response.Response.Data, "user")
	userObj := user.Value.(*resolve.Object)
	if f := fieldByName(t, userObj, "name"); f.Defer != nil {
		t.Fatalf("name must not carry a defer stamp, got %+v", f.Defer)
	}
	title := fieldByName(t, userObj, "title")
	if title.Defer == nil || title.Defer.DeferID != 1 {
		t.Fatalf("title must carry DeferField{DeferID: 1}, got %+v", title.Defer)
	}

	// The plan must survive the untouched postprocess defer pipeline: partitioning by DeferID,
	// per-tree organization, and the DeferTree build off the descriptors.
	postprocess.NewProcessor().Process(dp)
	if dp.Response.DeferTree == nil {
		t.Fatal("postprocess must build the DeferTree")
	}
	if dp.Response.Defers != nil {
		t.Fatal("postprocess must clear the intermediate Defers slice")
	}
}

// TestPlanner_DeferNested is the nested-defer class (v1: "nested defer on single subgraph"):
// descriptors 1 (parent 0) and 2 (parent 1), one scope variant per id, and a Sequence defer tree
// (child delivered after parent).
func TestPlanner_DeferNested(t *testing.T) {
	p := mustPlanner(t, hgtestdata.DeferUserSingleConfig())
	op, def, report := parseAndNormalizeDefer(t, deferUserSingleSupergraph,
		`query User { user { name ... @defer { title ... @defer { description } } } }`)

	result := p.Plan(op, def, "User", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	dp, ok := result.(*plan.DeferResponsePlan)
	if !ok {
		t.Fatalf("want *plan.DeferResponsePlan, got %T", result)
	}
	if len(dp.Response.DeferDescriptors) != 2 {
		t.Fatalf("want 2 descriptors, got %+v", dp.Response.DeferDescriptors)
	}
	d1, d2 := dp.Response.DeferDescriptors[1], dp.Response.DeferDescriptors[2]
	if d1.ParentID != 0 || d2.ParentID != 1 {
		t.Fatalf("want parent chain 0<-1<-2, got d1=%+v d2=%+v", d1, d2)
	}
	if len(d1.Path) != 1 || d1.Path[0] != "user" || len(d2.Path) != 1 || d2.Path[0] != "user" {
		t.Fatalf("both descriptors mount at [user], got d1=%v d2=%v", d1.Path, d2.Path)
	}

	fetches := rawDeferFetches(t, dp.Response.Response)
	byScope := map[int]deferFetch{}
	for _, f := range fetches {
		byScope[f.deferID] = f
	}
	if len(fetches) != 3 || len(byScope) != 3 {
		t.Fatalf("want one fetch per scope {0,1,2}, got %+v", fetches)
	}
	if q := byScope[1].query; !strings.Contains(q, "title") || strings.Contains(q, "description") {
		t.Fatalf("scope-1 fetch must select title only, got %q", q)
	}
	if q := byScope[2].query; !strings.Contains(q, "description") || strings.Contains(q, "title") {
		t.Fatalf("scope-2 fetch must select description only, got %q", q)
	}

	postprocess.NewProcessor().Process(dp)
	tree := dp.Response.DeferTree
	if tree == nil || tree.Kind != resolve.DeferTreeNodeKindSequence {
		t.Fatalf("nested defers must build a Sequence defer tree, got %+v", tree)
	}
}

// TestPlanner_DeferEntityJump is the entity re-entry anchor class (v1: "on entity from other
// subgraph"): `user { title ... @defer { lastName } }`. The deferred field re-enters through the
// User entity jump; the @key rides the PRIMARY root fetch (FS-DEF-6's worked example -- the initial
// set grows by exactly `__typename id`), the deferred _entities fetch carries DeferID 1 and cites
// the primary root fetch as its dependency, and no scope-0 entity fetch exists (nothing
// non-deferred needs subgraph second).
func TestPlanner_DeferEntityJump(t *testing.T) {
	p := mustPlanner(t, hgtestdata.DeferUserEntityConfig())
	op, def, report := parseAndNormalizeDefer(t, deferUserEntitySupergraph,
		`query User { user { title ... @defer { lastName } } }`)

	result := p.Plan(op, def, "User", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	dp, ok := result.(*plan.DeferResponsePlan)
	if !ok {
		t.Fatalf("want *plan.DeferResponsePlan, got %T", result)
	}

	fetches := rawDeferFetches(t, dp.Response.Response)
	if len(fetches) != 2 {
		t.Fatalf("want 2 fetches (primary root + deferred entity), got %+v", fetches)
	}
	var primary, deferred *deferFetch
	for i := range fetches {
		switch {
		case fetches[i].deferID == 0 && !fetches[i].entity:
			primary = &fetches[i]
		case fetches[i].deferID == 1 && fetches[i].entity:
			deferred = &fetches[i]
		}
	}
	if primary == nil || deferred == nil {
		t.Fatalf("want a scope-0 root fetch and a scope-1 entity fetch, got %+v", fetches)
	}
	// Keys in the parent (primary) scope: the root fetch selects title plus the key set.
	for _, want := range []string{"title", "id", "__typename"} {
		if !strings.Contains(primary.query, want) {
			t.Fatalf("primary root fetch must select %q (key in parent scope), got %q", want, primary.query)
		}
	}
	if strings.Contains(primary.query, "lastName") {
		t.Fatalf("primary fetch must not select the deferred field, got %q", primary.query)
	}
	if !strings.Contains(deferred.query, "_entities") || !strings.Contains(deferred.query, "lastName") {
		t.Fatalf("deferred fetch must be an _entities fetch selecting lastName, got %q", deferred.query)
	}
	// Cross-scope dependency metadata: the deferred entity fetch cites the primary root fetch.
	if len(deferred.dependsOn) != 1 || deferred.dependsOn[0] != primary.fetchID {
		t.Fatalf("deferred entity fetch must depend on the primary root fetch %d, got %v",
			primary.fetchID, deferred.dependsOn)
	}

	postprocess.NewProcessor().Process(dp)
	if dp.Response.DeferTree == nil {
		t.Fatal("postprocess must build the DeferTree")
	}
}

// TestPlanner_DeferAllDeferred is the placeholder class: when every child of a selection set is
// deferred, the primary fetch must still materialize the enclosing object (the normalization
// placeholder keeps the primary document non-empty), so the initial response renders `user` as {}.
func TestPlanner_DeferAllDeferred(t *testing.T) {
	p := mustPlanner(t, hgtestdata.DeferUserSingleConfig())
	op, def, report := parseAndNormalizeDefer(t, deferUserSingleSupergraph,
		`query User { user { ... @defer { title } } }`)

	result := p.Plan(op, def, "User", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	dp, ok := result.(*plan.DeferResponsePlan)
	if !ok {
		t.Fatalf("want *plan.DeferResponsePlan, got %T", result)
	}
	fetches := rawDeferFetches(t, dp.Response.Response)
	var primary *deferFetch
	for i := range fetches {
		if fetches[i].deferID == 0 {
			primary = &fetches[i]
		}
	}
	if primary == nil {
		t.Fatalf("want a primary fetch carrying the placeholder, got %+v", fetches)
	}
	if !strings.Contains(primary.query, "user") || !strings.Contains(primary.query, "__typename") {
		t.Fatalf("primary fetch must materialize user via the __typename placeholder, got %q", primary.query)
	}
	if strings.Contains(primary.query, "title") {
		t.Fatalf("primary fetch must not select the deferred field, got %q", primary.query)
	}
	// The engine-internal placeholder key must NOT surface in the client response shape.
	user := fieldByName(t, dp.Response.Response.Data, "user")
	for _, f := range user.Value.(*resolve.Object).Fields {
		if string(f.Name) == "__internal_typename" {
			t.Fatal("__internal_typename must be excluded from the client response shape")
		}
	}
}

// TestPlanner_DeferOnMutation pins FS-DEF-1/design parity: @defer inside a mutation is not
// honored -- the plan is the flattened synchronous plan (v1: only queries produce a
// DeferResponsePlan; mutations execute serially by design).
func TestPlanner_DeferOnMutation(t *testing.T) {
	p := mustPlanner(t, hgtestdata.DeferUserSingleConfig())
	op, def, report := parseAndNormalizeDefer(t, deferUserSingleSupergraph,
		`mutation Update { updateUser { name ... @defer { title } } }`)

	result := p.Plan(op, def, "Update", report)
	if report.HasErrors() {
		t.Fatalf("plan reported errors: %s", report.Error())
	}
	sp, ok := result.(*plan.SynchronousResponsePlan)
	if !ok {
		t.Fatalf("mutation with @defer must flatten to *plan.SynchronousResponsePlan, got %T", result)
	}
	updateUser := fieldByName(t, sp.Response.Data, "updateUser")
	for _, f := range updateUser.Value.(*resolve.Object).Fields {
		if f.Defer != nil {
			t.Fatalf("no field of a mutation plan may carry a defer stamp, got %s: %+v", f.Name, f.Defer)
		}
	}
	for _, f := range rawDeferFetches(t, sp.Response) {
		if f.deferID != 0 {
			t.Fatalf("no mutation fetch may carry a DeferID, got %+v", f)
		}
	}
}
