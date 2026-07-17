package search

// consistent.go -- the CHAIN-LAYERED consistent trace (FORMAL_SPEC D10 amendment -- chain-layered
// consistent trace; M2 class-C wave).
//
// The per-goal scoped walk (scope.go) traces a goal's route over a masked settle's back-edge table.
// That table keeps ONE derivation per node, and object nodes are keyed by (type, subgraph) only -- so
// when a goal's obligation chain revisits a TYPE it already passed (an expanded member position
// `products.~Book.reviews.product` whose `product` is the same abstract type the chain started on),
// the cheapest derivation of the shared node serves the SHALLOW position and the traced walk
// collapses: its Field-edge labels no longer spell the obligation chain. Lowering then cannot
// attribute the goal's positions (the walk is "phantom") and the subtree falls through to a parent
// group in the wrong subgraph -- the nested class-C signature.
//
// The refinement: when (and only when) a goal's default scoped walk is NOT chain-consistent, re-trace
// it over the CHAIN-LAYERED product of the masked graph with the goal's obligation chain -- states
// (node, i) where i counts the chain fields consumed root->leaf. On the layered graph the depth
// aliasing disappears (the same node at two chain depths is two states), so the minimum-cost
// chain-consistent walk to a candidate is found exactly, including the entity jumps the chain needs
// at each level. Spine transitions:
//
//   - a Field edge advances i iff its label is the chain's next field (off-chain Field edges are
//     never spine steps -- they exist in walks only as jump-tail sub-walks);
//   - Descent / TypeMove edges keep i (path-neutral refinement/descent);
//   - an EntityJump keeps i, steps from a pre-jump object of the edge (the enclosing object of a key
//     tail) to the head, and is usable iff every tail is reachable on the goal's masked table (the
//     tails' own sub-walks are traced from that table, exactly as the default trace does).
//
// CONDITIONAL: a goal whose default walk is already chain-consistent keeps it untouched -- selection,
// cost accounting (Cover.Edges/Cost), and every consistent plan are byte-identical. A goal with no
// chain-consistent route anywhere keeps the default behavior (the typed-loud D10 fall-back). The
// refinement changes Cover.Walks (and, for a repaired goal, Cover.Selected -- the candidate the
// consistent route reaches) -- the inputs of obligation-driven lowering -- never the settle tables.

import (
	"container/heap"
	"slices"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// chainFields returns goal g's obligation chain as root->leaf schema field labels (Refine/Typename
// obligations contribute no label). This is the label sequence a chain-consistent walk's spine must
// spell -- the same comparison lowering's position attribution makes.
func chainFields(o *obligation.Tree, g obligation.GoalID) []string {
	obs := o.Obligations()
	ob := o.Ob(g)
	var rev []string
	for guard := 0; guard < 1<<16; guard++ {
		if ob.Kind == obligation.Field {
			rev = append(rev, ob.Field)
		}
		if ob.Parent == obligation.NoParent || ob.Parent == ob.ID || int(ob.Parent) >= len(obs) {
			break
		}
		ob = obs[ob.Parent]
	}
	slices.Reverse(rev)
	return rev
}

// walkChainConsistent reports whether a traced walk's spine spells the goal's chain fields exactly --
// the same up-walk the lowering layer performs (leaf->root through the walk's produced-by map,
// ascending an entity jump through the enclosing object of its key tails).
func walkChainConsistent(h *hypergraph.Hypergraph, walk []hypergraph.EdgeID, leaf hypergraph.NodeID, fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	producedBy := make(map[hypergraph.NodeID]hypergraph.EdgeID, len(walk))
	for _, e := range walk {
		producedBy[h.EdgeHead(e)] = e
	}
	var revLabels []string
	cur := leaf
	for guard := 0; guard < 1<<16; guard++ {
		e, ok := producedBy[cur]
		if !ok {
			break
		}
		edge := h.Edge(e)
		if edge.Kind == hypergraph.EdgeField {
			revLabels = append(revLabels, edge.Label)
			if len(revLabels) > len(fields) {
				return false
			}
		}
		if edge.Kind == hypergraph.EdgeEntityJump {
			if pre, ok := jumpPreObject(h, producedBy, edge); ok {
				cur = pre
				continue
			}
		}
		if len(edge.Tails) == 0 {
			break
		}
		cur = edge.Tails[0]
	}
	if len(revLabels) != len(fields) {
		return false
	}
	for i, l := range revLabels {
		if l != fields[len(fields)-1-i] {
			return false
		}
	}
	return true
}

// jumpPreObject locates a jump's pre-jump entity object within a walk: the object every key tail's
// Field edge hangs off (the shallowest common ancestor for a nested key). Mirrors the lowering
// layer's spine-parent derivation so search and lowering judge consistency identically.
func jumpPreObject(h *hypergraph.Hypergraph, producedBy map[hypergraph.NodeID]hypergraph.EdgeID,
	edge hypergraph.Edge) (hypergraph.NodeID, bool) {

	tails := edge.KeyTails
	if len(tails) == 0 {
		tails = edge.Tails
	}
	var objs []hypergraph.NodeID
	for _, t := range tails {
		e, ok := producedBy[t]
		if !ok || len(h.Edge(e).Tails) == 0 {
			continue
		}
		objs = append(objs, h.Edge(e).Tails[0])
	}
	if len(objs) == 0 {
		return 0, false
	}
	ancestorsOf := func(n hypergraph.NodeID) map[hypergraph.NodeID]bool {
		out := map[hypergraph.NodeID]bool{n: true}
		cur := n
		for guard := 0; guard < 1<<16; guard++ {
			e, ok := producedBy[cur]
			if !ok || len(h.Edge(e).Tails) == 0 {
				break
			}
			cur = h.Edge(e).Tails[0]
			if out[cur] {
				break
			}
			out[cur] = true
		}
		return out
	}
	ancs := make([]map[hypergraph.NodeID]bool, len(objs))
	for i, o := range objs {
		ancs[i] = ancestorsOf(o)
	}
	for _, o := range objs {
		common := true
		for j := range objs {
			if !ancs[j][o] {
				common = false
				break
			}
		}
		if common {
			return o, true
		}
	}
	return objs[0], true
}

// layeredIndex holds the edge indexes the layered trace shares across goals. It is a pure function
// of the hypergraph -- no mask, obligation, or other per-Search state enters buildLayeredIndex -- and
// is read-only after construction, so one index is cached per graph (layeredIndexFor) and shared by
// every Search over it, including concurrent ones. Per-call state (the goal's mask, its settle
// table) stays in consistentTrace's parameters.
type layeredIndex struct {
	// singleByTail: Field/Descent/TypeMove edges keyed by their (single) tail node.
	singleByTail map[hypergraph.NodeID][]hypergraph.EdgeID
	// jumpsByPre: EntityJump edges keyed by each pre-jump object candidate -- the object node
	// (type, subgraph) of each key tail.
	jumpsByPre map[hypergraph.NodeID][]hypergraph.EdgeID
}

// layeredIndexFor returns the graph's layered index, building it on first use and caching it on the
// graph's memo slot (hypergraph.Memo, SlotSearchLayered) -- rebuilding this O(|V|+|E|) index on
// every Search was a measured kernel regression (Section 4 of BENCHMARKS.md: allocs growing with |E|). The
// cache changes no answer: buildLayeredIndex is deterministic in h, so the cached index is the one
// a fresh build would produce, and consistentTrace only reads it.
func layeredIndexFor(h *hypergraph.Hypergraph) *layeredIndex {
	return h.Memo(hypergraph.SlotSearchLayered, func() any { return buildLayeredIndex(h) }).(*layeredIndex)
}

func buildLayeredIndex(h *hypergraph.Hypergraph) *layeredIndex {
	idx := &layeredIndex{
		singleByTail: map[hypergraph.NodeID][]hypergraph.EdgeID{},
		jumpsByPre:   map[hypergraph.NodeID][]hypergraph.EdgeID{},
	}
	// Object nodes by (type, subgraph) for pre-jump lookup.
	type ts struct {
		t string
		s hypergraph.SubgraphID
	}
	objByTS := map[ts]hypergraph.NodeID{}
	for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
		n := h.Node(id)
		if n.Kind == hypergraph.NodeObject && n.Scope == "" {
			objByTS[ts{n.Type, n.Subgraph}] = id
		}
	}
	for e := 0; e < h.NumEdges(); e++ {
		id := hypergraph.EdgeID(e)
		switch h.EdgeKind(id) {
		case hypergraph.EdgeField, hypergraph.EdgeDescent, hypergraph.EdgeTypeMove:
			tails := h.EdgeTails(id)
			if len(tails) == 1 {
				idx.singleByTail[tails[0]] = append(idx.singleByTail[tails[0]], id)
			}
		case hypergraph.EdgeEntityJump:
			edge := h.Edge(id)
			keyTails := edge.KeyTails
			if len(keyTails) == 0 {
				keyTails = edge.Tails
			}
			seen := map[hypergraph.NodeID]bool{}
			for _, t := range keyTails {
				tn := h.Node(t)
				obj, ok := objByTS[ts{tn.Type, tn.Subgraph}]
				if !ok || seen[obj] {
					continue
				}
				seen[obj] = true
				idx.jumpsByPre[obj] = append(idx.jumpsByPre[obj], id)
			}
		}
	}
	return idx
}

// layeredState is a (node, chainPos) product state, flattened to node*(D+1)+pos.
type layeredPQItem struct {
	cost  int64
	state int32
}

type layeredPQ []layeredPQItem

func (q layeredPQ) Len() int { return len(q) }
func (q layeredPQ) Less(i, j int) bool {
	if q[i].cost != q[j].cost {
		return q[i].cost < q[j].cost
	}
	return q[i].state < q[j].state // deterministic tie-break
}
func (q layeredPQ) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *layeredPQ) Push(x any)        { *q = append(*q, x.(layeredPQItem)) }
func (q *layeredPQ) Pop() any          { old := *q; n := len(old); it := old[n-1]; *q = old[:n-1]; return it }
func (q layeredPQ) top() layeredPQItem { return q[0] }

// consistentTrace attempts the chain-layered trace for one goal: the minimum-cost walk over the
// goal's masked graph whose spine spells `fields` exactly and ends on one of the goal's candidate
// nodes. tb is the goal's masked settle table (tail reachability + tail sub-walk traces); mask is the
// goal's scope mask. Returns the chosen candidate, the full walk (spine + jump-tail sub-walks), the
// root->leaf SPINE (the ordered edge sequence -- the depth information a head-keyed walk set loses on
// revisited nodes, recorded for lowering as Cover.Spines), and ok=false when no chain-consistent
// route exists.
func consistentTrace(h *hypergraph.Hypergraph, idx *layeredIndex, fields []string,
	cand []hypergraph.NodeID, tb table, mask map[hypergraph.EdgeID]bool,
) (hypergraph.NodeID, []hypergraph.EdgeID, []hypergraph.EdgeID, bool) {

	D := len(fields)
	if D == 0 {
		return 0, nil, nil, false
	}
	nStates := h.NumNodes() * (D + 1)
	const inf = int64(1) << 62
	dist := make([]int64, nStates)
	for i := range dist {
		dist[i] = inf
	}
	// parent edge + previous state per settled state, for spine reconstruction.
	parentEdge := make([]hypergraph.EdgeID, nStates)
	parentState := make([]int32, nStates)
	for i := range parentEdge {
		parentEdge[i] = hypergraph.NoEdge
		parentState[i] = -1
	}
	stateOf := func(v hypergraph.NodeID, i int) int32 { return int32(int(v)*(D+1) + i) }

	pq := &layeredPQ{}
	for _, r := range h.Roots() {
		s := stateOf(r, 0)
		dist[s] = 0
		heap.Push(pq, layeredPQItem{cost: 0, state: s})
	}
	relax := func(from int32, to int32, e hypergraph.EdgeID, w int64) {
		nc := dist[from] + w
		if nc < dist[to] || (nc == dist[to] && (parentState[to] == -1 || from < parentState[to] ||
			(from == parentState[to] && e < parentEdge[to]))) {
			if nc < dist[to] {
				dist[to] = nc
				parentEdge[to] = e
				parentState[to] = from
				heap.Push(pq, layeredPQItem{cost: nc, state: to})
			}
			// equal-cost ties keep the first-settled derivation (deterministic via the PQ order).
		}
	}
	settled := make([]bool, nStates)
	for pq.Len() > 0 {
		it := heap.Pop(pq).(layeredPQItem)
		if settled[it.state] || it.cost > dist[it.state] {
			continue
		}
		settled[it.state] = true
		v := hypergraph.NodeID(int(it.state) / (D + 1))
		i := int(it.state) % (D + 1)
		for _, e := range idx.singleByTail[v] {
			if mask[e] {
				continue
			}
			w := h.EdgeWeight(e)
			head := h.EdgeHead(e)
			switch h.EdgeKind(e) {
			case hypergraph.EdgeField:
				if i < D && h.EdgeLabel(e) == fields[i] {
					relax(it.state, stateOf(head, i+1), e, w)
				}
			default: // Descent / TypeMove: path-neutral
				relax(it.state, stateOf(head, i), e, w)
			}
		}
		for _, e := range idx.jumpsByPre[v] {
			if mask[e] {
				continue
			}
			// A jump is usable iff every tail (keys + @requires) is reachable on the goal's table.
			usable := true
			for _, t := range h.EdgeTails(e) {
				if tb.pi[t] >= Inf {
					usable = false
					break
				}
			}
			if !usable {
				continue
			}
			relax(it.state, stateOf(h.EdgeHead(e), i), e, h.EdgeWeight(e))
		}
	}

	// Pick the C.4-min candidate reachable at layer D.
	best := hypergraph.NodeID(0)
	bestCost := inf
	found := false
	for _, c := range cand {
		s := stateOf(c, D)
		if dist[s] >= inf {
			continue
		}
		if !found || dist[s] < bestCost || (dist[s] == bestCost && nodeIDKey(h, c) < nodeIDKey(h, best)) {
			best, bestCost, found = c, dist[s], true
		}
	}
	if !found {
		return 0, nil, nil, false
	}

	// Reconstruct the spine (collected leaf->root, reversed to root->leaf below), then append each
	// spine jump's tail sub-walks from the masked back table.
	var spine []hypergraph.EdgeID
	spineNodes := map[hypergraph.NodeID]bool{}
	for s := stateOf(best, D); s >= 0 && parentState[s] != -1; s = parentState[s] {
		spine = append(spine, parentEdge[s])
		spineNodes[hypergraph.NodeID(int(s)/(D+1))] = true
	}
	slices.Reverse(spine) // root->leaf
	// Root state has no parent; mark the roots as visited too.
	for _, r := range h.Roots() {
		spineNodes[r] = true
	}
	walk := append([]hypergraph.EdgeID(nil), spine...)
	visited := map[hypergraph.NodeID]bool{}
	for n := range spineNodes {
		visited[n] = true
	}
	for _, e := range spine {
		if h.EdgeKind(e) != hypergraph.EdgeEntityJump {
			continue
		}
		for _, t := range h.EdgeTails(e) {
			walk = append(walk, Traceback(h, tb.back, t, visited)...)
		}
	}
	return best, walk, spine, true
}
