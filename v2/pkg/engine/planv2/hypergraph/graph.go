package hypergraph

import (
	"maps"
	"slices"
	"sort"
	"sync/atomic"
)

type SubgraphID uint32 // stable per-subgraph id; 0 reserved for "no subgraph" (root nodes)
type NodeID uint32
type EdgeID uint32

const NoEdge EdgeID = ^EdgeID(0) // back-pointer / absence sentinel

type NodeKind uint8

const (
	NodeObject NodeKind = iota + 1 // an object type in a subgraph
	NodeField                      // a field of an object type in a subgraph
	NodeRoot                       // a synthetic operation root (query/mutation)
)

// EdgeKind values are ordered so the search's tie-break prefers cheaper kinds first:
// Field < Descent < TypeMove < EntityJump.
type EdgeKind uint8

const (
	EdgeField EdgeKind = iota + 1
	EdgeDescent
	EdgeTypeMove
	EdgeEntityJump
)

type Node struct {
	Kind     NodeKind
	Type     string     // composed-schema type name; "" for roots (keyed by Field = operation kind)
	Subgraph SubgraphID // 0 for roots
	Field    string     // set only on a field node
	Scope    string     // @provides scope tag; "" if none
}

type Edge struct {
	Kind    EdgeKind
	Label   string // field name (Field), member type (TypeMove), else ""
	Head    NodeID
	Tails   []NodeID // sorted; length 1 except for an EntityJump. Always exactly one Head.
	Weight  int64    // cost of using this edge
	Members []string // member set on a TypeMove edge (sorted); nil otherwise
	Scope   string   // @provides scope tag, or ""
	// Conditions restricts a conditioned entity jump to paths matching the stated field coordinates;
	// nil for unconditional edges. It is NOT part of the edge's identity, so the same edge discovered
	// via two config routes is one edge, with the conditions unioned when they are merged.
	Conditions []KeyCondition
	// KeyTails is the @key subset of an entity jump's Tails (the rest are @requires tails), kept so
	// lowering can split the representation into its @key and @requires parts the way v1 does. Sorted;
	// nil for non-jump edges. Like Conditions it is outside the edge identity: when two discoveries
	// merge, the first-seen key subset is kept (the builder is deterministic, so the choice is too).
	KeyTails []NodeID
	// Requires holds the raw @requires selection strings (with their field arguments, e.g.
	// `price(currency: "USD") weight`) that gated this entity jump. Kept so lowering can render the
	// argument-bearing requires into both the source query and the entity fetch's representation -- the
	// Tails carry these fields without argument values, so the literal values would otherwise be lost.
	// Like KeyTails it is outside the edge identity; nil for non-jump edges and jumps whose target has
	// no @requires.
	Requires []string
	// RequiresBy is the requiring field's name for each entry of Requires (RequiresBy[i] is the field
	// whose @requires produced Requires[i]). The raw selection strings drop this field association, but
	// lowering needs it: when two fields on one entity require the SAME coordinate with DIFFERENT
	// argument values, lowering has to split them into separate entity fetches, and it can only do that
	// if it knows which requires belongs to which field. Outside the edge identity; nil when Requires
	// is nil.
	RequiresBy []string
	// KeySelection is the raw @key selection set that produced this EntityJump ("products{id pid}").
	// Recorded on EVERY D7-family jump: lowering's D11.11 key-input pipeline reads the key's path
	// STRUCTURE off it -- for a distributed jump the tails span subgraphs and the tail up-walk cannot
	// reconstruct the paths at all, and for a plain jump consumed as a gather producer the pre-jump
	// anchor path is derived from it. Outside the edge identity (first-seen kept on a merge, like
	// KeyTails); "" on non-jump edges and hand-assembled test graphs.
	KeySelection string
	// KeyDistributed marks a D7ppp distributed-key jump: the key has no single-source supplier and the
	// Tails are per-coordinate assignments spanning subgraphs. This is the gate for ALL new lowering
	// behavior (KeySelection alone is informational) -- a plain jump keeps the base D11 key injection
	// byte-identical. Outside the edge identity.
	KeyDistributed bool
	// OutputType is the field's printed return type in its OWNING subgraph's schema, with list/non-null
	// wrappers (e.g. "ID!", "[User!]!"); set on Field edges only, "" otherwise. It records the one thing
	// the composed client schema erases -- subgraph-local nullability -- which lowering's aliaser needs to
	// spot a response-shape conflict between two abstract members resolved in the SAME subgraph (a field
	// that is `ID!` under one member and `ID` under another composes to a uniform nullable `ID`).
	// Derived, and outside the edge identity.
	OutputType string
}

// KeyCondition mirrors plan.KeyCondition without importing plan (this package's core stays
// stdlib-only). It restricts a conditioned entity jump to paths matching the stated field
// coordinates, checked before the search as a filter on which jumps are usable.
type KeyCondition struct {
	Coordinates []string // "TypeName.FieldName" per coordinate
	FieldPath   []string
}

type nodeKey struct {
	kind              NodeKind
	typ, field, scope string
	subgraph          SubgraphID
}

type edgeKey struct {
	kind         EdgeKind
	label, scope string
	head         NodeID
	tails        string // sorted tail ids joined
}

type Builder struct {
	nodes         []Node
	nodeIdx       map[nodeKey]NodeID
	edges         []Edge
	edgeIdx       map[edgeKey]EdgeID
	subgraphNames map[SubgraphID]string
	keyHeads      map[SubgraphID]map[string]bool // type -> some heading @key is resolvable
	// entityInterfaces / interfaceObjects: interface-object metadata, see the Hypergraph fields.
	entityInterfaces map[string]bool
	interfaceObjects map[SubgraphID]map[string]bool
}

func NewBuilder() *Builder {
	return &Builder{nodeIdx: map[nodeKey]NodeID{}, edgeIdx: map[edgeKey]EdgeID{}}
}

// SetSubgraphName records a subgraph's name so the built graph can map ids back to names for the
// search's tie-break and for lowering.
func (b *Builder) SetSubgraphName(id SubgraphID, name string) {
	if b.subgraphNames == nil {
		b.subgraphNames = map[SubgraphID]string{}
	}
	b.subgraphNames[id] = name
}

// MarkKeyHead records that a @key declaration heads type typeName in subgraph s (per the D7
// jump-head mapping -- the builder calls this for every head type a key would produce jumps to),
// and whether that declaration is resolvable. A type is marked resolvable if ANY heading key is;
// a type whose every heading key carries resolvable:false stays non-resolvable. The search's D10
// fall-back guard reads this via Hypergraph.OnlyNonResolvableKeys to PROVE a goal non-resolvable
// (FORMAL_SPEC D10 amendment -- provable-non-resolvability narrowing). Metadata only: no node or
// edge is added, so covers, costs, and every golden are untouched. A graph built without any marks
// (hand-assembled test graphs) proves nothing -- the fall-back behavior there is unchanged.
func (b *Builder) MarkKeyHead(s SubgraphID, typeName string, resolvable bool) {
	if b.keyHeads == nil {
		b.keyHeads = map[SubgraphID]map[string]bool{}
	}
	m := b.keyHeads[s]
	if m == nil {
		m = map[string]bool{}
		b.keyHeads[s] = m
	}
	if resolvable {
		m[typeName] = true
	} else if _, declared := m[typeName]; !declared {
		m[typeName] = false
	}
}

func (b *Builder) AddNode(n Node) NodeID {
	k := nodeKey{n.Kind, n.Type, n.Field, n.Scope, n.Subgraph}
	if id, ok := b.nodeIdx[k]; ok {
		return id
	}
	id := NodeID(len(b.nodes))
	b.nodes = append(b.nodes, n)
	b.nodeIdx[k] = id
	return id
}

func tailsKey(tails []NodeID) string {
	buf := make([]byte, 0, len(tails)*5)
	for _, t := range tails {
		buf = append(buf, byte(t), byte(t>>8), byte(t>>16), byte(t>>24), '.')
	}
	return string(buf)
}

func (b *Builder) AddEdge(e Edge) EdgeID {
	// Sort the tails so an edge's identity doesn't depend on the order they were passed in; copy
	// first, so the caller's slice is never mutated.
	e.Tails = append([]NodeID(nil), e.Tails...)
	slices.Sort(e.Tails)
	tk := tailsKey(e.Tails)
	k := edgeKey{e.Kind, e.Label, e.Scope, e.Head, tk}
	if id, ok := b.edgeIdx[k]; ok {
		// The same edge was discovered again. Conditions aren't part of the identity, so union them:
		// an unconditional discovery (nil) makes the edge always available and wins; otherwise
		// accumulate the distinct conditions.
		ex := &b.edges[id]
		switch {
		case len(ex.Conditions) == 0:
			// already unconditional; stays unconditional
		case len(e.Conditions) == 0:
			ex.Conditions = nil
		default:
			for _, c := range e.Conditions {
				if !containsCondition(ex.Conditions, c) {
					ex.Conditions = append(ex.Conditions, c)
				}
			}
		}
		return id
	}
	if e.Members != nil {
		e.Members = append([]string(nil), e.Members...)
		sort.Strings(e.Members)
	}
	if e.KeyTails != nil { // copy -- AddEdge never retains caller memory -- and normalize order
		e.KeyTails = append([]NodeID(nil), e.KeyTails...)
		slices.Sort(e.KeyTails)
	}
	if e.Requires != nil { // copy (order is caller-significant: the requires configs' order); no sort
		e.Requires = append([]string(nil), e.Requires...)
	}
	if e.RequiresBy != nil { // copy; parallel to Requires (same caller-significant order)
		e.RequiresBy = append([]string(nil), e.RequiresBy...)
	}
	e.Conditions = dedupConditions(e.Conditions)
	id := EdgeID(len(b.edges))
	b.edges = append(b.edges, e)
	b.edgeIdx[k] = id
	return id
}

// equalCondition reports semantic equality of two KeyConditions (element-wise on both slices).
func equalCondition(a, b KeyCondition) bool {
	return slices.Equal(a.Coordinates, b.Coordinates) && slices.Equal(a.FieldPath, b.FieldPath)
}

func containsCondition(cs []KeyCondition, c KeyCondition) bool {
	return slices.ContainsFunc(cs, func(x KeyCondition) bool { return equalCondition(x, c) })
}

// dedupConditions copies the caller's slice (AddEdge never retains caller memory) and drops
// duplicate entries, keeping first-seen order. Returns nil for empty input (unconditional).
func dedupConditions(in []KeyCondition) []KeyCondition {
	if len(in) == 0 {
		return nil
	}
	out := make([]KeyCondition, 0, len(in))
	for _, c := range in {
		if !containsCondition(out, c) {
			out = append(out, c)
		}
	}
	return out
}

type Hypergraph struct {
	nodes         []Node
	edges         []Edge
	roots         []NodeID
	incoming      map[NodeID][]EdgeID
	tailInc       map[NodeID][]EdgeID
	subgraphNames map[SubgraphID]string
	keyHeads      map[SubgraphID]map[string]bool // see Builder.MarkKeyHead
	// entityInterfaces is the set of interface type names that are Fed 2.3 entity interfaces or
	// @interfaceObject interfaces in ANY subgraph (see Builder.MarkEntityInterface): the runtime
	// __typename at such a position can legitimately BE the interface name (an interface-object
	// subgraph reports it), so response completion must accept it as a possible type.
	entityInterfaces map[string]bool
	// interfaceObjects records, per subgraph, the interface type names that subgraph models as an
	// @interfaceObject OBJECT (see Builder.MarkInterfaceObject): an entity jump INTO such a
	// (subgraph, type) must send the INTERFACE name as the representation __typename -- the target
	// declares no concrete member types (v1's interface-object representation rewrite).
	interfaceObjects map[SubgraphID]map[string]bool

	// derived holds the per-graph caches behind Memo: opaque values that are pure functions of the
	// built (immutable) graph, published atomically. One slot per owner (see DerivedSlot); nothing
	// in this package reads them.
	derived [numDerivedSlots]atomic.Pointer[any]
}

// DerivedSlot names one per-graph cache slot for a value derived purely from the built graph (see
// Memo). Each slot has exactly ONE owner -- the package whose derived index it caches (which this
// package cannot name without an import cycle, hence the opaque `any`). A new derived index gets
// its own slot; slots are never shared.
type DerivedSlot uint8

const (
	// SlotSearchLayered caches planv2/search's chain-layered trace index (consistent.go).
	SlotSearchLayered DerivedSlot = iota
	// SlotObligationExpand caches planv2/obligation's D3pppp member-expansion trigger indexes
	// (expand.go).
	SlotObligationExpand
	// SlotObligationReachAllRoots / SlotObligationReachNoSubRoots cache planv2/obligation's
	// optimistic reachability per operation-kind root scope (D11.12 root scoping): the
	// no-subscription-roots variant serves query/mutation operations, the all-roots variant serves
	// subscription operations. Both are pure functions of the built graph, hence two slots.
	SlotObligationReachAllRoots
	SlotObligationReachNoSubRoots
	// SlotSearchOpScope caches planv2/search's operation-scoped-mode support index (opscope.go):
	// the pure-graph indexes the per-plan scope computation and mask derivation read (FORMAL_SPEC
	// Section 6.5). Only the scoped mode consults it; the default mode's per-plan scans are untouched.
	SlotSearchOpScope
	numDerivedSlots
)

// Memo returns the value cached in the given derived slot, computing it with build on first use.
// The graph is immutable after Build, so this is only sound for values that are pure functions of
// the graph (same graph, same value) and are read-only after construction -- rebuilding such an
// index per call was a measured kernel regression (BENCHMARKS.md Section 4/Section 5.3). Concurrent first calls
// may each run build, but exactly one result is published (compare-and-swap) and every caller
// returns that one -- safe under the race detector, and semantically indistinguishable because the
// results are identical.
func (h *Hypergraph) Memo(slot DerivedSlot, build func() any) any {
	p := &h.derived[slot]
	if v := p.Load(); v != nil {
		return *v
	}
	v := build()
	if p.CompareAndSwap(nil, &v) {
		return v
	}
	return *p.Load()
}

// SubgraphName maps a subgraph id back to its name, for the search's tie-break and for lowering.
// Returns "" for unknown ids (including the reserved 0 = "no subgraph").
func (h *Hypergraph) SubgraphName(s SubgraphID) string { return h.subgraphNames[s] }

// OnlyNonResolvableKeys reports whether type typeName in subgraph s is DECLARED as a @key jump
// head and EVERY declaration heading it carries resolvable:false -- the schema-level proof that no
// entity jump into (typeName, s) may ever be modelled (FORMAL_SPEC D10 amendment --
// provable-non-resolvability narrowing, condition 1). False when the type declares no key at all
// (nothing is proven -- the missing-jump class keeps the fall-back), when any heading key is
// resolvable, or when the graph carries no key metadata (hand-built graphs).
func (h *Hypergraph) OnlyNonResolvableKeys(typeName string, s SubgraphID) bool {
	resolvable, declared := h.keyHeads[s][typeName]
	return declared && !resolvable
}

// IsEntityInterface reports whether typeName is an entity interface or @interfaceObject interface
// in any subgraph (the runtime __typename at such a position may be the interface name itself).
func (h *Hypergraph) IsEntityInterface(typeName string) bool { return h.entityInterfaces[typeName] }

// IsInterfaceObject reports whether subgraph s models typeName as an @interfaceObject object type
// -- the target of a jump that must present the INTERFACE name in its representation __typename.
func (h *Hypergraph) IsInterfaceObject(typeName string, s SubgraphID) bool {
	return h.interfaceObjects[s][typeName]
}

// MarkEntityInterface records typeName as an entity-interface/@interfaceObject interface (see
// Hypergraph.IsEntityInterface). Metadata only; nodes, edges, and costs are untouched.
func (b *Builder) MarkEntityInterface(typeName string) {
	if b.entityInterfaces == nil {
		b.entityInterfaces = map[string]bool{}
	}
	b.entityInterfaces[typeName] = true
}

// MarkInterfaceObject records that subgraph s models interface typeName as an @interfaceObject
// object type (see Hypergraph.IsInterfaceObject). Metadata only.
func (b *Builder) MarkInterfaceObject(s SubgraphID, typeName string) {
	if b.interfaceObjects == nil {
		b.interfaceObjects = map[SubgraphID]map[string]bool{}
	}
	m := b.interfaceObjects[s]
	if m == nil {
		m = map[string]bool{}
		b.interfaceObjects[s] = m
	}
	m[typeName] = true
}

func (b *Builder) Build() *Hypergraph {
	used := make([]bool, len(b.nodes))
	for _, e := range b.edges {
		used[e.Head] = true
		for _, t := range e.Tails {
			used[t] = true
		}
	}
	// compact: drop isolated nodes, remap ids
	remap := make([]NodeID, len(b.nodes))
	var nodes []Node
	for i, n := range b.nodes {
		if !used[i] {
			continue
		}
		remap[i] = NodeID(len(nodes))
		nodes = append(nodes, n)
	}
	h := &Hypergraph{
		nodes:         nodes,
		incoming:      map[NodeID][]EdgeID{},
		tailInc:       map[NodeID][]EdgeID{},
		subgraphNames: map[SubgraphID]string{},
	}
	maps.Copy(h.subgraphNames, b.subgraphNames)
	if b.keyHeads != nil { // deep copy: the built graph is immutable, the builder is not
		h.keyHeads = make(map[SubgraphID]map[string]bool, len(b.keyHeads))
		for s, m := range b.keyHeads {
			cp := make(map[string]bool, len(m))
			maps.Copy(cp, m)
			h.keyHeads[s] = cp
		}
	}
	if b.entityInterfaces != nil {
		h.entityInterfaces = make(map[string]bool, len(b.entityInterfaces))
		maps.Copy(h.entityInterfaces, b.entityInterfaces)
	}
	if b.interfaceObjects != nil {
		h.interfaceObjects = make(map[SubgraphID]map[string]bool, len(b.interfaceObjects))
		for s, m := range b.interfaceObjects {
			cp := make(map[string]bool, len(m))
			maps.Copy(cp, m)
			h.interfaceObjects[s] = cp
		}
	}
	for _, e := range b.edges {
		e.Head = remap[e.Head]
		nt := make([]NodeID, len(e.Tails))
		for i, t := range e.Tails {
			nt[i] = remap[t]
		}
		slices.Sort(nt)
		e.Tails = nt
		if e.KeyTails != nil {
			kt := make([]NodeID, len(e.KeyTails))
			for i, t := range e.KeyTails {
				kt[i] = remap[t]
			}
			slices.Sort(kt)
			e.KeyTails = kt
		}
		id := EdgeID(len(h.edges))
		h.edges = append(h.edges, e)
		h.incoming[e.Head] = append(h.incoming[e.Head], id)
		for _, t := range nt {
			h.tailInc[t] = append(h.tailInc[t], id)
		}
	}
	for id, n := range nodes {
		if n.Kind == NodeRoot {
			h.roots = append(h.roots, NodeID(id))
		}
	}
	slices.Sort(h.roots)
	return h
}

// Restrict returns the sub-hypergraph induced by exactly the given node and edge sets (true =
// keep), for the operation-scoped search mode (FORMAL_SPEC Section 6.5). Along with the sub-graph it
// returns the id translation tables: origNode[subID] / origEdge[subID] give each sub item's id in
// the parent graph.
//
// Contract, load-bearing for the mode's plan-equality property:
//
//   - ORDER-PRESERVING: kept nodes and edges receive sub ids in ascending parent-id order, so any
//     numeric-relative-order tie-break (the layered trace's state order) and any index-order scan
//     visit sub items in the parent's relative order.
//   - CLOSED INPUT: every kept edge's head and tails must be kept nodes (the Section 6.5 backward closure
//     guarantees this); an edge violating it is skipped defensively rather than emitted dangling.
//   - SHARED METADATA: subgraphNames, keyHeads, entityInterfaces, and interfaceObjects are shared
//     by reference -- the parent is immutable after Build and the sub-graph is read-only, so the
//     content-based C.4 tie-break and the D10 narrowing guard read identical strings/verdicts.
//     Edge bodies are copied with remapped Head/Tails/KeyTails (fresh slices; the parent's are
//     never mutated); other edge fields (labels, conditions, requires, weights) share backing
//     arrays read-only.
//   - Isolated kept nodes are kept (unlike Build's pruning): the scope computation only emits
//     nodes that participate in a kept edge or are candidate seeds, and a seed with no derivation
//     must still exist so candidate lookups see it unreachable rather than out of range.
//
// The sub-graph has its own empty Memo slots (per-graph derived caches are not shared).
func (h *Hypergraph) Restrict(nodeIn, edgeIn []bool) (*Hypergraph, []NodeID, []EdgeID) {
	remap := make([]NodeID, len(h.nodes))
	var origNode []NodeID
	sub := &Hypergraph{
		incoming:         map[NodeID][]EdgeID{},
		tailInc:          map[NodeID][]EdgeID{},
		subgraphNames:    h.subgraphNames,
		keyHeads:         h.keyHeads,
		entityInterfaces: h.entityInterfaces,
		interfaceObjects: h.interfaceObjects,
	}
	for i, keep := range nodeIn {
		if !keep {
			continue
		}
		remap[i] = NodeID(len(sub.nodes))
		sub.nodes = append(sub.nodes, h.nodes[i])
		origNode = append(origNode, NodeID(i))
	}
	var origEdge []EdgeID
	for i, keep := range edgeIn {
		if !keep {
			continue
		}
		e := h.edges[i] // copy the struct; slices remapped below, the rest shared read-only
		if !nodeIn[e.Head] {
			continue // defensive: never emit a dangling edge (see contract)
		}
		closed := true
		for _, t := range e.Tails {
			if !nodeIn[t] {
				closed = false
				break
			}
		}
		if !closed {
			continue
		}
		e.Head = remap[e.Head]
		nt := make([]NodeID, len(e.Tails))
		for j, t := range e.Tails {
			nt[j] = remap[t] // order-preserving remap keeps the sorted tail order
		}
		e.Tails = nt
		if e.KeyTails != nil {
			kt := make([]NodeID, len(e.KeyTails))
			for j, t := range e.KeyTails {
				kt[j] = remap[t]
			}
			e.KeyTails = kt
		}
		id := EdgeID(len(sub.edges))
		sub.edges = append(sub.edges, e)
		origEdge = append(origEdge, EdgeID(i))
		sub.incoming[e.Head] = append(sub.incoming[e.Head], id)
		for _, t := range nt {
			sub.tailInc[t] = append(sub.tailInc[t], id)
		}
	}
	for id, n := range sub.nodes {
		if n.Kind == NodeRoot {
			sub.roots = append(sub.roots, NodeID(id))
		}
	}
	// Already ascending (nodes were emitted in ascending parent order), kept for parity with Build.
	slices.Sort(sub.roots)
	return sub, origNode, origEdge
}

func (h *Hypergraph) NumNodes() int       { return len(h.nodes) }
func (h *Hypergraph) NumEdges() int       { return len(h.edges) }
func (h *Hypergraph) Node(id NodeID) Node { return h.nodes[id] }
func (h *Hypergraph) Edge(id EdgeID) Edge { return h.edges[id] }

// EdgeHead and EdgeTails return a single Edge field without copying the whole (large) Edge struct.
// The incidence walks are hot paths, and copying every Edge's full body -- its slices and all -- on
// each visit showed up heavily in the profile. EdgeTails returns the internal, sorted tail slice;
// like Roots/Incoming/TailIncidence it is not copied, so callers must treat it as read-only.
func (h *Hypergraph) EdgeHead(id EdgeID) NodeID    { return h.edges[id].Head }
func (h *Hypergraph) EdgeTails(id EdgeID) []NodeID { return h.edges[id].Tails }
func (h *Hypergraph) EdgeKind(id EdgeID) EdgeKind  { return h.edges[id].Kind }
func (h *Hypergraph) EdgeLabel(id EdgeID) string   { return h.edges[id].Label }
func (h *Hypergraph) EdgeWeight(id EdgeID) int64   { return h.edges[id].Weight }

// EdgeConditions returns the internal conditions slice without copying the Edge; read-only, like
// EdgeTails.
func (h *Hypergraph) EdgeConditions(id EdgeID) []KeyCondition { return h.edges[id].Conditions }

// NodeKind, NodeType, and NodeField return a single Node field without copying the whole struct
// (a Node carries three string headers); same hot-path rationale as the Edge accessors above.
func (h *Hypergraph) NodeKind(id NodeID) NodeKind { return h.nodes[id].Kind }
func (h *Hypergraph) NodeType(id NodeID) string   { return h.nodes[id].Type }
func (h *Hypergraph) NodeField(id NodeID) string  { return h.nodes[id].Field }

// Roots, Incoming, and TailIncidence return internal slices WITHOUT copying --
// the hypergraph is immutable after Build and these sit on the search kernel's
// hot path, so per-call copies are deliberately avoided. Callers MUST treat the
// returned slices as read-only; mutating them corrupts shared state.
func (h *Hypergraph) Roots() []NodeID                 { return h.roots }
func (h *Hypergraph) Incoming(head NodeID) []EdgeID   { return h.incoming[head] }
func (h *Hypergraph) TailIncidence(t NodeID) []EdgeID { return h.tailInc[t] }
