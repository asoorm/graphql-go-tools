package obligation

// expand.go -- FORMAL_SPEC D3pppp: abstract-position member expansion (type explosion at abstract
// positions; M2 class-C wave).
//
// A field obligation <U.f> whose owner U is abstract can be locally unresolvable at its position: no
// subgraph that can supply the position's parent instances declares f on U, while the position's
// concrete members -- entities, each with a D7 jump -- declare f in another subgraph (abstract-types:
// `products { reviews }`; `reviews` lives only on Book/Magazine in the reviews subgraph). No single
// walk can serve such a goal (the parent instances must be fanned out per concrete member to move
// subgraphs), so the reference routers plan it by member expansion -- Apollo's type explosion, v1's
// abstract-selection rewrite. This pass performs that expansion on the obligation tree itself, ahead
// of goal resolution: the <U.f> subtree is replaced by one Refine <U|>C> per expansion member C, each
// holding a copy of the subtree with the top field's owner retyped to C. Everything downstream --
// D6pp classification, D11.7 member-qualified placement, D11.8 sibling response variants -- then treats
// the copies exactly as if the client had written the member fragments.
//
// The trigger is deliberately conservative (see FORMAL_SPEC D3pppp for the semantics of each gate):
//  1. U abstract in the composed schema, f not __typename, and the field NOT under a refinement onto
//     its own type (`... on U { f }` is the D3ppp member-fallback class, left untouched);
//  2. no capable position subgraph resolves f on U locally;
//  3. every position-possible set is KNOWN (an @interfaceObject-shaped TOP position cannot type
//     per-member representations -- that is D3io/D7p territory, never expansion);
//  4. every expansion member's f has a reachable field node somewhere -- otherwise the expansion is
//     skipped whole rather than trading a plannable shape for a hard error.
//
// The transform is a pure tree rewrite: H, the search kernel, SETTLE, and the cost model are
// untouched. It runs to a fixpoint (bounded by the tree depth: an expanded copy's top is concrete and
// never re-expands; deeper abstract positions inside copies expand on later passes).

import (
	"sort"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

// expandPassCap bounds the fixpoint loop defensively. Each pass consumes at least one abstract level
// of the tree, so real operations converge in a handful of passes; hitting the cap leaves the
// remaining positions unexpanded (the pre-amendment behavior), never an error.
const expandPassCap = 32

// expandAbstractPositionMembers runs the D3pppp expansion to fixpoint. Requires t.def (composed-schema
// type kinds and implementer sets); a hand-assembled tree without one is left unchanged.
func (t *Tree) expandAbstractPositionMembers(h *hypergraph.Hypergraph) {
	if t.def == nil {
		return
	}
	idx := expandIndexesFor(h)
	// Reachability is per operation kind (D11.12 root scoping), so it rides OUTSIDE the per-graph
	// memoized indexes: one graph serves query and subscription operations alike.
	reachable := reachableFor(h, t.opKind)
	info := &composedInfo{def: t.def, h: h} // per-call: composedInfo memoizes lazily (mutates), so it must NOT be cached on H
	for pass := 0; pass < expandPassCap; pass++ {
		dec := t.expansionDecisions(h, idx, info, reachable)
		if len(dec) == 0 {
			return
		}
		t.rewriteWithExpansions(dec)
	}
}

// expandIndexes are the H-derived lookups the trigger reads. They depend only on H (which the
// rewrite never changes) and are READ-ONLY after construction -- positionSet/gatePosSet copy member
// sets before narrowing them, fieldSubgraphs and gate 2/4 only read -- so one instance is cached per
// graph (expandIndexesFor) and shared by every Build over it, including concurrent ones. Per-call
// state (composedInfo, the tree itself) stays per-call.
type expandIndexes struct {
	descTarget    map[hypergraph.SubgraphID]map[string]string
	memBySubgraph map[hypergraph.SubgraphID]map[string]map[string]bool
	// fieldNodes maps "Type.field" to the (unscoped) field nodes declaring it, per subgraph.
	fieldNodes map[string][]hypergraph.NodeID
}

// expandIndexesFor returns the graph's expansion-trigger indexes, building them on first use and
// caching them on the graph's memo slot (hypergraph.Memo, SlotObligationExpand) -- rebuilding these
// O(|V|+|E|) maps on every obligation.Build was a measured per-plan memory regression at supergraph
// scale (Section 5.3 of BENCHMARKS.md). The cache changes no answer: newExpandIndexes is deterministic in
// h, so the cached indexes are the ones a fresh build would produce, and the trigger only reads
// them.
func expandIndexesFor(h *hypergraph.Hypergraph) *expandIndexes {
	return h.Memo(hypergraph.SlotObligationExpand, func() any { return newExpandIndexes(h) }).(*expandIndexes)
}

func newExpandIndexes(h *hypergraph.Hypergraph) *expandIndexes {
	idx := &expandIndexes{
		descTarget:    map[hypergraph.SubgraphID]map[string]string{},
		memBySubgraph: map[hypergraph.SubgraphID]map[string]map[string]bool{},
		fieldNodes:    map[string][]hypergraph.NodeID{},
	}
	for e := 0; e < h.NumEdges(); e++ {
		edge := h.Edge(hypergraph.EdgeID(e))
		switch edge.Kind {
		case hypergraph.EdgeTypeMove:
			from := h.Node(edge.Tails[0])
			byU := idx.memBySubgraph[from.Subgraph]
			if byU == nil {
				byU = map[string]map[string]bool{}
				idx.memBySubgraph[from.Subgraph] = byU
			}
			if byU[from.Type] == nil {
				byU[from.Type] = map[string]bool{}
			}
			for _, m := range edge.Members {
				byU[from.Type][m] = true
			}
		case hypergraph.EdgeDescent:
			if len(edge.Tails) != 1 || edge.Scope != "" {
				continue
			}
			fieldNode := h.Node(edge.Tails[0])
			if fieldNode.Kind != hypergraph.NodeField || fieldNode.Scope != "" {
				continue
			}
			byField := idx.descTarget[fieldNode.Subgraph]
			if byField == nil {
				byField = map[string]string{}
				idx.descTarget[fieldNode.Subgraph] = byField
			}
			byField[fieldNode.Type+"."+fieldNode.Field] = h.Node(edge.Head).Type
		}
	}
	for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
		n := h.Node(id)
		if n.Kind == hypergraph.NodeField && n.Scope == "" {
			coord := n.Type + "." + n.Field
			idx.fieldNodes[coord] = append(idx.fieldNodes[coord], id)
		}
	}
	return idx
}

// expansionDecisions evaluates the D3pppp trigger for every Field obligation of the current tree and
// returns the obligations to expand with their (sorted) member sets.
func (t *Tree) expansionDecisions(h *hypergraph.Hypergraph, idx *expandIndexes, info *composedInfo, reachable []bool) map[ObID][]string {
	dec := map[ObID][]string{}
	for _, ob := range t.obligations {
		if ob.Kind != Field || ob.Field == typenameField || ob.Type == "" {
			continue
		}
		if info.kind(ob.Type) != kindAbstract {
			continue // gate 1: owner must be abstract in the composed schema
		}
		// Gate 1 (cont.): `... on U { f }` -- a refinement onto the field's own type -- is the D3ppp
		// member-fallback class; expansion owns only the BARE abstract-owner field.
		field, intervening, ok := t.fieldPositionContext(ob)
		if !ok {
			continue // top-level field: the operation root is a concrete position
		}
		refinedOntoSelf := false
		for _, ref := range intervening {
			if ref.Concrete == ob.Type {
				refinedOntoSelf = true
				break
			}
		}
		if refinedOntoSelf {
			continue
		}
		// Capable position subgraphs (route-scoped) with their position-possible sets.
		subs := t.fieldSubgraphs(h, field, reachable)
		var capable []hypergraph.SubgraphID
		pos := map[hypergraph.SubgraphID]posSet{}
		for _, s := range subs {
			p := positionSet(idx.descTarget, idx.memBySubgraph, info, s, field)
			for i := len(intervening) - 1; i >= 0; i-- {
				p = gatePosSet(p, intervening[i].Concrete, info)
			}
			if p.empty() {
				continue
			}
			capable = append(capable, s)
			pos[s] = p
		}
		if len(capable) == 0 {
			continue
		}
		// Gate 2: no capable subgraph resolves f on U locally.
		local := false
		for _, s := range capable {
			for _, id := range idx.fieldNodes[ob.Type+"."+ob.Field] {
				if h.Node(id).Subgraph == s {
					local = true
					break
				}
			}
			if local {
				break
			}
		}
		if local {
			continue
		}
		// Gate 3: every position-possible set is KNOWN (no TOP) -- and gather the member union.
		members := map[string]bool{}
		known := true
		for _, s := range capable {
			p := pos[s]
			if p.all {
				known = false
				break
			}
			for m := range p.set {
				members[m] = true
			}
		}
		if !known || len(members) == 0 {
			continue
		}
		// Gate 4: every expansion member's f has a reachable field node somewhere.
		sorted := make([]string, 0, len(members))
		for m := range members {
			sorted = append(sorted, m)
		}
		sort.Strings(sorted)
		allRoutable := true
		for _, c := range sorted {
			routable := false
			for _, id := range idx.fieldNodes[c+"."+ob.Field] {
				if int(id) < len(reachable) && reachable[id] {
					routable = true
					break
				}
			}
			if !routable {
				allRoutable = false
				break
			}
		}
		if !allRoutable {
			continue
		}
		dec[ob.ID] = sorted
	}
	return dec
}

// fieldPositionContext walks up from a Field obligation to the nearest Field obligation ABOVE it (the
// position's parent field), collecting the intervening Refine obligations innermost-first -- the same
// shape refinementContext returns for a Refine. ok=false for a top-level field.
func (t *Tree) fieldPositionContext(ob Obligation) (field Obligation, intervening []Obligation, ok bool) {
	cur := ob
	for {
		if cur.Parent == NoParent || cur.Parent == cur.ID {
			return Obligation{}, nil, false
		}
		cur = t.obligations[cur.Parent]
		if cur.Kind == Field {
			return cur, intervening, true
		}
		if cur.Kind == Refine {
			intervening = append(intervening, cur)
		}
	}
}

// rewriteWithExpansions rebuilds the obligation slice, replacing each decided subtree with one
// Refine <U|>C> per member C holding a copy of the subtree (top field retyped to C). IDs are
// reassigned in emission order, so parents always precede children (the invariant
// resolveGoalsAndCand's reverse passes rely on).
func (t *Tree) rewriteWithExpansions(dec map[ObID][]string) {
	old := t.obligations
	children := map[ObID][]ObID{}
	var roots []ObID
	for _, ob := range old {
		if ob.Parent == NoParent || ob.Parent == ob.ID {
			roots = append(roots, ob.ID)
			continue
		}
		children[ob.Parent] = append(children[ob.Parent], ob.ID)
	}

	out := make([]Obligation, 0, len(old))
	// cloneSubtree copies the subtree rooted at id under parent; retype, when non-empty, replaces the
	// TOP obligation's owner type (the member the copy resolves on). Nested expansion decisions
	// inside a copy are NOT applied on this pass (the fixpoint loop re-decides on the rewritten tree).
	var cloneSubtree func(id, parent ObID, retype string)
	cloneSubtree = func(id, parent ObID, retype string) {
		ob := old[id]
		nid := ObID(len(out))
		ob.ID, ob.Parent = nid, parent
		if retype != "" {
			ob.Type = retype
		}
		out = append(out, ob)
		for _, c := range children[id] {
			cloneSubtree(c, nid, "")
		}
	}
	var emit func(id, parent ObID)
	emit = func(id, parent ObID) {
		ob := old[id]
		if members, decided := dec[id]; decided && ob.Kind == Field {
			for _, c := range members {
				refID := ObID(len(out))
				out = append(out, Obligation{ID: refID, Kind: Refine, Parent: parent, Type: ob.Type, Concrete: c})
				cloneSubtree(id, refID, c)
			}
			return
		}
		nid := ObID(len(out))
		nob := ob
		nob.ID, nob.Parent = nid, parent
		out = append(out, nob)
		for _, c := range children[id] {
			emit(c, nid)
		}
	}
	for _, r := range roots {
		emit(r, NoParent)
	}
	t.obligations = out
}
