package obligation

// narrow.go decides which requested fields must resolve to null because the concrete type they need
// can't actually occur at the response position they were selected under.
//
// The case is a field selected under an inline refinement `... on C` on an abstract type U. The
// verdicts are judged POSITIONALLY (FORMAL_SPEC D6pp): for each subgraph s able to supply the parent
// instance (route-scoped, D6p), the position-possible member set pos_s is derived from s's OWN
// output type for the parent field -- the concrete type itself when s declares the position concrete
// (the child-type-mismatch shape), s's local member set when it declares it abstract with modelled
// members, and TOP (unknown: everything possible) when s models the position without local member
// knowledge (the @interfaceObject shape -- no exemption may be derived from ignorance). C is possible
// in s when it is in pos_s (concrete C) or when one of its composed-schema implementers is
// (abstract C -- an interface refinement applies to any possible member implementing it).
//
// A goal under the refinement is "exempt" -- coverable by nothing, lowered to a response-only null --
// exactly when:
//
//	(1) DEAD MEMBER (D6pp verdict 1): C is possible in NO capable subgraph -- the position can never
//	    produce a C, regardless of C's entity-ness (an entity jump transports an existing instance
//	    between subgraphs; it cannot manufacture a parent instance of a type the position cannot
//	    produce); OR
//	(2) VALUE-TYPE INTERSECTION (the original D6 rule, possibility-tested): no member of U among
//	    the capable subgraphs is an entity (an entity heads an entity-jump edge) AND C is
//	    impossible in at least one capable subgraph.
//
// A member possible in only SOME capable subgraphs when the entity gate blocks (2) is the
// DISTRIBUTED member: not exempt -- it must be covered via a route through a subgraph where it is
// possible (the D10 member-scoped kappa mask and D11.7 member-qualified placement own that constraint).
//
// The verdict is computed once per tree (ClassifyNarrowing) and read by the search through the
// Exempt method, so the search stays a pure computation over IDs and never re-derives type
// membership. Determinism: every set operation here is an order-independent boolean (OR for the
// entity gate and deadness, AND for the intersection) over a sorted subgraph list, so no map
// iteration order leaks into the verdict or the downstream null ordering.
// Spec: FORMAL_SPEC D6, D6p, D6pp, D6ppp (position-scoping of the capable set), D6pppp
// (condition-aware transport admission -- key-obtainability at the position).

import (
	"sort"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

// ClassifyNarrowing computes the member-narrowing verdict for every goal, storing it (GoalID-indexed)
// in t.exempt. Build runs it at its tail, so narrowing is on by default; it stays exported for
// re-classification against a rebuilt H. Idempotent: a second call recomputes the same verdicts. The
// search must never call it -- it mutates the tree, and the search is pure over (H, O).
//
// It reads three facts straight off the graph in one edge scan:
//   - entityTypes: the types that head an entity-jump edge (the entity gate).
//   - memBySubgraph[s][U]: the members subgraph s declares for abstract type U, from the type-move
//     edge's member list.
//   - descTarget[s]["T.f"]: the object type field T.f descends into in subgraph s (the
//     subgraph-LOCAL output type, which decides the position-possible set, D6pp).
func (t *Tree) ClassifyNarrowing(h *hypergraph.Hypergraph) {
	t.exempt = make([]bool, len(t.goalOb))
	t.exemptDead = make([]bool, len(t.goalOb))

	// Route-scoping (D6p): the set of capable subgraphs is limited to those whose copy of the parent
	// field is actually REACHABLE, not merely present in some subgraph's schema. reachable is the
	// optimistic (condition-blind) reachability from the OPERATION'S roots (D11.12 root scoping -- a
	// query/mutation verdict must not change because a Subscription type is merely declared): an
	// over-approximation of the real route, so it removes ONLY nodes with no producing path at all.
	reachable := reachableFor(h, t.opKind)

	entityTypes := map[string]bool{}
	memBySubgraph := map[hypergraph.SubgraphID]map[string]map[string]bool{}
	descTarget := map[hypergraph.SubgraphID]map[string]string{}
	// jumpTransports[T]: the entity jumps heading type T, each reduced to its head subgraph and the
	// distinct subgraphs its KEY tails live in -- the condition-AWARE transport relation
	// position-scoping reads (D6pppp, positionSubgraphs): a jump can land a T instance in its head
	// subgraph only if every key-tail subgraph already holds the instance at the position (the key
	// values must be OBTAINABLE there). KeyTails is the @key subset of the tails; a jump without it
	// (hand-assembled graphs) falls back to all tails, and a tail-less jump admits unconditionally
	// (degenerate, lenient). Requires tails are deliberately excluded -- gathering inputs are judged
	// leniently (the never-narrow-on-ignorance direction).
	jumpTransports := map[string][]jumpTransport{}
	for e := 0; e < h.NumEdges(); e++ {
		edge := h.Edge(hypergraph.EdgeID(e))
		switch edge.Kind {
		case hypergraph.EdgeEntityJump:
			head := h.Node(edge.Head)
			entityTypes[head.Type] = true
			kt := edge.KeyTails
			if len(kt) == 0 {
				kt = edge.Tails
			}
			seen := map[hypergraph.SubgraphID]bool{}
			var tailSubs []hypergraph.SubgraphID
			for _, tn := range kt {
				s := h.Node(tn).Subgraph
				if !seen[s] {
					seen[s] = true
					tailSubs = append(tailSubs, s)
				}
			}
			jumpTransports[head.Type] = append(jumpTransports[head.Type],
				jumpTransport{head: head.Subgraph, tailSubs: tailSubs})
		case hypergraph.EdgeTypeMove:
			// A type-move edge has a single tail: Tails[0] is the abstract source node (U in subgraph s).
			from := h.Node(edge.Tails[0])
			byU := memBySubgraph[from.Subgraph]
			if byU == nil {
				byU = map[string]map[string]bool{}
				memBySubgraph[from.Subgraph] = byU
			}
			if byU[from.Type] == nil {
				byU[from.Type] = map[string]bool{}
			}
			for _, m := range edge.Members {
				byU[from.Type][m] = true
			}
		case hypergraph.EdgeDescent:
			// The field node's Descent target is the subgraph-local output type of the position.
			// @provides scope copies are skipped: the canonical (unscoped) descent decides the type.
			if len(edge.Tails) != 1 || edge.Scope != "" {
				continue
			}
			fieldNode := h.Node(edge.Tails[0])
			if fieldNode.Kind != hypergraph.NodeField || fieldNode.Scope != "" {
				continue
			}
			byField := descTarget[fieldNode.Subgraph]
			if byField == nil {
				byField = map[string]string{}
				descTarget[fieldNode.Subgraph] = byField
			}
			byField[fieldNode.Type+"."+fieldNode.Field] = h.Node(edge.Head).Type
		}
	}

	info := &composedInfo{def: t.def, h: h}

	for gi := range t.goalOb {
		g := GoalID(gi)
		ref, ok := t.refinementAncestor(g)
		if !ok {
			continue // not under any abstract refinement -- never narrowed
		}
		u, c := ref.Type, ref.Concrete
		if c == "" {
			continue // a type-condition-less fragment (normalization artifact) -- never narrowed
		}
		field, intervening, ok := t.refinementContext(ref)
		if !ok {
			continue // no parent field (e.g. a top-level refinement) -- vacuous, not narrowed
		}
		// Capable subgraphs, POSITION-scoped (D6ppp): judged over the chain of
		// field obligations from the operation root down to the parent field, so a position only
		// reachable through one subgraph's private descent (`rootA.bWrapper.actions` where bWrapper
		// exists only in b) is judged against THAT subgraph's members -- the field-global set
		// wrongly intersected a distributed member away (a response-only null where the executed
		// truth has data). Transport between subgraphs is admitted condition-AWARE (D6pppp): a
		// subgraph joins a level only via a jump whose KEY tails are obtainable at the position --
		// every key-tail subgraph already holds the instance there (the per-level fixpoint in
		// positionSubgraphs) -- so a ghost with a resolvable:true key UNOBTAINABLE at the requested
		// position no longer joins the verdict-2 intersection (the formerly REGISTERED planv2-gap
		// FS-ABS-4/abstract-narrowing/unobtainable-key-ghost, closed by this clause). An empty
		// chain step falls back to the field-global set (the previous, lenient behavior).
		subs := t.positionSubgraphs(h, ref, field, reachable, jumpTransports)

		// D6pp: per-subgraph position-possible sets, gated through the intervening refinements
		// (outermost first). A subgraph whose gated set comes out empty can supply no parent
		// instance at ref -- it drops out of P (the positional generalization of the old
		// name-membership intervening filter).
		var capable []hypergraph.SubgraphID
		pos := map[hypergraph.SubgraphID]posSet{}
		for _, s := range subs {
			p := positionSet(descTarget, memBySubgraph, info, s, field)
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
			continue // parent unreachable/unsuppliable everywhere -- stays a cover requirement
		}

		// Verdict 1 -- dead member: impossible at the position via EVERY capable subgraph.
		dead := true
		for _, s := range capable {
			if memberPossible(c, pos[s], info) {
				dead = false
				break
			}
		}
		if dead {
			t.exempt[gi] = true
			t.exemptDead[gi] = true
			continue
		}

		// Entity gate (unchanged from D6): if C itself, or any member of U in any capable subgraph,
		// is an entity, the members are individually reachable via D7 wherever possible -- never
		// intersection-narrow. (A possible-in-a-subset entity member is the DISTRIBUTED member.)
		if entityTypes[c] || anyEntityMember(entityTypes, memBySubgraph, capable, u) {
			continue
		}

		// Verdict 2 -- value-type intersection, possibility-tested: exempt iff C is impossible in
		// some capable subgraph.
		for _, s := range capable {
			if !memberPossible(c, pos[s], info) {
				t.exempt[gi] = true
				break
			}
		}
	}
}

// Exempt reports the precomputed member-narrowing verdict for goal g: true iff g sits under a
// refinement `... on C` that is a dead member at its position, or a value-type member outside the
// possibility intersection (D6/D6pp). Build classifies at its tail, so the verdict is populated by
// default; on a tree built without classification Exempt is nil-safe and reports false (no
// narrowing). The search consumes this as its exempt predicate.
func (t *Tree) Exempt(g GoalID) bool {
	return t.exempt != nil && int(g) < len(t.exempt) && t.exempt[g]
}

// --- D6pp position-possible machinery -----------------------------------------------------------

// posSet is a position-possible member set: either TOP (all -- unknown membership, every member
// possible) or an explicit set of concrete type names.
type posSet struct {
	all bool
	set map[string]bool
}

func (p posSet) empty() bool { return !p.all && len(p.set) == 0 }

// positionSet derives pos_s for the parent field obligation (D6pp): the subgraph-local Descent
// target type decides -- concrete object type => {X}; abstract with locally modelled members =>
// Mem_s(X); anything else => TOP (unknown).
func positionSet(descTarget map[hypergraph.SubgraphID]map[string]string,
	mem map[hypergraph.SubgraphID]map[string]map[string]bool, info *composedInfo,
	s hypergraph.SubgraphID, field Obligation) posSet {

	x := descTarget[s][field.Type+"."+field.Field]
	if x == "" {
		return posSet{all: true}
	}
	switch info.kind(x) {
	case kindObject:
		return posSet{set: map[string]bool{x: true}}
	case kindAbstract:
		if m := mem[s][x]; len(m) > 0 {
			out := make(map[string]bool, len(m))
			for k := range m {
				out[k] = true
			}
			return posSet{set: out}
		}
		return posSet{all: true} // abstract position without local member knowledge: TOP
	default:
		return posSet{all: true} // unknown type kind: TOP
	}
}

// gatePosSet narrows a position set through one intervening refinement gate `... on C_i`:
// a concrete gate pins the set to {C_i} (or {} if impossible); an abstract gate intersects with its
// implementer set (TOP absorbs -- an unknown set stays unknown under an abstract gate).
func gatePosSet(p posSet, gate string, info *composedInfo) posSet {
	if gate == "" {
		return p
	}
	switch info.kind(gate) {
	case kindObject:
		if p.all || p.set[gate] {
			return posSet{set: map[string]bool{gate: true}}
		}
		return posSet{}
	case kindAbstract:
		if p.all {
			return p
		}
		impl := info.impl(gate)
		out := map[string]bool{}
		for m := range p.set {
			if impl[m] {
				out[m] = true
			}
		}
		return posSet{set: out}
	default:
		return p // unknown gate kind: no information, never narrow on ignorance
	}
}

// memberPossible reports whether refinement type c can occur at a position with possible set p:
// a concrete c must be in the set; an abstract c needs one implementer in it; TOP admits everything.
func memberPossible(c string, p posSet, info *composedInfo) bool {
	if p.all {
		return true
	}
	switch info.kind(c) {
	case kindObject:
		return p.set[c]
	case kindAbstract:
		for m := range info.impl(c) {
			if p.set[m] {
				return true
			}
		}
		return false
	default:
		return true // unknown C: no exemption from ignorance
	}
}

// --- composed-schema knowledge (with an H-derived fallback) ------------------------------------

type typeKind uint8

const (
	kindUnknown typeKind = iota
	kindObject
	kindAbstract
)

// composedInfo answers "is this composed type concrete or abstract?" and "what are an abstract
// type's concrete implementers/members?" -- knowledge H does not carry. It prefers the composed
// client schema (t.def, set by Build); a hand-assembled tree without one falls back to H's TypeMove
// edges (abstract iff the type is a type-move source anywhere; implementers = the union of its
// member lists), leaving everything else kindUnknown (the never-narrow direction). Memoized.
type composedInfo struct {
	def      *ast.Document
	h        *hypergraph.Hypergraph
	kinds    map[string]typeKind
	impls    map[string]map[string]bool
	hMembers map[string]map[string]bool // lazy abstractMembers(h) for the fallback
}

func (ci *composedInfo) kind(name string) typeKind {
	if k, ok := ci.kinds[name]; ok {
		return k
	}
	k := kindUnknown
	if ci.def != nil {
		if node, ok := ci.def.Index.FirstNodeByNameStr(name); ok {
			switch node.Kind {
			case ast.NodeKindObjectTypeDefinition:
				k = kindObject
			case ast.NodeKindInterfaceTypeDefinition, ast.NodeKindUnionTypeDefinition:
				k = kindAbstract
			}
		}
	} else {
		if ci.hMembers == nil {
			ci.hMembers = abstractMembers(ci.h)
		}
		if len(ci.hMembers[name]) > 0 {
			k = kindAbstract
		} else if hasObjectNode(ci.h, name) {
			k = kindObject
		}
	}
	if ci.kinds == nil {
		ci.kinds = map[string]typeKind{}
	}
	ci.kinds[name] = k
	return k
}

// impl returns an abstract type's concrete implementer (interface) / member (union) names in the
// composed schema; the H fallback unions its TypeMove member lists.
func (ci *composedInfo) impl(name string) map[string]bool {
	if m, ok := ci.impls[name]; ok {
		return m
	}
	out := map[string]bool{}
	if ci.def != nil {
		if node, ok := ci.def.Index.FirstNodeByNameStr(name); ok {
			switch node.Kind {
			case ast.NodeKindUnionTypeDefinition:
				if names, ok := ci.def.UnionTypeDefinitionMemberTypeNames(node.Ref); ok {
					for _, n := range names {
						out[n] = true
					}
				}
			case ast.NodeKindInterfaceTypeDefinition:
				for objRef := range ci.def.ObjectTypeDefinitions {
					objName := ci.def.ObjectTypeDefinitionNameString(objRef)
					if n, ok := ci.def.Index.FirstNodeByNameStr(objName); ok &&
						ci.def.NodeImplementsInterface(n, ast.ByteSlice(name)) {
						out[objName] = true
					}
				}
			}
		}
	} else {
		if ci.hMembers == nil {
			ci.hMembers = abstractMembers(ci.h)
		}
		for m := range ci.hMembers[name] {
			out[m] = true
		}
	}
	if ci.impls == nil {
		ci.impls = map[string]map[string]bool{}
	}
	ci.impls[name] = out
	return out
}

// hasObjectNode reports whether H has an object node of the given type in any subgraph.
func hasObjectNode(h *hypergraph.Hypergraph, name string) bool {
	for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
		n := h.Node(id)
		if n.Kind == hypergraph.NodeObject && n.Type == name {
			return true
		}
	}
	return false
}

// anyEntityMember reports whether any member of abstract type u -- across the capable subgraphs -- is
// an entity (heads an entity-jump edge). It is a boolean OR, so map iteration order over the members
// is immaterial to the result.
func anyEntityMember(entityTypes map[string]bool, mem map[hypergraph.SubgraphID]map[string]map[string]bool, subs []hypergraph.SubgraphID, u string) bool {
	for _, s := range subs {
		for m := range mem[s][u] {
			if entityTypes[m] {
				return true
			}
		}
	}
	return false
}

// refinementAncestor walks up the parent chain from goal g's obligation to the nearest refinement
// obligation `... on C` (at or above g), returning it with ok=true. ok=false when g is under no
// refinement (the ordinary field-selection case). The walk stops at the root sentinel (NoParent; the
// legacy self-loop is kept for hand-assembled trees).
func (t *Tree) refinementAncestor(g GoalID) (Obligation, bool) {
	ob := t.obligations[t.goalOb[g]]
	for {
		if ob.Kind == Refine {
			return ob, true
		}
		if ob.Parent == NoParent || ob.Parent == ob.ID {
			return Obligation{}, false // reached the root sentinel without a refinement
		}
		ob = t.obligations[ob.Parent]
	}
}

// refinementContext walks up from refinement ref to the nearest field obligation <T.f> above it (the
// position's parent field), collecting the intervening refinements between them innermost-first.
// ok=false when ref has no parent field (a top-level refinement).
func (t *Tree) refinementContext(ref Obligation) (field Obligation, intervening []Obligation, ok bool) {
	ob := ref
	for {
		if ob.Parent == NoParent || ob.Parent == ob.ID {
			return Obligation{}, nil, false
		}
		ob = t.obligations[ob.Parent]
		if ob.Kind == Field {
			return ob, intervening, true
		}
		if ob.Kind == Refine {
			intervening = append(intervening, ob)
		}
	}
}

// jumpTransport is one entity jump reduced to what position-scoped transport admission needs
// (D6pppp): the subgraph the jump lands the instance in (head) and the distinct subgraphs its KEY
// tails live in -- the places the key values must be obtainable FROM.
type jumpTransport struct {
	head     hypergraph.SubgraphID
	tailSubs []hypergraph.SubgraphID
}

// positionSubgraphs computes the POSITION-scoped capable set for a refinement's parent field
// (FORMAL_SPEC D6ppp): the
// subgraphs that can actually supply the parent instance at THIS response position. It walks the
// chain of Field obligations root->parent; at each level the instance is available in a subgraph s
// iff s resolves the field AND s can HOLD the enclosing instance there: s was capable at the
// previous level (local descent), or an entity jump on the enclosing type lands in s whose KEY
// tails are obtainable at the position (D6pppp condition-aware transport) -- every key-tail
// subgraph is itself an instance holder, judged as a least fixpoint over the level's holder set so
// multi-hop relays (a -> b via k1, b -> c via k2) are admitted while a ghost whose admitting key
// no position-capable subgraph can produce is NOT (the closed
// FS-ABS-4/abstract-narrowing/unobtainable-key-ghost class -- under the old condition-BLIND
// admission its smaller member set joined the verdict-2 intersection and silently narrowed a real
// member away). HONEST SCOPE (D6pppp): obtainability is judged at SUBGRAPH granularity -- a
// key-tail subgraph that holds the instance is assumed able to render the key fields into a
// representation (the jump's build-time premise); a subgraph whose copy of the key fields is
// @external and fed only by a key it was not itself admitted under can still be over-admitted --
// strictly tighter than condition-blind, still an over-approximation, and over-approximation errs
// toward NOT narrowing for verdict 1 (never declares members dead from a failed walk). A level
// that comes out empty (a shape the transport model does not cover) falls back to that level's
// field-global set: position-scoping never fails louder than the field-global behavior.
func (t *Tree) positionSubgraphs(h *hypergraph.Hypergraph, ref, field Obligation, reachable []bool,
	jumpTransports map[string][]jumpTransport) []hypergraph.SubgraphID {

	chain := t.fieldChainAbove(ref)
	if len(chain) == 0 || chain[len(chain)-1].ID != field.ID {
		return t.fieldSubgraphs(h, field, reachable) // defensive: unexpected chain -- old behavior
	}
	cur := map[hypergraph.SubgraphID]bool{}
	for i, f := range chain {
		have := t.fieldSubgraphs(h, f, reachable)
		next := map[hypergraph.SubgraphID]bool{}
		if i == 0 {
			for _, s := range have {
				next[s] = true
			}
		} else {
			// holders: the subgraphs that can HOLD an f.Type instance at this position -- the
			// previous level's capable set closed under key-obtainable jumps (least fixpoint;
			// order-independent, so no map iteration order leaks into the verdict).
			holders := make(map[hypergraph.SubgraphID]bool, len(cur))
			for s := range cur {
				holders[s] = true
			}
			transports := jumpTransports[f.Type]
			for changed := true; changed; {
				changed = false
				for _, jt := range transports {
					if holders[jt.head] {
						continue
					}
					obtainable := true
					for _, ts := range jt.tailSubs {
						if !holders[ts] {
							obtainable = false
							break
						}
					}
					if obtainable {
						holders[jt.head] = true
						changed = true
					}
				}
			}
			for _, s := range have {
				if holders[s] {
					next[s] = true
				}
			}
			if len(next) == 0 {
				for _, s := range have {
					next[s] = true // fall back to field-global at this level (lenient)
				}
			}
		}
		cur = next
	}
	out := make([]hypergraph.SubgraphID, 0, len(cur))
	for s := range cur {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// fieldChainAbove returns the Field obligations strictly above ref, root->parent order (the parent
// field last). Empty when ref has no field ancestor (a top-level refinement).
func (t *Tree) fieldChainAbove(ref Obligation) []Obligation {
	var rev []Obligation
	ob := ref
	for {
		if ob.Parent == NoParent || ob.Parent == ob.ID {
			break
		}
		ob = t.obligations[ob.Parent]
		if ob.Kind == Field {
			rev = append(rev, ob)
		}
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// fieldSubgraphs returns the distinct subgraphs holding a REACHABLE field-resolution node for the
// given field obligation <T.f>, sorted ascending (deterministic downstream iteration). Route-scoping
// (D6p): an unreachable T.f node -- its parent object has no producing path in the graph -- cannot
// supply a parent instance, so it does not belong in the set.
//
// NOTE: the T.f lookup is a full node scan per refinement goal -- fine at current instance sizes; if
// H grows, build a (Type, Field) -> subgraphs index once per ClassifyNarrowing call.
func (t *Tree) fieldSubgraphs(h *hypergraph.Hypergraph, field Obligation, reachable []bool) []hypergraph.SubgraphID {
	seen := map[hypergraph.SubgraphID]bool{}
	var out []hypergraph.SubgraphID
	for id := hypergraph.NodeID(0); int(id) < h.NumNodes(); id++ {
		n := h.Node(id)
		if n.Kind != hypergraph.NodeField || n.Type != field.Type || n.Field != field.Field || seen[n.Subgraph] {
			continue
		}
		if reachable != nil && (int(id) >= len(reachable) || !reachable[id]) {
			continue
		}
		seen[n.Subgraph] = true
		out = append(out, n.Subgraph)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// optimisticReachable computes condition-blind reachability from the roots: reachable[v] is true iff
// some sequence of edges whose tails are all reachable produces v (entity-jump key conditions are
// ignored -- an over-approximation, so it never drops a node a real route could reach). It is the
// route-scoping input for fieldSubgraphs. A pure derivation over the graph; no search state.
//
// It is a worklist (Kahn-style propagation), not the earlier naive re-scan: on a deep topology (a ring
// of entity jumps across S subgraphs) reachability propagates one hop per full pass over the edges, so
// the re-scan was quadratic in S -- the measured cost that dominated per-request planning at scale
// (S=200: obligation.Build 183ms, ~84% of it here). The worklist touches each edge once per tail: each
// edge carries a count of tails not yet known reachable, decremented as tails are marked; the edge
// fires its head exactly when the count reaches zero (an edge with no tails fires immediately). The
// least fixpoint is unique, so the result is identical to the old routine, order-independent.
// EdgeTails/EdgeHead are used instead of Edge() to avoid copying the large Edge struct per visit.
func optimisticReachable(h *hypergraph.Hypergraph, includeSubscriptionRoots bool) []bool {
	n := h.NumNodes()
	ne := h.NumEdges()
	reach := make([]bool, n)
	remaining := make([]int, ne) // per edge: count of tails not yet known reachable
	queue := make([]hypergraph.NodeID, 0, n)

	mark := func(v hypergraph.NodeID) {
		if int(v) < n && !reach[v] {
			reach[v] = true
			queue = append(queue, v)
		}
	}
	for _, r := range h.Roots() {
		// D11.12 root scoping: a non-subscription operation's reachability never seeds the
		// subscription root -- a schema that merely DECLARES a Subscription type must leave
		// query/mutation verdicts (D6p route-scoping, expansion gates, terminal promotion)
		// byte-identical to the same schema without it. The root node's Field carries its
		// operation kind (builder: Node{Kind: NodeRoot, Field: "query"|"mutation"|"subscription"}).
		if !includeSubscriptionRoots && h.NodeField(r) == "subscription" {
			continue
		}
		mark(r)
	}
	for e := 0; e < ne; e++ {
		eid := hypergraph.EdgeID(e)
		tails := h.EdgeTails(eid)
		remaining[e] = len(tails)
		if len(tails) == 0 {
			mark(h.EdgeHead(eid)) // no prerequisites -- head is produced unconditionally
		}
	}
	for len(queue) > 0 {
		v := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, eid := range h.TailIncidence(v) {
			if remaining[eid] == 0 {
				continue // edge already fired (or v is a duplicate tail already accounted for)
			}
			remaining[eid]--
			if remaining[eid] == 0 {
				mark(h.EdgeHead(eid))
			}
		}
	}
	return reach
}

// reachableFor returns the optimistic reachability for the given operation kind (D11.12 root
// scoping): query/mutation (and unknown, for hand-assembled trees -- their graphs carry no
// subscription root, so the variants coincide) exclude the subscription root's seed; subscription
// operations seed every root, keeping the legacy any-root fall-back semantics for their nested
// goals. Both variants are pure functions of the built graph, memoized per graph
// (hypergraph.Memo) because reachability is on the measured per-Build hot path.
func reachableFor(h *hypergraph.Hypergraph, opKind ast.OperationType) []bool {
	if opKind == ast.OperationTypeSubscription {
		return h.Memo(hypergraph.SlotObligationReachAllRoots, func() any {
			return optimisticReachable(h, true)
		}).([]bool)
	}
	return h.Memo(hypergraph.SlotObligationReachNoSubRoots, func() any {
		return optimisticReachable(h, false)
	}).([]bool)
}
