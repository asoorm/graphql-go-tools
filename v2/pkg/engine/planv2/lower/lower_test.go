package lower

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// TestLowerEntityJumpTwoFetchesWithRepresentation is Section 7.2: the cover crosses one EntityJump, so
// lowering must emit two fetches (D11.1) -- root fetch to A for the key/@requires fields, child
// `_entities` fetch to B for shippingEstimate -- while the response shape stays { product { shippingEstimate } }.
// TestLowerPopulatesResponseInfo is the M1.5 executed-truth regression: the resolver dereferences
// GraphQLResponse.Info.OperationType unconditionally (resolve.go's ResolveGraphQLResponse), so a nil
// Info is a runtime nil-pointer panic -- not a plan-shape difference, which is why plan-level parity
// checks never caught it. Lower MUST populate Info with the operation's type.
func TestLowerPopulatesResponseInfo(t *testing.T) {
	h, o, res, op, def := planEntityJump(t)
	p, err := Lower(h, o, res, op, def)
	if err != nil {
		t.Fatal(err)
	}
	if p.Response.Info == nil {
		t.Fatal("Response.Info is nil -- resolver will nil-panic at execution (M1.5 executed-truth defect)")
	}
	if p.Response.Info.OperationType != ast.OperationTypeQuery {
		t.Fatalf("want OperationType Query, got %v", p.Response.Info.OperationType)
	}
}

func TestLowerEntityJumpTwoFetchesWithRepresentation(t *testing.T) {
	h, o, res, op, def := planEntityJump(t)
	p, err := Lower(h, o, res, op, def)
	if err != nil {
		t.Fatal(err)
	}
	// D11.1: two fetches -- A for {id organization{id} dimensions{...}}, B for _entities shippingEstimate.
	fetches := flattenFetches(p)
	if len(fetches) != 2 {
		t.Fatalf("want 2 fetches, got %d", len(fetches))
	}
	// One fetch must be an _entities (jump) fetch keyed by a representation; the other a root fetch.
	var jump, root int
	for _, f := range fetches {
		sf := f.Fetch.(*resolve.SingleFetch)
		if sf.RequiresEntityFetch {
			jump++
			if !strings.Contains(sf.Input, "_entities") || !strings.Contains(sf.Input, "shippingEstimate") {
				t.Fatalf("jump fetch document malformed: %q", sf.Input)
			}
			// D11.2 fetch attachment (M1.5, item 3): the `_entities` result merges into the `product`
			// response object, NOT the root data buffer. The fetch must carry that ResponsePath and a
			// matching FetchPath Object element -- else the loader (selectItemsForPath) attaches at root.
			if f.ResponsePath != "product" {
				t.Fatalf("jump fetch must attach at response path %q, got %q", "product", f.ResponsePath)
			}
			if len(f.FetchPath) != 1 || f.FetchPath[0].Kind != resolve.FetchItemPathElementKindObject ||
				len(f.FetchPath[0].Path) != 1 || f.FetchPath[0].Path[0] != "product" {
				t.Fatalf("jump fetch FetchPath must be one Object element [product], got %+v", f.FetchPath)
			}
			if len(sf.DependsOnFetchIDs) != 1 {
				t.Fatalf("jump fetch must depend on the root fetch (D11.2), deps=%v", sf.DependsOnFetchIDs)
			}
			// Section 7.2/M1: the representation is SPLIT -- the @key fragment carries the nested key
			// only (`{ __typename id organization { id } }`); the @requires selection rides as a
			// separate RepresentationKindRequires fragment, matching v1's rendering.
			if sf.QueryPlan == nil || len(sf.QueryPlan.DependsOnFields) != 2 {
				t.Fatalf("jump fetch must carry key + requires representations, got %+v", sf.QueryPlan)
			}
			key, req := sf.QueryPlan.DependsOnFields[0], sf.QueryPlan.DependsOnFields[1]
			if key.Kind != resolve.RepresentationKindKey || key.TypeName != "Product" {
				t.Fatalf("first representation must be the Product @key, got %+v", key)
			}
			for _, want := range []string{"__typename", "id", "organization { id }"} {
				if !strings.Contains(key.Fragment, want) {
					t.Fatalf("key fragment must contain %q; got %q", want, key.Fragment)
				}
			}
			if strings.Contains(key.Fragment, "dimensions") {
				t.Fatalf("@requires selection must NOT ride in the key fragment (Section 7.2): %q", key.Fragment)
			}
			if req.Kind != resolve.RepresentationKindRequires || !strings.Contains(req.Fragment, "dimensions { height length width }") {
				t.Fatalf("second representation must be the @requires dimensions fragment, got %+v", req)
			}
			// I2: the representations input variable -- postprocess's createEntityFetch splits the
			// InputTemplate at exactly this ResolvableObject segment.
			if len(sf.Variables) != 1 || sf.Variables[0].GetVariableKind() != resolve.ResolvableObjectVariableKind {
				t.Fatalf("jump fetch must carry one ResolvableObject representations variable, got %+v", sf.Variables)
			}
			if !strings.Contains(sf.Input, `"representations":[$$0$$]`) {
				t.Fatalf("jump fetch Input must reference the representations variable: %q", sf.Input)
			}
		} else {
			root++
			for _, want := range []string{"id", "organization", "dimensions"} {
				if !strings.Contains(sf.Input, want) {
					t.Fatalf("root fetch must supply key/@requires field %q; doc=%q", want, sf.Input)
				}
			}
			// A root fetch selects off the operation root, so it attaches at the (empty) root path.
			if f.ResponsePath != "" || len(f.FetchPath) != 0 {
				t.Fatalf("root fetch must attach at the root (empty path), got ResponsePath=%q FetchPath=%+v", f.ResponsePath, f.FetchPath)
			}
		}
	}
	if jump != 1 || root != 1 {
		t.Fatalf("want exactly one root and one jump fetch, got root=%d jump=%d", root, jump)
	}
	// D11.3: response shape is exactly { product { shippingEstimate } } (I4).
	assertResponseShape(t, p, "{ product { shippingEstimate } }")
}

// TestLowerResponseOnlyNullPreservesShape is Section 7.1: OnlyA/OnlyB are D6-narrowed (Cover.Nulls), so no
// fetch produces them, yet I4 keeps a/b in the response shape gated on their concrete __typename --
// the response-only-null contract (D11.3 clause 3 / PROOFS T4 clause 3).
func TestLowerResponseOnlyNullPreservesShape(t *testing.T) {
	h, o, res, op, def := planPartialUnion(t)
	p, err := Lower(h, o, res, op, def)
	if err != nil {
		t.Fatal(err)
	}
	// I4: a/b remain in the response shape gated on __typename, with no producing fetch.
	assertResponseShape(t, p, "{ wrapper { action { __typename ... on Common { c } ... on OnlyA { a } ... on OnlyB { b } } } }")
	assertNoFetchProduces(t, p, "OnlyA", "a")
	assertNoFetchProduces(t, p, "OnlyB", "b")

	// Section 7.1's subgraph query to A is `{ wrapper { action { __typename ... on Common { c } } } }`:
	// the single fetch MUST still resolve the covered member (TypeMove + Field c) and carry the
	// __typename the resolver needs to evaluate the gates -- narrowing removes OnlyA/OnlyB ONLY.
	// (Regression pin: a grouping bug that drops the TypeMove edge passes the shape assertions
	// above yet ships an empty `action { }` selection -- exactly the R3 corrupt-response class.)
	docs := fetchDocuments(p)
	if len(docs) != 1 {
		t.Fatalf("Section 7.1 is a single fetch, got %d", len(docs))
	}
	for _, want := range []string{"__typename", "... on Common { c }"} {
		if !strings.Contains(docs[0], want) {
			t.Fatalf("fetch document must contain %q; doc=%q", want, docs[0])
		}
	}
}

// TestLowerMultiRootSiblings is the review-C1 regression pin: EVERY top-level field is a sibling in
// the response tree. Before the obligation.NoParent sentinel, only obligation 0 was recognisable as
// a root (`Parent == ID` self-loop), so `{ a { x } b { y } }` lowered b UNDER a -- `a { x b { y } }`,
// a silently corrupted response shape.
func TestLowerMultiRootSiblings(t *testing.T) {
	h, o, res, op, def := planTwoRoots(t)
	p, err := Lower(h, o, res, op, def)
	if err != nil {
		t.Fatal(err)
	}
	assertResponseShape(t, p, "{ a { x } b { y } }")
	// Task 9 MERGE (Section 6.4 dedup) collapses the two same-subgraph root groups into ONE fetch that
	// selects both root fields -- the M1 "fetch count <= old planner" behavior change (was 2 pre-merge).
	docs := fetchDocuments(p)
	if len(docs) != 1 {
		t.Fatalf("want 1 merged root fetch (Section 6.4 dedup), got %d", len(docs))
	}
	if !strings.Contains(docs[0], "a { x }") || !strings.Contains(docs[0], "b { y }") {
		t.Fatalf("merged root fetch must select both root fields: %q", docs[0])
	}
}

// TestLowerThreeRootsTwoSubgraphs extends C1 across subgraphs and includes a LEAF top-level field:
// `{ a { x } b { y } c }` with a,b resolved in subgraph A and the leaf c only in B. The leaf root
// exercises the second half of the NoParent fix -- under the old Parent==0 overload, c marked
// obligation 0 as having children, so a leaf top-level field alongside other roots silently lost
// its goal (never fetched).
func TestLowerThreeRootsTwoSubgraphs(t *testing.T) {
	h, o, res, op, def := planThreeRoots(t)
	p, err := Lower(h, o, res, op, def)
	if err != nil {
		t.Fatal(err)
	}
	assertResponseShape(t, p, "{ a { x } b { y } c }")
	// Task 9 MERGE (Section 6.4 dedup) is PER-SUBGRAPH: the two A root groups (a,b) collapse into one fetch,
	// while the B leaf-root fetch (c) stays distinct -- 3 pre-merge groups -> 2 (was 3, review M3).
	docs := fetchDocuments(p)
	if len(docs) != 2 {
		t.Fatalf("want 2 fetches (1 merged A + 1 B), got %d", len(docs))
	}
	var aDoc, cDoc string
	for i, f := range p.Response.RawFetches {
		sf := f.Fetch.(*resolve.SingleFetch)
		if strings.Contains(docs[i], "a { x }") && strings.Contains(docs[i], "b { y }") {
			aDoc = string(sf.DataSourceIdentifier)
		}
		if strings.Contains(docs[i], "c") && !strings.Contains(docs[i], "a {") && !strings.Contains(docs[i], "b {") {
			cDoc = string(sf.DataSourceIdentifier)
		}
	}
	if aDoc != "A" {
		t.Fatalf("merged a+b fetch must be on subgraph A, got %q", aDoc)
	}
	if cDoc != "B" {
		t.Fatalf("leaf root c must be fetched from subgraph B, got %q", cDoc)
	}
}

// TestPostprocessEntityFetchTree is the review-I2 pin: the emitted plan must survive the UNTOUCHED
// postprocess pipeline (L14b) -- in particular createConcreteSingleFetchTypes, which slices the
// entity fetch's InputTemplate at its ResolvableObjectVariableKind segment and panics if lowering
// didn't emit one. Asserts the Section 7.2 plan processes without panic, the jump fetch becomes a concrete
// *resolve.EntityFetch, and the fetch tree orders the root fetch before its dependent entity fetch.
func TestPostprocessEntityFetchTree(t *testing.T) {
	h, o, res, op, def := planEntityJump(t)
	p, err := Lower(h, o, res, op, def)
	if err != nil {
		t.Fatal(err)
	}
	postprocess.NewProcessor().Process(p) // panics here = I2 regression

	var order []int // FetchIDs in tree order
	entityFetches := 0
	var walk func(n *resolve.FetchTreeNode)
	walk = func(n *resolve.FetchTreeNode) {
		if n == nil {
			return
		}
		if n.Kind == resolve.FetchTreeNodeKindSingle {
			order = append(order, n.Item.Fetch.Dependencies().FetchID)
			if _, ok := n.Item.Fetch.(*resolve.EntityFetch); ok {
				entityFetches++
			}
			return
		}
		for _, c := range n.ChildNodes {
			walk(c)
		}
	}
	walk(p.Response.Fetches)

	if entityFetches != 1 {
		t.Fatalf("postprocess must turn the jump fetch into one *resolve.EntityFetch, got %d", entityFetches)
	}
	if len(order) != 2 || order[0] != 0 || order[1] != 1 {
		t.Fatalf("fetch tree must order root fetch (0) before dependent entity fetch (1), got %v", order)
	}
}

// TestLowerTypedScalarLeaves is the Task-10 carry-forward pin: lower must type each scalar leaf from
// the composed definition so the resolver dispatches on node kind. A Float/Int/Boolean leaf rendered
// as resolve.String would hit walkString, which HARD-ERRORS on non-string JSON (Section 7.2's Float
// dimensions/shippingEstimate) -- so this asserts the exact typed node kinds, not just the shape.
func TestLowerTypedScalarLeaves(t *testing.T) {
	h, o, res, op, def := planTypedLeaves(t)
	p, err := Lower(h, o, res, op, def)
	if err != nil {
		t.Fatal(err)
	}
	assertResponseShape(t, p, "{ metrics { f i b s id } }")

	metrics := p.Response.Data.Fields[0]
	obj, ok := metrics.Value.(*resolve.Object)
	if !ok {
		t.Fatalf("metrics must be an object, got %T", metrics.Value)
	}
	want := map[string]resolve.NodeKind{
		"f":  resolve.NodeKindFloat,
		"i":  resolve.NodeKindInteger,
		"b":  resolve.NodeKindBoolean,
		"s":  resolve.NodeKindString,
		"id": resolve.NodeKindScalar, // ID is a scalar with no dedicated walker -> resolve.Scalar (v1 parity)
	}
	for _, f := range obj.Fields {
		name := string(f.Name)
		if want[name] != f.Value.NodeKind() {
			t.Fatalf("leaf %s: want node kind %v, got %v", name, want[name], f.Value.NodeKind())
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing typed leaves: %v", want)
	}
}

// TestLowerEnumLeaf pins the M1.5 leaf-kind fix for enums: an enum-typed leaf lowers to resolve.Enum
// carrying the enum's TypeName and accepted values (with @inaccessible values recorded separately),
// mirroring v1 visitor.go -- not the generic resolve.String M1 previously emitted.
func TestLowerEnumLeaf(t *testing.T) {
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	mf := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "hero", Subgraph: 1})
	obj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Hero", Subgraph: 1})
	roleF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Hero", Field: "role", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "hero", Head: mf, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{mf}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "role", Head: roleF, Tails: []hypergraph.NodeID{obj}, Weight: 1})
	h := b.Build()
	const schema = `schema { query: Query } type Query { hero: Hero } type Hero { role: Role } ` +
		`enum Role { ADMIN USER GUEST @inaccessible }`
	o, opDoc, defDoc := buildTree(t, h, schema, `{ hero { role } }`)
	res := runSearch(t, h, o)
	p, err := Lower(h, o, res, opDoc, defDoc)
	if err != nil {
		t.Fatal(err)
	}
	heroObj, ok := p.Response.Data.Fields[0].Value.(*resolve.Object)
	if !ok {
		t.Fatalf("hero must be an object, got %T", p.Response.Data.Fields[0].Value)
	}
	role, ok := heroObj.Fields[0].Value.(*resolve.Enum)
	if !ok {
		t.Fatalf("enum leaf must lower to *resolve.Enum, got %T", heroObj.Fields[0].Value)
	}
	if role.TypeName != "Role" {
		t.Fatalf("enum TypeName: want Role, got %q", role.TypeName)
	}
	wantValues := []string{"ADMIN", "USER", "GUEST"}
	if !reflect.DeepEqual(role.Values, wantValues) {
		t.Fatalf("enum Values: want %v, got %v", wantValues, role.Values)
	}
	if !reflect.DeepEqual(role.InaccessibleValues, []string{"GUEST"}) {
		t.Fatalf("enum InaccessibleValues: want [GUEST], got %v", role.InaccessibleValues)
	}
}

// TestLowerTypenameNodeKinds pins the M1.5 leaf-kind fix for __typename: selected on a root operation
// type it lowers to resolve.StaticString{Value: <TypeName>} (v1 parity -- a compile-time constant),
// while a nested __typename stays resolve.String{IsTypeName} (read from the fetch response).
func TestLowerTypenameNodeKinds(t *testing.T) {
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	itemF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "item", Subgraph: 1})
	obj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Item", Subgraph: 1})
	idF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Item", Field: "id", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "item", Head: itemF, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{itemF}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idF, Tails: []hypergraph.NodeID{obj}, Weight: 1})
	h := b.Build()
	const schema = `schema { query: Query } type Query { item: Item } type Item { id: ID }`
	o, opDoc, defDoc := buildTree(t, h, schema, `{ __typename item { __typename id } }`)
	res := runSearch(t, h, o)
	p, err := Lower(h, o, res, opDoc, defDoc)
	if err != nil {
		t.Fatal(err)
	}

	// Root __typename -> StaticString carrying the root type name.
	var rootTypename *resolve.Field
	for _, f := range p.Response.Data.Fields {
		if string(f.Name) == "__typename" {
			rootTypename = f
		}
	}
	if rootTypename == nil {
		t.Fatal("root __typename field not emitted")
	}
	ss, ok := rootTypename.Value.(*resolve.StaticString)
	if !ok {
		t.Fatalf("root __typename must lower to *resolve.StaticString, got %T", rootTypename.Value)
	}
	if ss.Value != "Query" {
		t.Fatalf("root __typename StaticString value: want Query, got %q", ss.Value)
	}

	// Nested __typename (on Item) -> String{IsTypeName}.
	itemObj, ok := p.Response.Data.Fields[len(p.Response.Data.Fields)-1].Value.(*resolve.Object)
	if !ok {
		// item may not be last after merge; find it explicitly.
		for _, f := range p.Response.Data.Fields {
			if o, isObj := f.Value.(*resolve.Object); isObj {
				itemObj = o
			}
		}
	}
	if itemObj == nil {
		t.Fatal("item object not found")
	}
	var nestedTypename *resolve.Field
	for _, f := range itemObj.Fields {
		if string(f.Name) == "__typename" {
			nestedTypename = f
		}
	}
	if nestedTypename == nil {
		t.Fatal("nested __typename field not emitted")
	}
	s, ok := nestedTypename.Value.(*resolve.String)
	if !ok || !s.IsTypeName {
		t.Fatalf("nested __typename must lower to *resolve.String{IsTypeName}, got %T (isTypeName=%v)", nestedTypename.Value, ok && s.IsTypeName)
	}
}

// TestLowerAliasedTypenameKeepsAlias pins the F2 fix: an aliased __typename (`tn: __typename`) must
// render under its CLIENT key (the alias `tn`), not the meta-field name `__typename`. The Typename
// branch of renderFields previously keyed Name on ob.Field ("__typename"), dropping the alias so the
// field vanished from the client response (the expected `tn` key was absent).
func TestLowerAliasedTypenameKeepsAlias(t *testing.T) {
	h, o, res, op, def := planAliasedTypename(t)
	p, err := Lower(h, o, res, op, def)
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := p.Response.Data.Fields[0].Value.(*resolve.Object)
	if !ok {
		t.Fatalf("item must be an object, got %T", p.Response.Data.Fields[0].Value)
	}
	var names []string
	var tn *resolve.Field
	for _, f := range obj.Fields {
		names = append(names, string(f.Name))
		if s, ok := f.Value.(*resolve.String); ok && s.IsTypeName {
			tn = f
		}
	}
	if tn == nil {
		t.Fatalf("no __typename field emitted; fields=%v", names)
	}
	if string(tn.Name) != "tn" {
		t.Fatalf("aliased __typename must render under client key \"tn\", got %q (fields=%v)", tn.Name, names)
	}
}

// TestTopoOrderGroupsNonTopologicalInput pins topoOrderGroups directly on a DELIBERATELY
// non-topological group order -- the shape end-to-end fixtures rarely produce (buildGroups' cover-edge
// order is usually topological by coincidence, which is exactly how the original bug hid; the
// boomerang/merge fixtures all bypass this path with already-valid orders). Input chain (old
// indices): 0 depends on 2, 2 depends on 1, 1 is free -- emission order 0,1,2 is invalid (fetch 0
// would depend on the LATER fetch 2). The stable Kahn order must emit 1,2,0 with DependsOn and
// groupOf remapped, and must leave an already-topological input untouched.
func TestTopoOrderGroupsNonTopologicalInput(t *testing.T) {
	t.Run("non-topological chain is reordered and remapped", func(t *testing.T) {
		groups := []*fetchGroup{
			{Subgraph: 1, DependsOn: []int{2}}, // old 0: depends on old 2 (violates emission order)
			{Subgraph: 2},                      // old 1: free
			{Subgraph: 3, DependsOn: []int{1}}, // old 2: depends on old 1
		}
		groupOf := map[hypergraph.EdgeID]int{10: 0, 11: 1, 12: 2}
		out, remapped := topoOrderGroups(groups, groupOf)
		if len(out) != 3 {
			t.Fatalf("want 3 groups, got %d", len(out))
		}
		// Stable Kahn: ready set {1} -> 1; then {2} -> 2; then {0} -> 0. New order = old 1, 2, 0.
		wantSub := []hypergraph.SubgraphID{2, 3, 1}
		for i, g := range out {
			if g.Subgraph != wantSub[i] {
				t.Fatalf("new index %d: want old group with subgraph %d, got %d", i, wantSub[i], g.Subgraph)
			}
		}
		// Every dependency must reference a strictly EARLIER new index (the D11.2 validity the
		// audit's dependency-order assertion checks).
		for i, g := range out {
			for _, d := range g.DependsOn {
				if d >= i {
					t.Fatalf("new group %d depends on %d which is not earlier", i, d)
				}
			}
		}
		if len(out[1].DependsOn) != 1 || out[1].DependsOn[0] != 0 {
			t.Fatalf("old 2 (new 1) must depend on new 0, got %v", out[1].DependsOn)
		}
		if len(out[2].DependsOn) != 1 || out[2].DependsOn[0] != 1 {
			t.Fatalf("old 0 (new 2) must depend on new 1, got %v", out[2].DependsOn)
		}
		// groupOf remap follows the same permutation: edge 10 (old group 0) -> new 2, 11 -> 0, 12 -> 1.
		wantEdges := map[hypergraph.EdgeID]int{10: 2, 11: 0, 12: 1}
		for e, want := range wantEdges {
			if got := remapped[e]; got != want {
				t.Fatalf("groupOf[%d]: want new index %d, got %d", e, want, got)
			}
		}
	})

	t.Run("already-topological input is unchanged", func(t *testing.T) {
		groups := []*fetchGroup{
			{Subgraph: 1},
			{Subgraph: 2, DependsOn: []int{0}},
			{Subgraph: 3, DependsOn: []int{1}},
		}
		groupOf := map[hypergraph.EdgeID]int{20: 0, 21: 1, 22: 2}
		out, remapped := topoOrderGroups(groups, groupOf)
		for i, g := range out {
			if g.Subgraph != hypergraph.SubgraphID(i+1) {
				t.Fatalf("topological input must keep its order: index %d has subgraph %d", i, g.Subgraph)
			}
		}
		for e, gi := range groupOf {
			if remapped[e] != gi {
				t.Fatalf("groupOf must be identity for topological input: edge %d moved %d->%d", e, gi, remapped[e])
			}
		}
	})
}

// TestSameKeyMemberVariantsStaySiblings pins the D11.8 contract that replaced the F3 lowering-side
// merge: a response key selected at the abstract (interface) level AND inside concrete member
// refinements renders as SIBLING fields of one key, each with its own gate at its own depth --
// exactly v1's visitor output. The depth-correct fold is postprocess merge_fields' job (it
// propagates gates onto folded children as ParentOnTypeNames records with the discriminating
// ancestor's depth); a lowering-side fold put the gate at the child's own depth, where the resolver
// matched it against the WRONG object's __typename and dropped every folded member field (the
// executed-truth Abstract_object family: {a} where v1 returns {a,b,c}).
func TestSameKeyMemberVariantsStaySiblings(t *testing.T) {
	h := buildAbstractObjectH(t)
	o, op, def := buildTree(t, h,
		`schema { query: Query } type Query { others: [Iface] } interface Iface { obj: Obj } type T1 implements Iface { obj: Obj } type Obj { a: String b: String }`,
		`{ others { obj { a } ... on T1 { obj { b } } } }`)
	res := runSearch(t, h, o)
	p := newPath(t, h, o, res, op, def)

	others := p.Response.Data.Fields[0]
	if string(others.Name) != "others" {
		t.Fatalf("want top-level others, got %q", others.Name)
	}
	item, ok := others.Value.(*resolve.Array).Item.(*resolve.Object)
	if !ok {
		t.Fatalf("others must be a list of objects")
	}
	var objFields []*resolve.Field
	for _, f := range item.Fields {
		if string(f.Name) == "obj" {
			objFields = append(objFields, f)
		}
	}
	if len(objFields) != 2 {
		t.Fatalf("want the ungated obj AND the T1-gated obj as SIBLINGS (v1 parity, D11.8), got %d obj fields", len(objFields))
	}
	ungated, gated := objFields[0], objFields[1]
	if ungated.OnTypeNames != nil {
		ungated, gated = gated, ungated
	}
	if ungated.OnTypeNames != nil || len(gated.OnTypeNames) != 1 || string(gated.OnTypeNames[0]) != "T1" {
		t.Fatalf("want one ungated and one T1-gated obj variant, got gates %v / %v", ungated.OnTypeNames, gated.OnTypeNames)
	}
	// The gated variant's child keeps NO gate of its own -- the discriminator lives on the variant
	// field, at the depth where the resolver reads the right __typename; postprocess owns the fold.
	gatedObj := gated.Value.(*resolve.Object)
	if len(gatedObj.Fields) != 1 || string(gatedObj.Fields[0].Name) != "b" || gatedObj.Fields[0].OnTypeNames != nil {
		t.Fatalf("gated variant must carry exactly child b with no child-depth gate, got %+v", gatedObj.Fields)
	}
	// And the untouched postprocess pipeline must accept the sibling shape (it performs the fold).
	postprocess.NewProcessor().Process(p)
}

// buildAbstractObjectH: (Query,A).others -> (Iface,A); TypeMove Iface->T1; obj declared on BOTH the
// interface and the member, descending to the same (Obj,A) with fields a and b -- the
// Abstract_object shape, single subgraph.
func buildAbstractObjectH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qOthers := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "others", Subgraph: 1})
	iface := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Iface", Subgraph: 1})
	ifaceObjF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Iface", Field: "obj", Subgraph: 1})
	t1 := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "T1", Subgraph: 1})
	t1ObjF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "T1", Field: "obj", Subgraph: 1})
	obj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Obj", Subgraph: 1})
	objA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Obj", Field: "a", Subgraph: 1})
	objB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Obj", Field: "b", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "others", Head: qOthers, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: iface, Tails: []hypergraph.NodeID{qOthers}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "T1", Head: t1, Tails: []hypergraph.NodeID{iface}, Weight: 1, Members: []string{"T1"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "obj", Head: ifaceObjF, Tails: []hypergraph.NodeID{iface}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{ifaceObjF}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "obj", Head: t1ObjF, Tails: []hypergraph.NodeID{t1}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{t1ObjF}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: objA, Tails: []hypergraph.NodeID{obj}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: objB, Tails: []hypergraph.NodeID{obj}, Weight: 1})
	return b.Build()
}

// --- fixtures ---------------------------------------------------------------------------------

// planAliasedTypename hand-builds `{ item { tn: __typename id } }` over one subgraph A. __typename
// carries no hypergraph node (synthesized at lowering), so H only needs item -> Item.id.
func planAliasedTypename(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	itemF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "item", Subgraph: 1})
	obj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Item", Subgraph: 1})
	idF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Item", Field: "id", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "item", Head: itemF, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{itemF}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idF, Tails: []hypergraph.NodeID{obj}, Weight: 1})
	h := b.Build()
	const schema = `schema { query: Query } type Query { item: Item } type Item { id: ID }`
	o, opDoc, defDoc := buildTree(t, h, schema, `{ item { tn: __typename id } }`)
	res := runSearch(t, h, o)
	return h, o, res, opDoc, defDoc
}

// planTypedLeaves hand-builds `{ metrics { f i b s id } }` over one subgraph A, with f:Float i:Int
// b:Boolean s:String id:ID -- exercising every scalar branch of leafValue.
func planTypedLeaves(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	mf := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "metrics", Subgraph: 1})
	obj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Metrics", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "metrics", Head: mf, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{mf}, Weight: 0})
	for _, leaf := range []string{"f", "i", "b", "s", "id"} {
		ln := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Metrics", Field: leaf, Subgraph: 1})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: leaf, Head: ln, Tails: []hypergraph.NodeID{obj}, Weight: 1})
	}
	h := b.Build()
	const schema = `schema { query: Query } type Query { metrics: Metrics } type Metrics { f: Float i: Int b: Boolean s: String id: ID }`
	o, opDoc, defDoc := buildTree(t, h, schema, `{ metrics { f i b s id } }`)
	res := runSearch(t, h, o)
	return h, o, res, opDoc, defDoc
}

// planTwoRoots hand-builds `{ a { x } b { y } }` over one subgraph A (two root-entering fields).
func planTwoRoots(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	for _, root := range []struct{ field, typ, leaf string }{{"a", "TA", "x"}, {"b", "TB", "y"}} {
		ff := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: root.field, Subgraph: 1})
		obj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: root.typ, Subgraph: 1})
		leaf := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: root.typ, Field: root.leaf, Subgraph: 1})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: root.field, Head: ff, Tails: []hypergraph.NodeID{r}, Weight: 1000})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{ff}, Weight: 0})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: root.leaf, Head: leaf, Tails: []hypergraph.NodeID{obj}, Weight: 1})
	}
	h := b.Build()
	const schema = `schema { query: Query } type Query { a: TA b: TB } type TA { x: String } type TB { y: String }`
	o, opDoc, defDoc := buildTree(t, h, schema, `{ a { x } b { y } }`)
	res := runSearch(t, h, o)
	return h, o, res, opDoc, defDoc
}

// planThreeRoots hand-builds `{ a { x } b { y } c }`: a,b in subgraph A, the LEAF root c only in B.
func planThreeRoots(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	for _, root := range []struct{ field, typ, leaf string }{{"a", "TA", "x"}, {"b", "TB", "y"}} {
		ff := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: root.field, Subgraph: 1})
		obj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: root.typ, Subgraph: 1})
		leaf := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: root.typ, Field: root.leaf, Subgraph: 1})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: root.field, Head: ff, Tails: []hypergraph.NodeID{r}, Weight: 1000})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{ff}, Weight: 0})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: root.leaf, Head: leaf, Tails: []hypergraph.NodeID{obj}, Weight: 1})
	}
	cf := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "c", Subgraph: 2})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "c", Head: cf, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	h := b.Build()
	const schema = `schema { query: Query } type Query { a: TA b: TB c: String } type TA { x: String } type TB { y: String }`
	o, opDoc, defDoc := buildTree(t, h, schema, `{ a { x } b { y } c }`)
	res := runSearch(t, h, o)
	return h, o, res, opDoc, defDoc
}

// TestLowerListFieldEmitsArray pins the list-lowering fix (Task 11): a list-typed field lowers to a
// resolve.Array wrapping the item node -- a scalar list to Array{Item: typed-scalar}, an object list
// to Array{Item: Object} -- with the outermost Array carrying the field Path and the item reading its
// element directly (Path nil), mirroring v1 visitor.go. Before the fix, list leaves flattened to a
// bare scalar (no Array), rendering a single value where the client expects a JSON array.
func TestLowerListFieldEmitsArray(t *testing.T) {
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	// Query.tags: [String!] -- scalar list leaf.
	tags := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "tags", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "tags", Head: tags, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	// Query.items: [Item] with Item.name: String -- object list.
	items := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "items", Subgraph: 1})
	itemObj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Item", Subgraph: 1})
	name := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Item", Field: "name", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "items", Head: items, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: itemObj, Tails: []hypergraph.NodeID{items}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "name", Head: name, Tails: []hypergraph.NodeID{itemObj}, Weight: 1})
	h := b.Build()

	const schema = `schema { query: Query } type Query { tags: [String!] items: [Item] } type Item { name: String }`
	o, opDoc, defDoc := buildTree(t, h, schema, `{ tags items { name } }`)
	res := runSearch(t, h, o)
	p, err := Lower(h, o, res, opDoc, defDoc)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]*resolve.Field{}
	for _, f := range p.Response.Data.Fields {
		byName[string(f.Name)] = f
	}

	// tags: Array{ Path:["tags"], Item: String{Path:nil} }.
	tagsArr, ok := byName["tags"].Value.(*resolve.Array)
	if !ok {
		t.Fatalf("tags must lower to resolve.Array, got %T", byName["tags"].Value)
	}
	if len(tagsArr.Path) != 1 || tagsArr.Path[0] != "tags" {
		t.Fatalf("tags Array must carry field path [tags], got %v", tagsArr.Path)
	}
	if _, ok := tagsArr.Item.(*resolve.String); !ok {
		t.Fatalf("tags Array item must be a scalar String leaf, got %T", tagsArr.Item)
	}

	// items: Array{ Path:["items"], Item: Object{ Path:nil, Fields:[name] } }.
	itemsArr, ok := byName["items"].Value.(*resolve.Array)
	if !ok {
		t.Fatalf("items must lower to resolve.Array, got %T", byName["items"].Value)
	}
	itemObjNode, ok := itemsArr.Item.(*resolve.Object)
	if !ok {
		t.Fatalf("items Array item must be an Object, got %T", itemsArr.Item)
	}
	if len(itemObjNode.Path) != 0 {
		t.Fatalf("items Array item Object must read its element directly (Path nil), got %v", itemObjNode.Path)
	}
	if len(itemObjNode.Fields) != 1 || string(itemObjNode.Fields[0].Name) != "name" {
		t.Fatalf("items element object must contain the name field, got %+v", itemObjNode.Fields)
	}
}

func planPartialUnion(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	h := buildH(t, hgtestdata.PartialUnionConfig())
	const schema = `
schema { query: Query }
type Query { wrapper: Wrapper }
type Wrapper { id: ID! action: Action }
union Action = Common | OnlyA | OnlyB
type Common { c: String }
type OnlyA { a: String }
type OnlyB { b: String }
`
	const op = `{ wrapper { action { __typename ... on Common { c } ... on OnlyA { a } ... on OnlyB { b } } } }`
	o, opDoc, defDoc := buildTree(t, h, schema, op)
	res := runSearch(t, h, o)
	return h, o, res, opDoc, defDoc
}

func planEntityJump(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	h := buildH(t, hgtestdata.EntityJumpConfig())
	const schema = `
schema { query: Query }
type Query { product: Product }
type Product { id: ID! organization: Organization dimensions: Dimensions shippingEstimate: Float }
type Organization { id: ID! }
type Dimensions { length: Float width: Float height: Float }
`
	const op = `{ product { shippingEstimate } }`
	o, opDoc, defDoc := buildTree(t, h, schema, op)
	res := runSearch(t, h, o)
	return h, o, res, opDoc, defDoc
}

func buildH(t *testing.T, ds []plan.DataSource) *hypergraph.Hypergraph {
	t.Helper()
	h, err := hypergraph.Build(ds, hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query"},
	})
	if err != nil {
		t.Fatalf("build H: %v", err)
	}
	return h
}

func buildTree(t *testing.T, h *hypergraph.Hypergraph, schema, op string) (*obligation.Tree, *ast.Document, *ast.Document) {
	t.Helper()
	opDoc := unsafeparser.ParseGraphqlDocumentString(op)
	defDoc := unsafeparser.ParseGraphqlDocumentStringWithBaseSchema(schema)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
	).NormalizeOperation(&opDoc, &defDoc, report)
	if report.HasErrors() {
		t.Fatalf("normalize operation: %s", report.Error())
	}
	tree, err := obligation.Build(&opDoc, &defDoc, "", h)
	if err != nil {
		t.Fatalf("build obligation tree: %v", err)
	}
	return tree, &opDoc, &defDoc
}

func runSearch(t *testing.T, h *hypergraph.Hypergraph, o *obligation.Tree) *search.Result {
	t.Helper()
	res, err := search.Search(h, o, search.Config{Combine: search.Sum, PreflightCap: 1 << 30, StateCap: 1 << 20})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return res
}

// --- assertions -------------------------------------------------------------------------------

func flattenFetches(p *plan.SynchronousResponsePlan) []*resolve.FetchItem {
	return p.Response.RawFetches
}

// assertResponseShape checks I4 verbatim: the emitted response tree equals the client selection tree
// key-by-key -- same response keys, nesting, and __typename gates (OnTypeNames) -- comparing a
// canonical rendering of p.Response.Data against a canonicalized parse of the expected selection set.
func assertResponseShape(t *testing.T, p *plan.SynchronousResponsePlan, shape string) {
	t.Helper()
	got := canonResolveFields(p.Response.Data.Fields)
	want := canonShape(t, shape)
	if got != want {
		t.Fatalf("response shape mismatch (I4):\n got: %s\nwant: %s", got, want)
	}
}

// assertNoFetchProduces asserts no fetch document resolves field `field` under concrete type `typ` --
// a D6-narrowed member is a response-only null: it appears in the response shape but no subgraph
// query selects it (its concrete type never enters any fetch document).
func assertNoFetchProduces(t *testing.T, p *plan.SynchronousResponsePlan, typ, field string) {
	t.Helper()
	for _, doc := range fetchDocuments(p) {
		if strings.Contains(doc, "on "+typ) {
			t.Fatalf("fetch document must not resolve narrowed type %s (field %s): %q", typ, field, doc)
		}
	}
}

// canonResolveFields renders a resolve response field list into the canonical shape string
// `key@Gate{children}` (gate omitted when ungated, braces omitted for leaves), order preserved.
func canonResolveFields(fields []*resolve.Field) string {
	var parts []string
	for _, f := range fields {
		s := string(f.Name) + gateString(f.OnTypeNames)
		if obj, ok := f.Value.(*resolve.Object); ok {
			s += "{" + canonResolveFields(obj.Fields) + "}"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func gateString(on [][]byte) string {
	if len(on) == 0 {
		return ""
	}
	names := make([]string, len(on))
	for i, n := range on {
		names[i] = string(n)
	}
	return "@" + strings.Join(names, "|")
}

// canonShape parses an expected selection-set string into the same canonical form as
// canonResolveFields: inline fragments `... on T { ... }` are flattened into their fields, each
// gated with `@T`.
func canonShape(t *testing.T, shape string) string {
	t.Helper()
	toks := tokenizeShape(shape)
	fields, i := parseSelSet(t, toks, 0)
	if i != len(toks) {
		t.Fatalf("trailing tokens after selection set: %v", toks[i:])
	}
	return strings.Join(fields, " ")
}

func tokenizeShape(s string) []string {
	s = strings.ReplaceAll(s, "{", " { ")
	s = strings.ReplaceAll(s, "}", " } ")
	return strings.Fields(s)
}

// parseSelSet parses toks starting at a "{" and returns the canonical field entries plus the index
// just past the matching "}".
func parseSelSet(t *testing.T, toks []string, i int) ([]string, int) {
	t.Helper()
	if toks[i] != "{" {
		t.Fatalf("expected '{' at %d, got %q", i, toks[i])
	}
	i++
	var parts []string
	for toks[i] != "}" {
		if toks[i] == "..." {
			// ... on T { ... }
			typeName := toks[i+2]
			body, ni := parseSelSet(t, toks, i+3)
			for _, e := range body {
				parts = append(parts, applyGate(e, typeName))
			}
			i = ni
			continue
		}
		name := toks[i]
		i++
		if i < len(toks) && toks[i] == "{" {
			body, ni := parseSelSet(t, toks, i)
			parts = append(parts, name+"{"+strings.Join(body, " ")+"}")
			i = ni
		} else {
			parts = append(parts, name)
		}
	}
	return parts, i + 1
}

// applyGate inserts an @Type gate right after the key of a canonical field entry.
func applyGate(entry, typeName string) string {
	b := strings.IndexByte(entry, '{')
	key, rest := entry, ""
	if b >= 0 {
		key, rest = entry[:b], entry[b:]
	}
	if strings.ContainsRune(key, '@') {
		key += "|" + typeName
	} else {
		key += "@" + typeName
	}
	return key + rest
}
