package hypergraph

import "testing"

func TestAddNodeInternsByIdentity(t *testing.T) {
	b := NewBuilder()
	a := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 1})
	again := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 1})
	other := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 2})
	if a != again {
		t.Fatalf("identical nodes must intern to one id: %d != %d", a, again)
	}
	if a == other {
		t.Fatalf("same type in different subgraphs must be distinct nodes (D4)")
	}
}

func TestAddEdgeDedupsByIdentityTuple(t *testing.T) {
	b := NewBuilder()
	p := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 1})
	f := b.AddNode(Node{Kind: NodeField, Type: "Product", Subgraph: 1, Field: "id"})
	e1 := b.AddEdge(Edge{Kind: EdgeField, Label: "id", Head: f, Tails: []NodeID{p}, Weight: 1})
	// same identity tuple discovered via a second D1 config route (A-3, gap G6)
	e2 := b.AddEdge(Edge{Kind: EdgeField, Label: "id", Head: f, Tails: []NodeID{p}, Weight: 1})
	if e1 != e2 {
		t.Fatalf("edges with equal identity tuples must dedup to one (A-3): %d != %d", e1, e2)
	}
	if got := b.Build().NumEdges(); got != 1 {
		t.Fatalf("E is a set: want 1 edge, got %d", got)
	}
}

func TestAddEdgeUnionsConditionsOnDedup(t *testing.T) {
	condA := KeyCondition{Coordinates: []string{"Product.category"}, FieldPath: []string{"category"}}
	condB := KeyCondition{Coordinates: []string{"Product.brand"}, FieldPath: []string{"brand"}}

	newPair := func(b *Builder) (NodeID, NodeID) {
		p := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 1})
		q := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 2})
		return p, q
	}

	t.Run("two conditional adds OR-union", func(t *testing.T) {
		b := NewBuilder()
		p, q := newPair(b)
		e1 := b.AddEdge(Edge{Kind: EdgeEntityJump, Head: q, Tails: []NodeID{p}, Conditions: []KeyCondition{condA}})
		e2 := b.AddEdge(Edge{Kind: EdgeEntityJump, Head: q, Tails: []NodeID{p}, Conditions: []KeyCondition{condB}})
		if e1 != e2 {
			t.Fatalf("same identity tuple must dedup (A-3): %d != %d", e1, e2)
		}
		got := b.Build().Edge(e1).Conditions
		if len(got) != 2 || !equalCondition(got[0], condA) || !equalCondition(got[1], condB) {
			t.Fatalf("conditions must union to {condA, condB}, got %+v", got)
		}
	})

	t.Run("unconditional add subsumes prior conditions", func(t *testing.T) {
		b := NewBuilder()
		p, q := newPair(b)
		e1 := b.AddEdge(Edge{Kind: EdgeEntityJump, Head: q, Tails: []NodeID{p}, Conditions: []KeyCondition{condA}})
		b.AddEdge(Edge{Kind: EdgeEntityJump, Head: q, Tails: []NodeID{p}}) // nil Conditions
		if got := b.Build().Edge(e1).Conditions; got != nil {
			t.Fatalf("unconditional discovery must make the edge unconditional, got %+v", got)
		}
	})

	t.Run("prior unconditional stays unconditional", func(t *testing.T) {
		b := NewBuilder()
		p, q := newPair(b)
		e1 := b.AddEdge(Edge{Kind: EdgeEntityJump, Head: q, Tails: []NodeID{p}})
		b.AddEdge(Edge{Kind: EdgeEntityJump, Head: q, Tails: []NodeID{p}, Conditions: []KeyCondition{condA}})
		if got := b.Build().Edge(e1).Conditions; got != nil {
			t.Fatalf("edge already unconditional must ignore later conditions, got %+v", got)
		}
	})

	t.Run("idempotent no duplicate entries", func(t *testing.T) {
		b := NewBuilder()
		p, q := newPair(b)
		// duplicate within one add AND across a repeated add
		e1 := b.AddEdge(Edge{Kind: EdgeEntityJump, Head: q, Tails: []NodeID{p}, Conditions: []KeyCondition{condA, condA}})
		b.AddEdge(Edge{Kind: EdgeEntityJump, Head: q, Tails: []NodeID{p}, Conditions: []KeyCondition{condA}})
		got := b.Build().Edge(e1).Conditions
		if len(got) != 1 || !equalCondition(got[0], condA) {
			t.Fatalf("condition union must be idempotent: want exactly {condA}, got %+v", got)
		}
	})
}

func TestBuildPrunesIsolatedNodes(t *testing.T) {
	b := NewBuilder()
	r := b.AddNode(Node{Kind: NodeRoot, Field: "query"})
	p := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 1})
	_ = b.AddNode(Node{Kind: NodeObject, Type: "Orphan", Subgraph: 1}) // isolated
	b.AddEdge(Edge{Kind: EdgeField, Label: "product", Head: p, Tails: []NodeID{r}, Weight: 1000})
	h := b.Build()
	if h.NumNodes() != 2 { // Orphan pruned so |V| = O(size(H)) holds (T5/G2)
		t.Fatalf("isolated node must be pruned: want 2 nodes, got %d", h.NumNodes())
	}
}

func TestAddEdgeDoesNotMutateCallerSlices(t *testing.T) {
	b := NewBuilder()
	r := b.AddNode(Node{Kind: NodeRoot, Field: "query"})
	p := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 1})
	q := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 2})
	tails := []NodeID{q, p, r} // deliberately unsorted; caller keeps a reference
	members := []string{"Zeta", "Alpha"}
	b.AddEdge(Edge{Kind: EdgeTypeMove, Label: "Action", Head: p, Tails: tails, Members: members})
	if tails[0] != q || tails[1] != p || tails[2] != r {
		t.Fatalf("AddEdge must not reorder the caller's Tails slice: got %v", tails)
	}
	if members[0] != "Zeta" || members[1] != "Alpha" {
		t.Fatalf("AddEdge must not reorder the caller's Members slice: got %v", members)
	}
}

func TestTailIncidenceAndIncoming(t *testing.T) {
	b := NewBuilder()
	r := b.AddNode(Node{Kind: NodeRoot, Field: "query"})
	p := b.AddNode(Node{Kind: NodeObject, Type: "Product", Subgraph: 1})
	e := b.AddEdge(Edge{Kind: EdgeField, Label: "product", Head: p, Tails: []NodeID{r}, Weight: 1000})
	h := b.Build()
	if in := h.Incoming(p); len(in) != 1 || in[0] != e {
		t.Fatalf("Incoming(p) must be [e]")
	}
	if ti := h.TailIncidence(r); len(ti) != 1 || ti[0] != e {
		t.Fatalf("TailIncidence(r) must be [e]")
	}
}
