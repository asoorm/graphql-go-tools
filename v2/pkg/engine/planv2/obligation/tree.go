// Package obligation turns a normalized GraphQL operation into an "obligation tree": a restatement
// of the client's query as a tree of things the plan must satisfy -- one node per selected field and
// per inline type refinement. The search package then finds the cheapest way to satisfy them. Build
// hands search only IDs (ObID, GoalID, node ids) -- never the parsed operation -- so search stays a
// pure graph problem with no GraphQL knowledge.
// Spec: FORMAL_SPEC D2, D3.
package obligation

import (
	"sort"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

// GoalID indexes one goal: a leaf of the obligation tree that the search must actually resolve.
// Goals are the leaf field selections, plus a couple of special cases (a composite field whose only
// descendants are __typename or type refinements, and a childless type refinement). They are
// numbered in document order.
type GoalID uint32

// ObID indexes an Obligation in the tree, numbered in the order they were created while walking the
// operation (document order).
type ObID uint32

// NoParent marks a top-level obligation -- one created while the walk had no enclosing obligation.
// It is a reserved ID that can never point at a real obligation, so "is this a root?" is an
// unambiguous test. An earlier scheme instead marked roots with Parent == ID, but only obligation 0
// could express that; any later top-level field (Parent 0, ID != 0) looked exactly like a child of
// obligation 0. That misfiled multi-root operations like `{ a { x } b { y } }` (b rendered under a)
// and, for `{ a b }`, made the leaf root `a` look like it had children so it never became a goal.
// Parent-chain walks stop at NoParent; the old self-loop check is kept alongside as a guard for
// hand-assembled trees.
const NoParent ObID = ^ObID(0)

type Kind uint8

const (
	// Field means "field f on an instance of type T must be resolved."
	Field Kind = iota + 1
	// Refine mirrors an inline fragment `... on C` under an abstract type U: "under abstract type U,
	// resolve the members selected for concrete type C."
	Refine
	// Typename is a __typename meta-field selection. It stays in the tree so the response shape is
	// preserved exactly, but it is NOT a search goal: __typename is filled in later at lowering and
	// has no node in the graph, so the search never sees it. Typename obligations are excluded from
	// Goals() and never get a GoalID, so Cand is never called for one; lowering synthesizes the
	// __typename selections from the obligation list directly.
	Typename
)

// Obligation is one node of the tree.
type Obligation struct {
	ID     ObID
	Kind   Kind
	Parent ObID // enclosing obligation (parent resolved before child); NoParent for a top-level
	// obligation (see NoParent and resolveGoalsAndCand).
	Type     string // owner type T (Field/Typename) or abstract type U (Refine)
	Field    string // field name f (Field), "__typename" (Typename); "" for Refine
	Concrete string // concrete type C (Refine); "" for Field/Typename
	RespKey  string // client response key, preserved for lowering; "" for Refine
	// Arguments is the field's argument list rendered WITHOUT the enclosing parens: e.g. `id: $a` or
	// `input: $a, dryRun: $b`. Empty when the field takes no arguments. The operation is already
	// normalized with variables extracted, so every argument value is a variable reference -- but the
	// renderer (builder.renderArguments) prints any value kind, so a non-extracted literal still
	// round-trips. Lowering wraps this back in parens and prints it verbatim into the fetch document.
	Arguments string
	// ArgVars are the operation variable names referenced anywhere in Arguments (deduped, document
	// order). Lowering uses these to declare and forward only the variables a fetch actually needs.
	ArgVars []string
	// DeferID is the field's defer scope (FORMAL_SPEC D11.13): the @__defer_internal id the engine's
	// defer normalization stamped it with, 0 when the field is not deferred. Recorded ONLY for query
	// operations (FS-DEF-7: subscriptions never honor @defer; mutations are serial by design), so a
	// non-zero scope implies the plan partitions into primary + increments. Goals, candidates,
	// narrowing, and the search never read it -- routing is scope-blind (FS-DEF-6).
	DeferID int
}

// DeferInfo is the obligation-layer record of one @defer fragment (FORMAL_SPEC D11.13 defer
// descriptor): its id, the id of the enclosing @defer fragment (0 for top-level), the client's
// label, and the mount path -- the response path of the enclosing selection set (field-obligation
// response keys root->parent; refinements contribute no segment). Lowering re-encodes it verbatim
// as resolve.DeferDescriptor; this package stays free of resolve imports.
type DeferInfo struct {
	ID       int
	ParentID int
	Label    string
	Path     []string
}

// Tree is the built obligation tree, with its goals and each goal's candidate nodes resolved against
// the graph H. Immutable once returned from Build.
type Tree struct {
	obligations []Obligation

	// opKind is the selected operation's type, set by Build (zero = OperationTypeUnknown on a
	// hand-assembled tree). It scopes which operation roots participate in reachability and routing
	// (D11.12 root scoping): a query/mutation tree never sees the subscription root, so a schema
	// that merely declares a Subscription type cannot perturb query/mutation planning. The search
	// layer reads it through OperationType.
	opKind ast.OperationType

	// Parallel, GoalID-indexed: goalOb[g] is the obligation goal g maps to, cand[g] the graph nodes
	// that could serve it (field-resolution nodes, or object nodes for the rare leaf refinement; see
	// candFor in builder.go). Typename obligations never appear here.
	goalOb []ObID
	cand   [][]hypergraph.NodeID

	// exempt is the member-narrowing verdict for each goal, precomputed by ClassifyNarrowing
	// (narrow.go), which Build runs at its tail -- so narrowing is on by default. nil on a tree built
	// without classification, in which case Exempt reports false for every goal (no narrowing, the
	// conservative direction).
	exempt []bool
	// exemptDead marks WHICH exempt verdicts are D6pp dead members (the position can never produce
	// the member) as opposed to value-type intersection narrowings. The distinction matters to
	// promoteExemptTerminalGoals: a dead member's `{ __typename }` cover is exact no matter whether
	// the member's own candidates are reachable -- the member never occurs -- while an intersection
	// narrowing keeps the reachability gate. Parallel to exempt; nil without classification.
	exemptDead []bool

	// def is the composed client-facing schema the operation was normalized against, kept for the
	// D6pp position-possible classification (composed type kinds and implementer sets -- knowledge H
	// does not carry). Set by Build; nil on a hand-assembled tree, in which case classification
	// falls back to H-derived kind/implementer approximations (see composedInfo).
	def *ast.Document

	// defers records one DeferInfo per distinct @defer id the (query) operation carries, keyed by
	// id. nil/empty when the operation defers nothing -- the "plan is an ordinary synchronous plan"
	// signal the facade reads. See Obligation.DeferID for the per-field scope.
	defers map[int]DeferInfo
}

// Defers returns the operation's @defer records keyed by id -- empty for an operation that defers
// nothing (including any mutation/subscription, where @defer is not honored; FS-DEF-1/FS-DEF-7).
func (t *Tree) Defers() map[int]DeferInfo { return t.defers }

// Obligations returns every node of the tree, in creation (document) order.
func (t *Tree) Obligations() []Obligation { return t.obligations }

// Goals returns every goal -- the leaf field obligations (plus the typename-terminal composites and
// leaf refinements described above) -- in document order. Typename obligations are never goals: they
// are satisfied at lowering and the search never sees them.
func (t *Tree) Goals() []GoalID {
	out := make([]GoalID, len(t.goalOb))
	for i := range out {
		out[i] = GoalID(i)
	}
	return out
}

// Cand returns the candidate nodes that could serve goal g, sorted by node id -- one per candidate
// subgraph (or per candidate refinement route).
func (t *Tree) Cand(g GoalID) []hypergraph.NodeID { return t.cand[g] }

// Ob returns the Obligation a goal maps to.
func (t *Tree) Ob(g GoalID) Obligation { return t.obligations[t.goalOb[g]] }

// SubscriptionOperation reports whether the tree was built from a subscription operation (false on
// a hand-assembled tree). The search layer consults it to scope which operation roots may
// participate in routing (D11.12 root scoping) without importing the ast package -- search's import
// discipline is the graph and obligation types only.
func (t *Tree) SubscriptionOperation() bool { return t.opKind == ast.OperationTypeSubscription }

// MutationOperation reports whether the tree was built from a mutation operation (false on a
// hand-assembled tree, whose opKind is Unknown). The search layer consults it for the
// mutation-root subgraph pin (FORMAL_SPEC D10 amendment -- mutation-root subgraph pin): a
// shareable mutation root field's whole selection must enter through ONE subgraph's root fetch,
// or the side effect executes once per participating root fetch (FS-ROOT-6).
func (t *Tree) MutationOperation() bool { return t.opKind == ast.OperationTypeMutation }

// RootField returns the top-level operation field that goal g sits under -- the field at the top of
// g's parent chain (walk up until Parent is NoParent). This is the root field the client selected g
// beneath, and it anchors path consistency: g's covering route may enter the query roots only through
// this field (the search masks out every other root-entering field edge). In a well-formed operation
// the top-level selections are fields on the operation root, so that ancestor is a Field obligation
// and its field name is returned; the loop is defensive against hand-assembled trees.
func (t *Tree) RootField(g GoalID) string {
	ob := t.obligations[t.goalOb[g]]
	for ob.Parent != NoParent && ob.Parent != ob.ID {
		ob = t.obligations[ob.Parent]
	}
	return ob.Field
}

// resolveGoalsAndCand picks out the goals and their candidate nodes once the visitor has built the
// full tree. Goals are taken in append (document) order, which is what makes Goals() deterministic.
// Typename obligations are never goals: they have no graph node and are handled entirely by lowering,
// so the search never sees them.
//
// A Field obligation becomes a goal when it is a resolution leaf -- it has no field obligation below
// it. This is broader than "a plain leaf field": a composite field whose whole subtree is only
// __typename meta-fields and/or type refinements with no resolvable field beneath
// (`{ union { __typename } }`, `{ node { ... on X { __typename } } }`) is a resolution leaf too. The
// router must still resolve that field to produce its __typename, but nothing below it carries a
// goal, so without this rule the composite would never be covered by any fetch and the whole
// __typename-only subtree would silently drop. The rule only ever ADDS goals -- a plain leaf field has
// no children and was already a goal -- so every earlier goal is preserved. candFor gives such a goal
// the field's own resolution nodes, exactly as for a leaf field, so lowering emits
// `field { __typename }` at its position.
//
// A Refine obligation becomes a goal when it is childless (a leaf refinement, which is practically
// unreachable from a normalized operation; candFor handles it defensively via object nodes).
//
// Top-level obligations carry Parent == NoParent; they are excluded from hasChildren so a childless
// top-level obligation (the second root of `{ a b }`, or the single-field query `{ __typename }`) is
// still recognised as a leaf. The legacy self-loop convention (Parent == ID) is excluded too, as a
// guard for hand-assembled trees that predate NoParent.
func (t *Tree) resolveGoalsAndCand(h *hypergraph.Hypergraph) {
	hasChildren := make([]bool, len(t.obligations))
	// hasFieldDescendant[o] is true iff some obligation strictly below o in the tree is a Field.
	// Parents are always created before their children (EnterField/EnterInlineFragment push onto the
	// ancestry stack before descending), so Parent < ID for every real edge; a single reverse pass
	// therefore propagates each Field's "there is a field here" signal up its whole ancestor chain.
	hasFieldDescendant := make([]bool, len(t.obligations))
	for _, ob := range t.obligations {
		if ob.Parent == NoParent || ob.Parent == ob.ID {
			continue // root sentinel, not a real parent-child edge
		}
		hasChildren[ob.Parent] = true
	}
	for i := len(t.obligations) - 1; i >= 0; i-- {
		ob := t.obligations[i]
		if ob.Parent == NoParent || ob.Parent == ob.ID {
			continue
		}
		if ob.Kind == Field || hasFieldDescendant[ob.ID] {
			hasFieldDescendant[ob.Parent] = true
		}
	}
	for _, ob := range t.obligations {
		var goal bool
		switch ob.Kind {
		case Field:
			goal = !hasFieldDescendant[ob.ID] // resolution leaf: no resolvable field beneath
		case Refine:
			goal = !hasChildren[ob.ID] // defensive leaf refinement
		case Typename:
			goal = false // never a search goal (see Kind Typename)
		}
		if !goal {
			continue
		}
		t.goalOb = append(t.goalOb, ob.ID)
		t.cand = append(t.cand, candFor(h, ob))
	}
}

// expandInterfaceRefinementGoals handles a field selected on an interface type where the interface
// itself is never returned directly. Take a field <U.f> whose owner U is abstract and whose own
// candidate nodes are all unreachable: the interface node is a "mixin" that no route in the graph ever
// produces, yet the concrete member the parent instance actually is IS reachable and declares f.
// Without help this goal is a hard "no valid plan" error (seen on ~13 real customer blockers).
//
// The fix adds, to the goal's candidates, the concrete members' field nodes for f: for each member C
// of U (read from the type-move edges) that both declares f and is reachable -- the entity members
// reachable via their entity jumps. This only ADDS candidates, and only when the goal's own candidates
// are all unreachable, so it fires exclusively on goals that would otherwise fail; a working interface
// case (its own nodes reachable) is left untouched, so candidate selection and cost never change on
// any plan that already works.
//
// It fires ONLY for this exact blocker class; every gate must hold:
//   - the goal is NOT a member-narrowed null (an exempt goal is a deliberate response-only null, not
//     a routing gap);
//   - the field is selected under an inline refinement onto its own type (`... on U { f }` -- the
//     nearest Refine ancestor's Concrete is U). A bare field on an abstract owner with no refinement
//     is the @interfaceObject / entity-interface co-resolution machinery, a different case; expanding
//     it would hand a bogus member plan to cases whose correct outcome is an honest error;
//   - U itself is NOT an entity (nothing produces a (U,s) node via an entity jump). An entity
//     interface is directly reachable through its jump, so an unreachable candidate there is a
//     different (jump-routing) gap that must stay an honest error.
func (t *Tree) expandInterfaceRefinementGoals(h *hypergraph.Hypergraph, reachable []bool) {
	if len(t.exempt) != len(t.goalOb) {
		return
	}
	// entityTypes: types that head an entity-jump edge anywhere (the entity gate). members[U] = the
	// concrete member type names of abstract U, from its type-move edges. Both built lazily once.
	var entityTypes map[string]bool
	var members map[string]map[string]bool
	for gi, obID := range t.goalOb {
		if t.exempt[gi] {
			continue
		}
		ob := t.obligations[obID]
		if ob.Kind != Field {
			continue
		}
		if ref, ok := t.refinementAncestor(GoalID(gi)); !ok || ref.Concrete != ob.Type {
			continue // not a `... on U { f }` goal -- not this class
		}
		if candAnyReachable(t.cand[gi], reachable) {
			continue // the goal's own candidates are reachable -- an ordinary goal, never expanded
		}
		if entityTypes == nil {
			entityTypes = map[string]bool{}
			for e := 0; e < h.NumEdges(); e++ {
				if edge := h.Edge(hypergraph.EdgeID(e)); edge.Kind == hypergraph.EdgeEntityJump {
					entityTypes[h.Node(edge.Head).Type] = true
				}
			}
		}
		if entityTypes[ob.Type] {
			continue // U is an entity interface -- its unreachability is a jump gap, not a mixin
		}
		if members == nil {
			members = abstractMembers(h)
		}
		mem := members[ob.Type]
		if len(mem) == 0 {
			continue // U is not an abstract type with modelled members -- not this class
		}
		var add []hypergraph.NodeID
		for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
			n := h.Node(id)
			if n.Kind != hypergraph.NodeField || n.Field != ob.Field || !mem[n.Type] {
				continue
			}
			if int(id) < len(reachable) && reachable[id] {
				add = append(add, id)
			}
		}
		if len(add) == 0 {
			continue // no reachable member resolves f -- genuinely unplannable, keep the honest error
		}
		t.cand[gi] = mergeSortedNodes(t.cand[gi], add)
	}
}

// expandInterfaceObjectFlattening (FORMAL_SPEC D3io -- @interfaceObject member-flattening candidates,
// M2 class-C wave) handles the reverse of expandInterfaceRefinementGoals: a field selected under a
// concrete member refinement (`... on C { f }`) where f lives ONLY on an @interfaceObject subgraph --
// the subgraph declares an interface I (with C among its composed implementers) as a plain object and
// serves f for ALL implementers without knowing the concrete types. cand(g) = {(C,s).f} then contains
// only orphan propagated nodes (the interface-object subgraph has no producing route to a CONCRETE
// (C,s) -- its instances are interface-typed), so the goal is a hard "no valid plan" error
// (`simple-interface-object`: `users { ... on User { username } }`, username declared only on the
// interface-object NodeWithName in subgraph b).
//
// The fix augments cand(g) with the interface-flattened field nodes (I,s).f for each composed
// interface I of C where the object node (I,s) heads an EntityJump (the gate distinguishing a
// genuinely enterable interface-object / entity-interface node from a plain interface another
// subgraph merely declares) and (I,s).f is reachable. Like D3ppp it is CONDITIONAL -- it fires only
// when every primary (C,s).f is unreachable, so no working concrete route is ever displaced -- and
// additive (more candidates => more derivations; kernel/settle/cost untouched). Lowering places the
// covered field at the interface level of its document (D11.9 flattened member placement); the
// response tree keeps the member gate.
func (t *Tree) expandInterfaceObjectFlattening(h *hypergraph.Hypergraph, reachable []bool) {
	if t.def == nil || len(t.exempt) != len(t.goalOb) {
		return // composed-schema implementer knowledge and classification are required
	}
	var jumpHeadObjects map[typeSub]bool // (type, subgraph) pairs of EntityJump head object nodes
	ifaceCache := map[string]map[string]bool{}
	for gi, obID := range t.goalOb {
		if t.exempt[gi] {
			continue // a member-narrowed null is a deliberate response-only null, not a routing gap
		}
		ob := t.obligations[obID]
		if ob.Kind != Field || ob.Field == typenameField {
			continue
		}
		// The goal's owner C must be the position's concrete type: a `... on C { f }` member goal,
		// OR a bare field at a concrete-typed position (no refinement at all -- the D3io
		// concrete-position clause; witness FS-IFO-1/interface-object/contributed-field-concrete,
		// `usersConcrete: [User]` selecting an @interfaceObject-contributed field fragment-free).
		// A goal under a refinement onto a DIFFERENT type is not this class.
		if ref, ok := t.refinementAncestor(GoalID(gi)); ok && ref.Concrete != ob.Type {
			continue
		}
		if !isComposedObjectType(t.def, ob.Type) {
			continue // C must be a concrete member; the abstract-owner case is D3ppp territory
		}
		if candAnyReachable(t.cand[gi], reachable) {
			continue // a concrete route exists -- never displaced
		}
		ifaces, ok := ifaceCache[ob.Type]
		if !ok {
			ifaces = composedImplementedInterfaces(t.def, ob.Type)
			ifaceCache[ob.Type] = ifaces
		}
		if len(ifaces) == 0 {
			continue
		}
		if jumpHeadObjects == nil {
			jumpHeadObjects = entityJumpHeadObjects(h)
		}
		var add []hypergraph.NodeID
		for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
			n := h.Node(id)
			if n.Kind != hypergraph.NodeField || n.Field != ob.Field || n.Scope != "" || !ifaces[n.Type] {
				continue
			}
			if !jumpHeadObjects[typeSub{n.Type, n.Subgraph}] {
				continue // the interface node is not enterable via a key -- not an interface-object shape
			}
			if int(id) < len(reachable) && reachable[id] {
				add = append(add, id)
			}
		}
		if len(add) == 0 {
			continue // nothing flattenable -- the goal keeps its honest error
		}
		t.cand[gi] = mergeSortedNodes(t.cand[gi], add)
	}
}

// typeSub keys an object node by its (composed type name, subgraph) pair.
type typeSub struct {
	typ string
	s   hypergraph.SubgraphID
}

// entityJumpHeadObjects returns the (type, subgraph) pairs of every object node that heads an
// EntityJump edge -- the "enterable via a key" gate of D3io.
func entityJumpHeadObjects(h *hypergraph.Hypergraph) map[typeSub]bool {
	out := map[typeSub]bool{}
	for e := 0; e < h.NumEdges(); e++ {
		if h.EdgeKind(hypergraph.EdgeID(e)) != hypergraph.EdgeEntityJump {
			continue
		}
		head := h.Node(h.EdgeHead(hypergraph.EdgeID(e)))
		out[typeSub{head.Type, head.Subgraph}] = true
	}
	return out
}

// isComposedObjectType reports whether name is a concrete object type in the composed client schema.
func isComposedObjectType(def *ast.Document, name string) bool {
	node, ok := def.Index.FirstNodeByNameStr(name)
	return ok && node.Kind == ast.NodeKindObjectTypeDefinition
}

// composedImplementedInterfaces returns the interface names concrete type c implements in the
// composed client schema.
func composedImplementedInterfaces(def *ast.Document, c string) map[string]bool {
	node, ok := def.Index.FirstNodeByNameStr(c)
	if !ok {
		return nil
	}
	out := map[string]bool{}
	for ref := range def.InterfaceTypeDefinitions {
		iface := def.InterfaceTypeDefinitionNameString(ref)
		if def.NodeImplementsInterface(node, ast.ByteSlice(iface)) {
			out[iface] = true
		}
	}
	return out
}

// abstractMembers returns, for each abstract type that is a type-move source in H, the set of its
// concrete member type names (taken from the type-move edges, unioned over all subgraphs).
func abstractMembers(h *hypergraph.Hypergraph) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for e := 0; e < h.NumEdges(); e++ {
		edge := h.Edge(hypergraph.EdgeID(e))
		if edge.Kind != hypergraph.EdgeTypeMove || len(edge.Tails) == 0 {
			continue
		}
		u := h.Node(edge.Tails[0]).Type
		set := out[u]
		if set == nil {
			set = map[string]bool{}
			out[u] = set
		}
		for _, m := range edge.Members {
			set[m] = true
		}
	}
	return out
}

// mergeSortedNodes returns the sorted-unique union of a (already sorted) with the extra nodes.
func mergeSortedNodes(a, extra []hypergraph.NodeID) []hypergraph.NodeID {
	seen := make(map[hypergraph.NodeID]bool, len(a)+len(extra))
	out := make([]hypergraph.NodeID, 0, len(a)+len(extra))
	for _, n := range a {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range extra {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// promoteExemptTerminalGoals adds a coverage goal for a composite field whose entire field-subtree
// was member-narrowed away to null. It runs AFTER ClassifyNarrowing so it can read the exempt verdict.
//
// The problem it closes: a composite whose only selected members are all narrowed out (a value-type
// partial union where the operation selects only out-of-intersection members, e.g.
// `wrapper { actions { __typename ... on OnlyB { b } } }` with OnlyB narrowed out) has NO covered goal
// anywhere in its subtree, so no fetch selects the composite, its ancestors, or its `__typename`, and
// the whole subtree silently drops -- even though the router must still resolve the composite to
// produce the surviving members' `__typename`. This is the same idea as the typename-terminal rule
// above, widened from "no field descendant" to "no non-exempt-and-reachable field descendant".
//
// Safety gate -- this is what separates a correct null from a real distributed-interface gap. Only
// composites whose exempt descendants are exempt DESPITE being reachable are promoted -- genuine
// value-type narrowing, which the reference gateway also nulls. A goal exempt because its own
// candidates are UNREACHABLE (an interface refinement whose abstract node is never produced) is NOT a
// correct null: it must be member-expanded, not covered as `{ __typename }` -- covering it would drop
// the field and pass the plan with wrong data. Requiring every exempt descendant to have a reachable
// candidate excludes exactly that class, so promotion never turns a real gap into a silently-wrong
// plan. Only the DEEPEST qualifying composite is promoted (the fetch closest to the members); its
// ancestors are covered by the walk down to it.
func (t *Tree) promoteExemptTerminalGoals(h *hypergraph.Hypergraph, reachable []bool) {
	if len(t.exempt) != len(t.goalOb) {
		return // classification did not run -- nothing to promote against
	}
	obs := t.obligations
	existingGoal := make([]bool, len(obs))
	exemptGoalReachable := make([]bool, len(obs)) // per Field goal: exempt AND has a reachable candidate
	fieldGoalOb := make([]bool, len(obs))         // per obligation: is it a Field goal (leaf or typename-terminal)
	for gi, obID := range t.goalOb {
		existingGoal[obID] = true
		if obs[obID].Kind != Field {
			continue
		}
		fieldGoalOb[obID] = true
		// A goal qualifies as a promotable narrowing when it is exempt AND its null is provably
		// correct: either a D6pp DEAD member (the position can never produce the member, so
		// `{ __typename }` is exact regardless of where the member's own candidates live) or a
		// value-type intersection narrowing whose candidates are reachable (narrowed DESPITE being
		// resolvable -- the genuine D6 null the reference gateway also produces).
		if t.exempt[gi] && (t.exemptDeadAt(gi) || candAnyReachable(t.cand[gi], reachable)) {
			exemptGoalReachable[obID] = true
		}
	}

	// state[o] tells whether o's subtree contains at least one Field goal and every Field goal in it
	// is an exempt-and-reachable narrowing. Reverse pass (parents precede children, so Parent < ID).
	state := make([]int, len(obs))
	for i := len(obs) - 1; i >= 0; i-- {
		ob := obs[i]
		s := state[ob.ID]
		if fieldGoalOb[ob.ID] { // ob itself is a Field goal (a leaf)
			if exemptGoalReachable[ob.ID] {
				s = mergeNar(s, narAll)
			} else {
				s = narMixed
			}
		}
		state[ob.ID] = s
		if ob.Parent == NoParent || ob.Parent == ob.ID {
			continue
		}
		state[ob.Parent] = mergeNar(state[ob.Parent], s)
	}

	// A composite qualifies when its whole subtree is narAll and it is NOT already a goal (it has field
	// descendants). Promote only the DEEPEST such composite per branch: skip one that has a qualifying
	// composite descendant (the deeper one carries the fetch; ancestors ride the walk down to it).
	qualifies := func(id ObID) bool {
		return obs[id].Kind == Field && !existingGoal[id] && state[id] == narAll
	}
	hasQualifyingDescendant := make([]bool, len(obs))
	for i := len(obs) - 1; i >= 0; i-- {
		ob := obs[i]
		if ob.Parent == NoParent || ob.Parent == ob.ID {
			continue
		}
		if qualifies(ob.ID) || hasQualifyingDescendant[ob.ID] {
			hasQualifyingDescendant[ob.Parent] = true
		}
	}
	for _, ob := range obs {
		if qualifies(ob.ID) && !hasQualifyingDescendant[ob.ID] {
			t.goalOb = append(t.goalOb, ob.ID)
			t.cand = append(t.cand, candFor(h, ob))
			t.exempt = append(t.exempt, false) // the composite itself is coverable (typename-terminal)
			if t.exemptDead != nil {
				t.exemptDead = append(t.exemptDead, false)
			}
		}
	}
}

// exemptDeadAt reports whether goal gi's exemption is a D6pp dead-member verdict. Nil-safe for trees
// classified before exemptDead existed (hand-assembled tests): absent means "not dead".
func (t *Tree) exemptDeadAt(gi int) bool {
	return t.exemptDead != nil && gi < len(t.exemptDead) && t.exemptDead[gi]
}

// Subtree narrowing states for promoteExemptTerminalGoals (compared by value, not just distinct).
const (
	narNone  = iota // no field goal in subtree
	narAll          // at least one field goal, every one exempt+reachable (narrowed)
	narMixed        // at least one non-narrowed field goal present
)

// mergeNar combines two subtree narrowing states: mixed dominates, then allNar, then none.
func mergeNar(a, b int) int {
	if a == narMixed || b == narMixed {
		return narMixed
	}
	if a == narAll || b == narAll {
		return narAll
	}
	return narNone
}

// candAnyReachable reports whether any candidate node of a goal is optimistically reachable.
func candAnyReachable(cand []hypergraph.NodeID, reachable []bool) bool {
	for _, c := range cand {
		if int(c) < len(reachable) && reachable[c] {
			return true
		}
	}
	return false
}
