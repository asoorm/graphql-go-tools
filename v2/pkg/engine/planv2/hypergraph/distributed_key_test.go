package hypergraph

import (
	"testing"

	. "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
)

// D7ppp -- distributed @key (FORMAL_SPEC). These tests pin the compiled graph shape on the
// complex-entity-call configuration: composite keys no single source supplies.

// jumpsInto returns every EntityJump edge headed at the PLAIN (un-scoped) object (typeName, sid).
func jumpsInto(h *Hypergraph, typeName string, sid SubgraphID) []EdgeID {
	var out []EdgeID
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind != EdgeEntityJump {
			continue
		}
		head := h.Node(e.Head)
		if head.Type == typeName && head.Subgraph == sid && head.Scope == "" {
			out = append(out, id)
		}
	}
	return out
}

// tailSubgraphs returns the distinct subgraph ids of an edge's field-node tails.
func tailSubgraphs(h *Hypergraph, id EdgeID) map[SubgraphID]bool {
	out := map[SubgraphID]bool{}
	for _, tail := range h.Edge(id).Tails {
		if n := h.Node(tail); n.Kind == NodeField {
			out[n.Subgraph] = true
		}
	}
	return out
}

func TestBuildDistributedKey_ProductJumpIntoPrice(t *testing.T) {
	h, err := Build(DistributedKeyConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	price := subgraphIDByName(t, h, "price")
	products := subgraphIDByName(t, h, "products")
	link := subgraphIDByName(t, h, "link")

	// D7ppp: price's Product key `id pid category{id tag}` has no full supplier (products lacks pid,
	// link/list lack category) -- base D7 emitted NO jump; the distributed construction must.
	jumps := jumpsInto(h, "Product", price)
	if len(jumps) == 0 {
		t.Fatal("D7ppp: no EntityJump into (Product, price) -- distributed key not modelled")
	}
	// Every edge: marked distributed, carries the raw key, spans >1 subgraph, and never assigns a
	// coordinate to the target itself (D7ppp(3)).
	coherent := false
	for _, j := range jumps {
		e := h.Edge(j)
		if !e.KeyDistributed {
			t.Fatalf("jump %d into (Product, price) not marked KeyDistributed", j)
		}
		if e.KeySelection != "id pid category{id tag}" {
			t.Fatalf("jump %d KeySelection = %q, want the raw key", j, e.KeySelection)
		}
		sgs := tailSubgraphs(h, j)
		if sgs[price] {
			t.Fatalf("D7ppp(3): jump %d assigns a key coordinate to the TARGET subgraph price: %v",
				j, tailCoordsWithSubgraph(h, j))
		}
		if len(sgs) < 2 {
			t.Fatalf("distributed jump %d tails live in one subgraph: %v", j, tailCoordsWithSubgraph(h, j))
		}
		// The path-coherent cheap vector: id+category(+id,tag) in products, pid in link.
		var idAtProducts, pidAtLink, catAtProducts, tagAtProducts bool
		for _, tc := range tailCoordsWithSubgraph(h, j) {
			switch tc {
			case "Product.id@products":
				idAtProducts = true
			case "Product.pid@link":
				pidAtLink = true
			case "Product.category@products":
				catAtProducts = true
			case "Category.tag@products":
				tagAtProducts = true
			}
		}
		if idAtProducts && pidAtLink && catAtProducts && tagAtProducts {
			coherent = true
		}
	}
	if !coherent {
		t.Fatal("D7ppp(2): no assignment vector with id/category@products + pid@link (the path-coherent vector)")
	}
	_ = products
	_ = link
}

func TestBuildDistributedKey_ProductListJumpIntoPrice(t *testing.T) {
	h, err := Build(DistributedKeyConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	price := subgraphIDByName(t, h, "price")

	// price's ProductList key additionally needs selected{id} -- resolvable outside the target only
	// in list. Every distributed edge must carry the EXPLICIT path tails (D7ppp(2)): the
	// ProductList.products path in its assigned subgraph and ProductList.selected@list.
	jumps := jumpsInto(h, "ProductList", price)
	if len(jumps) == 0 {
		t.Fatal("D7ppp: no EntityJump into (ProductList, price)")
	}
	for _, j := range jumps {
		var productsPath, selectedAtList bool
		for _, tc := range tailCoordsWithSubgraph(h, j) {
			if tc == "ProductList.products@products" || tc == "ProductList.products@list" {
				productsPath = true
			}
			if tc == "ProductList.selected@list" {
				selectedAtList = true
			}
		}
		if !productsPath {
			t.Fatalf("D7ppp(2): jump %d carries no explicit ProductList.products path tail: %v",
				j, tailCoordsWithSubgraph(h, j))
		}
		if !selectedAtList {
			t.Fatalf("D7ppp(2): jump %d carries no ProductList.selected@list path tail: %v",
				j, tailCoordsWithSubgraph(h, j))
		}
	}
}

func TestBuildDistributedKey_GateZeroDrift(t *testing.T) {
	h, err := Build(DistributedKeyConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	list := subgraphIDByName(t, h, "list")
	price := subgraphIDByName(t, h, "price")
	link := subgraphIDByName(t, h, "link")

	// D7ppp(1) gate: list's ProductList key `products{id pid}` IS fully supplied by price, so it keeps
	// exactly the base-D7 single-source jumps -- no distributed edge into (ProductList, list).
	for _, j := range jumpsInto(h, "ProductList", list) {
		e := h.Edge(j)
		if e.KeyDistributed {
			t.Fatalf("D7ppp(1): (ProductList, list) has a full supplier (price); distributed edge %d must not exist", j)
		}
		sgs := tailSubgraphs(h, j)
		if len(sgs) != 1 || !sgs[price] {
			t.Fatalf("plain jump %d into (ProductList, list) must source wholly from price: %v",
				j, tailCoordsWithSubgraph(h, j))
		}
	}
	// D7ppp(5): plain jumps carry the raw key too (KeySelection recorded on every D7-family jump),
	// but stay unmarked.
	for _, j := range jumpsInto(h, "Product", link) {
		e := h.Edge(j)
		if e.KeyDistributed {
			t.Fatalf("plain jump %d into (Product, link) must not be marked distributed", j)
		}
		if e.KeySelection == "" {
			t.Fatalf("D7ppp(5): plain jump %d into (Product, link) must record its raw KeySelection", j)
		}
	}
}
