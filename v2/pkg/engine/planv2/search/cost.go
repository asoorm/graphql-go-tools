// Package search finds the cheapest way to answer a query over the composed hypergraph. It imports
// only the graph and obligation types (no GraphQL AST, plan, or datasource types) so it can be
// reasoned about and model-checked on its own.
//
// This file is the cost layer. Each edge already carries a weight; this file only defines how those
// weights combine up a path, what a finished plan costs, and the total order the search uses to pick
// the cheapest ready edge (with a deterministic tie-break so the same query always plans the same
// way). Spec: FORMAL_SPEC Section 6, cost model C.1-C.4.
package search

import (
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

// Inf is the cost of an unreachable node. It sits well below the maximum int64 so that adding one
// edge weight to it can never overflow; once a cost reaches Inf it stays there.
const Inf int64 = 1 << 62

// Combinator selects how the costs of an edge's tails combine into a single input cost for the edge.
// Both options work correctly with the search (they never decrease as tail costs grow). The default
// is Sum.
type Combinator uint8

const (
	Sum Combinator = iota // add tail costs -- total work / number of round-trips (the default)
	Max                   // take the largest tail cost -- critical-path latency under parallelism
)

// combine reduces a set of tail costs to one value under the chosen combinator. If any tail is
// unreachable the result is Inf (an edge can't be used if one of its inputs can't be reached), and
// Sum clamps to Inf rather than overflowing. No tails (an edge fed only by roots) combines to 0.
func combine(op Combinator, tails []int64) int64 {
	if op == Max {
		var m int64
		for _, t := range tails {
			if t == Inf {
				return Inf
			}
			if t > m {
				m = t
			}
		}
		return m
	}
	var s int64
	for _, t := range tails {
		if t == Inf {
			return Inf
		}
		s += t
		if s >= Inf {
			return Inf
		}
	}
	return s
}

// tentativeF is the cost of reaching an edge's head through that edge: the edge's own weight plus the
// combined costs of its tails. This is the value the search queues a ready edge under, and it must
// be exactly this -- not the edge weight alone, not the cheapest tail, not the head's current cost --
// or the search quietly stops being optimal. It is well-defined only when every tail is already
// settled, which is the case whenever an edge becomes ready; callers must uphold that (pi[t] holds
// the final cost of each tail t).
func tentativeF(h *hypergraph.Hypergraph, e hypergraph.EdgeID, pi []int64, op Combinator) int64 {
	tailNodes := h.EdgeTails(e) // use the field accessor to avoid copying the whole Edge struct
	tails := make([]int64, len(tailNodes))
	for i, t := range tailNodes {
		tails[i] = pi[t]
	}
	c := combine(op, tails)
	if c == Inf {
		return Inf
	}
	return h.EdgeWeight(e) + c
}

// CoverCost is the total cost of a plan. Exported so the lowering step's co-location pass can check
// whether merging two fetches actually lowers the cost, without re-implementing the sum here.
func CoverCost(h *hypergraph.Hypergraph, edges []hypergraph.EdgeID) int64 { return coverCost(h, edges) }

// coverCost sums the weights of the edges in a plan, counting each distinct edge once even when two
// goals share it (the plan is a DAG, not a tree).
func coverCost(h *hypergraph.Hypergraph, edges []hypergraph.EdgeID) int64 {
	seen := map[hypergraph.EdgeID]struct{}{}
	var total int64
	for _, e := range edges {
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		total += h.EdgeWeight(e)
	}
	return total
}

// nodeIDKey renders a node's full identity -- type name, subgraph, field label (or empty), and
// provided-scope tag (or empty) -- as a single string, joined with NUL bytes so two different nodes
// can never produce the same string. Used as the final tie-break in the total order below.
func nodeIDKey(h *hypergraph.Hypergraph, id hypergraph.NodeID) string {
	n := h.Node(id)
	return n.Type + "\x00" + n.Field + "\x00" + n.Scope + "\x00" + h.SubgraphName(n.Subgraph)
}

// less is the total order the ready queue uses; it reports whether edge a should come before edge b.
// Cheaper head cost wins first (the caller passes the precomputed costs fa and fb). Exact ties are
// broken, in order, by: head subgraph name, edge kind (Field < Descent < TypeMove < EntityJump),
// field/member label, then the full head identity, then the sorted tail identities. Because those
// steps can never all tie for two different edges, the search's choice is unique and the same query
// always plans identically.
func less(h *hypergraph.Hypergraph, a, b hypergraph.EdgeID, fa, fb int64) bool {
	if fa != fb { // cheaper head cost first
		return fa < fb
	}
	// Use the field accessors, not h.Edge, so each comparison reads one field instead of copying two
	// whole Edge structs -- this comparator runs on the heap's hot path.
	headA, headB := h.EdgeHead(a), h.EdgeHead(b)
	if sa, sb := h.SubgraphName(h.Node(headA).Subgraph), h.SubgraphName(h.Node(headB).Subgraph); sa != sb {
		return sa < sb // head subgraph name
	}
	if ka, kb := h.EdgeKind(a), h.EdgeKind(b); ka != kb {
		return ka < kb // edge kind: Field < Descent < TypeMove < EntityJump
	}
	if la, lb := h.EdgeLabel(a), h.EdgeLabel(b); la != lb {
		return la < lb // field/member label
	}
	// full head identity, then the sorted list of full tail identities
	if ka, kb := nodeIDKey(h, headA), nodeIDKey(h, headB); ka != kb {
		return ka < kb
	}
	return tailKey(h, h.EdgeTails(a)) < tailKey(h, h.EdgeTails(b))
}

// tailKey renders an edge's tail nodes as their full identities, sorted and joined, for the final
// tie-break in less. The slices returned by hypergraph are read-only, so it sorts a fresh slice of
// rendered keys and never mutates the edge's tails.
func tailKey(h *hypergraph.Hypergraph, tails []hypergraph.NodeID) string {
	ks := make([]string, len(tails))
	for i, t := range tails {
		ks[i] = nodeIDKey(h, t)
	}
	sort.Strings(ks)
	var out strings.Builder
	for _, k := range ks {
		out.WriteString(k)
		out.WriteByte('\x01')
	}
	return out.String()
}
