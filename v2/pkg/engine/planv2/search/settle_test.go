package search

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// TestSettlePartialUnionPi is the Go analog of TLA PU_ExpectedPi (Section 7.1). SETTLE over the
// partial-union H must produce the spec-committed pi progression: subgraph-enter (Wrapper,A)=1000,
// Wrapper.action=1001, the TypeMove head (Common,A)=1002, and the leaf (Common,A).c=1003.
func TestSettlePartialUnionPi(t *testing.T) {
	h := buildPartialUnionH(t)
	pi, _, _, err := settle(h, Config{Combine: Sum, StateCap: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	assertPi(t, h, pi, "Wrapper", "A", "", 1000) // (Query,A).wrapper enter -> Descent
	assertPiField(t, h, pi, "Wrapper", "A", "action", 1001)
	assertPi(t, h, pi, "Common", "A", "", 1002) // TypeMove Action -> Common head
	assertPiField(t, h, pi, "Common", "A", "c", 1003)
}

// TestSettleEntityJumpPi is the Go analog of TLA EJ_ExpectedPi (Section 7.2). The multi-tail EntityJump
// into (Product,B) folds five settled tails (Product.id=1001, Organization.id=1002, and the three
// @requires dimensions=1002 each) atop w_f+w_d=1010, giving pi((Product,B))=6019 and its leaf
// shippingEstimate=6020.
func TestSettleEntityJumpPi(t *testing.T) {
	h := buildEntityJumpH(t)
	pi, _, _, err := settle(h, Config{Combine: Sum, StateCap: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	assertPi(t, h, pi, "Product", "B", "", 6019)
	assertPiField(t, h, pi, "Product", "B", "shippingEstimate", 6020)
}

// TestSettleCompetingExtractMinPicksMinimum is the R1 guard (PROOFS T3.1;
// PlannerSearchCompeting.cfg): node fx is reachable both directly (f=1001) and via a jump detour
// (f=2012). Keying EXTRACT-MIN on tentativeF (A-2) settles fx at 1001; keying on anything else
// (min tail pi, w(e), head's current pi) silently admits 2012 and destroys optimality.
func TestSettleCompetingExtractMinPicksMinimum(t *testing.T) {
	h := buildCompetingH(t)
	pi, _, _, err := settle(h, Config{Combine: Sum, StateCap: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	assertPiNode(t, h, pi, "fx", 1001)   // direct derivation wins over the 2012 detour
	assertPiNode(t, h, pi, "FooB", 2011) // fx(1001) + entity jump(1010)
}

// TestSettleStateCapTrips exercises the L7 in-search backstop: a StateCap of 1 must surface a typed
// *ErrSearchStateCap, never a silently truncated (degraded) pi.
func TestSettleStateCapTrips(t *testing.T) {
	h := buildEntityJumpH(t)
	_, _, _, err := settle(h, Config{Combine: Sum, StateCap: 1})
	if _, ok := err.(*ErrSearchStateCap); !ok {
		t.Fatalf("want *ErrSearchStateCap, got %v", err)
	}
}

// TestSettleStatsWithinBounds is the T5 property that Task 6's TestT5_PushExtractBounds builds on:
// each edge is pushed at most once and extracted at most once, so pushes/extracts/states are all
// bounded by |E| (Section 6.2). Visited stays 0 -- it is SEARCH's traceback counter, not SETTLE's.
func TestSettleStatsWithinBounds(t *testing.T) {
	h := buildPartialUnionH(t)
	_, _, stats, err := settle(h, Config{Combine: Sum, StateCap: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	e := int64(h.NumEdges())
	if stats.Pushes > e || stats.Extracts > e || stats.States > e {
		t.Fatalf("stats exceed |E|=%d: %+v", e, stats)
	}
	if stats.Extracts > stats.Pushes {
		t.Fatalf("extracts %d must not exceed pushes %d", stats.Extracts, stats.Pushes)
	}
	if stats.Visited != 0 {
		t.Fatalf("SETTLE must not touch Visited (SEARCH traceback owns it), got %d", stats.Visited)
	}
}

// TestPreflightPlanTooLarge exercises the L19 pre-flight guard (Section 6.3): the cheap structural bound
// est = Sum_g |cand(g)|*maxFanIn(H) surfaces a typed *ErrPlanTooLarge above the cap, is disabled at
// cap 0, and passes cleanly under a generous cap -- never a silent degrade.
func TestPreflightPlanTooLarge(t *testing.T) {
	h := buildPartialUnionH(t)
	tree := buildPartialUnionTree(t, h)

	if err := preflight(h, tree, Config{PreflightCap: 0}); err != nil {
		t.Fatalf("cap 0 disables pre-flight, got %v", err)
	}
	if err := preflight(h, tree, Config{PreflightCap: 1 << 40}); err != nil {
		t.Fatalf("generous cap must pass, got %v", err)
	}
	err := preflight(h, tree, Config{PreflightCap: 1})
	pe, ok := err.(*ErrPlanTooLarge)
	if !ok {
		t.Fatalf("want *ErrPlanTooLarge, got %v", err)
	}
	if pe.Est <= pe.Cap {
		t.Fatalf("ErrPlanTooLarge.Est %d must exceed Cap %d", pe.Est, pe.Cap)
	}
}

// --- fixtures & helpers ------------------------------------------------------------------

func buildPartialUnionH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	h, err := hypergraph.Build(hgtestdata.PartialUnionConfig(), hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query"},
	})
	if err != nil {
		t.Fatalf("build partial-union H: %v", err)
	}
	return h
}

func buildEntityJumpH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	h, err := hypergraph.Build(hgtestdata.EntityJumpConfig(), hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query"},
	})
	if err != nil {
		t.Fatalf("build entity-jump H: %v", err)
	}
	return h
}

// buildCompetingH hand-builds the PlannerSearchCompeting instance: fx has a direct derivation
// (f=1001) and a jump detour through Mid (pi=1000, jump w=1012 -> f=2012); FooB then jumps off fx
// (1001 + 1010 = 2011). It is deliberately built with explicit weights so the golden f-values are
// self-evident from the fixture.
func buildCompetingH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "q"})
	mid := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Mid", Subgraph: 2})
	fx := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "fx", Subgraph: 1})
	foob := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "FooB", Subgraph: 2})

	// mid reachable from the root at 1000.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "mid", Head: mid, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	// fx direct: f = 1001 + pi[r]=0 = 1001.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "fx", Head: fx, Tails: []hypergraph.NodeID{r}, Weight: 1001})
	// fx detour: f = 1012 + pi[mid]=1000 = 2012. EXTRACT-MIN must NOT settle fx here.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: fx, Tails: []hypergraph.NodeID{mid}, Weight: 1012})
	// FooB jumps off fx: f = 1010 + pi[fx]=1001 = 2011.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: foob, Tails: []hypergraph.NodeID{fx}, Weight: 1010})
	return b.Build()
}

func buildPartialUnionTree(t *testing.T, h *hypergraph.Hypergraph) *obligation.Tree {
	t.Helper()
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
	return tree
}

// nodeID locates the (Type, subgraph, field) node: an object node when field=="", else a field
// node. It is O(|V|) -- fine for tests.
func nodeID(t *testing.T, h *hypergraph.Hypergraph, typ, subgraph, field string) hypergraph.NodeID {
	t.Helper()
	wantKind := hypergraph.NodeObject
	if field != "" {
		wantKind = hypergraph.NodeField
	}
	for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
		n := h.Node(id)
		if n.Kind == wantKind && n.Type == typ && n.Field == field && h.SubgraphName(n.Subgraph) == subgraph {
			return id
		}
	}
	t.Fatalf("no node (type=%q subgraph=%q field=%q kind=%v)", typ, subgraph, field, wantKind)
	return 0
}

func assertPi(t *testing.T, h *hypergraph.Hypergraph, pi []int64, typ, subgraph, field string, want int64) {
	t.Helper()
	id := nodeID(t, h, typ, subgraph, field)
	if pi[id] != want {
		t.Fatalf("pi(%s,%s).%q = %d, want %d", typ, subgraph, field, pi[id], want)
	}
}

func assertPiField(t *testing.T, h *hypergraph.Hypergraph, pi []int64, typ, subgraph, field string, want int64) {
	t.Helper()
	assertPi(t, h, pi, typ, subgraph, field, want)
}

// assertPiNode matches a node by Type alone (the hand-built competing fixture names its object
// nodes uniquely).
func assertPiNode(t *testing.T, h *hypergraph.Hypergraph, pi []int64, typ string, want int64) {
	t.Helper()
	for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
		if h.Node(id).Type == typ {
			if pi[id] != want {
				t.Fatalf("pi(%s) = %d, want %d", typ, pi[id], want)
			}
			return
		}
	}
	t.Fatalf("no node with type %q", typ)
}
