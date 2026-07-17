package lower

// merge.go tries to reduce the number of fetches after grouping. It is a separate, strictly bounded
// phase -- deliberately NOT folded into lowering. It has two steps, each of which can only keep the
// plan correct and never make it cost more:
//
//  1. Deduplicate -- collapse fetches that are provably redundant (same subgraph, same entity
//     representation key-set, no argument conflict). Exact; invents no new sharing.
//  2. Co-locate -- ONE pass over the requested fields, in the search's deterministic order. A field is
//     re-routed to a subgraph a sibling already fetches ONLY when the move strictly reduces the plan
//     cost (which, with a fetch far costlier than an in-subgraph field, means fewer fetches) and the
//     alternative route is a valid one. It never worsens the plan, runs once, and claims no
//     optimality. If you're ever tempted to iterate this until stable -- DON'T. Turning this into an
//     optimizer is exactly the expensive, NP-hard thing this bounded pass exists to avoid.
//
// On arguments: the graph ignores argument values on its field edges, but an @requires' argument
// values do ride on the entity-jump edge. argsConflict uses them to enforce a rule: two candidate-for-
// merge entity fetches whose @requires bind the same coordinate to DIFFERENT argument values are kept
// split, never merged into one representation. On the default (obligation-driven) path this split
// already happens upstream in lowering and merge is not run; this hook keeps the legacy path honest to
// the same rule.

import (
	"slices"
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// merge runs the dedup + bounded co-location pass and returns the merged fetch groups together with a
// fresh edge->group map over the merged documents.
//
// The fresh map matters: the aliaser decides response-key collisions against the document each edge
// prints into, and merge changes document composition -- dedup unions two groups' selections into one
// document, and a co-location move rebuilds groups from a new plan. Rebuilding the map over the merged
// groups (rather than reusing the pre-merge one) is what keeps the aliases correct on the merged
// documents.
func merge(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result, groups []*fetchGroup) ([]*fetchGroup, map[hypergraph.EdgeID]int) {
	groups = dedupFetches(h, groups)
	if colocate(h, o, res) {
		// A move changed the plan; regroup from the improved plan and dedup again (re-routing can expose
		// fresh same-subgraph redundancy). This is a fixed short sequence dedup -> colocate -> dedup,
		// NOT a loop-until-stable.
		groups, _ = buildGroups(h, res.Cover)
		groups = dedupFetches(h, groups)
	}
	return groups, groupOfEdges(groups)
}

// dedupFetches collapses provably-redundant fetches: two groups merge only if they target the same
// subgraph, are the same fetch kind (both root or both `_entities`), land on the same entry node, and
// share the same entity representation key-set -- with no argument conflict and no dependency ordering
// between them. Merging unions the selection edges and the dependency sets; it removes only redundant
// identical fetches and never invents new cross-branch sharing. Dependency indices are rewired through
// the final remap.
//
// Why the entry node is part of the bucket key: two entity groups can share the same "Type.field"
// representation key strings while landing on DIFFERENT object nodes -- a @provides scope makes two
// otherwise-identical object nodes distinct. groupDocument prints only the survivor's selection, so
// bucketing those together would silently drop the absorbed group's selections. Keying on the entry
// node is also the truer reading of "same representation": the representation is anchored at the object
// the jump lands on.
//
// A note on the key split: the representation key strings are built from ALL of a jump's tails and are
// not split into @key vs @requires. Two jumps with the same tail coordinates but a different split
// would render different representations -- but two same-entry groups in a real plan come from the same
// jump edge (each node has one back-edge), so their split matches anyway. Hand-assembled group lists
// must uphold this.
func dedupFetches(h *hypergraph.Hypergraph, groups []*fetchGroup) []*fetchGroup {
	n := len(groups)
	comp := make([]int, n) // union-find over group indices; each component's rep is its bucket survivor
	for i := range comp {
		comp[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if comp[i] != i {
			comp[i] = find(comp[i])
		}
		return comp[i]
	}
	// dependsTransitively reports whether component(from) transitively depends on component(to) in
	// the CURRENT (partially merged) dependency graph: DFS over original group indices, treating the
	// members of one component as a single node (free moves within a component).
	dependsTransitively := func(from, to int) bool {
		target := find(to)
		seen := make([]bool, n)
		var stack []int
		pushComponent := func(c int) {
			for i := range n {
				if find(i) == c && !seen[i] {
					seen[i] = true
					stack = append(stack, i)
				}
			}
		}
		pushComponent(find(from))
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, d := range groups[cur].DependsOn {
				fd := find(d)
				if fd == target {
					return true
				}
				pushComponent(fd)
			}
		}
		return false
	}

	type bucketKey struct {
		subgraph hypergraph.SubgraphID
		isRoot   bool
		entry    hypergraph.NodeID // the object node the fetch lands on (see doc comment)
		repKeys  string
	}
	buckets := map[bucketKey]int{} // key -> survivor's ORIGINAL group index
	for gi, g := range groups {
		k := bucketKey{g.Subgraph, g.Jump == hypergraph.NoEdge, g.Entry, strings.Join(g.RepKeys, ",")}
		si, ok := buckets[k]
		if !ok {
			buckets[k] = gi
			continue
		}
		if argsConflict(h, groups[si], g) {
			continue // an argument conflict keeps the fetches split
		}
		// Don't merge two groups that are ordered by dependency -- one transitively needs the other's
		// output first. Merging them would create a cycle in the dependency graph (Q+R would depend on
		// W while W depends on Q+R), which would deadlock or misorder the fetches. Such a pair isn't
		// redundant anyway -- the two fetches run at different stages -- so skip the merge. This is a
		// one-shot reachability check per candidate, not a loop.
		if dependsTransitively(gi, si) || dependsTransitively(si, gi) {
			continue
		}
		comp[find(gi)] = find(si) // commit: gi joins the survivor's component
	}

	// Emit one group per component in survivor order (a component's survivor is its lowest member
	// index, so first appearance over gi ascending is the original order), unioning the absorbed
	// members' selection edges and dependencies into the survivor. The aliases on the merged document
	// are re-established by assignAliases over the edge->group map merge() returns.
	remap := make([]int, n)
	outIdx := map[int]int{} // component rep -> out index
	var out []*fetchGroup
	for gi, g := range groups {
		c := find(gi)
		oi, ok := outIdx[c]
		if !ok {
			oi = len(out)
			outIdx[c] = oi
			out = append(out, groups[c])
		}
		remap[gi] = oi
		if gi != c {
			out[oi].Edges = append(out[oi].Edges, g.Edges...)
			out[oi].DependsOn = append(out[oi].DependsOn, g.DependsOn...)
		}
	}
	// Rewire dependencies through the remap. The self-drop is defensive only: the acyclicity guard
	// already refuses any merge that would map a dependency into its own group.
	for oi, g := range out {
		filtered := g.DependsOn[:0]
		for _, d := range g.DependsOn {
			nd := remap[d]
			if nd != oi {
				filtered = append(filtered, nd)
			}
		}
		sort.Ints(filtered)
		g.DependsOn = dedupInts(filtered)
	}
	return out
}

// argsConflict reports whether two same-subgraph, same-representation entity fetch groups must be kept
// split because some coordinate is bound by their @requires to DIFFERENT argument values. The values
// ride on each group's entity-jump edge, so the check reads them there. A root group (no jump, no
// requires) never conflicts. This is the same argument-conflict check the v1 planner runs, applied per
// coordinate.
func argsConflict(h *hypergraph.Hypergraph, a, b *fetchGroup) bool {
	ra, rb := groupRequires(h, a), groupRequires(h, b)
	if len(ra) == 0 || len(rb) == 0 {
		return false
	}
	seen := map[string]string{} // coord -> argument body, from a's requires
	for _, rs := range ra {
		for c, v := range requiresCoordArgs(rs) {
			seen[c] = v
		}
	}
	for _, rs := range rb {
		for c, v := range requiresCoordArgs(rs) {
			if prev, ok := seen[c]; ok && prev != v {
				return true
			}
		}
	}
	return false
}

// groupRequires returns a group's @requires selection strings (from its EntityJump edge), or nil for a
// root group.
func groupRequires(h *hypergraph.Hypergraph, g *fetchGroup) []string {
	if g.Jump == hypergraph.NoEdge {
		return nil
	}
	return h.Edge(g.Jump).Requires
}

// colocate is the bounded co-location pass. It makes ONE pass over the requested fields, in the
// search's deterministic order. For each covered field, if there's an alternative candidate node that
// is reachable and lives in a subgraph a sibling already fetches, it tentatively re-routes the field
// there and keeps the move only if it strictly reduces the plan cost. On an applied move it updates
// res.Cover and returns whether any move was applied. It never worsens the plan, runs once, and claims
// no optimality.
func colocate(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result) bool {
	// Which subgraphs the current plan already fetches -- a co-location target must be one of these.
	// The cost guard below is what actually decides; this just scopes candidates to real co-locations
	// ("a subgraph a sibling already fetches").
	fetched := map[hypergraph.SubgraphID]bool{}
	for _, v := range res.Cover.Selected {
		fetched[h.Node(v).Subgraph] = true
	}
	before := search.CoverCost(h, res.Cover.Edges)
	changed := false
	for _, g := range o.Goals() {
		v, covered := res.Cover.Selected[g]
		if !covered {
			continue
		}
		// Read reachability from the same table the search chose this field against, so a co-location
		// target is only considered when the field can actually reach it without exiting through a
		// foreign root.
		gpi := res.PiFor(g)
		for _, alt := range o.Cand(g) {
			if alt == v || gpi[alt] >= search.Inf || !fetched[h.Node(alt).Subgraph] {
				continue
			}
			trial := refold(h, o, res, g, alt)
			c := search.CoverCost(h, trial)
			if c >= before { // apply only when the cost strictly drops
				continue
			}
			res.Cover.Selected[g] = alt
			res.Cover.Edges = trial
			res.Cover.Cost = c
			before = c
			fetched[h.Node(alt).Subgraph] = true
			changed = true
			break // at most one move per field -- single pass
		}
	}
	return changed
}

// refold rebuilds the deduplicated edge set with field g routed to alt: it re-traces every covered
// field's chosen node (g via alt, all others unchanged) through ONE shared visited set, exactly as the
// search folds the plan. Edges used only by g drop out and shared edges stay counted once. The result
// is a valid plan for the same fields, so its cost is directly comparable to the current plan's.
func refold(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	g obligation.GoalID, alt hypergraph.NodeID) []hypergraph.EdgeID {
	visited := map[hypergraph.NodeID]bool{}
	set := map[hypergraph.EdgeID]struct{}{}
	for _, other := range o.Goals() {
		v, covered := res.Cover.Selected[other]
		if !covered {
			continue
		}
		if other == g {
			v = alt
		}
		// Trace each field through its own back table (masked when the field is root-pinned), so a
		// re-fold reconstructs the same routes the search emitted rather than a wrong one.
		for _, e := range search.Traceback(h, res.BackFor(other), v, visited) {
			set[e] = struct{}{}
		}
	}
	out := make([]hypergraph.EdgeID, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
}

// groupOfEdges maps every selection edge to its final (merged) group index -- the per-document scope
// the aliaser needs. It covers every Field/Descent/TypeMove edge (all live in a group's Edges);
// entity-jump edges are never looked up by the aliaser, so leaving them out is correct.
func groupOfEdges(groups []*fetchGroup) map[hypergraph.EdgeID]int {
	m := make(map[hypergraph.EdgeID]int)
	for gi, g := range groups {
		for _, e := range g.Edges {
			m[e] = gi
		}
	}
	return m
}

// dedupInts removes adjacent duplicates from a sorted slice in place.
func dedupInts(in []int) []int {
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}
