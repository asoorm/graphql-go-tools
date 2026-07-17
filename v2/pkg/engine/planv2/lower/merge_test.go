package lower

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// TestMergeDedupsSameSubgraphRootFetches is Section 6.4's syntactic dedup: two root fetches to the SAME
// subgraph with the same (empty) representation key-set are provably redundant siblings -- they must
// collapse into one document. This is the M1 "fetch count <= old planner" bar for multi-root queries:
// pre-merge (Task 8) `{ a { x } b { y } }` over one subgraph produces two same-subgraph root fetches;
// MERGE combines them.
func TestMergeDedupsSameSubgraphRootFetches(t *testing.T) {
	h, o, res, _, _ := planTwoRoots(t)
	groups, _ := buildGroups(h, res.Cover)
	if len(groups) != 2 {
		t.Fatalf("precondition: expected 2 pre-merge root groups, got %d", len(groups))
	}
	merged, _ := merge(h, o, res, groups)
	if len(merged) != 1 {
		t.Fatalf("same-subgraph root fetches must dedup to 1, got %d", len(merged))
	}
	// The surviving group must carry BOTH root selections (a{x} and b{y}) so the single document
	// resolves everything the two originals did.
	ge := indexGroup(h, merged[0])
	if len(ge.fieldsByObj[merged[0].Entry]) != 2 {
		t.Fatalf("merged root group must select both root fields, got %d", len(ge.fieldsByObj[merged[0].Entry]))
	}
}

// TestMergeThreeRootsCollapsesPerSubgraph checks dedup is per-subgraph: `{ a { x } b { y } c }` with
// a,b in A and the leaf c in B must collapse the two A root fetches while KEEPING the B fetch distinct
// -- 3 pre-merge groups -> 2 merged (one per subgraph).
func TestMergeThreeRootsCollapsesPerSubgraph(t *testing.T) {
	h, o, res, _, _ := planThreeRoots(t)
	groups, _ := buildGroups(h, res.Cover)
	if len(groups) != 3 {
		t.Fatalf("precondition: expected 3 pre-merge groups, got %d", len(groups))
	}
	merged, _ := merge(h, o, res, groups)
	if len(merged) != 2 {
		t.Fatalf("dedup must collapse to one fetch per subgraph (2), got %d", len(merged))
	}
	subgraphs := map[hypergraph.SubgraphID]bool{}
	for _, g := range merged {
		if subgraphs[g.Subgraph] {
			t.Fatalf("dedup left two fetches on subgraph %d", g.Subgraph)
		}
		subgraphs[g.Subgraph] = true
	}
}

// TestColocationStrictlyReducesCost is Section 6.4's bounded co-location move. Two sibling fields a,b live on
// one entity: a is resolvable only via a jump to B, b via a jump to B OR a CHEAPER jump to C. Per-goal
// C.4 routing therefore splits them -- b takes the cheaper C jump -- yielding one jump to B (for a) and
// one to C (for b): two entity fetches. Co-locating b onto B (already fetched for a) drops the entire
// C jump, strictly reducing the folded C(K) and the fetch count. The move MUST be applied (PROOFS L7).
func TestColocationStrictlyReducesCost(t *testing.T) {
	h, o, res := planColocatableSplit(t)
	before := search.CoverCost(h, res.Cover.Edges)
	beforeGroups, _ := buildGroups(h, res.Cover)

	changed := colocate(h, o, res)
	if !changed {
		t.Fatalf("co-location move that strictly reduces C(K) must be applied")
	}

	after := search.CoverCost(h, res.Cover.Edges)
	if !(after < before) {
		t.Fatalf("co-location must strictly reduce C(K): before=%d after=%d", before, after)
	}
	afterGroups, _ := buildGroups(h, res.Cover)
	if len(afterGroups) >= len(beforeGroups) {
		t.Fatalf("co-location must reduce the fetch count: %d -> %d", len(beforeGroups), len(afterGroups))
	}
}

// TestMergeNeverIncreasesCost is PROOFS L7(b) as a safety invariant across every deterministic fixture
// the package builds: MERGE (dedup + one bounded co-location pass) never increases the realized folded
// C(K), and never increases the fetch count. Run on real planned instances, not an external
// property-testing dependency (the package convention -- see property_test.go in search/).
func TestMergeNeverIncreasesCost(t *testing.T) {
	cases := map[string]func(*testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result){
		"two-roots": func(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result) {
			h, o, r, _, _ := planTwoRoots(t)
			return h, o, r
		},
		"three-roots": func(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result) {
			h, o, r, _, _ := planThreeRoots(t)
			return h, o, r
		},
		"entity-jump": func(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result) {
			h, o, r, _, _ := planEntityJump(t)
			return h, o, r
		},
		"partial-union": func(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result) {
			h, o, r, _, _ := planPartialUnion(t)
			return h, o, r
		},
		"colocatable":         planColocatableSplit,
		"adversarial-weights": planAdversarialWeights,
		"merge-induced-collision": func(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result) {
			h, o, r, _, _ := planMergeInducedCollision(t)
			return h, o, r
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			h, o, res := mk(t)
			before := search.CoverCost(h, res.Cover.Edges)
			beforeGroups, _ := buildGroups(h, res.Cover)
			merged, _ := merge(h, o, res, beforeGroups)
			after := search.CoverCost(h, res.Cover.Edges)
			if after > before {
				t.Fatalf("MERGE increased C(K): %d -> %d (violates L7(b))", before, after)
			}
			if len(merged) > len(beforeGroups) {
				t.Fatalf("MERGE increased the fetch count: %d -> %d", len(beforeGroups), len(merged))
			}
		})
	}
}

// planColocatableSplit hand-builds the co-location fixture (see TestColocationStrictlyReducesCost):
// `{ root { a b } }`. `root` resolves in A to an entity Wrap keyed by `id`; Wrap.a resolves only in B,
// Wrap.b in B and C. The C jump is made cheaper (w=999 vs 1000) so per-goal routing sends b to C while
// a goes to B -- the sibling split co-location must repair.
func planColocatableSplit(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	b.SetSubgraphName(3, "C")

	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	fRoot := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "root", Subgraph: 1})
	wA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 1})
	idA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "id", Subgraph: 1})
	wB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 2})
	aB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "a", Subgraph: 2})
	bB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "b", Subgraph: 2})
	wC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 3})
	bC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "b", Subgraph: 3})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "root", Head: fRoot, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: wA, Tails: []hypergraph.NodeID{fRoot}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idA, Tails: []hypergraph.NodeID{wA}, Weight: 1})
	// a: only reachable via the A->B jump.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wB, Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: aB, Tails: []hypergraph.NodeID{wB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: bB, Tails: []hypergraph.NodeID{wB}, Weight: 1})
	// b: also reachable via a CHEAPER A->C jump, so per-goal C.4 routing prefers C for b.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wC, Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 999})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: bC, Tails: []hypergraph.NodeID{wC}, Weight: 1})

	h := b.Build()
	const schema = `schema { query: Query } type Query { root: Wrap } type Wrap { id: ID! a: String b: String }`
	o, _, _ := buildTree(t, h, schema, `{ root { a b } }`)
	res := runSearch(t, h, o)

	// Sanity: the fixture must actually split a and b across subgraphs, or the test asserts nothing.
	ga := goalOf(t, o, "Wrap", "a")
	gb := goalOf(t, o, "Wrap", "b")
	if h.Node(res.Cover.Selected[ga]).Subgraph == h.Node(res.Cover.Selected[gb]).Subgraph {
		t.Fatalf("fixture precondition: a and b must route to different subgraphs pre-merge")
	}
	return h, o, res
}

// goalOf finds the GoalID for a given Type.field coordinate in a built tree.
func goalOf(t *testing.T, o *obligation.Tree, typeName, field string) obligation.GoalID {
	t.Helper()
	for _, g := range o.Goals() {
		ob := o.Ob(g)
		if ob.Type == typeName && ob.Field == field {
			return g
		}
	}
	t.Fatalf("no goal for %s.%s", typeName, field)
	return 0
}

// TestDedupScopeDivergedEntityGroupsStaySeparate (M1 task-9 review, IMPORTANT 1a): two EntityJump
// groups to the SAME subgraph with identical "Type.field" repKey STRINGS but DIFFERENT head object
// nodes (D8 scope divergence: (Wrap,B,prov) vs (Wrap,B)) must NOT bucket together -- groupDocument
// walks only the survivor's Entry, so a merge would silently drop the absorbed group's selections
// from the emitted document (an I1 violation). Post-fix, Entry identity is part of the dedup key:
// both groups survive and BOTH documents retain their fields.
func TestDedupScopeDivergedEntityGroupsStaySeparate(t *testing.T) {
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")

	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	fRoot := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "root", Subgraph: 1})
	wA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 1})
	idA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "id", Subgraph: 1})
	// D8 scope divergence: same (Type, Subgraph), different Scope -> two DISTINCT object nodes whose
	// jump-tail repKey coordinates ("Wrap.id") are string-identical.
	wBProv := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 2, Scope: "prov"})
	wBPlain := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 2})
	aB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "a", Subgraph: 2, Scope: "prov"})
	bB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "b", Subgraph: 2})

	e0 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "root", Head: fRoot, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	e1 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: wA, Tails: []hypergraph.NodeID{fRoot}, Weight: 0})
	e2 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idA, Tails: []hypergraph.NodeID{wA}, Weight: 1})
	e3 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wBProv, Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 1000, Scope: "prov"})
	e4 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wBPlain, Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 1000})
	e5 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: aB, Tails: []hypergraph.NodeID{wBProv}, Weight: 1})
	e6 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: bB, Tails: []hypergraph.NodeID{wBPlain}, Weight: 1})
	h := b.Build()

	cover := &search.Cover{Edges: []hypergraph.EdgeID{e0, e1, e2, e3, e4, e5, e6}}
	groups, _ := buildGroups(h, cover)
	if len(groups) != 3 {
		t.Fatalf("precondition: want 3 groups (root + 2 scope-diverged jumps), got %d", len(groups))
	}

	merged := dedupFetches(h, groups)
	if len(merged) != 3 {
		t.Fatalf("scope-diverged entity groups (different Entry nodes) must stay separate: got %d groups", len(merged))
	}
	am := emptyAliasMap()
	var seenA, seenB bool
	for _, g := range merged {
		if g.Jump == hypergraph.NoEdge {
			continue
		}
		doc := groupDocument(h, g, am)
		if strings.Contains(doc, "a") && strings.Contains(doc, "... on Wrap { a }") {
			seenA = true
		}
		if strings.Contains(doc, "... on Wrap { b }") {
			seenB = true
		}
	}
	if !seenA || !seenB {
		t.Fatalf("both scope-diverged documents must retain their selections (a=%v b=%v)", seenA, seenB)
	}
}

// TestDedupBoomerangSkipsCyclicMerge (M1 task-9 review, IMPORTANT 1b): the boomerang A->B->C->B. Groups
// P(root@A), Q(entity@B, deps->P), W(entity@C, deps->Q), R(entity@B, deps->W) where Q and R share the
// SAME subgraph, Entry node, and repKeys -- a dedup bucket match. Merging Q+R would give the merged
// fetch deps {P,W} while W deps->(Q+R): a dependency cycle that deadlocks/misorders postprocess. The
// acyclicity guard must SKIP the merge; the emitted deps stay acyclic and postprocess.Process orders
// the chain P < Q < W < R.
func TestDedupBoomerangSkipsCyclicMerge(t *testing.T) {
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	b.SetSubgraphName(3, "C")

	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	fRoot := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "root", Subgraph: 1})
	wA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 1})
	idA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "id", Subgraph: 1})
	wB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 2})
	idB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "id", Subgraph: 2})
	wC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 3})
	idC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "id", Subgraph: 3})
	// extraC keeps W's document textually distinct from Q's -- postprocess's deduplicateSingleFetches
	// (L14b, reused untouched) would otherwise collapse two same-rendering fetches and mask the
	// ordering assertion this test exists for.
	extraC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "extra", Subgraph: 3})
	zB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "z", Subgraph: 2})

	e0 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "root", Head: fRoot, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	e1 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: wA, Tails: []hypergraph.NodeID{fRoot}, Weight: 0})
	e2 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idA, Tails: []hypergraph.NodeID{wA}, Weight: 1})
	j1 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wB, Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 1000})
	e3 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idB, Tails: []hypergraph.NodeID{wB}, Weight: 1})
	j2 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wC, Tails: []hypergraph.NodeID{idB}, KeyTails: []hypergraph.NodeID{idB}, Weight: 1000})
	e4 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idC, Tails: []hypergraph.NodeID{wC}, Weight: 1})
	e4x := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "extra", Head: extraC, Tails: []hypergraph.NodeID{wC}, Weight: 1})
	j3 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wB, Tails: []hypergraph.NodeID{idC}, KeyTails: []hypergraph.NodeID{idC}, Weight: 1000})
	e5 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "z", Head: zB, Tails: []hypergraph.NodeID{wB}, Weight: 1})
	h := b.Build()

	// Hand-assembled groups: a real cover cannot host two jump groups on ONE head node (traceback
	// keeps a single back-pointer per node), so the boomerang is constructed directly -- exactly how
	// the reviewer produced it -- to pin dedupFetches' guard as a standalone invariant.
	groups := []*fetchGroup{
		{Subgraph: 1, Jump: hypergraph.NoEdge, Entry: r, Edges: []hypergraph.EdgeID{e0, e1, e2}},                                   // P
		{Subgraph: 2, Jump: j1, Entry: wB, RepKeys: []string{"Wrap.id"}, Edges: []hypergraph.EdgeID{e3}, DependsOn: []int{0}},      // Q
		{Subgraph: 3, Jump: j2, Entry: wC, RepKeys: []string{"Wrap.id"}, Edges: []hypergraph.EdgeID{e4, e4x}, DependsOn: []int{1}}, // W
		{Subgraph: 2, Jump: j3, Entry: wB, RepKeys: []string{"Wrap.id"}, Edges: []hypergraph.EdgeID{e5}, DependsOn: []int{2}},      // R
	}

	merged := dedupFetches(h, groups)
	if len(merged) != 4 {
		t.Fatalf("boomerang merge must be skipped (Q,R are dependency-ordered): want 4 groups, got %d", len(merged))
	}
	assertAcyclicDeps(t, merged)

	// End-to-end: the emitted fetches must survive UNTOUCHED postprocess (L14b) and come out in
	// dependency order P < Q < W < R -- the deadlock/misorder the guard exists to prevent.
	cover := &search.Cover{Edges: []hypergraph.EdgeID{e0, e1, e2, j1, e3, j2, e4, e4x, j3, e5}}
	items, err := buildRawFetches(h, cover, merged, emptyAliasMap(), nil, nil)
	if err != nil {
		t.Fatalf("buildRawFetches: %v", err)
	}
	p := &plan.SynchronousResponsePlan{Response: &resolve.GraphQLResponse{Data: &resolve.Object{}, RawFetches: items}}
	postprocess.NewProcessor().Process(p)

	var order []int
	var walk func(n *resolve.FetchTreeNode)
	walk = func(n *resolve.FetchTreeNode) {
		if n == nil {
			return
		}
		if n.Kind == resolve.FetchTreeNodeKindSingle {
			order = append(order, n.Item.Fetch.Dependencies().FetchID)
			return
		}
		for _, c := range n.ChildNodes {
			walk(c)
		}
	}
	walk(p.Response.Fetches)
	if len(order) != 4 {
		t.Fatalf("postprocess must schedule all 4 fetches, got %v", order)
	}
	pos := map[int]int{}
	for i, id := range order {
		pos[id] = i
	}
	if !(pos[0] < pos[1] && pos[1] < pos[2] && pos[2] < pos[3]) {
		t.Fatalf("postprocess must order the dependency chain P<Q<W<R, got tree order %v", order)
	}
}

// TestDedupEntityFetchUnion (M1 task-9 review, IMPORTANT 2a): the genuine entity-dedup positive case --
// two `_entities` groups to the same subgraph with the same Entry node, same representation key-set,
// and compatible (identical, unordered) dependencies union into ONE fetch whose document carries both
// selection sets and whose deps remap onto the surviving root group.
func TestDedupEntityFetchUnion(t *testing.T) {
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")

	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	fRoot := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "root", Subgraph: 1})
	wA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 1})
	idA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "id", Subgraph: 1})
	wB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 2})
	aB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "a", Subgraph: 2})
	bB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "b", Subgraph: 2})

	e0 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "root", Head: fRoot, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	e1 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: wA, Tails: []hypergraph.NodeID{fRoot}, Weight: 0})
	e2 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idA, Tails: []hypergraph.NodeID{wA}, Weight: 1})
	j1 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wB, Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 1000})
	e3 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: aB, Tails: []hypergraph.NodeID{wB}, Weight: 1})
	e4 := b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: bB, Tails: []hypergraph.NodeID{wB}, Weight: 1})
	h := b.Build()

	groups := []*fetchGroup{
		{Subgraph: 1, Jump: hypergraph.NoEdge, Entry: r, Edges: []hypergraph.EdgeID{e0, e1, e2}},
		{Subgraph: 2, Jump: j1, Entry: wB, RepKeys: []string{"Wrap.id"}, Edges: []hypergraph.EdgeID{e3}, DependsOn: []int{0}},
		{Subgraph: 2, Jump: j1, Entry: wB, RepKeys: []string{"Wrap.id"}, Edges: []hypergraph.EdgeID{e4}, DependsOn: []int{0}},
	}
	merged := dedupFetches(h, groups)
	if len(merged) != 2 {
		t.Fatalf("identical entity fetches must union into one: want 2 groups, got %d", len(merged))
	}
	entity := merged[1]
	if entity.Jump == hypergraph.NoEdge || entity.Subgraph != 2 {
		t.Fatalf("survivor order broken: second group must be the merged entity fetch, got %+v", entity)
	}
	if len(entity.DependsOn) != 1 || entity.DependsOn[0] != 0 {
		t.Fatalf("merged entity fetch deps must remap to the root group: got %v", entity.DependsOn)
	}
	doc := groupDocument(h, entity, emptyAliasMap())
	if !strings.Contains(doc, "_entities") || !strings.Contains(doc, "... on Wrap { a b }") {
		t.Fatalf("merged entity document must carry BOTH selection sets: %q", doc)
	}
}

// TestColocationGuardRejectsCostIncreasingMove (M1 task-9 review, IMPORTANT 2b): the guard is on
// C(K), NOT on fetch count. Same topology as planColocatableSplit but with an adversarial non-default
// weight (w for Wrap.b in B = 5000, a large w_s): co-locating b onto B would DROP one jump fetch
// (3 -> 2 fetches) yet INCREASE the folded C(K). The move must be rejected and the cover untouched.
func TestColocationGuardRejectsCostIncreasingMove(t *testing.T) {
	h, o, res := planAdversarialWeights(t)
	before := search.CoverCost(h, res.Cover.Edges)
	beforeGroups, _ := buildGroups(h, res.Cover)
	gb := goalOf(t, o, "Wrap", "b")
	selectedBefore := res.Cover.Selected[gb]

	// Prove the adversarial premise on the fixture itself: the candidate move reduces the fetch
	// count but increases C(K) -- exactly the case a fetch-count guard would wrongly accept.
	var altB hypergraph.NodeID
	found := false
	for _, alt := range o.Cand(gb) {
		if alt != selectedBefore && res.Pi[alt] < search.Inf {
			altB, found = alt, true
		}
	}
	if !found {
		t.Fatalf("fixture precondition: b must have a reachable alternative candidate")
	}
	trial := refold(h, o, res, gb, altB)
	trialGroups, _ := buildGroups(h, &search.Cover{Edges: trial, Selected: res.Cover.Selected})
	if len(trialGroups) >= len(beforeGroups) {
		t.Fatalf("fixture precondition: the move must reduce the fetch count (%d -> %d)", len(beforeGroups), len(trialGroups))
	}
	if search.CoverCost(h, trial) <= before {
		t.Fatalf("fixture precondition: the move must increase C(K) (%d -> %d)", before, search.CoverCost(h, trial))
	}

	if colocate(h, o, res) {
		t.Fatalf("guard is on C(K): a cost-increasing move must be rejected even though it reduces fetch count")
	}
	if got := search.CoverCost(h, res.Cover.Edges); got != before {
		t.Fatalf("rejected move must leave the cover untouched: C(K) %d -> %d", before, got)
	}
	if res.Cover.Selected[gb] != selectedBefore {
		t.Fatalf("rejected move must leave the goal selection untouched")
	}
}

// TestMergeInducedAliasing (M1 task-9 review, IMPORTANT 2c): the positive, load-bearing path of the
// Task-8 carry-forward. Two same-response-key member selections (`... on C1 { x }`, `... on C2 { x }`,
// x: Int vs String -- a SameResponseShape conflict) start in DIFFERENT documents (C2.x routed to the
// cheaper subgraph C), where D11.4 correctly mints NO alias. Co-location then folds C2.x into B's
// document: assignAliases, keyed on the POST-merge groupOf, must now mint injective _planv2_ aliases
// for both, and the response fields must read their values at the alias Paths while keeping rho as the
// client key.
//
// SWITCH-OFF-SCOPED (M1.5 wave-1b THE FLIP): this pins the LEGACY node-keyed path's co-location MERGE
// mechanism (per-goal C.4 routing that splits members across subgraphs, then folds them back), which
// is exclusive to the escape hatch -- the shipping obligation-driven path groups per response position
// and has no pre-merge split to fold. Both paths emit the IDENTICAL aliased document
// (`... on C1 { _planv2_x_0: x } ... on C2 { _planv2_x_1: x }`); they differ only in the subgraph the
// folded group targets -- legacy correctly picks B (the only subgraph resolving BOTH members), the new
// path picks the cheaper C on this adversarial-weight fixture (a corpus-invisible group-target
// resolvability gap, registered in the flip report; INVALID=0 across the whole audit corpus). The
// general conflicting-type aliasing property on the shipping path is covered by
// TestI4_CrossSubgraphAliasingPreservesClientKey.
func TestMergeInducedAliasing(t *testing.T) {
	h, o, res, op, def := planMergeInducedCollision(t)
	p, err := lowerWithConfig(h, o, res, op, def, LowerConfig{LegacyNodeKeyedGrouping: true}, nil, InfoConfig{})
	if err != nil {
		t.Fatal(err)
	}
	docs := fetchDocuments(p)
	if len(docs) != 1 {
		t.Fatalf("co-location must fold both member selections into ONE document, got %d: %v", len(docs), docs)
	}
	for _, want := range []string{"_planv2_x_0: x", "_planv2_x_1: x"} {
		if !strings.Contains(docs[0], want) {
			t.Fatalf("merged document must alias the colliding selection %q: %q", want, docs[0])
		}
	}
	sf := p.Response.RawFetches[0].Fetch.(*resolve.SingleFetch)
	if string(sf.DataSourceIdentifier) != "B" {
		t.Fatalf("merged fetch must target B, got %q", sf.DataSourceIdentifier)
	}
	// I4: client keys stay rho; the values are read at the fetch aliases (D11.4 response mapping).
	assertResponseShape(t, p, "{ w { ... on C1 { x } ... on C2 { x } } }")
	fields := responseFieldsNamed(p.Response.Data, "x")
	if len(fields) != 2 {
		t.Fatalf("want 2 response fields %q, got %d", "x", len(fields))
	}
	seen := map[string]bool{}
	for _, f := range fields {
		// Leaves are typed by scalar now; read the Path through the resolve.Node interface.
		vp := f.Value.NodePath()
		if len(vp) != 1 || !strings.HasPrefix(vp[0], "_planv2_x_") {
			t.Fatalf("merged-collision field must read at its alias, got Path=%v", vp)
		}
		if seen[vp[0]] {
			t.Fatalf("alias %q assigned to two response fields (alpha not injective)", vp[0])
		}
		seen[vp[0]] = true
	}
}

// planAdversarialWeights is planColocatableSplit with a non-default, adversarial weight: Wrap.b costs
// 5000 in B (huge w_s), so the co-location move that would save the C jump fetch INCREASES C(K).
func planAdversarialWeights(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	b.SetSubgraphName(3, "C")

	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	fRoot := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "root", Subgraph: 1})
	wA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 1})
	idA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "id", Subgraph: 1})
	wB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 2})
	aB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "a", Subgraph: 2})
	bB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "b", Subgraph: 2})
	wC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Wrap", Subgraph: 3})
	bC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Wrap", Field: "b", Subgraph: 3})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "root", Head: fRoot, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: wA, Tails: []hypergraph.NodeID{fRoot}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: idA, Tails: []hypergraph.NodeID{wA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wB, Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "a", Head: aB, Tails: []hypergraph.NodeID{wB}, Weight: 1})
	// ADVERSARIAL: resolving b in B is very expensive -- dropping the C jump does not pay for it.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: bB, Tails: []hypergraph.NodeID{wB}, Weight: 5000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: wC, Tails: []hypergraph.NodeID{idA}, KeyTails: []hypergraph.NodeID{idA}, Weight: 999})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "b", Head: bC, Tails: []hypergraph.NodeID{wC}, Weight: 1})

	h := b.Build()
	const schema = `schema { query: Query } type Query { root: Wrap } type Wrap { id: ID! a: String b: String }`
	o, _, _ := buildTree(t, h, schema, `{ root { a b } }`)
	res := runSearch(t, h, o)
	return h, o, res
}

// planMergeInducedCollision builds `{ w { ... on C1 { x } ... on C2 { x } } }` over union U = C1|C2
// with x: Int on C1 and x: String on C2 (a SameResponseShape conflict). Both subgraphs B and C resolve
// Query.w and declare BOTH members (Mem_B(U)=Mem_C(U)={C1,C2}, so D6 narrows nothing); C1.x exists only
// in B while C2.x is CHEAPER in C (TypeMove w=9 vs 10) -- per-goal routing splits the two members across
// two root fetches, and co-location folds C2.x into B's document, inducing the collision.
func planMergeInducedCollision(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "B")
	b.SetSubgraphName(2, "C")

	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	// Subgraph B: w -> U with both members and both x fields.
	fwB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "w", Subgraph: 1})
	uB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "U", Subgraph: 1})
	c1B := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "C1", Subgraph: 1})
	c2B := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "C2", Subgraph: 1})
	x1B := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "C1", Field: "x", Subgraph: 1})
	x2B := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "C2", Field: "x", Subgraph: 1})
	// Subgraph C: w -> U; declares both members (no D6 narrowing) but only C2.x is selectable.
	fwC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "w", Subgraph: 2})
	uC := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "U", Subgraph: 2})
	c2C := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "C2", Subgraph: 2})
	x2C := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "C2", Field: "x", Subgraph: 2})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "w", Head: fwB, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: uB, Tails: []hypergraph.NodeID{fwB}, Weight: 0})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "C1", Head: c1B, Tails: []hypergraph.NodeID{uB}, Weight: 10, Members: []string{"C1", "C2"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "C2", Head: c2B, Tails: []hypergraph.NodeID{uB}, Weight: 10, Members: []string{"C1", "C2"}})
	// OutputType mirrors hypergraph.Build (printedFieldOutputType): C1.x:Int vs C2.x:String is the
	// subgraph-local SameResponseShape conflict the D11.4 aliaser (both paths) decides on.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "x", Head: x1B, Tails: []hypergraph.NodeID{c1B}, Weight: 1, OutputType: "Int"})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "x", Head: x2B, Tails: []hypergraph.NodeID{c2B}, Weight: 1, OutputType: "String"})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "w", Head: fwC, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: uC, Tails: []hypergraph.NodeID{fwC}, Weight: 0})
	// CHEAPER TypeMove: per-goal C.4 routing sends C2.x to subgraph C pre-merge.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "C2", Head: c2C, Tails: []hypergraph.NodeID{uC}, Weight: 9, Members: []string{"C1", "C2"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "x", Head: x2C, Tails: []hypergraph.NodeID{c2C}, Weight: 1, OutputType: "String"})

	h := b.Build()
	const schema = `schema { query: Query } type Query { w: U } union U = C1 | C2 type C1 { x: Int } type C2 { x: String }`
	o, opDoc, defDoc := buildTree(t, h, schema, `{ w { ... on C1 { x } ... on C2 { x } } }`)
	res := runSearch(t, h, o)

	// Fixture preconditions: the two members split across subgraphs pre-merge (else nothing is
	// merge-induced) and neither is D6-narrowed.
	g1 := goalOf(t, o, "C1", "x")
	g2 := goalOf(t, o, "C2", "x")
	if o.Exempt(g1) || o.Exempt(g2) {
		t.Fatalf("fixture precondition: no D6 narrowing (both members in both Mem sets)")
	}
	if h.Node(res.Cover.Selected[g1]).Subgraph == h.Node(res.Cover.Selected[g2]).Subgraph {
		t.Fatalf("fixture precondition: C1.x and C2.x must route to different subgraphs pre-merge")
	}
	return h, o, res, opDoc, defDoc
}

// emptyAliasMap returns a no-alias aliasMap for direct groupDocument calls in structural tests.
func emptyAliasMap() *aliasMap {
	return &aliasMap{
		byEdge: map[hypergraph.EdgeID]string{},
		byOb:   map[obligation.ObID]string{},
		toResp: map[string]string{},
	}
}

// assertAcyclicDeps topologically sorts the groups' dependency graph and fails on any cycle.
func assertAcyclicDeps(t *testing.T, groups []*fetchGroup) {
	t.Helper()
	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := make([]int, len(groups))
	var visit func(i int)
	visit = func(i int) {
		if state[i] == black {
			return
		}
		if state[i] == grey {
			t.Fatalf("dependency cycle through group %d: %+v", i, depsOf(groups))
		}
		state[i] = grey
		for _, d := range groups[i].DependsOn {
			visit(d)
		}
		state[i] = black
	}
	for i := range groups {
		visit(i)
	}
}

func depsOf(groups []*fetchGroup) [][]int {
	out := make([][]int, len(groups))
	for i, g := range groups {
		out[i] = g.DependsOn
	}
	return out
}
