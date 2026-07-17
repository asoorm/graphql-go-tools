package search

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"

// bruteForceMinTreeCost enumerates every irredundant derivation (PROOFS T3.1: no node label
// repeats on any root-to-leaf branch) of any node in cand and returns the minimum C.2 tree value,
// or Inf if no derivation exists. It is the I3 optimality ORACLE.
//
// INDEPENDENCE: this oracle uses ONLY the hypergraph read API (Roots/Incoming/Edge/Tails/Weight) and
// the Inf/Combinator constants -- no settle, no readyQueue, no back-pointers, and no cost.go helpers
// (the (+) fold is inlined below) -- so a bug shared with the kernel cannot hide behind it. Deliberately
// exponential; the property-test generator bounds |V| and max |T(e)| so the recursion stays feasible.
//
// Pruning is safe: a branch on which a label repeats is not irredundant, and T3.1's splice argument
// shows some irredundant derivation attains the minimum, so discarding repeats never loses the
// optimum.
func bruteForceMinTreeCost(h *hypergraph.Hypergraph, op Combinator, cand []hypergraph.NodeID) int64 {
	roots := map[hypergraph.NodeID]bool{}
	for _, r := range h.Roots() {
		roots[r] = true
	}
	var derive func(v hypergraph.NodeID, onBranch map[hypergraph.NodeID]bool) int64
	derive = func(v hypergraph.NodeID, onBranch map[hypergraph.NodeID]bool) int64 {
		if roots[v] {
			return 0 // leaf derivation: val(root) = 0 (PROOFS Section 0)
		}
		if onBranch[v] {
			return Inf // label repeat on this root-to-leaf branch: not irredundant, prune
		}
		onBranch[v] = true
		defer delete(onBranch, v)
		best := Inf
		for _, e := range h.Incoming(v) { // every choice of final edge e_v with H(e_v)=v
			edge := h.Edge(e)
			// inline (+) fold (independent of cost.go): sum or max over tail sub-derivations
			var acc int64
			feasible := true
			for i, t := range edge.Tails {
				tv := derive(t, onBranch)
				if tv == Inf {
					feasible = false
					break
				}
				if op == Max {
					if i == 0 || tv > acc {
						acc = tv
					}
				} else {
					acc += tv
				}
			}
			if !feasible {
				continue
			}
			if val := edge.Weight + acc; val < best {
				best = val
			}
		}
		return best
	}
	best := Inf
	for _, v := range cand {
		if val := derive(v, map[hypergraph.NodeID]bool{}); val < best {
			best = val
		}
	}
	return best
}
