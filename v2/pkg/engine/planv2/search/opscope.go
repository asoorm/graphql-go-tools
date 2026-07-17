package search

// opscope.go -- the operation-scoped settle domain (FORMAL_SPEC Section 6.5; PROOFS Section 8 S1-S3).
//
// The default search settles the WHOLE graph on every plan, so per-plan cost is proportional to
// the schema, not the query (BENCHMARKS.md Section 5.3's residual). The operation-scoped mode
// (Config.OperationScoped) computes, per plan, the backward closure of the operation's candidate
// nodes -- every edge any derivation of any goal candidate could use (S1, proven) -- induces an
// order-preserving sub-hypergraph over it, and runs the UNCHANGED settle/traceback/consistent-trace
// machinery on that sub-graph. The kernel is untouched; only its input domain shrinks.
//
// Plan equality is the mode's contract: masks are derived from whole-graph indexes in both modes
// (memoized per graph here; computed per plan by the default path), the C.4 order is content-based
// so it is preserved verbatim under the order-preserving id remap, and results are translated back
// to whole-graph ids before they escape. S2/S3 (table and layered-trace agreement) carry the
// argument; the dual-mode oracle and the audit/conformance/differential equality gates enforce it
// per committed case. What may legitimately differ: resource-guard behavior only -- the preflight
// runs on the FULL graph in both modes, but the scoped settle counts strictly fewer states, so a
// StateCap-tripping instance can plan scoped (Section 6.5 "what may legitimately differ").

import (
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// tsKey identifies a plain object node by (composed type name, subgraph) -- the pre-jump-object
// lookup key (Section 6.5 rule 3), mirroring consistent.go's objByTS.
type tsKey struct {
	t string
	s hypergraph.SubgraphID
}

// scopeSupport holds the pure-graph indexes the scoped mode reads: the per-plan scope computation
// (objByTS), the base-mask derivation (conditioned jumps, subscription-root edges), and the
// whole-graph mask semantics (rootEntry, objFieldEdges, coordObj -- the same values the default
// path computes per plan). Read-only after construction; memoized per graph (hypergraph.Memo,
// SlotSearchOpScope). Only the scoped mode consults it, so the default path's per-plan scans are
// byte-identical to before this file existed.
type scopeSupport struct {
	objByTS      map[tsKey]hypergraph.NodeID  // plain (scope=="") object node per (type, subgraph)
	conditioned  []hypergraph.EdgeID          // entity jumps carrying key conditions, edge-id order
	subRootEdges []hypergraph.EdgeID          // root-entering Field edges departing a subscription root
	rootEntry    map[hypergraph.EdgeID]string // rootEntryFields(h), whole graph
	graphIndex   *scopeGraphIndex             // objFieldEdges + coordObj, whole graph (scope.go)
}

// scopeSupportFor returns the graph's scoped-mode support index, building it on first use.
func scopeSupportFor(h *hypergraph.Hypergraph) *scopeSupport {
	return h.Memo(hypergraph.SlotSearchOpScope, func() any { return buildScopeSupport(h) }).(*scopeSupport)
}

func buildScopeSupport(h *hypergraph.Hypergraph) *scopeSupport {
	sup := &scopeSupport{
		objByTS:    map[tsKey]hypergraph.NodeID{},
		rootEntry:  rootEntryFields(h),
		graphIndex: buildScopeGraphIndex(h, fieldNodeObjectType(h)),
	}
	for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
		n := h.Node(id)
		if n.Kind == hypergraph.NodeObject && n.Scope == "" {
			sup.objByTS[tsKey{n.Type, n.Subgraph}] = id
		}
	}
	// Subscription roots, for the D11.12 base mask (maskForeignSubscriptionRoots' scan, hoisted).
	subRoot := map[hypergraph.NodeID]bool{}
	for _, r := range h.Roots() {
		if h.NodeField(r) == "subscription" {
			subRoot[r] = true
		}
	}
	for i := 0; i < h.NumEdges(); i++ {
		id := hypergraph.EdgeID(i)
		switch h.EdgeKind(id) {
		case hypergraph.EdgeEntityJump:
			if len(h.EdgeConditions(id)) > 0 {
				sup.conditioned = append(sup.conditioned, id)
			}
		case hypergraph.EdgeField:
			if len(subRoot) == 0 {
				continue
			}
			tails := h.EdgeTails(id)
			if len(tails) > 0 && subRoot[tails[0]] {
				sup.subRootEdges = append(sup.subRootEdges, id)
			}
		}
	}
	return sup
}

// opScope is one plan's operation-scoped domain: the induced sub-hypergraph, the id translation
// tables, and the per-plan sub-space projections of the whole-graph mask indexes. Per-plan and
// single-goroutine; nothing here is shared across Search calls except the memoized support.
type opScope struct {
	full     *hypergraph.Hypergraph
	sub      *hypergraph.Hypergraph
	origNode []hypergraph.NodeID // sub node -> full node (ascending; order-preserving)
	origEdge []hypergraph.EdgeID // sub edge -> full edge (ascending; order-preserving)
	subNode  []int32             // full node -> sub node; -1 outside the scope
	subEdge  []int32             // full edge -> sub edge; -1 outside the scope
	sup      *scopeSupport

	// rootEntrySub is the whole-graph rootEntry restricted to the scope, in sub edge ids -- the
	// mask-building view. Mutated by searchWith's disabled-entry filter exactly like the default
	// path's map (it is per-plan, so that is safe).
	rootEntrySub map[hypergraph.EdgeID]string
	// pinnable is the whole-graph fieldHasOwnRoot set: the names of root fields that keep a
	// modelled root-entering Field edge after the D11.12 subscription-root filter. Root-pinning
	// DECISIONS must follow the whole graph (a goal whose own root edge exists but lies outside
	// the scope must still pin -- and then fall back -- exactly as the default mode does), while the
	// pinning MASKS are sub-space; hence decision set and mask view are split.
	pinnable map[string]bool
	// objFieldSub is the whole-graph objFieldEdges restricted to the scope, translated to sub edge
	// ids with the whole-graph objType/coord strings kept -- so scopeMask masks exactly the default
	// mode's mask  intersect  scope (edges outside the scope are absent from the sub-graph entirely, which
	// masking subsumes).
	objFieldSub []objFieldEdge

	candCache map[obligation.GoalID][]hypergraph.NodeID
}

// computeOpScope computes the Section 6.5 scope for one plan: candidate seeds, backward closure over the
// incoming-edge index, and the jump pre-object widening (rule 3, for S3 layered-trace agreement).
// Cost is proportional to the scope itself plus the per-plan projections (one pass over the
// support's whole-graph index slices).
func computeOpScope(h *hypergraph.Hypergraph, o *obligation.Tree) *opScope {
	sup := scopeSupportFor(h)
	nodeIn := make([]bool, h.NumNodes())
	edgeIn := make([]bool, h.NumEdges())
	stack := make([]hypergraph.NodeID, 0, 64)
	push := func(v hypergraph.NodeID) {
		if !nodeIn[v] {
			nodeIn[v] = true
			stack = append(stack, v)
		}
	}
	for _, g := range o.Goals() {
		for _, v := range o.Cand(g) {
			push(v) // seeds: every candidate of every goal (exempt or not -- over-approximation)
		}
	}
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range h.Incoming(v) {
			if edgeIn[e] {
				continue
			}
			edgeIn[e] = true
			for _, t := range h.EdgeTails(e) {
				push(t)
			}
			if h.EdgeKind(e) == hypergraph.EdgeEntityJump {
				// Section 6.5 rule 3: widen with the jump's pre-jump object nodes -- the layered trace
				// relaxes from them (consistent.go jumpsByPre), and they are not graph tails.
				edge := h.Edge(e)
				kt := edge.KeyTails
				if len(kt) == 0 {
					kt = edge.Tails
				}
				for _, t := range kt {
					n := h.Node(t)
					if obj, ok := sup.objByTS[tsKey{n.Type, n.Subgraph}]; ok {
						push(obj)
					}
				}
			}
		}
	}

	sub, origNode, origEdge := h.Restrict(nodeIn, edgeIn)
	subNode := make([]int32, h.NumNodes())
	for i := range subNode {
		subNode[i] = -1
	}
	for i, v := range origNode {
		subNode[v] = int32(i)
	}
	subEdge := make([]int32, h.NumEdges())
	for i := range subEdge {
		subEdge[i] = -1
	}
	for i, e := range origEdge {
		subEdge[e] = int32(i)
	}

	sc := &opScope{
		full: h, sub: sub,
		origNode: origNode, origEdge: origEdge,
		subNode: subNode, subEdge: subEdge,
		sup:       sup,
		candCache: map[obligation.GoalID][]hypergraph.NodeID{},
	}

	// Per-plan sub-space projections of the whole-graph mask indexes.
	sc.rootEntrySub = make(map[hypergraph.EdgeID]string, len(sup.rootEntry))
	for e, f := range sup.rootEntry {
		if s := subEdge[e]; s >= 0 {
			sc.rootEntrySub[hypergraph.EdgeID(s)] = f
		}
	}
	maskedRoot := map[hypergraph.EdgeID]bool{}
	if !o.SubscriptionOperation() {
		for _, e := range sup.subRootEdges {
			maskedRoot[e] = true
		}
	}
	sc.pinnable = make(map[string]bool, len(sup.rootEntry))
	for e, f := range sup.rootEntry {
		if !maskedRoot[e] {
			sc.pinnable[f] = true // whole-graph decision set (see the field's doc)
		}
	}
	for _, fe := range sup.graphIndex.objFieldEdges {
		if s := subEdge[fe.id]; s >= 0 {
			sc.objFieldSub = append(sc.objFieldSub, objFieldEdge{
				id: hypergraph.EdgeID(s), objType: fe.objType, coord: fe.coord,
			})
		}
	}
	return sc
}

// baseDisabledSub is the scoped mode's base mask in sub edge ids: the conditioned entity jumps the
// operation doesn't justify (disabledConditionEdges' semantics over the memoized jump list) plus
// the D11.12 foreign-subscription-root edges (maskForeignSubscriptionRoots' semantics over the
// memoized edge list). Entries outside the scope are dropped -- the sub-graph doesn't contain them,
// which masking subsumes.
func (sc *opScope) baseDisabledSub(o *obligation.Tree) map[hypergraph.EdgeID]bool {
	var disabled map[hypergraph.EdgeID]bool
	add := func(orig hypergraph.EdgeID) {
		if s := sc.subEdge[orig]; s >= 0 {
			if disabled == nil {
				disabled = map[hypergraph.EdgeID]bool{}
			}
			disabled[hypergraph.EdgeID(s)] = true
		}
	}
	coords, fields := operationCoordSets(o)
	for _, e := range sc.sup.conditioned {
		if !conditionsSatisfied(sc.full.EdgeConditions(e), coords, fields) {
			add(e)
		}
	}
	if !o.SubscriptionOperation() {
		for _, e := range sc.sup.subRootEdges {
			add(e)
		}
	}
	return disabled
}

// cand translates a goal's candidate nodes to sub ids, preserving order (bestCandidate's tie-break
// is content-based, so order carries no meaning, but identical iteration order keeps the modes
// step-for-step comparable). Candidates are the scope's seeds, so every one is present; the guard
// is defensive.
func (sc *opScope) cand(o *obligation.Tree, g obligation.GoalID) []hypergraph.NodeID {
	if v, ok := sc.candCache[g]; ok {
		return v
	}
	orig := o.Cand(g)
	out := make([]hypergraph.NodeID, 0, len(orig))
	for _, v := range orig {
		if s := sc.subNode[v]; s >= 0 {
			out = append(out, hypergraph.NodeID(s))
		}
	}
	sc.candCache[g] = out
	return out
}

// cands is the candidate-set seam shared by the goal loop and scopedWalks: the tree's candidates
// in the graph the search is running over (sub ids under the scoped mode, the tree's own ids
// otherwise).
func cands(o *obligation.Tree, g obligation.GoalID, sc *opScope) []hypergraph.NodeID {
	if sc == nil {
		return o.Cand(g)
	}
	return sc.cand(o, g)
}

// translateResult maps a Result computed over the sub-graph back to whole-graph ids, in place:
// cover edges/selections/walks/spines, fall-back records, and the pi/back tables the lowering
// merge reads (expanded to full-size arrays -- nodes outside the scope read Inf/NoEdge, which S1
// guarantees no consumer with a plan-observable output ever dereferences: merge reads candidates
// and re-traces from them, both inside the scope).
func (sc *opScope) translateResult(res *Result) {
	res.Pi = sc.expandPi(res.Pi)
	res.Back = sc.expandBack(res.Back)
	for k, t := range res.rootTables {
		res.rootTables[k] = rootTable{pi: sc.expandPi(t.pi), back: sc.expandBack(t.back)}
	}
	c := res.Cover
	for i, e := range c.Edges {
		c.Edges[i] = sc.origEdge[e] // order-preserving remap keeps the slice sorted
	}
	for g, v := range c.Selected {
		c.Selected[g] = sc.origNode[v]
	}
	// Route slices can ALIAS walk slices (a scoped-walk RouteFallback records Route: walks[g],
	// the same backing array), so in-place translation must run at most once per distinct slice --
	// dedupe by the backing array's first element address.
	seen := map[*hypergraph.EdgeID]bool{}
	translate := func(edges []hypergraph.EdgeID) {
		if len(edges) == 0 || seen[&edges[0]] {
			return
		}
		seen[&edges[0]] = true
		for i, e := range edges {
			edges[i] = sc.origEdge[e]
		}
	}
	for _, w := range c.Walks {
		translate(w)
	}
	for _, s := range c.Spines {
		translate(s)
	}
	for i := range res.RouteFallbacks {
		f := &res.RouteFallbacks[i]
		f.Node = sc.origNode[f.Node]
		translate(f.Route)
	}
}

func (sc *opScope) expandPi(sub []int64) []int64 {
	out := make([]int64, sc.full.NumNodes())
	for i := range out {
		out[i] = Inf
	}
	for s, v := range sub {
		out[sc.origNode[s]] = v
	}
	return out
}

func (sc *opScope) expandBack(sub []hypergraph.EdgeID) []hypergraph.EdgeID {
	out := make([]hypergraph.EdgeID, sc.full.NumNodes())
	for i := range out {
		out[i] = hypergraph.NoEdge
	}
	for s, e := range sub {
		if e != hypergraph.NoEdge {
			out[sc.origNode[s]] = sc.origEdge[e]
		}
	}
	return out
}
