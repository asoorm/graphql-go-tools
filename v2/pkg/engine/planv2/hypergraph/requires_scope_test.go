package hypergraph

import (
	"testing"

	. "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
)

// D7pp -- requires-scoped resolution (FORMAL_SPEC). These tests pin the compiled graph shape on the
// requires-chain (nested @requires) and distributed-@requires configurations.

// findScopedJumps returns every EntityJump whose head is a REQUIRES-SCOPE node (Scope != "") of the
// given type/subgraph, keyed for inspection.
func findScopedJumps(h *Hypergraph, typeName string, sid SubgraphID, scope string) []EdgeID {
	var out []EdgeID
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind != EdgeEntityJump {
			continue
		}
		head := h.Node(e.Head)
		if head.Type == typeName && head.Subgraph == sid && head.Scope == scope {
			out = append(out, id)
		}
	}
	return out
}

// tailCoordsWithSubgraph renders an edge's field-node tails as "Type.field@subgraphName".
func tailCoordsWithSubgraph(h *Hypergraph, id EdgeID) []string {
	var out []string
	for _, tail := range h.Edge(id).Tails {
		n := h.Node(tail)
		if n.Kind == NodeField {
			out = append(out, n.Type+"."+n.Field+"@"+h.SubgraphName(n.Subgraph))
		}
	}
	return out
}

// fieldEdgeTailScope returns the Scope of the object node the (typeName, subgraph).field Field edge
// hangs off, and whether the edge exists.
func fieldEdgeTailScope(h *Hypergraph, typeName string, sid SubgraphID, field string) (string, bool) {
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind != EdgeField {
			continue
		}
		n := h.Node(e.Head)
		if n.Kind != NodeField || n.Type != typeName || n.Subgraph != sid || n.Field != field {
			continue
		}
		if len(e.Tails) != 1 {
			continue
		}
		return h.Node(e.Tails[0]).Scope, true
	}
	return "", false
}

func TestBuildRequiresScope_FieldEdgeReRooted(t *testing.T) {
	h, err := Build(RequiresChainConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	c := subgraphIDByName(t, h, "c")
	// D7pp(1): the @requires field's Field edge hangs off its requires-scope node, never the plain
	// object node -- a locally-descended (Product,c) cannot resolve isExpensive (the bypass is gone).
	scope, ok := fieldEdgeTailScope(h, "Product", c, "isExpensive")
	if !ok {
		t.Fatal("no Field edge for (Product,c).isExpensive")
	}
	if scope == "" {
		t.Fatal("D7pp: (Product,c).isExpensive resolves from the PLAIN object node -- requires-bypass still modelled")
	}
	// A non-requires field keeps its plain tail.
	scope, ok = fieldEdgeTailScope(h, "Product", c, "id")
	if !ok || scope != "" {
		t.Fatalf("(Product,c).id must resolve from the plain object node (scope=%q ok=%v)", scope, ok)
	}
}

func TestBuildRequiresScope_PlainJumpKeyOnly(t *testing.T) {
	h, err := Build(RequiresChainConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	// D7pp(2): the plain jump into (Product,c) exists (base D7 emitted NONE -- no single source
	// resolves both price and hasDiscount) and carries key tails only.
	jump := findEntityJump(t, h, "Product", "c") // findEntityJump matches the un-scoped head first
	e := h.Edge(jump)
	if h.Node(e.Head).Scope != "" {
		t.Fatalf("expected a plain (un-scoped) jump into (Product,c); got scope %q", h.Node(e.Head).Scope)
	}
	if len(e.Requires) != 0 {
		t.Fatalf("plain jump must carry no @requires ride-along; got %v", e.Requires)
	}
	for _, l := range tailFieldLabels(h, jump) {
		if l != "Product.id" {
			t.Fatalf("plain jump tails must be key-only; got %v", tailFieldLabels(h, jump))
		}
	}
}

func TestBuildRequiresScope_ScopedJumpsAndDistributedTails(t *testing.T) {
	h, err := Build(RequiresChainConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	c := subgraphIDByName(t, h, "c")
	d := subgraphIDByName(t, h, "d")

	// D7pp(3): a scoped jump into (Product,c | req isExpensive) exists and carries the price tail;
	// at least one variant resolves price in subgraph a (the owner).
	isExpScope, _ := fieldEdgeTailScope(h, "Product", c, "isExpensive")
	jumps := findScopedJumps(h, "Product", c, isExpScope)
	if len(jumps) == 0 {
		t.Fatal("D7pp: no requires-scoped jump into (Product,c | isExpensive)")
	}
	foundPriceAtA := false
	for _, j := range jumps {
		e := h.Edge(j)
		if len(e.Requires) != 1 || len(e.RequiresBy) != 1 || e.RequiresBy[0] != "isExpensive" {
			t.Fatalf("scoped jump must carry exactly its own requires (isExpensive); got %v / %v", e.Requires, e.RequiresBy)
		}
		for _, tc := range tailCoordsWithSubgraph(h, j) {
			if tc == "Product.price@a" {
				foundPriceAtA = true
			}
		}
	}
	if !foundPriceAtA {
		t.Fatal("D7pp(4): no scoped jump into (Product,c | isExpensive) carries the Product.price@a tail")
	}

	// D7pp(4) nested chain: a scoped jump into (Product,d | req canAfford) whose isExpensive tail is
	// the subgraph-c field node -- itself resolvable only behind c's own requires scope. AND-relaxation
	// orders the chain; no ride-along blocks it.
	canAffScope, ok := fieldEdgeTailScope(h, "Product", d, "canAfford")
	if !ok || canAffScope == "" {
		t.Fatal("no requires-scope for (Product,d).canAfford")
	}
	jumps = findScopedJumps(h, "Product", d, canAffScope)
	foundIsExpAtC := false
	for _, j := range jumps {
		for _, tc := range tailCoordsWithSubgraph(h, j) {
			if tc == "Product.isExpensive@c" {
				foundIsExpAtC = true
			}
		}
	}
	if !foundIsExpAtC {
		t.Fatal("D7pp(4): no scoped jump into (Product,d | canAfford) carries the Product.isExpensive@c tail")
	}
}

func TestBuildRequiresScope_DistributedPathAndSameSubgraphRelay(t *testing.T) {
	h, err := Build(RequiresDistributedConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	b := subgraphIDByName(t, h, "b")
	byNoviceScope, ok := fieldEdgeTailScope(h, "Post", b, "byNovice")
	if !ok || byNoviceScope == "" {
		t.Fatal("no requires-scope for (Post,b).byNovice")
	}
	jumps := findScopedJumps(h, "Post", b, byNoviceScope)
	if len(jumps) == 0 {
		t.Fatal("D7pp: no requires-scoped jump into (Post,b | byNovice)")
	}
	// D7pp(3) same-subgraph relay: a scoped jump SOURCED IN b itself (key tail Post.id@b) must exist --
	// v1's b->a->b relay re-enters the subgraph with the gathered inputs.
	// D7pp(4) distributed leaf: the yearsOfExperience tail resolves in a (b's copy is @external
	// non-key). The path field `author` is b-resolvable, so the same-subgraph variant carries no path
	// tail; a source that does NOT resolve the path (a) carries Post.author@b.
	sameSubgraph, leafAtA := false, false
	for _, j := range jumps {
		coords := tailCoordsWithSubgraph(h, j)
		hasKeyB := false
		for _, tc := range coords {
			if tc == "Post.id@b" {
				hasKeyB = true
			}
			if tc == "Author.yearsOfExperience@a" {
				leafAtA = true
			}
		}
		if hasKeyB {
			sameSubgraph = true
		}
	}
	if !sameSubgraph {
		t.Fatal("D7pp(3): no same-subgraph (b->b) requires-scoped jump into (Post,b | byNovice)")
	}
	if !leafAtA {
		t.Fatal("D7pp(4): no scoped jump into (Post,b | byNovice) carries the Author.yearsOfExperience@a tail")
	}
}
