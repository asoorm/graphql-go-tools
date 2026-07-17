package search

// settle.go runs the core search: starting from the query roots, it repeatedly picks the cheapest
// way to reach any not-yet-reached node and records the cost and the edge used. One pass covers
// every reachable node, so each field the client asked for can then trace back its cheapest route.
// Two guards bound the work: a quick size estimate up front and a hard cap during the search -- both
// fail with typed errors rather than degrading silently. This package deliberately imports only the
// graph and obligation types so it can be reasoned about (and model-checked) in isolation.
// Spec: FORMAL_SPEC Section 6.1.

import (
	"container/heap"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// Config parameterizes one search run. The zero Combinator is Sum (the default cost model). Either
// cap set to 0 or below disables that guard.
type Config struct {
	Combine      Combinator // how tail costs are combined; default Sum (zero value)
	PreflightCap int64      // reject up front if the size estimate exceeds this (0 = off)
	StateCap     int64      // hard cap on nodes settled during the search (0 = off)
	// OperationScoped selects the operation-scoped settle domain (FORMAL_SPEC Section 6.5): every settle
	// runs over the operation's backward-closure sub-graph instead of the whole graph, making
	// per-plan cost proportional to the query rather than the schema. Plans are mode-identical
	// (asserted by the dual-mode oracle and the corpus equality gates); resource-guard behavior is
	// the one legitimate difference -- the preflight still runs on the FULL graph, but StateCap
	// counts strictly fewer settled states here. Read only by the search orchestration
	// (search.go/opscope.go); the settle kernel in this file never consults it.
	OperationScoped bool
}

// SettleStats are counters the search records for its own bounds checks and for tests: each edge is
// pushed onto the queue at most once (when its last tail settles) and popped at most once, so
// Pushes, Extracts, and States never exceed the edge count. Visited is filled later by the
// traceback in search.go, not here.
type SettleStats struct{ States, Pushes, Extracts, Visited int64 }

// readyItem is one entry of the ready queue: an edge whose every tail is already settled, tagged
// with the head cost f it was computed with at push time. Carrying f on the item means the queue
// ordering and the later relaxation both reuse the exact value the edge was queued under, instead of
// recomputing it from state that may have moved on.
type readyItem struct {
	edge hypergraph.EdgeID
	f    int64
}

// readyQueue is a min-heap of ready edges ordered by the total order in cost.go (cheapest head cost
// first, then a deterministic tie-break). It holds at most one entry per edge.
type readyQueue struct {
	h     *hypergraph.Hypergraph
	items []readyItem
}

func (q *readyQueue) Len() int { return len(q.items) }
func (q *readyQueue) Less(i, j int) bool {
	return less(q.h, q.items[i].edge, q.items[j].edge, q.items[i].f, q.items[j].f)
}
func (q *readyQueue) Swap(i, j int) { q.items[i], q.items[j] = q.items[j], q.items[i] }
func (q *readyQueue) Push(x any)    { q.items = append(q.items, x.(readyItem)) }
func (q *readyQueue) Pop() any {
	old := q.items
	n := len(old)
	it := old[n-1]
	q.items = old[:n-1]
	return it
}

// preflight is the up-front size guard. Before the search allocates anything it computes a cheap
// upper bound on how many edges the search could touch -- for every goal, the number of candidate
// nodes it could resolve to, times the largest tail count of any edge -- and rejects with
// *ErrPlanTooLarge if that exceeds the cap. It never mutates state. A cap of 0 or below disables it.
// Spec: FORMAL_SPEC Section 6.3.
func preflight(h *hypergraph.Hypergraph, o *obligation.Tree, cfg Config) error {
	if cfg.PreflightCap <= 0 {
		return nil
	}
	fanIn := maxFanIn(h)
	var est int64
	for _, g := range o.Goals() {
		est += int64(len(o.Cand(g))) * fanIn
	}
	if est > cfg.PreflightCap {
		return &ErrPlanTooLarge{Est: est, Cap: cfg.PreflightCap}
	}
	return nil
}

// maxFanIn is the largest number of tails on any edge. Every edge has exactly one head, so this is
// the fan-in factor the preflight size estimate multiplies by.
func maxFanIn(h *hypergraph.Hypergraph) int64 {
	var m int64
	for e := 0; e < h.NumEdges(); e++ {
		if n := int64(len(h.EdgeTails(hypergraph.EdgeID(e)))); n > m {
			m = n
		}
	}
	return m
}

// settle runs one search pass. It returns, indexed by node id: pi, the cheapest cost to reach each
// node (Inf for a node nothing can reach); back, the edge that achieved that cost for each node
// (NoEdge for roots and for unreached nodes); the counters; and, if the state cap trips, a typed
// *ErrSearchStateCap. It never degrades silently -- an unreachable node simply keeps pi=Inf, which
// search.go later reports as ErrNoValidPlan.
func settle(h *hypergraph.Hypergraph, cfg Config) ([]int64, []hypergraph.EdgeID, SettleStats, error) {
	return settleMasked(h, cfg, nil)
}

// settleMasked is settle with a set of edges to ignore. An edge id in disabled is treated as if it
// were not in the graph at all: it is never queued, so it can never be used to reach its head. If
// the head has no other way in, it stays unreachable and surfaces as ErrNoValidPlan -- exactly as a
// genuinely missing edge would.
//
// This is how the search prunes an entity jump whose required key fields the operation never asks
// for. The search works one node at a time and has no memory of the path taken to reach a node, so
// this "is this jump usable here?" decision cannot be made mid-search; it is made once, up front, by
// leaving the edge out. settle passes disabled=nil. Masking only removes edges from consideration,
// so the counters can only go down -- the per-edge bounds still hold.
func settleMasked(h *hypergraph.Hypergraph, cfg Config, disabled map[hypergraph.EdgeID]bool) ([]int64, []hypergraph.EdgeID, SettleStats, error) {
	n := h.NumNodes()
	pi := make([]int64, n)
	back := make([]hypergraph.EdgeID, n)
	settled := make([]bool, n)
	for i := range pi {
		pi[i] = Inf
		back[i] = hypergraph.NoEdge
	}

	// Roots start settled at cost 0. No edge mixes root and non-root tails, so need[e] below counts
	// exactly the not-yet-settled tails an edge is still waiting on.
	roots := make(map[hypergraph.NodeID]bool)
	for _, r := range h.Roots() {
		pi[r] = 0
		settled[r] = true
		roots[r] = true
	}

	var stats SettleStats
	need := make([]int, h.NumEdges())
	rq := &readyQueue{h: h}
	heap.Init(rq)

	// Set need[e] to the number of its non-root tails; an edge whose tails are all roots is ready
	// straight away. Tails are counted with repeats (an edge can list the same node twice): counting
	// occurrences here matches the per-occurrence decrement on settle below, so the count reaches
	// zero exactly once and never goes negative or fires twice.
	for e := 0; e < h.NumEdges(); e++ {
		id := hypergraph.EdgeID(e)
		cnt := 0
		for _, tail := range h.EdgeTails(id) {
			if !roots[tail] {
				cnt++
			}
		}
		need[e] = cnt
		if cnt == 0 && !disabled[id] { // all tails are roots, and the edge is not masked out
			stats.Pushes++
			heap.Push(rq, readyItem{edge: id, f: tentativeF(h, id, pi, cfg.Combine)})
		}
	}

	for rq.Len() > 0 {
		stats.States++
		if cfg.StateCap > 0 && stats.States > cfg.StateCap {
			return pi, back, stats, &ErrSearchStateCap{States: stats.States, Cap: cfg.StateCap}
		}
		it := heap.Pop(rq).(readyItem)
		stats.Extracts++
		head := h.EdgeHead(it.edge)
		if settled[head] {
			continue // a cheaper way to reach this node already settled it
		}
		// it.f is the head cost computed when this edge was queued, at which point every tail was
		// already settled at its final cost. Set the node's cost straight from that queued value --
		// never recompute it here, where state may have moved on. This is the one line the
		// optimality of the whole search hinges on.
		pi[head] = it.f
		back[head] = it.edge
		settled[head] = true

		// Every edge that lists this node as a tail now has one fewer tail to wait on; the edge
		// becomes ready when its last tail clears. An edge stuck in a requirement cycle never
		// reaches zero, so its head stays unreachable.
		for _, e2 := range h.TailIncidence(head) {
			ei := int(e2)
			need[ei]--
			if need[ei] == 0 && !disabled[e2] { // last tail cleared, and the edge is not masked out
				stats.Pushes++
				heap.Push(rq, readyItem{edge: e2, f: tentativeF(h, e2, pi, cfg.Combine)})
			}
		}
	}

	return pi, back, stats, nil
}
