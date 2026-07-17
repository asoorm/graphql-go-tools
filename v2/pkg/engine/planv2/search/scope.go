package search

// scope.go works out the specific route that serves each requested field, so lowering can place each
// fetch at the right response position.
//
// Why it's needed. The main settle keeps only one back-edge per node, so two sibling fields that
// resolve to the same node collapse onto a single route and one sibling's own edges vanish. Take
// `order { buyer { rating } seller { rating } }`: buyer.rating and seller.rating both resolve to the
// same user's rating, reached through one shared user object whose back-edge enters via `buyer` only,
// so `seller` never gets its own route. The fix, per field, is to mask out the routes into that
// field's ancestor objects that don't go through its own obligation parent -- so buyer.rating's route
// enters the user via `buyer` and seller.rating's via `seller`.
//
// This is purely additive. Cover.Edges, Selected, Cost, and Stats are exactly what the goal loop
// already computed; this file only adds Walks (the per-field routes), so all the golden numbers and
// counter bounds are unchanged -- masking only removes edges, never adds any.
//
// Completeness fallback. Like root pinning, the scoped route is a preference: if a field's node is
// unreachable under its mask (reachable only through a foreign route -- a genuine model gap), its
// route is traced over the plain back-edges instead, so no field the plan covered is ever lost.
//
// This package imports only the graph and obligation types.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// table is one settle result (cost + back-edge tables), shared/memoized by mask signature.
type table struct {
	pi   []int64
	back []hypergraph.EdgeID
}

// scopedWalks returns the route for every covered field, for Cover.Walks, plus the typed record of
// every scoped-walk fall-back firing (D10 amendment -- typed-loud fall-back). baseDisabled is the
// conditioned-jump filter; unmaskedPi/unmaskedBack are the plain settle tables used as the fallback.
// rootEntry is the shared root-entering-Field-edge index (computed once by searchWith); memo arrives
// pre-seeded with the tables searchWith already settled (keyed by mask signature), so a scope mask
// identical in content to one of those reuses the table instead of settling again -- settle is
// deterministic, so this changes no route. The only error it can raise is a state-cap trip on one of
// its settles -- which can't actually happen here, since the main settle already succeeded and these
// settles run over fewer edges, but it's propagated rather than swallowed.
func scopedWalks(h *hypergraph.Hypergraph, o *obligation.Tree, cfg Config,
	baseDisabled map[hypergraph.EdgeID]bool, cover *Cover,
	unmaskedPi []int64, unmaskedBack []hypergraph.EdgeID,
	rootEntry map[hypergraph.EdgeID]string,
	memo map[string]table, osc *opScope) (map[obligation.GoalID][]hypergraph.EdgeID, []RouteFallback, error) {

	// Per-Search indexes shared by every goal's mask. Under the operation-scoped mode they are the
	// memoized WHOLE-graph indexes projected to sub ids (opscope.go) -- mask semantics must follow
	// the whole graph in both modes (FORMAL_SPEC Section 6.5, S2) -- with the same per-plan obCoord build.
	var pre *scopeIndex
	if osc != nil {
		pre = &scopeIndex{
			objFieldEdges: osc.objFieldSub,
			coordObj:      osc.sup.graphIndex.coordObj,
			modelledRoots: osc.pinnable,
			obCoord:       buildObCoord(o),
		}
	} else {
		fieldObj := fieldNodeObjectType(h)               // field node -> the object type it descends into
		pre = precomputeScope(h, o, rootEntry, fieldObj) // exactly the pre-scoped-mode computation
	}

	// Per-goal traces need a fresh visited set each. An epoch-stamped slice reused across goals
	// avoids allocating a map per goal (bumping the epoch is the reset) -- but it is sized by node
	// count, so on a big graph with few goals the small per-goal maps are cheaper than zeroing it.
	// Both bookkeeping schemes visit in the same order and return identical routes.
	useEpoch := h.NumNodes() <= 128*(len(cover.Selected)+1)
	var seen []uint32
	var epoch uint32
	if useEpoch {
		seen = make([]uint32, h.NumNodes())
	}

	// A goal's mask is a pure function of (its parent obligation, its root field): baseDisabled is
	// fixed, the foreign-root part depends only on the root field, and chainCoords walks the chain
	// starting at the parent. Sibling goals therefore share one mask (and one signature) -- a wide
	// selection of N siblings builds it once, not N times.
	type scopeKey struct {
		parent obligation.ObID
		rf     string
	}
	type goalScope struct {
		mask map[hypergraph.EdgeID]bool
		sig  string
	}
	scopeCache := map[scopeKey]goalScope{}

	walks := make(map[obligation.GoalID][]hypergraph.EdgeID, len(cover.Selected))
	var fallbacks []RouteFallback
	var layered *layeredIndex // fetched lazily; only inconsistent walks need the layered trace
	for _, g := range o.Goals() {
		v, covered := cover.Selected[g]
		if !covered {
			continue // null / uncovered fields have no route
		}
		key := scopeKey{parent: o.Ob(g).Parent, rf: o.RootField(g)}
		sc, ok := scopeCache[key]
		if !ok {
			sc.mask = scopeMask(h, o, g, baseDisabled, rootEntry, pre)
			if len(sc.mask) > 0 {
				sc.sig = maskSignature(sc.mask)
			}
			scopeCache[key] = sc
		}
		back := unmaskedBack // fallback (and the no-mask case)
		goalTable := table{pi: unmaskedPi, back: unmaskedBack}
		fellBack := false
		if len(sc.mask) > 0 {
			tb, ok := memo[sc.sig]
			if !ok {
				mpi, mback, _, err := settleMasked(h, cfg, sc.mask)
				if err != nil {
					return nil, nil, err
				}
				tb = table{pi: mpi, back: mback}
				memo[sc.sig] = tb
			}
			goalTable = tb
			if tb.pi[v] < Inf { // the masked route still reaches the node -- prefer it
				back = tb.back
			} else {
				fellBack = true // reachable only through a foreign route -- a genuine model gap
			}
		}
		if useEpoch {
			epoch++
			walks[g] = tracebackEpoch(h, back, v, seen, epoch, nil)
		} else {
			walks[g] = Traceback(h, back, v, map[hypergraph.NodeID]bool{})
		}
		// CHAIN-LAYERED consistent trace (FORMAL_SPEC D10 amendment -- chain-layered consistent
		// trace): when the traced walk's spine does not spell the goal's obligation chain -- depth
		// aliasing on a repeated type, a sneak entry, or a foreign-root fallback -- re-trace over the
		// (node, chainPos) layered product of the goal's masked graph, which finds the minimum-cost
		// chain-consistent walk exactly (and the candidate it reaches) when one exists. Conditional:
		// a goal whose default walk is already consistent is untouched, so every consistent plan is
		// byte-identical. On success the goal is no longer fallback-served (the route IS consistent).
		if fields := chainFields(o, g); !walkChainConsistent(h, walks[g], v, fields) {
			if layered == nil {
				layered = layeredIndexFor(h) // cached per-H: pure function of the immutable graph
			}
			nv, nwalk, spine, ok := consistentTrace(h, layered, fields, cands(o, g, osc), goalTable, sc.mask)
			if !ok && len(sc.mask) > 0 {
				// Tail-reachability RETRY tier (FORMAL_SPEC D10 amendment -- chain-layered consistent
				// trace; realizes D7ppp): a distributed-key jump's tails are gathering inputs that
				// legitimately live on SIBLING coordinates of the goal's own chain, which the kappa mask
				// (correctly, for spine purposes) removes -- making the jump unusable on the masked
				// table even though a chain-consistent spine exists. Retry with the UNMASKED table
				// for jump-tail reachability and tail sub-walks; the mask still constrains the spine
				// edges (the layered product itself forces the spine to spell the chain). Fires only
				// where the goal would otherwise be fallback-served or phantom, so it can only
				// convert a fall-back into a chain-consistent route.
				nv, nwalk, spine, ok = consistentTrace(h, layered, fields, cands(o, g, osc),
					table{pi: unmaskedPi, back: unmaskedBack}, sc.mask)
			}
			if ok {
				walks[g] = nwalk
				if cover.Spines == nil {
					cover.Spines = map[obligation.GoalID][]hypergraph.EdgeID{}
				}
				cover.Spines[g] = spine
				if nv != v {
					// The consistent route may end on a DIFFERENT candidate (the position-local one);
					// repair the selection so lowering reads the node the walk actually serves.
					cover.Selected[g] = nv
					v = nv
				}
				fellBack = false
			}
		}
		if fellBack {
			// NARROWING guard (FORMAL_SPEC D10 amendment -- provable-non-resolvability narrowing),
			// same shared predicate as the root-pin site: a provably non-resolvable goal is never
			// served through the fall-back route. Checked only after the chain-layered repair
			// attempt -- a goal the consistent trace repaired is not fallback-served at all.
			if provablyNonResolvable(h, cands(o, g, osc)) {
				return nil, nil, &ErrNoValidPlan{Obligation: g, Reason: reasonProvablyNonResolvable}
			}
			// Record the firing (observability only -- the walk above is unchanged by this).
			fallbacks = append(fallbacks, RouteFallback{
				Goal:       g,
				Kind:       RouteFallbackScopedWalk,
				Coordinate: goalCoordinate(o, g),
				RootField:  o.RootField(g),
				Node:       v,
				Subgraph:   h.SubgraphName(h.Node(v).Subgraph),
				Route:      walks[g],
			})
		}
	}
	return walks, fallbacks, nil
}

// scopeMask builds one field's mask: the conditioned-jump filter, plus the foreign operation roots,
// plus every "sibling" route into the field's ancestor objects. A sibling route is a Field edge that
// leads into an object the field's ancestor chain passes through, but via a field that is not on that
// chain. This is applied at every ancestor level, not just the immediate parent -- so a divergence two
// hops up (`order { buyer { pets { name } } seller { pets { name } } }` splitting at the user level
// under a pet-level field) is masked just like an immediate one. If the chain reaches the same object
// type by several on-chain fields (order.buyer and user.friends both lead to a user), all of them are
// kept -- the test is "on the chain?", not "the immediate parent?". Descent, type-move, and
// entity-jump edges are never masked, and the field's own edge and its key/@requires tails are left
// alone (they lead to other types, not ancestor objects).
func scopeMask(h *hypergraph.Hypergraph, o *obligation.Tree, g obligation.GoalID,
	baseDisabled map[hypergraph.EdgeID]bool, rootEntry map[hypergraph.EdgeID]string,
	pre *scopeIndex) map[hypergraph.EdgeID]bool {

	mask := make(map[hypergraph.EdgeID]bool, len(baseDisabled))
	for e := range baseDisabled {
		mask[e] = true
	}

	// Forbid entry through any other operation root, but only when this field's own root is a modelled
	// root edge (otherwise pinning to a root that doesn't exist would forbid every entrance -- see
	// buildRootTables).
	if rf := o.RootField(g); rf != "" && pre.modelledRoots[rf] {
		for e, f := range rootEntry {
			if f != rf {
				mask[e] = true
			}
		}
	}

	// allow is the set of on-chain (object type, field coordinate) pairs permitted to lead into the
	// chain's object types; any other Field edge leading into a chain object type is masked. Object
	// types the chain never touches are left alone (no pair mentions them). A flat pair slice with
	// linear membership scans replaces the earlier map-of-sets: chains are short (bounded by query
	// depth), so the scan is cheap and the per-goal map churn is gone. Membership semantics are
	// identical -- duplicates in the slice change no answer.
	allow := chainCoords(o, g, pre.coordObj, pre.obCoord)
	if len(allow) == 0 {
		return mask
	}
	if len(allow) <= smallChainCutoff {
		// Typical chains are short: a bounded linear scan per edge beats building maps, and it
		// allocates nothing.
		for _, fe := range pre.objFieldEdges { // every Field edge leading into an object, in edge order
			constrained := false // does the chain pass through this edge's object type?
			allowed := false     // and if so, is this edge's coordinate on the chain?
			for _, pr := range allow {
				if pr.objType != fe.objType {
					continue
				}
				constrained = true
				if pr.coord == fe.coord {
					allowed = true
					break
				}
			}
			if constrained && !allowed {
				mask[fe.id] = true // a sibling route into a chain object type
			}
		}
		return mask
	}
	// A long pair list (a chain coordinate descending into many object types) would make the scan
	// above quadratic, so index it first -- this is exactly the map the pre-flattening chainCoords
	// built per goal, producing the identical mask.
	allowSet := make(map[string]map[string]bool, len(allow))
	for _, pr := range allow {
		if allowSet[pr.objType] == nil {
			allowSet[pr.objType] = map[string]bool{}
		}
		allowSet[pr.objType][pr.coord] = true
	}
	for _, fe := range pre.objFieldEdges {
		coords, constrained := allowSet[fe.objType]
		if !constrained {
			continue // the chain never passes through this object type
		}
		if !coords[fe.coord] {
			mask[fe.id] = true // a sibling route into a chain object type
		}
	}
	return mask
}

// smallChainCutoff is the allow-pair count up to which scopeMask uses the allocation-free linear
// scan; above it, it indexes the pairs into maps first (identical mask either way).
const smallChainCutoff = 16

// compactStrings removes adjacent duplicates from a sorted slice, in place.
func compactStrings(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// scopeIndex holds the per-Search indexes every goal's scopeMask/chainCoords share, so the O(|E|)
// and O(|V|) scans (and their "Type.field" string builds) run once per Search instead of once per
// goal. Contents are read-only after precomputeScope.
type scopeIndex struct {
	// objFieldEdges lists every Field edge whose head field descends into an object type, with the
	// head's coordinate and that object type precomputed, in edge-ID order (the same order the old
	// per-goal full-edge scan visited them; mask content is order-independent anyway).
	objFieldEdges []objFieldEdge
	// coordObj maps a field coordinate ("Type.field") to the object types it descends into.
	coordObj map[string][]string
	// modelledRoots is the set of root fields that have a modelled root-entering Field edge.
	modelledRoots map[string]bool
	// obCoord holds each Field obligation's "Type.field" coordinate, indexed by ObID ("" for
	// non-Field obligations), so chainCoords doesn't rebuild the string per chain step per goal.
	obCoord []string
}

type objFieldEdge struct {
	id      hypergraph.EdgeID
	objType string // the object type the head field descends into
	coord   string // the head field's "Type.field" coordinate
}

// scopeGraphIndex is the pure-graph half of the scopeIndex: objFieldEdges and coordObj depend only
// on the immutable graph, so the operation-scoped mode memoizes them per graph (opscope.go) while
// the default path keeps computing them per plan inside precomputeScope, byte-identically.
type scopeGraphIndex struct {
	objFieldEdges []objFieldEdge
	coordObj      map[string][]string
}

// buildScopeGraphIndex builds the pure-graph half: one pass over fieldObj for coordObj, one pass
// over the edges for objFieldEdges. Extracted verbatim from precomputeScope.
func buildScopeGraphIndex(h *hypergraph.Hypergraph, fieldObj map[hypergraph.NodeID]string) *scopeGraphIndex {
	gi := &scopeGraphIndex{coordObj: make(map[string][]string, len(fieldObj))}
	// Field coordinate -> the object types it descends into, from the graph's Descent edges (read
	// via fieldObj, exactly as chainCoords used to build this per goal). Values are sorted and
	// deduplicated: everything downstream only tests membership (the old per-goal map collapsed
	// duplicates anyway), and fieldObj is a map, so the pre-sort order carried no meaning.
	coordOf := make(map[hypergraph.NodeID]string, len(fieldObj)) // field node -> its coordinate
	for n, objType := range fieldObj {
		coord := h.NodeType(n) + "." + h.NodeField(n)
		coordOf[n] = coord
		gi.coordObj[coord] = append(gi.coordObj[coord], objType)
	}
	for coord, types := range gi.coordObj {
		sort.Strings(types)
		gi.coordObj[coord] = compactStrings(types)
	}
	for i := 0; i < h.NumEdges(); i++ {
		id := hypergraph.EdgeID(i)
		if h.EdgeKind(id) != hypergraph.EdgeField {
			continue
		}
		head := h.EdgeHead(id)
		objType, produces := fieldObj[head]
		if !produces {
			continue // a scalar-leaf field: leads into no object
		}
		gi.objFieldEdges = append(gi.objFieldEdges, objFieldEdge{id: id, objType: objType, coord: coordOf[head]})
	}
	return gi
}

// buildObCoord prebuilds each Field obligation's "Type.field" coordinate string (per-plan; the
// obligation tree is per-operation). Extracted from precomputeScope so the operation-scoped mode
// shares it.
func buildObCoord(o *obligation.Tree) []string {
	obs := o.Obligations()
	obCoord := make([]string, len(obs))
	for i := range obs {
		if obs[i].Kind == obligation.Field {
			obCoord[i] = obs[i].Type + "." + obs[i].Field
		}
	}
	return obCoord
}

// precomputeScope builds the scopeIndex: one pass over the edges for objFieldEdges, one pass over
// fieldObj for coordObj, one pass over the obligations for obCoord, and one pass over rootEntry for
// modelledRoots. Pure re-arrangement of what scopeMask/chainCoords previously recomputed per goal --
// no behavioral change.
func precomputeScope(h *hypergraph.Hypergraph, o *obligation.Tree,
	rootEntry map[hypergraph.EdgeID]string, fieldObj map[hypergraph.NodeID]string) *scopeIndex {

	gi := buildScopeGraphIndex(h, fieldObj)
	pre := &scopeIndex{
		objFieldEdges: gi.objFieldEdges,
		coordObj:      gi.coordObj,
		modelledRoots: make(map[string]bool, len(rootEntry)),
		obCoord:       buildObCoord(o),
	}
	for _, f := range rootEntry {
		pre.modelledRoots[f] = true
	}
	return pre
}

// coordPair is one on-chain permission: the chain reaches objType via the field at coord.
type coordPair struct {
	objType string
	coord   string
}

// chainCoords walks a field's chain of ancestor obligations (bounded by the tree size, so it always
// terminates) and returns the on-chain (object type, "Type.field") pairs: per object type the chain
// passes through, which on-chain coordinates are allowed to lead into it. Each Field ancestor
// contributes its coordinate to every object type that field descends into -- coordObj is that
// mapping, read from the graph's Descent edges (see precomputeScope), so it's keyed the same way the
// mask loop reads it back (including abstract descent, where the object is the abstract type rather
// than a concrete member); obCoord holds each Field obligation's prebuilt coordinate string. Refine
// ancestors carry no field and are skipped. The self-loop guard mirrors the one in tree.go for
// hand-assembled trees.
func chainCoords(o *obligation.Tree, g obligation.GoalID,
	coordObj map[string][]string, obCoord []string) []coordPair {

	var allow []coordPair
	obs := o.Obligations()
	ob := o.Ob(g)
	for ob.Parent != obligation.NoParent && ob.Parent != ob.ID && int(ob.Parent) < len(obs) {
		p := obs[ob.Parent]
		if p.Kind == obligation.Field {
			coord := obCoord[ob.Parent]
			for _, objType := range coordObj[coord] {
				allow = append(allow, coordPair{objType: objType, coord: coord})
			}
		}
		ob = p
	}
	return allow
}

// fieldNodeObjectType maps each Field node to the object type its Descent edge leads into (e.g. the
// order.buyer field node -> "User"). A scalar-leaf field node (no Descent) is absent.
func fieldNodeObjectType(h *hypergraph.Hypergraph) map[hypergraph.NodeID]string {
	out := map[hypergraph.NodeID]string{}
	for i := 0; i < h.NumEdges(); i++ {
		id := hypergraph.EdgeID(i)
		if h.EdgeKind(id) != hypergraph.EdgeDescent {
			continue
		}
		objType := h.NodeType(h.EdgeHead(id))
		for _, t := range h.EdgeTails(id) { // a Descent edge has one tail: the field node
			if h.NodeKind(t) == hypergraph.NodeField {
				out[t] = objType
			}
		}
	}
	return out
}

// rootEntryFields indexes each root-entering Field edge by the field it resolves.
func rootEntryFields(h *hypergraph.Hypergraph) map[hypergraph.EdgeID]string {
	out := map[hypergraph.EdgeID]string{}
	for i := 0; i < h.NumEdges(); i++ {
		id := hypergraph.EdgeID(i)
		if h.EdgeKind(id) != hypergraph.EdgeField {
			continue
		}
		tails := h.EdgeTails(id)
		if len(tails) == 0 {
			continue
		}
		if h.NodeKind(tails[0]) != hypergraph.NodeRoot {
			continue
		}
		out[id] = h.NodeField(h.EdgeHead(id))
	}
	return out
}

// immediateParentCoord returns the "Type.field" coordinate of a field's immediate parent obligation.
// ok is false when the field is top-level (no parent) or its parent is not a Field obligation (a
// Refine gate has no coordinate).
func immediateParentCoord(o *obligation.Tree, g obligation.GoalID) (string, bool) {
	ob := o.Ob(g)
	if ob.Parent == obligation.NoParent || ob.Parent == ob.ID {
		return "", false
	}
	obs := o.Obligations()
	if int(ob.Parent) >= len(obs) {
		return "", false
	}
	p := obs[ob.Parent]
	if p.Kind != obligation.Field {
		return "", false
	}
	return p.Type + "." + p.Field, true
}

// tracebackEpoch is Traceback with an epoch-stamped visited slice instead of a per-call map, and a
// caller-provided output slice it appends into. Visit order -- and so the returned route -- is
// IDENTICAL to Traceback's (edge first, then each tail in order); only the bookkeeping differs.
// scopedWalks runs one trace per goal, so the map-per-goal was pure allocation churn: bumping the
// epoch resets the reused slice for free.
func tracebackEpoch(h *hypergraph.Hypergraph, back []hypergraph.EdgeID, v hypergraph.NodeID,
	seen []uint32, epoch uint32, out []hypergraph.EdgeID) []hypergraph.EdgeID {
	if seen[v] == epoch {
		return out
	}
	seen[v] = epoch
	e := back[v]
	if e == hypergraph.NoEdge { // a root, or a node that was never reached
		return out
	}
	out = append(out, e)
	for _, t := range h.EdgeTails(e) {
		out = tracebackEpoch(h, back, t, seen, epoch, out)
	}
	return out
}

// maskSignature renders a stable, order-independent key for a mask so equal masks reuse one settle.
func maskSignature(mask map[hypergraph.EdgeID]bool) string {
	ids := make([]int, 0, len(mask))
	for e := range mask {
		ids = append(ids, int(e))
	}
	sort.Ints(ids)
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(strconv.Itoa(id))
		b.WriteByte(',')
	}
	return b.String()
}
