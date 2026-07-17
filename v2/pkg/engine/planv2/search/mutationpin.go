package search

// mutationpin.go -- the mutation-root subgraph pin (FORMAL_SPEC D10 amendment -- mutation-root
// subgraph pin; FEDERATION_SEMANTICS FS-ROOT-6).
//
// The defect this closes (real Guild audit mutations_3; DIVERGENCES executed-truth residual
// entry 1): a MUTATION root field declared shareable in several subgraphs has root-entering Field
// edges in each of them, and the per-goal cheapest-route selection can serve one part of the
// field's selection through subgraph a's root and another through subgraph b's -- two root fetches,
// each executing the side effect (silent wrong EFFECT: the merged data can come out byte-correct
// while the mutation applied twice). Queries tolerate the same split because reads are idempotent;
// mutations do not (GraphQL spec Section 6.2.2 executes each top-level mutation field exactly
// once). v1 pins each mutation root field to one planner/subgraph; this file is planv2's
// realization of the same obligation.
//
// Mechanism: for a mutation operation, per root field rf with root-entering Field edges in TWO or
// more subgraphs, choose ONE subgraph sigma(rf) and extend the BASE mask with rf's root edges in
// every other subgraph. The base mask flows into every table the search consults -- the plain
// table, the per-root-field pinned tables, the per-goal scoped-walk masks, and the chain-layered
// consistent trace -- so no tier of the fall-back ladder can re-admit a second root entrance for
// rf. Single-subgraph mutation root fields are untouched (nothing to pin), queries and
// subscriptions are untouched, and hand-assembled trees (opKind Unknown) are untouched.
//
// Choosing sigma(rf) is a JOINT per-root-field decision, not a per-goal one: sigma(rf) is the
// candidate subgraph minimizing the SUM of best-candidate settle costs over rf's (non-exempt)
// goals, over the viability mask that keeps rf's entrance in that subgraph only and removes every
// OTHER mutation root entrance (other mutation root fields must not serve as side entrances --
// routing a goal through a foreign mutation root would execute THAT mutation as a side channel).
// Query-root edges stay open in the viability mask, mirroring the plain-table fall-back tier the
// pinned goal keeps (query/mutation cross-participation is reads-only and predates this pin).
// Viability = every non-exempt goal under rf reaches some candidate. Ties break by subgraph name,
// then subgraph id -- content-based, so the decision depends only on the graph (C.4 discipline).
// Because the per-goal minimum over the union table is a lower bound of every single-subgraph
// sum, an operation the unpinned search already served through one subgraph keeps that subgraph
// as a minimizer (the pin never worsens an already-single-subgraph plan's cost).
//
// No fall-back: when NO single subgraph can anchor rf's whole selection, the only executable
// plans double-execute the side effect, so if every goal is reachable on the unpinned table the
// search fails LOUD (ErrNoValidPlan, reasonMutationRootUnpinnable) rather than emit one --
// single-execution dominates completeness for side effects (the L7 no-silent-degrade principle;
// FS-PLAN-6). When some goal is unreachable even unpinned, the field is left unpinned and the
// goal loop reports the ordinary "unreachable" diagnosis (the pre-existing honest error class).
//
// Operation-scoped mode: the function runs over the mode's own graph and index views (sc.sub,
// rootEntrySub, sub-id candidates). Decision equality with the default mode follows from S1/S2:
// every derivation of every goal candidate lies inside the scope, so a candidate subgraph is
// viable in one mode iff it is viable in the other, and the settle tables agree on every
// in-scope candidate under corresponding masks; the committed dual-mode equality gates enforce
// this per case.

import (
	"sort"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// reasonMutationRootUnpinnable is the ErrNoValidPlan reason for a mutation root field whose whole
// selection no single declaring subgraph can anchor: every executable plan would split the root
// field across subgraphs and execute its side effect more than once.
const reasonMutationRootUnpinnable = "mutation root field not single-subgraph servable: every plan splits the shareable root field across subgraphs and re-executes its side effect (FS-ROOT-6)"

// pinMutationRoots extends the base mask with the mutation-root subgraph pin and returns it (the
// input map is never mutated; a new map is returned only when a pin fires). No-op for
// non-mutation trees and for mutation root fields with at most one live root entrance.
func pinMutationRoots(h *hypergraph.Hypergraph, o *obligation.Tree, cfg Config,
	disabled map[hypergraph.EdgeID]bool, rootEntry map[hypergraph.EdgeID]string,
	exempt exemptFn, sc *opScope) (map[hypergraph.EdgeID]bool, error) {

	if !o.MutationOperation() {
		return disabled, nil
	}

	// Index the MUTATION-root-entering Field edges: rf -> subgraph -> its edges. Query roots are
	// deliberately excluded -- the pin governs side-effecting entrances only.
	type subEdges struct {
		sub   hypergraph.SubgraphID
		edges []hypergraph.EdgeID
	}
	byField := map[string][]*subEdges{}
	var allMutEdges []hypergraph.EdgeID
	for e, rf := range rootEntry {
		if disabled[e] {
			continue
		}
		tails := h.EdgeTails(e)
		if len(tails) == 0 || h.NodeKind(tails[0]) != hypergraph.NodeRoot || h.NodeField(tails[0]) != "mutation" {
			continue
		}
		allMutEdges = append(allMutEdges, e)
		s := h.Node(h.EdgeHead(e)).Subgraph
		list := byField[rf]
		var entry *subEdges
		for _, se := range list {
			if se.sub == s {
				entry = se
				break
			}
		}
		if entry == nil {
			entry = &subEdges{sub: s}
			byField[rf] = append(byField[rf], entry)
		}
		entry.edges = append(entry.edges, e)
	}

	// Deterministic root-field order (decisions are independent -- the viability mask removes every
	// other mutation entrance regardless of its own pin -- but a stable order keeps error identity
	// stable).
	fields := make([]string, 0, len(byField))
	for rf, subs := range byField {
		if len(subs) >= 2 {
			fields = append(fields, rf)
		}
	}
	if len(fields) == 0 {
		return disabled, nil // no shareable mutation root field -- the common path, zero extra settles
	}
	sort.Strings(fields)

	out := disabled // copy-on-write below
	copied := false
	var plainPi []int64
	for _, rf := range fields {
		subs := byField[rf]
		sort.Slice(subs, func(i, j int) bool {
			ni, nj := h.SubgraphName(subs[i].sub), h.SubgraphName(subs[j].sub)
			if ni != nj {
				return ni < nj
			}
			return subs[i].sub < subs[j].sub
		})

		var goals []obligation.GoalID
		for _, g := range o.Goals() {
			if o.RootField(g) != rf {
				continue
			}
			if exempt != nil && exempt(g) {
				continue // a narrowed-away null needs no route
			}
			goals = append(goals, g)
		}
		if len(goals) == 0 {
			continue
		}

		bestIdx := -1
		var bestScore int64
		for i, cand := range subs {
			mask := make(map[hypergraph.EdgeID]bool, len(out)+len(allMutEdges))
			for e := range out {
				mask[e] = true
			}
			own := map[hypergraph.EdgeID]bool{}
			for _, e := range cand.edges {
				own[e] = true
			}
			for _, e := range allMutEdges {
				if !own[e] {
					mask[e] = true
				}
			}
			pi, _, _, err := settleMasked(h, cfg, mask)
			if err != nil {
				return nil, err // state-cap trip: propagated, never swallowed
			}
			viable := true
			var score int64
			for _, g := range goals {
				v, ok := bestCandidate(h, cands(o, g, sc), pi)
				if !ok {
					viable = false
					break
				}
				score += pi[v]
			}
			if !viable {
				continue
			}
			if bestIdx < 0 || score < bestScore {
				bestIdx, bestScore = i, score
			}
		}

		if bestIdx < 0 {
			// No single subgraph anchors the whole selection. If every goal is reachable UNPINNED,
			// the only plans split the mutation -- fail loud. If some goal is unreachable anyway,
			// leave rf unpinned: the goal loop's ordinary "unreachable" diagnosis owns it.
			if plainPi == nil {
				pi, _, _, err := settleMasked(h, cfg, out)
				if err != nil {
					return nil, err
				}
				plainPi = pi
			}
			witness := goals[0]
			allReachable := true
			for _, g := range goals {
				if _, ok := bestCandidate(h, cands(o, g, sc), plainPi); !ok {
					allReachable = false
					witness = g
					break
				}
			}
			if allReachable {
				return nil, &ErrNoValidPlan{Obligation: witness, Reason: reasonMutationRootUnpinnable}
			}
			continue
		}

		// Pin: mask rf's root entrances in every non-chosen subgraph.
		if !copied {
			out = make(map[hypergraph.EdgeID]bool, len(disabled)+4)
			for e := range disabled {
				out[e] = true
			}
			copied = true
		}
		for i, cand := range subs {
			if i == bestIdx {
				continue
			}
			for _, e := range cand.edges {
				out[e] = true
			}
		}
		plainPi = nil // the base mask changed; any later plain-reachability check must re-settle
	}
	return out, nil
}
