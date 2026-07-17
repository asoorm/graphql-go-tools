package search

// search.go is the entry point. Search runs the whole thing: reject too-large queries up front,
// settle the cheapest cost to every node, then for each field the client asked for pick the cheapest
// candidate node that can serve it and trace its route back to the roots. Routes that several fields
// share are traced once, so the returned plan is a DAG of edges with its total cost. This package
// imports only the graph and obligation types so it can be reasoned about and model-checked on its
// own. Spec: FORMAL_SPEC Section 6.1.
//
// Conditioned entity jumps. Some entity jumps are only usable when the operation actually supplies
// the key fields they need. The search works one node at a time and has no memory of the path taken
// to reach a node, so it cannot make that decision mid-search. Instead it decides up front: a
// conditioned jump is kept only if at least one of its key conditions is satisfied -- every field the
// condition names appears somewhere in what the operation asks for. Jumps that fail this are masked
// out before the search, so a field reachable only through an unusable jump ends up unreachable and
// is reported as ErrNoValidPlan, rather than producing a plan that fetches through a key the
// operation never provides. Unconditional jumps are always kept.
//
// Known limitation: this "does the field appear anywhere?" check is deliberately generous. It looks
// across the whole operation, not at the specific branch where the jump is used, so a field present
// on a different branch can still enable a jump. This never produces an unsound plan (a jump still
// can't fire until all its key tails are reached); the residual risk is a plan routing through a
// conditioned jump on a branch that would not supply the key. The audit and differential layers
// check this against real subgraph schemas. Tightening the check to the exact branch is a follow-up.

import (
	"sort"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// Cover is the plan the search produces: which edges to use, which node serves each requested field,
// which fields resolve to null instead, and the total cost.
type Cover struct {
	Edges    []hypergraph.EdgeID                     // the edges in the plan, sorted and deduped
	Selected map[obligation.GoalID]hypergraph.NodeID // the chosen serving node for each covered field
	Nulls    []obligation.GoalID                     // fields that resolve to null (narrowed-away members)
	Cost     int64                                   // total cost, summed over the deduped Edges

	// Walks records, per requested field, the specific route (list of edges) that serves it. Edges is
	// the deduped union of all these routes, so pricing over Edges is unchanged. This matters when two
	// sibling fields resolve to the same node: they get distinct routes here (buyer.rating enters the
	// user via `buyer`, seller.rating via `seller`) even though the deduped Edges set collapses them.
	// Lowering uses these routes to place each fetch at the right response position.
	Walks map[obligation.GoalID][]hypergraph.EdgeID

	// Spines records, for each goal repaired by the CHAIN-LAYERED consistent trace (D10 amendment --
	// chain-layered consistent trace), the ordered root->leaf SPINE of its walk: the exact edge
	// sequence whose Field edges spell the goal's obligation chain. A walk is an edge SET keyed by
	// head node, and a chain that revisits a (type, subgraph) object collapses onto one node -- the
	// spine preserves the order/depth information the set cannot (which entity jump enters at which
	// obligation depth). Goals absent from this map keep the walk-derived attribution (their walks
	// are chain-consistent by the default trace, or fell back). Lowering consumes this; recording it
	// here is what keeps lowering from re-deriving routing upstream.
	Spines map[obligation.GoalID][]hypergraph.EdgeID
}

// Result is Search's full output: the plan, plus the raw tables the later lowering and merge steps
// need. Pi holds the cheapest cost to each node and Back the edge that achieved it; both are used
// when the merge step re-traces or co-locates fetches. Stats are the counters. Visited is filled by
// the traceback below (each node is expanded at most once across all fields).
type Result struct {
	Cover *Cover
	Pi    []int64             // cheapest cost to reach each node
	Back  []hypergraph.EdgeID // the edge that achieved that cost, per node
	Stats SettleStats         // counters; Visited is filled by Search

	// RouteFallbacks are the D10 fall-back firings for this plan (FORMAL_SPEC D10 amendment --
	// typed-loud fall-back): each records a goal that could not be served path-consistently and the
	// route taken instead. Empty on a fully path-consistent plan. Root-pin records (goal-loop order)
	// precede scoped-walk records (goal order); both orders are deterministic. Observability only --
	// the recorded routes are exactly the ones the plan already uses.
	RouteFallbacks []RouteFallback

	// Some fields are pinned to enter through their own operation root rather than a foreign one (see
	// buildRootTables). For those, we compute a separate cost/back table with the foreign root
	// entrances removed. goalRoot names which field a constrained goal is pinned to, and rootTables
	// holds that field's table. A goal absent from goalRoot uses the plain Pi/Back. PiFor/BackFor
	// below hand out the right table, so the merge step re-traces on the same table the field was
	// actually chosen against -- otherwise a re-trace could bring back the very route this pinning
	// removed.
	goalRoot   map[obligation.GoalID]string
	rootTables map[string]rootTable
}

// rootTable is one cost/back table computed with foreign root entrances removed, for a single root
// field.
type rootTable struct {
	pi   []int64
	back []hypergraph.EdgeID
}

// PiFor returns the cost table the given field was actually chosen against: its pinned table if it
// is root-pinned, otherwise the plain Pi. The merge step reads this so its reachability check
// matches the plan the search emitted.
func (r *Result) PiFor(g obligation.GoalID) []int64 {
	if k, ok := r.goalRoot[g]; ok {
		if t, ok := r.rootTables[k]; ok {
			return t.pi
		}
	}
	return r.Pi
}

// BackFor returns the back-edge table the given field was traced through: its pinned table if it is
// root-pinned, otherwise the plain Back. The merge step re-traces through this so a re-trace can't
// exit via a foreign root that the pinned table removed.
func (r *Result) BackFor(g obligation.GoalID) []hypergraph.EdgeID {
	if k, ok := r.goalRoot[g]; ok {
		if t, ok := r.rootTables[k]; ok {
			return t.back
		}
	}
	return r.Back
}

// exemptFn reports whether a requested field resolves to null instead of being fetched -- because the
// concrete type it would need is not present in every subgraph that could serve the position. The
// obligation tree computes this verdict once; a nil function means no such narrowing, so every
// reachable field is served.
type exemptFn func(g obligation.GoalID) bool

// Search plans a query over the graph H and obligation tree O. It is pure: same inputs, same result.
// It returns a *Result, or one of the typed errors (*ErrPlanTooLarge, *ErrSearchStateCap,
// *ErrNoValidPlan). The narrowing verdict comes straight from the tree, which classifies fields when
// it is built; Search must not re-classify, as that would mutate the tree and break this purity. A
// tree built without classification reports no narrowing, so every reachable field is served.
func Search(h *hypergraph.Hypergraph, o *obligation.Tree, cfg Config) (*Result, error) {
	if cfg.OperationScoped {
		return searchScoped(h, o, cfg, o.Exempt)
	}
	return searchWith(h, o, cfg, o.Exempt, nil)
}

// searchScoped is the operation-scoped mode (FORMAL_SPEC Section 6.5): the identical orchestration run
// over the operation's backward-closure sub-graph, with the result translated back to whole-graph
// ids. The preflight guard runs on the FULL graph first, so ErrPlanTooLarge behavior is
// mode-identical (Section 6.5 "what may legitimately differ" covers the remaining guard, StateCap, which
// counts strictly fewer states here).
func searchScoped(h *hypergraph.Hypergraph, o *obligation.Tree, cfg Config, exempt exemptFn) (*Result, error) {
	if err := preflight(h, o, cfg); err != nil {
		return nil, err
	}
	sc := computeOpScope(h, o)
	res, err := searchWith(sc.sub, o, cfg, exempt, sc)
	if err != nil {
		return nil, err // typed errors carry goal ids and reasons only -- nothing to translate
	}
	sc.translateResult(res)
	return res, nil
}

// searchWith runs the search over h. sc is nil in the default (whole-graph) mode; under the
// operation-scoped mode h IS sc.sub and sc supplies the sub-space views of the whole-graph mask
// indexes (base mask, root entries, pinning decision set, objFieldEdges) plus the candidate-id
// translation -- every mask DECISION follows the whole graph in both modes, only the settle domain
// shrinks (FORMAL_SPEC Section 6.5, S2).
func searchWith(h *hypergraph.Hypergraph, o *obligation.Tree, cfg Config, exempt exemptFn, sc *opScope) (*Result, error) {
	var disabled map[hypergraph.EdgeID]bool
	var rootEntry map[hypergraph.EdgeID]string
	if sc != nil {
		// Scoped mode: searchScoped already ran the FULL-graph preflight; the base mask and root
		// entries come from the memoized whole-graph indexes, projected to sub ids.
		disabled = sc.baseDisabledSub(o)
		rootEntry = sc.rootEntrySub
	} else {
		if err := preflight(h, o, cfg); err != nil { // cheap size check, before any allocation
			return nil, err
		}
		disabled = disabledConditionEdges(h, o) // conditioned jumps the operation doesn't justify
		// D11.12 root scoping: a non-subscription operation never routes through the subscription
		// root. Every root-entering Field edge departing a subscription root joins the base mask here --
		// which flows into the plain, pinned, scoped, and layered tables alike -- so a schema that
		// merely DECLARES a Subscription type leaves query/mutation routing byte-identical (the
		// subscription region is an unreachable island in every table). A subscription operation keeps
		// every root: its nested goals share the legacy any-root fall-back semantics query goals have
		// across query/mutation roots.
		disabled = maskForeignSubscriptionRoots(h, o, disabled)
		// Index the root-entering Field edges once -- buildRootTables and scopedWalks both need this
		// O(|E|) scan, so it is shared rather than recomputed.
		rootEntry = rootEntryFields(h)
	}
	// Mutation-root subgraph pin (FORMAL_SPEC D10 amendment -- mutation-root subgraph pin;
	// FS-ROOT-6): on a mutation operation, a root field with root entrances in several subgraphs is
	// pinned to ONE of them by extending the BASE mask -- every downstream table (plain, pinned,
	// scoped, layered) inherits the pin, so no fall-back tier can split the side-effecting root
	// fetch across subgraphs (the mutations_3 double-execution). No-op for queries/subscriptions
	// and for single-subgraph mutation roots.
	disabled, err := pinMutationRoots(h, o, cfg, disabled, rootEntry, exempt, sc)
	if err != nil {
		return nil, err
	}
	pi, back, stats, err := settleMasked(h, cfg, disabled)
	if err != nil { // only ErrSearchStateCap; settle never degrades silently
		return nil, err
	}

	// Base-masked (subscription-root) entries are dropped so the name-keyed pinning machinery
	// (fieldHasOwnRoot/modelledRoots) never sees a subscription field name on a non-subscription
	// operation. (Scoped mode: rootEntrySub is per-plan, so mutating it here is safe, and
	// sc.pinnable applied the same filter to the whole-graph name set.)
	for e := range rootEntry {
		if disabled[e] {
			delete(rootEntry, e)
		}
	}

	// Some fields must enter through their own operation root, not a foreign one. Precompute a cost
	// table per such root field with the foreign root entrances removed. This does nothing for
	// single-root operations and for synthetic test instances whose root field isn't a modelled root
	// edge.
	rootTables, goalRootCandidate, rootMaskSig, err := buildRootTables(h, o, cfg, disabled, rootEntry, sc)
	if err != nil {
		return nil, err
	}

	cover := &Cover{Selected: map[obligation.GoalID]hypergraph.NodeID{}}
	visited := map[hypergraph.NodeID]bool{}
	edgeSet := map[hypergraph.EdgeID]struct{}{}
	// Record which fields actually used their pinned table (the fallback below may leave a pinnable
	// field on the plain table). Only fields listed here get the pinned PiFor/BackFor later.
	goalRoot := map[obligation.GoalID]string{}
	var fallbacks []RouteFallback // D10 typed-loud fall-back records; nil on a fully consistent plan

	for _, g := range o.Goals() {
		if exempt != nil && exempt(g) { // narrowed away -> resolves to null
			cover.Nulls = append(cover.Nulls, g)
			continue
		}
		gpi, gback := pi, back // default: the plain table
		// Root pinning is a preference, not a hard rule. Use the field's pinned table only if the
		// field is still reachable under it -- i.e. a route through its own root exists. If not (the
		// only route is through a foreign root: a deeper model gap, such as a missing entity jump into
		// the requested root's subgraph), fall back to the plain table rather than fail. So a field
		// that was reachable before stays reachable, and the route is pinned wherever a pinned route
		// exists; the fallback re-admits the old (route-imperfect) plan only for genuinely unroutable
		// cases, never as a new failure. Each firing is recorded as a typed RouteFallback below
		// (D10 amendment -- typed-loud fall-back); recording changes no route.
		pinFellBack := false
		if key, ok := goalRootCandidate[g]; ok {
			t := rootTables[key]
			if _, reachable := bestCandidate(h, cands(o, g, sc), t.pi); reachable {
				gpi, gback = t.pi, t.back
				goalRoot[g] = key
			} else {
				// NARROWING guard (FORMAL_SPEC D10 amendment -- provable-non-resolvability
				// narrowing): a goal whose every candidate provably cannot be jumped into
				// (all heading keys resolvable:false, root-only entry) must NOT be rescued --
				// the fall-back route would be a provably wrong foreign-root fetch. Fail loud.
				if provablyNonResolvable(h, cands(o, g, sc)) {
					return nil, &ErrNoValidPlan{Obligation: g, Reason: reasonProvablyNonResolvable}
				}
				pinFellBack = true
			}
		}
		bestNode, ok := bestCandidate(h, cands(o, g, sc), gpi)
		if !ok { // no candidate node is reachable even on the plain table
			return nil, &ErrNoValidPlan{Obligation: g, Reason: "unreachable"}
		}
		cover.Selected[g] = bestNode
		for _, e := range Traceback(h, gback, bestNode, visited) {
			edgeSet[e] = struct{}{}
		}
		if pinFellBack {
			// Re-trace with a fresh visited set so the record carries the goal's FULL route (the
			// shared-visited trace above may have folded parts of it into earlier goals' routes).
			fallbacks = append(fallbacks, RouteFallback{
				Goal:       g,
				Kind:       RouteFallbackRootPin,
				Coordinate: goalCoordinate(o, g),
				RootField:  goalRootCandidate[g],
				Node:       bestNode,
				Subgraph:   h.SubgraphName(h.Node(bestNode).Subgraph),
				Route:      Traceback(h, gback, bestNode, map[hypergraph.NodeID]bool{}),
			})
		}
	}

	cover.Edges = make([]hypergraph.EdgeID, 0, len(edgeSet))
	for e := range edgeSet {
		cover.Edges = append(cover.Edges, e)
	}
	sort.Slice(cover.Edges, func(i, j int) bool { return cover.Edges[i] < cover.Edges[j] })
	cover.Cost = coverCost(h, cover.Edges)

	// The shared visited set means the traceback expands each node at most once across all fields, so
	// Visited never exceeds the node count. settle leaves it at 0; Search fills it. Stats report the
	// main settle only -- the extra pinned/scoped settles run over strictly fewer edges, so they can't
	// push the counters higher.
	stats.Visited = int64(len(visited))

	// Compute the per-field routes (see scope.go). This is purely additive over the Edges/Cost/Stats
	// above and doesn't change them. The only error it can raise is a state-cap trip on one of its
	// settles, which is propagated rather than swallowed. Its own fall-back firings (the scoped-walk
	// branch) are appended after the goal loop's root-pin records. The main/pinned settle tables are
	// handed over keyed by their mask signature so a scope mask identical to one already settled here
	// reuses that table instead of settling again (identical mask -> identical table; settle is
	// deterministic).
	seeded := map[string]table{}
	if len(disabled) > 0 {
		seeded[maskSignature(disabled)] = table{pi: pi, back: back}
	}
	for rf, sig := range rootMaskSig {
		if sig == "" {
			continue // empty mask: sig never consulted (scopedWalks only memoizes non-empty masks)
		}
		t := rootTables[rf]
		seeded[sig] = table{pi: t.pi, back: t.back}
	}
	kappa, walkFallbacks, err := scopedWalks(h, o, cfg, disabled, cover, pi, back, rootEntry, seeded, sc)
	if err != nil {
		return nil, err
	}
	cover.Walks = kappa
	// A goal repaired by the chain-layered consistent trace (Cover.Spines) is no longer
	// fallback-served: its walk (and selection) is path-consistent, so a root-pin record the goal
	// loop filed against the pre-repair route would misreport the plan. Drop those records; the
	// scoped-walk branch never files one for a repaired goal in the first place.
	if len(cover.Spines) > 0 && len(fallbacks) > 0 {
		kept := fallbacks[:0]
		for _, f := range fallbacks {
			if _, repaired := cover.Spines[f.Goal]; !repaired {
				kept = append(kept, f)
			}
		}
		fallbacks = kept
	}
	fallbacks = append(fallbacks, walkFallbacks...)

	return &Result{
		Cover: cover, Pi: pi, Back: back, Stats: stats,
		RouteFallbacks: fallbacks,
		goalRoot:       goalRoot, rootTables: rootTables,
	}, nil
}

// buildRootTables computes, for each root field that some requested field is anchored to, a cost
// table with entry through any other operation root forbidden -- so a field whose ancestor is
// `query.order` can't be reached by sneaking in through `query.account`. It returns those tables
// keyed by root field, plus a map from each anchored field to its root-field key (the goal loop
// decides per field whether to actually use the pinned table or fall back). A field whose root
// ancestor isn't a modelled root edge (single synthetic nodes, abstract instances) is left
// unpinned: pinning it to a root that doesn't exist would forbid every entrance and wrongly make it
// unreachable.
//
// The mask for a root field is the conditioned-jump filter plus every root-entering Field edge for a
// different root field. Descent, type-move, and entity-jump edges are never masked. Each mask only
// removes edges, so these settles touch no more nodes than the main settle -- which the caller has
// already checked against the state cap -- but any cap error is still propagated, never swallowed.
//
// rootEntry is the shared root-entering-Field-edge index (rootEntryFields, computed once by the
// caller). The third result maps each settled root field to its mask's signature, so scopedWalks can
// reuse these tables for scope masks with identical content instead of settling them again.
//
// Under the operation-scoped mode (sc != nil) the pinning DECISION set comes from the whole graph
// (sc.pinnable): a goal whose own root edge exists but lies outside the scope must still pin -- and
// then fall back exactly as the default mode does -- or the D10 fall-back register would differ
// between modes. The masks stay in the running graph's (sub) id space.
func buildRootTables(h *hypergraph.Hypergraph, o *obligation.Tree, cfg Config,
	baseDisabled map[hypergraph.EdgeID]bool, rootEntry map[hypergraph.EdgeID]string, sc *opScope,
) (map[string]rootTable, map[obligation.GoalID]string, map[string]string, error) {

	// A field listed in rootEntry has a modelled root edge of its own.
	var fieldHasOwnRoot map[string]bool
	if sc != nil {
		fieldHasOwnRoot = sc.pinnable
	} else {
		fieldHasOwnRoot = map[string]bool{}
		for _, f := range rootEntry {
			fieldHasOwnRoot[f] = true
		}
	}

	goalRoot := map[obligation.GoalID]string{}
	tables := map[string]rootTable{}
	maskSigs := map[string]string{}
	for _, g := range o.Goals() {
		rf := o.RootField(g)
		if rf == "" || !fieldHasOwnRoot[rf] {
			continue // unpinned: no modelled root of its own to anchor to
		}
		goalRoot[g] = rf
		if _, done := tables[rf]; done {
			continue // one table serves every field sharing this root (memoized)
		}
		mask := make(map[hypergraph.EdgeID]bool, len(baseDisabled)+len(rootEntry))
		for e := range baseDisabled {
			mask[e] = true
		}
		for e, f := range rootEntry {
			if f != rf { // forbid entry through any other operation root
				mask[e] = true
			}
		}
		mpi, mback, _, err := settleMasked(h, cfg, mask)
		if err != nil {
			return nil, nil, nil, err
		}
		tables[rf] = rootTable{pi: mpi, back: mback}
		maskSigs[rf] = maskSignature(mask)
	}
	return tables, goalRoot, maskSigs, nil
}

// bestCandidate picks the cheapest reachable candidate node for a field: lowest cost wins, exact
// ties broken by full node identity so the choice depends only on the graph. Returns ok=false when
// no candidate is reachable (none given, or all unreachable).
func bestCandidate(h *hypergraph.Hypergraph, cand []hypergraph.NodeID, pi []int64) (hypergraph.NodeID, bool) {
	var best hypergraph.NodeID
	bestPi := Inf
	found := false
	for _, v := range cand {
		if pi[v] >= Inf {
			continue
		}
		if !found || pi[v] < bestPi || (pi[v] == bestPi && nodeIDKey(h, v) < nodeIDKey(h, best)) {
			best, bestPi, found = v, pi[v], true
		}
	}
	return best, found
}

// Traceback follows the back-edges from a node down to the roots, collecting the edges of its route.
// The shared visited set means a node already seen (by this field or an earlier one) contributes
// nothing, so a route shared by several fields is walked once and each shared edge is collected once.
// At an entity jump it recurses into every tail -- key fields and @requires fields alike, with no
// special handling. Exported so the lowering step's co-location pass can re-trace an alternative
// candidate without duplicating this.
func Traceback(h *hypergraph.Hypergraph, back []hypergraph.EdgeID, v hypergraph.NodeID, visited map[hypergraph.NodeID]bool) []hypergraph.EdgeID {
	if visited[v] {
		return nil
	}
	visited[v] = true
	e := back[v]
	if e == hypergraph.NoEdge { // a root, or a node that was never reached
		return nil
	}
	out := []hypergraph.EdgeID{e}
	for _, t := range h.EdgeTails(e) {
		out = append(out, Traceback(h, back, t, visited)...)
	}
	return out
}

// maskForeignSubscriptionRoots extends the base mask with every root-entering Field edge that
// departs a SUBSCRIPTION root, unless the operation itself is a subscription (D11.12 root scoping).
// The root node's Field carries its operation kind (hypergraph builder: Node{Kind: NodeRoot,
// Field: "query"|"mutation"|"subscription"}). Query/mutation cross-participation is deliberately
// untouched -- it predates subscriptions and is baked into the D10 fall-back corpus figures.
// Returns the (possibly newly allocated) mask; a graph without a subscription root returns the
// input unchanged, byte-for-byte the pre-subscription behavior.
func maskForeignSubscriptionRoots(h *hypergraph.Hypergraph, o *obligation.Tree,
	disabled map[hypergraph.EdgeID]bool) map[hypergraph.EdgeID]bool {

	if o.SubscriptionOperation() {
		return disabled
	}
	subRoot := map[hypergraph.NodeID]bool{}
	for _, r := range h.Roots() {
		if h.NodeField(r) == "subscription" {
			subRoot[r] = true
		}
	}
	if len(subRoot) == 0 {
		return disabled
	}
	for i := 0; i < h.NumEdges(); i++ {
		id := hypergraph.EdgeID(i)
		if h.EdgeKind(id) != hypergraph.EdgeField {
			continue
		}
		tails := h.EdgeTails(id)
		if len(tails) == 0 || !subRoot[tails[0]] {
			continue
		}
		if disabled == nil {
			disabled = map[hypergraph.EdgeID]bool{}
		}
		disabled[id] = true
	}
	return disabled
}

// disabledConditionEdges returns the conditioned entity jumps to mask out: the ones whose key fields
// the operation doesn't ask for. A jump is kept if it is unconditional, or if at least one of its
// key conditions is satisfied -- every "Type.field" coordinate it names appears among the operation's
// fields, or (for a condition with no coordinates) every field name in its path appears. Everything
// else is masked out. Presence is checked across the whole operation, not the specific branch where
// the jump is used -- see the known-limitation note in the file header. Only entity jumps carry
// conditions; nothing else is ever masked here.
func disabledConditionEdges(h *hypergraph.Hypergraph, o *obligation.Tree) map[hypergraph.EdgeID]bool {
	coords, fields := operationCoordSets(o)

	var disabled map[hypergraph.EdgeID]bool
	for i := 0; i < h.NumEdges(); i++ {
		id := hypergraph.EdgeID(i)
		if h.EdgeKind(id) != hypergraph.EdgeEntityJump {
			continue // only entity jumps carry conditions
		}
		conditions := h.EdgeConditions(id)
		if len(conditions) == 0 {
			continue // not a conditioned jump: always available
		}
		if conditionsSatisfied(conditions, coords, fields) {
			continue
		}
		if disabled == nil {
			disabled = map[hypergraph.EdgeID]bool{}
		}
		disabled[id] = true
	}
	return disabled
}

// operationCoordSets renders what the operation asks for as the two membership sets the
// conditioned-jump filter checks: the "Type.field" coordinates and the bare field names of every
// Field obligation. Extracted from disabledConditionEdges so the operation-scoped mode's base-mask
// derivation (opscope.go) shares it verbatim.
func operationCoordSets(o *obligation.Tree) (coords, fields map[string]struct{}) {
	coords = map[string]struct{}{}
	fields = map[string]struct{}{}
	for _, ob := range o.Obligations() {
		if ob.Field == "" {
			continue
		}
		coords[ob.Type+"."+ob.Field] = struct{}{}
		fields[ob.Field] = struct{}{}
	}
	return coords, fields
}

// conditionsSatisfied reports whether at least one of the edge's key conditions is met by what the
// operation asks for. A condition with coordinates is met when all of them are present; a condition
// with no coordinates is met when all its path field names are present; a wholly empty condition asks
// for nothing and counts as met.
func conditionsSatisfied(cs []hypergraph.KeyCondition, coords, fields map[string]struct{}) bool {
	for _, c := range cs {
		if len(c.Coordinates) == 0 && len(c.FieldPath) == 0 {
			return true
		}
		ok := true
		for _, co := range c.Coordinates {
			if _, present := coords[co]; !present {
				ok = false
				break
			}
		}
		if ok && len(c.Coordinates) == 0 {
			for _, f := range c.FieldPath {
				if _, present := fields[f]; !present {
					ok = false
					break
				}
			}
		}
		if ok {
			return true
		}
	}
	return false
}
