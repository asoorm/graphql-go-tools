package lower

// obligation_driven.go is the shipping lowering path: it turns the search's abstract plan (a set of
// edges plus, per requested field, the route that serves it) back into a concrete tree of subgraph
// fetches and the response shape to assemble from them. The zero-value LowerConfig routes here. An
// older path in lower.go is kept one release cycle behind LowerConfig{LegacyNodeKeyedGrouping: true}
// as an emergency escape hatch; see that file's LowerConfig doc.
//
// Why a second path. The old path grouped the flat, deduplicated edge set by node identity: one fetch
// per distinct node. That collapses any response shape that visits the SAME node type at two
// positions. For `order { buyer { rating } seller { rating } }` the deduped edge set keeps only the
// buyer route, so both siblings ended up in ONE entity fetch. For
// `order { buyer { friends { friends { id } } } }` every `friends` level maps onto the same User node,
// and grouping by node identity keeps only the shortest route, dropping the nesting. Both produce
// wrong data.
//
// The fix:
//   - STRUCTURE comes from the obligation tree -- one node per requested field, bounded by query depth.
//     A self-referential shape (friends of friends) therefore terminates by construction, and every
//     response position is distinct.
//   - GROUP BOUNDARIES and KEYS come from the per-field routes (Cover.Walks): each covered field's
//     route is walked to assign every response position to a fetch group, keyed by (entry response
//     path, target subgraph, opening entity jump). That splits order.buyer from order.seller even
//     though both reuse the same User->User entity jump and the same User.rating node.
//   - One ENTITY FETCH per landing position, each carrying the correct response path and fetch path,
//     computed from that position's hop stack in the obligation tree -- not from node identity, which
//     is exactly the ambiguity this path routes around.
//
// The RESPONSE object is unchanged: buildResponseObject/renderFields are already driven by the
// obligation tree, so this path reuses them verbatim; only the FETCH side differs from the old path.
//
// Scope (validated by test witnesses, honest about the rest): output-type aliasing is not re-derived
// here -- the witnesses carry no cross-subgraph output-type collision, so an empty alias map is exact
// for them; general aliasing stays on the old path pending the default flip. Field response paths are
// keyed on the field name (no alias), which the witnesses satisfy.
// Spec: FORMAL_SPEC D10/D11.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// obGroupKey identifies a fetch group by RESPONSE POSITION, not node identity: two positions of one
// entity type get distinct keys (the whole point). entryPath is the dotted response path of the
// group's entry object ("" for a root group); subgraph is the fetch target; jump is the entity jump
// opening the group (NoEdge for a root group).
type obGroupKey struct {
	entryPath string
	subgraph  hypergraph.SubgraphID
	jump      hypergraph.EdgeID
	// split separates fetch groups produced by an argument-conflict split. When two @requires fields on
	// one entity call the same field with different argument values, they must land in separate entity
	// fetches even though they share the same entryPath, subgraph, and jump. Zero for every ordinary
	// (unsplit) group, so the key is byte-identical to the pre-split key on nearly all paths.
	split int
	// deferID is the group's defer scope (FORMAL_SPEC D11.13): 0 for the primary (initial-response)
	// partition, a @defer id for a scope variant serving that fragment's fields. Zero on every path
	// of an undeferred operation, so the key is byte-identical to the pre-defer key there.
	deferID int
	// rootSeq is the serial index of the top-level MUTATION field a root group serves (document
	// order). The GraphQL spec mandates root mutation fields execute in series; v1 gives every
	// mutation root field its OWN fetch (even consecutive fields on one subgraph) and chains each
	// on all previous ones -- this key component reproduces the per-field split, and the chain is
	// added after emitFields. Zero for every query/subscription group and every non-root group, so
	// the key is byte-identical outside mutation roots.
	rootSeq int
}

// obGroup is one fetch in the obligation-driven path.
type obGroup struct {
	key       obGroupKey
	entryType string            // entity type at the entry (for `... on Type`); "" for a root group
	jumpEdge  hypergraph.EdgeID // the opening entity jump; NoEdge for a root group
	dependsOn map[int]bool      // indices of the groups that must run before this one
	sel       *docSel           // the group's selection tree (client fields + injected keys)
	hops      []attachHop       // root->leaf response hops to the entry object (where the fetch attaches)
	// srcType is the pre-jump entity type -- the type of the object the jump departs from in the SOURCE
	// subgraph, taken from the field's route. It differs from entryType on @interfaceObject and
	// entity-interface jumps: the jump lands on the interface (or a concrete member) as the TARGET
	// subgraph models it, but the key selection injected into the PARENT document must be typed on what
	// the SOURCE subgraph declares (e.g. a flat `id` on an @interfaceObject `Product`, never
	// `... on Bread { id }` -- `Bread` does not exist there). "" when it can't be derived (falls back to
	// entryType, the older behavior).
	srcType string
	// --- @requires argument-conflict split ---
	// isSplit marks a group produced (or narrowed) by splitArgConflictGroups. When set, reqStrings is
	// the authoritative @requires for this group (even when empty) instead of the jump edge's full
	// bundle; an unsplit group keeps isSplit=false and reads the edge's Requires verbatim.
	isSplit bool
	// reqStrings is this group's subset of the @requires selection set (the requires of the requiring
	// fields the conflict split assigned to it). Only consulted when isSplit.
	reqStrings []string
	// reqReadAlias maps a dotted requires-field path ("price", "category.averagePrice") to the response
	// key the parent (source) document aliased that selection to, when the same field called with
	// different argument values forced an alias. buildFetches threads it onto the representation so the
	// entity fetch reads the correct argument-variant value while still presenting the field by its real
	// name. nil when this group's requires needed no aliasing.
	reqReadAlias map[string]string
	// pipeDeps marks the dependsOn entries added by the D11.10 requires-input pipeline (consumer ->
	// input-producing group). They count for topo ordering like any dependency, but are EXCLUDED from
	// the parent (source-group) derivation injectKeys and the pipeline itself use -- the source of a
	// jump is its spine predecessor, never an input-gathering branch.
	pipeDeps map[int]bool
	// keyViaPipeline marks a group whose @key selection is placed by the D11.11 key-input pipeline
	// instead of injectKeys: a branch group minted as a gather producer under a distributed-key
	// placement (its source objects may live BELOW its entry, so the base entryTarget navigation is
	// undefined for it). A D7ppp distributed-key jump group needs no flag -- Edge.KeyDistributed gates
	// it. buildFetches renders a pipeline group's Key fragment from Edge.KeySelection.
	keyViaPipeline bool
	// base is the index of this group's scope-0 base group (D11.13): itself for every base group,
	// the cloned-from group for a defer scope variant. Consulted only during emitFields (variant
	// resolution happens before pruning/topo reindex, so the index is stable there).
	base int
	// synthetic marks a group minted by the C-disc DISCRIMINATOR SYNTHESIS (no client field routed
	// it -- it exists purely to fetch the concrete __typename through an entity interface). Its @key
	// paths come from Edge.KeySelection: no goal walk covered its tails, so the tail-up-walk key
	// recovery is undefined for it.
	synthetic bool
}

// usesKeyPipeline reports whether group g's @key is realized by the D11.11 key-input pipeline
// (a D7ppp distributed-key jump, or a branch group minted under one) -- the gate that keeps every
// single-source jump on the byte-identical base injectKeys/tailFieldPath path.
func (gb *obGroupBuilder) usesKeyPipeline(h *hypergraph.Hypergraph, g *obGroup) bool {
	if g.jumpEdge == hypergraph.NoEdge {
		return false
	}
	return g.keyViaPipeline || h.Edge(g.jumpEdge).KeyDistributed
}

// sourceParent returns the group's spine predecessor: the smallest dependsOn index that is not a
// pipeline-added input dependency (the pre-D11.10 min-dependsOn rule, unchanged for every group the
// pipeline leaves alone). -1 when the group has no producer (a root group).
func (g *obGroup) sourceParent() int {
	parent := -1
	for p := range g.dependsOn {
		if g.pipeDeps[p] {
			continue
		}
		if parent == -1 || p < parent {
			parent = p
		}
	}
	return parent
}

// requiresOf returns the @requires selection strings a group renders: its conflict-split subset when
// split, else the jump edge's full bundle (the behavior for every ordinary jump group).
func (g *obGroup) requiresOf(h *hypergraph.Hypergraph) []string {
	if g.isSplit {
		return g.reqStrings
	}
	if g.jumpEdge == hypergraph.NoEdge {
		return nil
	}
	return h.Edge(g.jumpEdge).Requires
}

// docSel is a subgraph selection set as a tree of fields plus abstract-member inline fragments and an
// optional __typename. It is printed by response-position recursion (never node identity), so a
// self-referential shape terminates on the finite obligation tree it is built from.
type docSel struct {
	typename bool
	// typenameWeak requests __typename only if the selection actually carries content (fields, member
	// fragments, or injected keys) after pruning. The abstract-member discriminator mark is weak
	// (D11.7): under member-qualified placement a position's members can ALL re-root into other
	// groups, and a strong mark would leave an invalid `field { __typename }` residue in a document
	// whose subgraph may not even declare the field; each group that materializes the position marks
	// its discriminator strongly itself.
	typenameWeak bool
	fields       []*docField
	byName       map[string]*docField
	frags        map[string]*docSel // member type -> its selection
	fragOrder    []string
}

type docField struct {
	name    string
	alias   string   // fetch-side alias (printed `alias: name`); "" = no alias
	args    string   // rendered argument body without parens ("id: $a"); "" = no args
	argVars []string // operation variable names referenced by args (for the fetch's variable set)
	sub     *docSel  // nil for a scalar leaf
}

func newDocSel() *docSel {
	return &docSel{byName: map[string]*docField{}, frags: map[string]*docSel{}}
}

// field returns (creating if absent) the child field `name`, marking it composite when `composite`.
func (d *docSel) field(name string, composite bool) *docField {
	return d.clientField(name, name, composite)
}

// clientField returns (creating if absent) the child selection with response key `key` for schema
// field `name`, printing `key: name` when they differ (a client alias). Entries are keyed by the
// RESPONSE key, so two aliases of one field (`book: similar(...)`, `magazine: similar(...)`) stay
// distinct selections with their own arguments and sub-selections; an un-aliased field (key == name)
// keys and prints exactly as before.
func (d *docSel) clientField(key, name string, composite bool) *docField {
	if f, ok := d.byName[key]; ok {
		if composite && f.sub == nil {
			f.sub = newDocSel()
		}
		return f
	}
	f := &docField{name: name}
	if key != name {
		f.alias = key
	}
	if composite {
		f.sub = newDocSel()
	}
	d.byName[key] = f
	d.fields = append(d.fields, f)
	return f
}

// frag returns (creating if absent) the inline-fragment selection for member type `t`.
func (d *docSel) frag(t string) *docSel {
	if s, ok := d.frags[t]; ok {
		return s
	}
	s := newDocSel()
	d.frags[t] = s
	d.fragOrder = append(d.fragOrder, t)
	return s
}

// print renders the selection set body (no enclosing braces), deterministically: __typename first,
// then fields in insertion order, then member fragments in sorted type order. A weak __typename
// (typenameWeak) prints only when some other content survived pruning -- see docSel.typenameWeak.
func (d *docSel) print() string {
	var parts []string
	for _, f := range d.fields {
		// Fetch-side alias: an aliased selection prints `alias: name` so two selections with the same key
		// but conflicting output types no longer overlap in the subgraph document (which graphql-js
		// rejects as un-mergeable fields). The response side reads the value back at the alias
		// (buildResponseObject).
		label := f.name
		if f.alias != "" {
			label = f.alias + ": " + f.name
		}
		// Render the field's argument list. After normalization every argument value is a variable
		// reference ($a), so the printed body carries no string literals to JSON-escape here -- the
		// escaping happens at input assembly (buildFetches), which JSON-encodes the whole document.
		if f.args != "" {
			label += "(" + f.args + ")"
		}
		if f.sub == nil {
			parts = append(parts, label)
			continue
		}
		// A composite field whose sub-selection is empty (its only client children were resolved in a
		// different fetch group -- a jump re-rooted them) must NOT print `field { }`: an empty selection
		// set is an invalid subgraph document. Prune it.
		inner := f.sub.print()
		if inner == "" {
			continue
		}
		parts = append(parts, label+" { "+inner+" }")
	}
	frags := append([]string(nil), d.fragOrder...)
	sort.Strings(frags)
	for _, t := range frags {
		// Same for an abstract-member fragment whose members were all resolved elsewhere: skip an empty
		// `... on T { }` rather than emit an invalid empty fragment.
		inner := d.frags[t].print()
		if inner == "" {
			continue
		}
		parts = append(parts, "... on "+t+" { "+inner+" }")
	}
	if d.typename || (d.typenameWeak && len(parts) > 0) {
		parts = append([]string{"__typename"}, parts...)
	}
	return joinNonEmpty(parts)
}

// lowerObligationDriven is the entry point of this path for synchronous (query/mutation) plans. It
// wraps lowerObligationDrivenParts -- which builds per-position fetch groups from the obligation tree
// and the per-field routes, prints their documents by walking response positions, and emits one fetch
// per group -- in the SynchronousResponsePlan output contract. Subscription plans reuse the same parts
// through lowerSubscription (subscription.go), which splits the root fetch into the trigger (D11.12).
func lowerObligationDriven(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable, info InfoConfig) (*plan.SynchronousResponsePlan, error) {

	data, raw, _, err := lowerObligationDrivenParts(h, o, res, operation, definition, transport, info)
	if err != nil {
		return nil, err
	}
	return &plan.SynchronousResponsePlan{
		Response: &resolve.GraphQLResponse{
			Data:       data,
			RawFetches: raw,
			Info:       &resolve.GraphQLResponseInfo{OperationType: operationResponseType(operation)},
		},
	}, nil
}

// lowerObligationDrivenParts is the shared body of the obligation-driven path: it returns the
// response object tree, the fetch items in dependency order, and the indices (into the returned
// items) of the ROOT fetch groups -- the groups that select off the operation root rather than an
// entity jump. Query/mutation lowering wraps the parts unchanged; subscription lowering (D11.12)
// consumes the root index to split the trigger out of the fetch list.
func lowerObligationDrivenParts(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable, info InfoConfig) (*resolve.Object, []*resolve.FetchItem, []int, error) {

	obs := o.Obligations()
	children, roots := obligationChildren(obs)

	// Argument lowering reads two facts off the raw operation: the variable TYPE map (name -> printed
	// GraphQL type, e.g. "ID!") for declaring `query($v: T)` headers on fetch documents that forward
	// client arguments, and the operation TYPE (which sets the root fetch keyword: `mutation` for a
	// mutation, `subscription` for a subscription trigger document, `query` otherwise).
	varTypes := operationVariableTypes(operation)
	opType := operationResponseType(operation)

	// producedByAll unions EVERY requested field's route -- a superset of the deduplicated Cover.Edges
	// that recovers the routes the dedup dropped (e.g. `seller`). It is used ONLY to recover the field
	// paths of jump key tails (tailFieldPath walks up from a unique key node), never for response
	// placement.
	producedByAll := map[hypergraph.NodeID]hypergraph.EdgeID{}
	for _, g := range o.Goals() {
		for _, e := range res.Cover.Walks[g] {
			producedByAll[h.Edge(e).Head] = e
		}
	}

	gb := &obGroupBuilder{
		h:          h,
		byKey:      map[obGroupKey]int{},
		posGroup:   map[string]int{},
		posScope:   map[string]int{},
		obField:    map[obligation.ObID]*docField{},
		obSubgraph: map[obligation.ObID]hypergraph.SubgraphID{},
		info:       info,
	}

	// Serial root mutation execution (GraphQL spec Section 6.2.2; v1 path_builder parity): every top-level
	// MUTATION field gets its own serial index in document order. walkSpine keys each root group on
	// its top-level field's index (one fetch per mutation root field, even two consecutive fields on
	// one subgraph), and the chain below emitFields makes each depend on all previous ones.
	if opType == ast.OperationTypeMutation {
		gb.mutationRootSeq = map[string]int{}
		for _, r := range roots {
			if obs[r].Kind == obligation.Field {
				gb.mutationRootSeq[obs[r].RespKey] = len(gb.mutationRootSeq)
			}
		}
	}

	// Flattened member goals (D3io/D11.9): a member-refined field covered by an INTERFACE-typed node
	// (the covering subgraph is an @interfaceObject / entity-interface that serves the field for all
	// implementers) must print at the interface level of its document -- the member fragment would name
	// a type the covering subgraph does not declare. The covering node's type is the intrinsic signal:
	// it differs from the obligation's owner type and is abstract in the composed schema (a D3ppp member
	// candidate is the opposite shape -- abstract owner covered by a CONCRETE member node -- and is
	// excluded by the abstractness test).
	gb.flattenedOb = map[obligation.ObID]bool{}
	for _, g := range o.Goals() {
		v, covered := res.Cover.Selected[g]
		if !covered {
			continue
		}
		ob := o.Ob(g)
		if ob.Kind != obligation.Field {
			continue
		}
		if nt := h.Node(v).Type; nt != ob.Type && isAbstractType(definition, nt) {
			gb.flattenedOb[ob.ID] = true
		}
	}

	// Covered COMPOSITE goals are the typename-terminal shapes (D3p typename-terminal, D3pp
	// exempt-terminal promotion, D6pp dead-member composites): the composite must be resolved for its
	// __typename even when nothing under it is live, so emitFields prints `{ __typename }` at its
	// position (D11.7 discriminator rule). Without this, a composite whose whole subtree is exempt
	// printed an empty selection and was pruned -- no fetch selected it at all.
	gb.coveredComposite = map[obligation.ObID]bool{}
	for _, g := range o.Goals() {
		if _, ok := res.Cover.Selected[g]; !ok {
			continue
		}
		if ob := o.Ob(g); ob.Kind == obligation.Field && len(children[ob.ID]) > 0 {
			gb.coveredComposite[ob.ID] = true
		}
	}

	// Phase A: walk each field's route to create groups and attribute each field's response path to the
	// group that resolves it (posGroup). Well-formed routes (buyer/seller, entity jumps) place fields at
	// their true positions in the obligation tree; a collapsed route (self-referential friends, no jump)
	// writes phantom paths the obligation tree never visits, so the affected positions fall through to
	// parent-group inheritance in phase B -- which is correct, because a no-jump shape is one
	// single-subgraph group anyway.
	for _, g := range o.Goals() {
		v, covered := res.Cover.Selected[g]
		if !covered {
			continue
		}
		gb.walkSpine(res.Cover.Walks[g], res.Cover.Spines[g], v, o, g, definition)
	}

	// Phase B: build documents by walking the obligation tree. Each field is emitted into its resolving
	// group (from posGroup, defaulting to the parent's group when a position was never attributed in
	// phase A); a field whose group differs from its parent's opens a jump group (opened in phase A) and
	// the parent group receives the jump's @key/@requires fields.
	live := liveObligations(children, roots, res, o)
	// Top-level selections are fields on the operation root; each is attributed to its own root group
	// (multiple subgraphs -> multiple root groups). parentGroup = -1 forces every root field into its
	// resolving group's top-level selection via posGroup (never the unused scratch sel).
	scratch := newDocSel()
	gb.emitFields(obs, children, live, roots, "", nil, -1, -1, 0, scratch, scratch)

	// Serial root mutation chain (v1 parity: "each next mutation root field planner depends on all
	// previous mutation root field planners"): every mutation root group depends on every root group
	// with a smaller serial index. Queries/subscriptions have no mutationRootSeq, so this is a no-op
	// for them (root groups stay independent, byte-identical output).
	if gb.mutationRootSeq != nil {
		for gi, g := range gb.groups {
			if g.jumpEdge != hypergraph.NoEdge {
				continue
			}
			for pj, p := range gb.groups {
				if pj != gi && p.jumpEdge == hypergraph.NoEdge && p.key.rootSeq < g.key.rootSeq {
					g.dependsOn[pj] = true
				}
			}
		}
	}

	// C-disc discriminator synthesis (see synthesizeDiscriminators): positions observed for their
	// concrete runtime type whose data an @interfaceObject subgraph serves get the entity-interface
	// __typename fetch. Before the pipelines/injectKeys so the new group's key is injected too.
	gb.synthesizeDiscriminators(h, obs, definition)

	// @requires argument-conflict SPLIT. Two @requires on one entity type that select the SAME field
	// with DIFFERENT argument values (`shippingEstimate` requiring `price(currency: "USD")` vs
	// `shippingEstimateEUR` requiring `price(currency: "EUR")`) cannot be served by ONE entity
	// representation -- a single `price` value cannot be both. splitArgConflictGroups partitions the
	// requiring fields into separate entity fetches so each group's representation carries a consistent
	// argument binding; injectKeys/buildFetches then alias the conflicting selections in the shared
	// source document and read each representation's value from its aliased key. Runs BEFORE injectKeys
	// so the split groups get their keys/requires injected.
	gb.splitArgConflictGroups(h)

	// D11.10 requires-input pipeline: place each jump group's @requires selection into the group that
	// PRODUCES each coordinate (per the search's tail routes), materializing branch fetch groups for
	// inputs the source subgraph does not resolve, with dependency edges. Byte-identical to the old
	// parent-document rendering when every coordinate is source-local. Runs before injectKeys so the
	// branch groups get their own keys injected. An unattributable gather (a cyclic producer map --
	// the self-nested-entity invariant) fails the plan loudly rather than truncating.
	if err := gb.materializeRequiresPipelines(h, producedByAll, definition); err != nil {
		return nil, nil, nil, err
	}

	// Inject each jump group's @key selection into its parent group's document at the jump's
	// entry position (these keys are what the entity fetch's representation is built from -- they are NOT
	// client-requested fields, so the obligation tree does not carry them). Uses producedByAll to
	// recover tail field paths.
	gb.injectKeys(h, producedByAll, definition)

	// Scoped/unscoped twin CO-LOCATION (the registered D11.10 quality residual, made load-bearing
	// by the audit's consume-on-reference subgraphs): a requires-scoped jump and a plain jump into
	// the same subgraph at one position merge into ONE entity fetch -- v1 sends a single _entities
	// call whose representation carries the key AND the @requires values; two calls make a
	// side-effecting reference resolver observable (mutations_0/_1: the second fetch finds the
	// entity consumed). Runs after the pipelines and key injection so both documents are complete.
	gb.mergeScopedTwins(h)

	// Aliasing. The old path keys its collision domain on the cover topology, which reflects that path's
	// routing: members it splits into separate fetches never collide. This path instead merges into ONE
	// subgraph document members that the old path would route to separate entity fetches (for example
	// `... on Admin { id }` and `... on User { id }`). The conflict therefore lives in the ACTUAL printed
	// document, not the cover, so the domain is computed over the docSel trees. graphql-js's
	// "fields must have the same response shape" rule is decided on the SUBGRAPH-local output types
	// (Edge.OutputType), which is the only place the `ID!` vs `ID` difference survives -- the composed
	// client schema erases it to a uniform `ID`. obOutputTy maps each covered field to its covering Field
	// edge's subgraph output type.
	obOutputTy := map[obligation.ObID]string{}
	for _, g := range o.Goals() {
		v, covered := res.Cover.Selected[g]
		if !covered {
			continue
		}
		if e := coveringFieldEdge(h, res.Cover, v); e != hypergraph.NoEdge {
			obOutputTy[o.Ob(g).ID] = h.Edge(e).OutputType
		}
	}
	aliases := gb.assignDocAliases(obs, obOutputTy)

	// Prune groups whose printed document body is empty: a fetch with an empty selection set (`query { }`
	// or `_entities { ... on T { } }`) is an invalid GraphQL document. A group is empty when every client
	// field it would have resolved was re-rooted into a deeper group and no key/@requires landed in it --
	// e.g. a spurious secondary root group for a subgraph the search entered but placed no field in
	// (shared-root: three root groups, two empty). Dropping it removes the invalid fetch; dependents lose
	// the (nonexistent) dependency, which is correct since it produced nothing.
	gb.pruneEmptyGroups()

	groups := gb.topoOrder()

	// M4.1 Info emission (info.go): the per-obligation FieldInfo map renderFields stamps onto the
	// response tree. nil under the zero InfoConfig -- byte-identical pre-M4.1 output.
	var em *infoEmission
	if info.IncludeInfo {
		em = &infoEmission{infos: gb.buildFieldInfos(h, obs, definition, transport)}
	}

	data := buildResponseObject(h, o, aliases, definition, em)
	raw, err := gb.buildFetches(h, groups, producedByAll, definition, varTypes, opType, transport)
	if err != nil {
		return nil, nil, nil, err
	}

	// Root fetch groups, by item index (items and groups share indexing). Exactly one for a valid
	// subscription (D11.12 single-root precondition); possibly several for a multi-subgraph query root.
	var rootItems []int
	for gi, g := range groups {
		if g.jumpEdge == hypergraph.NoEdge {
			rootItems = append(rootItems, gi)
		}
	}
	return data, raw, rootItems, nil
}

// obGroupBuilder accumulates groups and the response-path->group attribution.
type obGroupBuilder struct {
	h        *hypergraph.Hypergraph
	groups   []*obGroup
	byKey    map[obGroupKey]int
	posGroup map[string]int                // field response path -> resolving group index
	posScope map[string]int                // field response path -> defer scope (D11.13; 0/absent = primary)
	newIndex []int                         // pre-topo group index -> post-topo position (set by topoOrder)
	obField  map[obligation.ObID]*docField // Field obligation -> its docField (for late alias application)
	// coveredComposite marks the covered typename-terminal composite goals (D3p/D3pp/D6pp): their
	// selection must print `{ __typename }` even when no child is live (see lowerObligationDriven).
	coveredComposite map[obligation.ObID]bool
	// flattenedOb marks the member-refined field obligations covered by an INTERFACE-typed node
	// (D3io): D11.9 prints them at the interface level of their document -- outside the `... on C`
	// member fragments -- because the covering subgraph does not declare the member type.
	flattenedOb map[obligation.ObID]bool
	// mutationRootSeq maps a top-level MUTATION field's response key to its serial index (document
	// order) -- the per-field root-group split for serial mutation execution. nil for queries and
	// subscriptions (rootSeq stays 0 everywhere: byte-identical keys).
	mutationRootSeq map[string]int
	// discCandidates records, during emitFields, every composite position whose client selection
	// OBSERVES the concrete runtime type (__typename output or member gates) -- the C-disc
	// discriminator-synthesis candidates (see synthesizeDiscriminators).
	discCandidates []discCandidate
	// obSubgraph records, during emitFields, the subgraph whose group each Field/Typename
	// obligation was emitted into -- the FieldInfo Source attribution (info.go). Recorded as the
	// subgraph (not the group index) because group indices are remapped by pruning/topo-ordering.
	obSubgraph map[obligation.ObID]hypergraph.SubgraphID
	// info is the M4.1 Info-emission configuration (FieldInfo/RootFields); the zero value emits
	// nothing (see InfoConfig).
	info InfoConfig
}

// discCandidate is one concrete-type-observing composite position: its member-blind response path,
// the group that resolves the position's object, and the composite Field obligation.
type discCandidate struct {
	path string
	gi   int
	ob   obligation.ObID
}

// rootSeqOf returns the serial root-group index for a goal whose top-level response key is topKey:
// the mutation field's document-order index, or 0 outside mutations (nil map).
func (gb *obGroupBuilder) rootSeqOf(topKey string) int {
	if gb.mutationRootSeq == nil {
		return 0
	}
	return gb.mutationRootSeq[topKey]
}

func (gb *obGroupBuilder) group(key obGroupKey) int {
	if gi, ok := gb.byKey[key]; ok {
		return gi
	}
	gi := len(gb.groups)
	gb.groups = append(gb.groups, &obGroup{key: key, jumpEdge: key.jump, dependsOn: map[int]bool{}, sel: newDocSel(), base: gi})
	gb.byKey[key] = gi
	return gi
}

// variant returns (creating if absent) the defer scope-d variant of group g (D11.13): the group
// with g's base key re-keyed to scope d -- same entry position, subgraph, and jump edge, its own
// fetch document. Scope 0 is the base itself, so undeferred paths are untouched. A jump variant's
// dependency is its base's spine predecessor taken in the KEY scope -- the scope of the entry
// position's enclosing field (usually 0): the @key must be fetched before the deferred entity
// fetch runs, so it rides the parent scope's document (FS-DEF-6's worked example, v1's
// "@key fields use the parent defer ID"). A root variant re-walks from the operation root and
// depends on nothing (v1's defer-parent re-walk).
func (gb *obGroupBuilder) variant(g int, d int) int {
	base := gb.groups[g].base
	if d == gb.groups[g].key.deferID {
		return g // already in scope d
	}
	if d == 0 {
		return base
	}
	b := gb.groups[base]
	key := b.key
	key.deferID = d
	if vi, ok := gb.byKey[key]; ok {
		return vi
	}
	vi := len(gb.groups)
	v := &obGroup{
		key:       key,
		entryType: b.entryType,
		jumpEdge:  b.jumpEdge,
		dependsOn: map[int]bool{},
		sel:       newDocSel(),
		hops:      b.hops,
		srcType:   b.srcType,
		base:      base,
	}
	gb.groups = append(gb.groups, v)
	gb.byKey[key] = vi
	if b.jumpEdge != hypergraph.NoEdge {
		if parent := b.sourceParent(); parent != -1 {
			keyScope := gb.posScope[b.key.entryPath]
			v.dependsOn[gb.variant(parent, keyScope)] = true
		}
	}
	return vi
}

// chainSeg is one response-path level of a goal's obligation chain (root->leaf), Field obligations
// only (Refine/Typename ancestors carry no response segment). respKey is the client response-path
// segment; field is the schema field name (for list-nesting lookup, which differs from respKey under an
// alias); enclosingType is the type the field is selected on. gates are the concrete types of the
// Refine obligations crossed between this field and its PARENT field (outermost first) -- the
// `... on Member` context the field sits under, used by the member-qualified position key (D11.7)
// and never by the wire paths (segPath/attach hops stay member-blind).
type chainSeg struct {
	respKey       string
	field         string
	enclosingType string
	gates         []string
}

// goalChain returns goal g's obligation chain as response-path segments, root->leaf. Bounded by the
// size of the obligation tree, so a self-referential shape terminates by construction (the obligation
// tree is finite even where the hypergraph is cyclic). Refine obligations contribute no segment of
// their own; each attaches its gate to the Field segment BELOW it (walking up, gates are seen
// inner->outer after their field, so they are prepended to keep outermost-first order).
func goalChain(o *obligation.Tree, g obligation.GoalID) []chainSeg {
	return obligationChainFrom(o.Obligations(), o.Ob(g))
}

// obligationChainFrom is goalChain starting from an arbitrary obligation (the discriminator
// synthesis derives a POSITION's chain from its composite Field obligation, which is not a goal).
func obligationChainFrom(obs []obligation.Obligation, ob obligation.Obligation) []chainSeg {
	var rev []chainSeg
	for guard := 0; guard < 1<<16; guard++ {
		switch ob.Kind {
		case obligation.Field:
			rev = append(rev, chainSeg{respKey: ob.RespKey, field: ob.Field, enclosingType: ob.Type})
		case obligation.Refine:
			// A gate above the most recently appended (deeper) field segment. A Refine with no field
			// beneath it in the chain (the goal itself is a Refine -- defensive) has no segment to gate.
			if ob.Concrete != "" && len(rev) > 0 {
				last := &rev[len(rev)-1]
				last.gates = append([]string{ob.Concrete}, last.gates...)
			}
		}
		if ob.Parent == obligation.NoParent || ob.Parent == ob.ID || int(ob.Parent) >= len(obs) {
			break
		}
		ob = obs[ob.Parent]
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// segPath joins the first n response-key segments into a dotted response path (member-blind: the
// wire vocabulary -- ResponsePath, FetchPath, attach hops).
func segPath(segs []chainSeg, n int) string {
	p := ""
	for i := 0; i < n && i < len(segs); i++ {
		p = joinPath(p, segs[i].respKey)
	}
	return p
}

// memberMarkerPrefix marks a member-qualification segment in a position key ("~Sale"). "~" is not a
// valid GraphQL name character, so a marker can never collide with a response key.
const memberMarkerPrefix = "~"

// posKey joins the first n segments into the MEMBER-QUALIFIED position key (D11.7): each segment's
// Refine gates print as marker segments before its response key ("me.history.~Sale.product"), so
// distinct members of one abstract position are distinct positions for grouping/attribution. Equal
// to segPath wherever the chain crosses no refinement.
func posKey(segs []chainSeg, n int) string {
	p := ""
	for i := 0; i < n && i < len(segs); i++ {
		for _, gate := range segs[i].gates {
			p = joinPath(p, memberMarkerPrefix+gate)
		}
		p = joinPath(p, segs[i].respKey)
	}
	return p
}

// attachHopsFromSegs builds the root->leaf attach hops to the object at depth n (the first n segments).
// The top-level field (i==0) is selected on the operation root, which carries no __typename gate in the
// fetch attach path -- matching how the old path treats the root object (Type ""). Its LIST-ness,
// however, is still detected from the real root operation type: a top-level list field
// (`Query.topProducts: [Product]`) makes an entity fetch under it a BATCH fetch that must carry an `@`
// array marker, so array detection reads the ACTUAL enclosing type even at i==0 while the __typename
// gate stays suppressed.
func attachHopsFromSegs(segs []chainSeg, n int, def *ast.Document) []attachHop {
	var hops []attachHop
	for i := 0; i < n && i < len(segs); i++ {
		encActual := segs[i].enclosingType // real enclosing type (root op type at i==0)
		enc := encActual
		if i == 0 {
			enc = "" // suppress the __typename gate on the root hop (but not list detection)
		}
		hops = append(hops, attachHop{
			field:         segs[i].respKey,
			enclosingType: enc,
			array:         encActual != "" && listNesting(def, encActual, segs[i].field) > 0,
		})
	}
	return hops
}

// walkSpine attributes goal g's response positions to fetch groups, keyed by POSITION IN THE OBLIGATION
// TREE (its hop stack) rather than by hypergraph-node identity. The STRUCTURE -- the response path of
// every position, its depth, and the client field/response keys -- comes from g's obligation chain,
// which is unambiguous even for self-referential or repeated response positions. The field's route is
// consulted ONLY to locate the subgraph transitions (the entity jumps) and how deep each sits relative
// to the LEAF (the end that never collapses): a jump N response-object nestings above the leaf lands at
// obligation depth (D-1-N), where D is the chain length.
//
// Anchoring each jump's depth to D and counting Descents ONLY in the collapse-free leaf suffix keeps a
// self-referential shape correct -- the node-keyed producedByWalk map collapses every repeated ancestor
// onto one node, but the suffix from the leaf up to and including each jump never collapses, and that
// suffix is all the depth derivation reads. Two corrections over the naive up-walk make multi-jump
// attribution correct: (1) a jump's spine parent is followed through its @KEY tail (never a
// @requires/@override tail, which may live in a foreign subgraph and would derail the walk off the
// response spine); (2) several jumps landing on ONE response position -- an @override->@requires provider
// relay (a->b->c) or a null-key upc->id->... relay -- are ordered root->leaf into a real producer CHAIN
// (each jump depending on the previous) rather than the arbitrary order a non-stable sort produced,
// which reversed the dependency and mis-injected the keys.
func (gb *obGroupBuilder) walkSpine(walk, spine []hypergraph.EdgeID, leaf hypergraph.NodeID,
	o *obligation.Tree, g obligation.GoalID, def *ast.Document) {

	h := gb.h
	segs := goalChain(o, g)
	D := len(segs)
	if D == 0 {
		return
	}
	if len(spine) > 0 {
		// The goal was repaired by the CHAIN-LAYERED consistent trace (Cover.Spines): the search
		// recorded the ordered root->leaf spine, which carries exactly the depth information the
		// head-keyed walk map loses when the chain revisits a (type, subgraph) node (an expanded
		// member position of a repeated abstract type). Derive the jump chain from it directly.
		gb.walkSpineFromSpine(spine, o, g, segs, def)
		return
	}

	// Reconstruct the goal's spine leaf->root from the walk. The map is node-keyed, so a repeated
	// ancestor collapses ABOVE the deepest jump -- but the suffix from the leaf up to and including each
	// jump never collapses, and that is all we read: jump presence and the Descent count between a jump
	// and the leaf. A jump's spine parent is reached through its @KEY tail's Field edge (the pre-jump
	// entity object in the SOURCE subgraph), never Tails[0] -- a @requires/@override tail can sit in a
	// foreign subgraph and would send the up-walk off the response spine.
	producedByWalk := map[hypergraph.NodeID]hypergraph.EdgeID{}
	for _, e := range walk {
		producedByWalk[h.Edge(e).Head] = e
	}
	srcTypeOf := map[hypergraph.EdgeID]string{} // jump edge -> pre-jump entity type (this walk)
	var up []hypergraph.EdgeID
	cur := leaf
	for guard := 0; guard < 1<<16; guard++ {
		e, ok := producedByWalk[cur]
		if !ok {
			break
		}
		edge := h.Edge(e)
		up = append(up, e)
		if edge.Kind == hypergraph.EdgeEntityJump {
			if pre, ok := jumpSpineParent(h, producedByWalk, edge); ok {
				srcTypeOf[e] = h.Node(pre).Type
				cur = pre
				continue
			}
		}
		if len(edge.Tails) == 0 {
			break
		}
		cur = edge.Tails[0]
	}

	rootSg := hypergraph.SubgraphID(0)
	if len(up) > 0 {
		rootSg = h.Node(h.Edge(up[len(up)-1]).Head).Subgraph
	}
	rootGroup := gb.group(obGroupKey{entryPath: "", subgraph: rootSg, jump: hypergraph.NoEdge,
		rootSeq: gb.rootSeqOf(segs[0].respKey)})

	// pathConsistent: the spine's Field edges reproduce the obligation chain exactly (count and field
	// names, root->leaf). A FALLBACK walk (a goal whose masked route was unreachable -- a genuine model
	// gap) enters through a FOREIGN root field (`userById` for a `randomUser` goal) or otherwise diverges
	// from the obligation chain; its positions are phantom and must NOT pin deep root-group attributions
	// (the leaf falls through to parent-group inheritance).
	pathConsistent := true
	fieldCount := 0
	for i := len(up) - 1; i >= 0; i-- {
		edge := h.Edge(up[i])
		if edge.Kind != hypergraph.EdgeField {
			continue
		}
		if fieldCount >= D || edge.Label != segs[fieldCount].field {
			pathConsistent = false
			break
		}
		fieldCount++
	}
	if fieldCount != D {
		pathConsistent = false
	}

	// Member leaves are recorded like any other leaf since D11.7: the position keys below are
	// MEMBER-QUALIFIED (posKey interleaves the `... on C` gates), so member goals of distinct
	// concrete types no longer share one key and cannot steal each other's positions, and the
	// ancestry materialization reproduces the member-fragment wrappers (emitFields).

	// Collect jumps with the response depth of the object each lands on. `descents` counts response
	// nestings seen BELOW the current point (leaf->root order); a jump's entry object is at obligation
	// depth D-1-descents (drop the leaf segment plus one segment per nesting below the jump).
	type jumpAt struct {
		edge       hypergraph.EdgeID
		entryDepth int
	}
	var jumps []jumpAt
	descents := 0
	for _, e := range up {
		edge := h.Edge(e)
		switch edge.Kind {
		case hypergraph.EdgeDescent:
			descents++
		case hypergraph.EdgeEntityJump:
			d := D - 1 - descents
			if d < 0 {
				d = 0
			}
			jumps = append(jumps, jumpAt{edge: e, entryDepth: d})
		}
	}
	// Order root->leaf: reverse the leaf->root collection (so a same-depth jump chain is in producer
	// order a->b->c) then STABLE-sort by entry depth. Along a linear spine root->leaf, entry depth is
	// non-decreasing, so the stable sort only interleaves distinct-depth jumps and preserves the
	// within-depth chain order the reverse established -- the fix for the non-stable sort.Slice that
	// scrambled same-depth jumps and reversed their producer dependency.
	for i, j := 0, len(jumps)-1; i < j; i, j = i+1, j-1 {
		jumps[i], jumps[j] = jumps[j], jumps[i]
	}
	sort.SliceStable(jumps, func(i, j int) bool { return jumps[i].entryDepth < jumps[j].entryDepth })

	type grpInfo struct {
		gi         int
		entryDepth int
	}
	var chain []grpInfo
	prev := rootGroup
	for _, j := range jumps {
		head := h.Node(h.Edge(j.edge).Head)
		gi := gb.group(obGroupKey{entryPath: posKey(segs, j.entryDepth), subgraph: head.Subgraph, jump: j.edge})
		grp := gb.groups[gi]
		grp.dependsOn[prev] = true
		grp.entryType = head.Type
		grp.jumpEdge = j.edge
		if st := srcTypeOf[j.edge]; st != "" {
			grp.srcType = st
		}
		if grp.hops == nil {
			grp.hops = attachHopsFromSegs(segs, j.entryDepth, def)
		}
		chain = append(chain, grpInfo{gi: gi, entryDepth: j.entryDepth})
		prev = gi
	}

	// Attribute each CLIENT field position to its resolving group: the deepest jump whose entry object
	// is at depth <= i, else the root group. For a same-depth jump chain the LAST such jump is the one
	// whose subgraph resolves the field at that position (the chain ends on the leaf field's subgraph).
	//
	//  - The TOP-LEVEL position (i==0) is always recorded: emitFields drives root fields off posGroup
	//    (it enters with parentGroup = -1, so a top-level field with no posGroup entry vanishes).
	//  - A deeper JUMP-group position is recorded (a jump re-roots that field into the entity fetch).
	//  - The goal's own LEAF position (i==D-1) is recorded -- including a root-group owner -- when the
	//    walk is PATH-CONSISTENT. The leaf is g's OWN response position; no sibling goal legitimately
	//    owns it, and g's walk names the one subgraph that resolves it. This is what places a
	//    distributed-root sibling (`product.name.*` via subgraph b's root while `product.price.*` enters
	//    via c's), a root-resolved leaf under a jump-owned parent (`category.id` from the root subgraph
	//    while `category.details` sits in the jump group), and a DISTRIBUTED-MEMBER leaf
	//    (`media { ... on Movie { title } }` resolvable only via subgraph b's shared root -- D11.7) in
	//    the subgraph that has the field. emitFields materializes the ancestor chain -- member fragment
	//    wrappers included -- in that root group's document. A FALLBACK (foreign-root / diverged) walk's
	//    leaf is NOT recorded -- its route does not exist under the client's path, so the leaf inherits
	//    its parent's group.
	//  - An INTERMEDIATE root-group position (0 < i < D-1) is NOT recorded: it must INHERIT its parent's
	//    group via emitFields. Recording it as the root group would OVERRIDE the attribution a sibling
	//    goal made (the "phantom path" fall-through this path was designed around).
	for i := 0; i < D; i++ {
		gi := rootGroup
		for _, ci := range chain {
			if ci.entryDepth <= i {
				gi = ci.gi
			}
		}
		if i > 0 && gi == rootGroup && (i < D-1 || !pathConsistent) {
			continue
		}
		gb.posGroup[posKey(segs, i+1)] = gi
	}
}

// walkSpineFromSpine is walkSpine for a goal repaired by the chain-layered consistent trace: the
// ordered spine's Field edges spell the obligation chain exactly (the search guarantees it), so the
// jump chain, each jump's entry depth (the count of chain fields consumed before it), its pre-jump
// source type (the previous spine edge's head), and the position attributions all read off the spine
// in order -- no up-walk, no producedBy ambiguity, pathConsistent by construction.
func (gb *obGroupBuilder) walkSpineFromSpine(spine []hypergraph.EdgeID,
	o *obligation.Tree, g obligation.GoalID, segs []chainSeg, def *ast.Document) {

	h := gb.h
	D := len(segs)
	rootSg := h.Node(h.Edge(spine[0]).Head).Subgraph
	rootGroup := gb.group(obGroupKey{entryPath: "", subgraph: rootSg, jump: hypergraph.NoEdge,
		rootSeq: gb.rootSeqOf(segs[0].respKey)})

	type grpInfo struct {
		gi         int
		entryDepth int
	}
	var chain []grpInfo
	prev := rootGroup
	fieldsConsumed := 0
	var prevHead hypergraph.NodeID
	havePrev := false
	for _, e := range spine {
		edge := h.Edge(e)
		switch edge.Kind {
		case hypergraph.EdgeField:
			fieldsConsumed++
		case hypergraph.EdgeEntityJump:
			head := h.Node(edge.Head)
			gi := gb.group(obGroupKey{entryPath: posKey(segs, fieldsConsumed), subgraph: head.Subgraph, jump: e})
			grp := gb.groups[gi]
			grp.dependsOn[prev] = true
			grp.entryType = head.Type
			grp.jumpEdge = e
			if havePrev {
				grp.srcType = h.Node(prevHead).Type // the pre-jump object the spine stepped from
			}
			if grp.hops == nil {
				grp.hops = attachHopsFromSegs(segs, fieldsConsumed, def)
			}
			chain = append(chain, grpInfo{gi: gi, entryDepth: fieldsConsumed})
			prev = gi
		}
		prevHead = edge.Head
		havePrev = true
	}

	// Position attribution: identical rules to walkSpine, with pathConsistent == true by construction.
	for i := 0; i < D; i++ {
		gi := rootGroup
		for _, ci := range chain {
			if ci.entryDepth <= i {
				gi = ci.gi
			}
		}
		if i > 0 && gi == rootGroup && i < D-1 {
			continue
		}
		gb.posGroup[posKey(segs, i+1)] = gi
	}
}

// jumpSpineParent locates a jump's PRE-JUMP entity object within a goal's walk: the object all the
// jump's @key tails hang off in the SOURCE subgraph. Only @key tails are considered (a @requires /
// @override tail may live in a foreign subgraph -- following it derails the up-walk off the response
// spine, the multi-jump collapse this fix removes). For a NESTED key (`id organization { id }`) the
// tails' enclosing objects differ (Product vs Organization); the pre-jump object is the one that is an
// ANCESTOR of every other tail's object along the walk (Product), so the choice is independent of the
// arbitrary node order of the key tails. ok=false when no tail has a resolvable chain in producedByWalk
// (a severed/collapsed walk) -- the caller falls back to the generic Tails[0] ascent.
func jumpSpineParent(h *hypergraph.Hypergraph, producedByWalk map[hypergraph.NodeID]hypergraph.EdgeID,
	edge hypergraph.Edge) (hypergraph.NodeID, bool) {

	tails := edge.KeyTails
	if len(tails) == 0 {
		tails = edge.Tails
	}
	// Each key tail is a Field node; its own Field edge's tail is its enclosing object.
	var objs []hypergraph.NodeID
	for _, t := range tails {
		e, ok := producedByWalk[t]
		if !ok || len(h.Edge(e).Tails) == 0 {
			continue
		}
		objs = append(objs, h.Edge(e).Tails[0])
	}
	if len(objs) == 0 {
		return 0, false
	}
	// ancestorsOf climbs the walk from n collecting every node passed (bounded; a self-loop repeat
	// terminates on the visited check). Membership of one candidate in another's ancestry decides
	// which enclosing object is the true (shallowest) pre-jump entity object.
	ancestorsOf := func(n hypergraph.NodeID) map[hypergraph.NodeID]bool {
		out := map[hypergraph.NodeID]bool{n: true}
		cur := n
		for guard := 0; guard < 1<<16; guard++ {
			e, ok := producedByWalk[cur]
			if !ok || len(h.Edge(e).Tails) == 0 {
				break
			}
			cur = h.Edge(e).Tails[0]
			if out[cur] {
				break
			}
			out[cur] = true
		}
		return out
	}
	ancs := make([]map[hypergraph.NodeID]bool, len(objs))
	for i, o := range objs {
		ancs[i] = ancestorsOf(o)
	}
	for _, o := range objs {
		common := true
		for j := range objs {
			if !ancs[j][o] {
				common = false
				break
			}
		}
		if common {
			return o, true
		}
	}
	return objs[0], true // defensive: disjoint chains -- keep the first tail's object
}

// ancestorSeg is one root->parent hop in the emitFields ancestry: the schema field name plus its
// rendered arguments (and referenced variables), so an ancestor field re-materialized in a sibling
// subgraph's root document reproduces its arguments. A member entry (member != "", field == "")
// records a `... on Member` refinement crossed on the way down: materialization reproduces it as an
// inline fragment and marks the abstract position's __typename (the discriminator the resolver
// gates the member's fields on) -- D11.7 fragment materialization.
type ancestorSeg struct {
	field   string
	respKey string // client response key; equals field unless aliased (prints `respKey: field`)
	args    string
	argVars []string
	member  string
}

// emitFields places a list of sibling obligations (at the response path `path` of their common parent,
// whose group is `parentGroup` and selection `parentSel`) into their resolving groups' documents,
// recursing per child with the child's own group so a jump re-roots the walk into the entity fetch's
// document. `ancestry` is the root->parent chain of SCHEMA field names to the current position: when a
// child resolves in a ROOT group different from its parent's group (a distributed-root sibling, or a
// root-resolved leaf under a jump-owned parent), the field cannot be dumped at that root document's top
// level -- the ancestor chain is materialized in it instead (`product { name { ... } }` in the subgraph
// that owns `name`), which the walk guarantees is selectable there (the goal's route entered through
// that subgraph's own root chain). Bounded by query depth: a self-referential shape terminates on the
// finite obligation tree.
// hostSel is the selection set of the nearest enclosing FIELD (identical to parentSel outside any
// refinement): the D11.9 flattened-placement target. A `Refine` narrows parentSel to its member
// fragment but leaves hostSel alone, so a flattened member goal -- covered by an interface-typed node
// whose subgraph does not declare the member -- prints as a direct selection of the interface-typed
// position instead of inside `... on C { ... }`.
//
// D11.13 defer partitioning: parentBase is the parent's scope-0 BASE group (position attribution
// and inheritance are scope-blind -- walkSpine knows nothing of defer), parentGroup its EFFECTIVE
// group (the scope variant its own scope routed it to), and parentScope its defer scope. Each field
// resolves its base group as before, then routes into that base's scope variant; a defer boundary
// therefore re-roots exactly like any group change -- a root variant materializes the full ancestor
// chain (v1's defer-parent re-walk), a jump variant re-enters the entity fetch and, when the
// boundary sits BELOW the jump's entry, materializes the ancestry suffix between entry and boundary.
func (gb *obGroupBuilder) emitFields(obs []obligation.Obligation, children map[obligation.ObID][]obligation.ObID,
	live map[obligation.ObID]bool, sibs []obligation.ObID, path string, ancestry []ancestorSeg,
	parentGroup, parentBase, parentScope int, parentSel, hostSel *docSel) {

	for _, cid := range sibs {
		c := obs[cid]
		// __typename is response-shape bookkeeping (never a search goal, hence never `live`), but it
		// must still be SELECTED in whatever subgraph document resolves its enclosing object -- the
		// router reads it from the fetch response. It rides with the parent: we reach this child only
		// while emitting the parent's own selection (parentSel), so marking parentSel.typename here
		// selects `__typename` in the parent's group at the parent's position. This is what carries a
		// __typename-terminal selection (`{ union { __typename } }`), a typename-terminal coverage
		// goal) into a real fetch -- without it the parent's selection body is empty and pruned, so the
		// composite was never selected by any fetch.
		if c.Kind == obligation.Typename {
			// Every __typename selection -- the bare meta-field OR a client alias of it -- rides the
			// parent's document as the ONE plain `__typename` selection: the response side reads
			// every alias off the merged object's `__typename` key (typenameLeaf), because the value
			// is the same field by definition. Selecting the alias itself would not cover an alias
			// under a NON-live member refinement (`... on Apple { typename: __typename }` with no
			// covered goal beneath), whose fragment must not print in a subgraph that may not
			// declare the member type (the interface-object rule below).
			parentSel.typename = true
			// FieldInfo Source attribution (info.go): __typename rides the parent's group.
			if parentGroup >= 0 && parentGroup < len(gb.groups) {
				gb.obSubgraph[cid] = gb.groups[parentGroup].key.subgraph
			}
			continue
		}
		if !live[cid] {
			// A NON-live member Refine -- no covered goal beneath, e.g.
			// `... on Admin { __typename }` whose only content is response-shape bookkeeping -- must
			// still get the DISCRIMINATOR selected: mark __typename at the PARENT position, which is
			// what the response object's OnTypeNames gate reads to decide the member applies. The
			// member fragment itself is NOT emitted: its only selection would be __typename (already
			// carried at the parent level), and printing `... on Admin` can be invalid in the very
			// subgraph that resolves the parent (an @interfaceObject subgraph does not declare the
			// concrete member type). Without this the parent's selection body stays empty and is
			// pruned, dropping the entire fetch.
			if c.Kind == obligation.Refine && subtreeHasTypename(obs, children, cid) {
				parentSel.typename = true
			}
			continue
		}
		switch c.Kind {
		case obligation.Refine:
			// An abstract-member narrowing: an inline fragment on the concrete member, plus a WEAK
			// __typename discriminator (prints only if this document keeps content at the position --
			// D11.7: the members may all re-root into other groups, each of which marks its own
			// discriminator strongly during ancestry materialization). The position key and ancestry
			// extend with the member gate so member leaves resolve their own member-qualified groups.
			parentSel.typenameWeak = true
			gb.emitFields(obs, children, live, children[cid],
				joinPath(path, memberMarkerPrefix+c.Concrete),
				append(ancestry, ancestorSeg{member: c.Concrete}),
				parentGroup, parentBase, parentScope, parentSel.frag(c.Concrete), hostSel)
		case obligation.Field:
			childPath := joinPath(path, c.RespKey)
			// D11.13 defer scope: the field's own @__defer_internal id wins; an unstamped field
			// inherits the enclosing scope (normalization stamps deferred subtrees recursively, so
			// inheritance is a defensive fallback). Recorded per position BEFORE descendants resolve
			// their groups -- a deferred jump variant reads its entry position's scope to decide
			// which scope fetches its @key.
			d := parentScope
			if c.DeferID != 0 {
				d = c.DeferID
			}
			gb.posScope[childPath] = d
			// Base attribution is scope-blind (walkSpine/posGroup never see defer); the field then
			// routes into its base group's scope variant. Scope 0 keeps the base -- byte-identical
			// behavior for undeferred operations.
			cgBase, ok := gb.posGroup[childPath]
			if !ok {
				cgBase = parentBase // inherit the parent's base group (a position no scoped walk crossed)
			}
			cg := cgBase
			if d != 0 && cgBase >= 0 {
				cg = gb.variant(cgBase, d)
			}
			// FieldInfo Source attribution (info.go): the group that resolves this field. Recorded
			// as the subgraph -- group indices are remapped by pruning/topo-ordering later.
			if cg >= 0 && cg < len(gb.groups) {
				gb.obSubgraph[cid] = gb.groups[cg].key.subgraph
			}
			flattened := gb.flattenedOb != nil && gb.flattenedOb[cid]
			target := parentSel
			if flattened && cg == parentGroup {
				// D11.9 same-document placement: the covering subgraph resolves the field on the
				// INTERFACE type, so it prints outside the member fragments, directly on the
				// interface-typed position (the fragment would name a type the subgraph lacks).
				target = hostSel
			}
			if cg != parentGroup {
				// The child is resolved in a different group. A JUMP group re-roots at its entity entry
				// (== this position), so the child is a top-level selection of the entity fetch. A ROOT
				// group serves the child at its FULL response position: materialize the ancestor field
				// chain in that root document (empty ancestry at top level keeps the old behavior).
				target = gb.groups[cg].sel
				if gb.groups[cg].jumpEdge == hypergraph.NoEdge {
					// Materialize the ancestor chain, carrying each ancestor's ARGUMENTS: a shared root
					// field whose children split across subgraphs (a @shareable `addCategory(name:) { ... }`
					// resolving `id` in one subgraph and `name` in another) must print its arguments in
					// EVERY root document it appears in, or the re-materialized copy is an invalid document
					// (a required argument silently dropped). ancestorSeg carries args + argVars for this.
					// A member entry reproduces its `... on Member` wrapper and strongly marks the abstract
					// position's __typename (the discriminator this document now supplies) -- D11.7.
					// A FLATTENED goal (D11.9) drops the TRAILING member wrappers: the resolving
					// subgraph serves the field on the interface type and does not declare the member.
					anc := ancestry
					if flattened {
						anc = trimTrailingMembers(ancestry)
					}
					for _, seg := range anc {
						if seg.member != "" {
							target.typename = true
							target = target.frag(seg.member)
							continue
						}
						key := seg.respKey
						if key == "" {
							key = seg.field
						}
						af := target.clientField(key, seg.field, true)
						if seg.args != "" {
							af.args = seg.args
							af.argVars = seg.argVars
						}
						target = af.sub
					}
				} else if gb.groups[cg].key.deferID != 0 {
					// D11.13 jump-variant boundary BELOW the entity entry: the deferred field is not
					// at the jump's entry position, so re-select the ancestor chain between entry and
					// boundary inside the entity fragment (`_entities { ... on User { info { phone } } }`).
					// The entry depth is the group's attach-hop count (one hop per FIELD segment); an
					// at-entry boundary yields the empty suffix and the plain re-root behavior.
					for _, seg := range ancestrySuffixBelowEntry(ancestry, len(gb.groups[cg].hops)) {
						if seg.member != "" {
							target.typename = true
							target = target.frag(seg.member)
							continue
						}
						key := seg.respKey
						if key == "" {
							key = seg.field
						}
						af := target.clientField(key, seg.field, true)
						if seg.args != "" {
							af.args = seg.args
							af.argVars = seg.argVars
						}
						target = af.sub
					}
				}
			}
			composite := hasSelectableChild(obs, children[cid])
			// C-disc discriminator-synthesis candidate: the client OBSERVES the concrete runtime
			// type at this composite position (__typename output, or a member gate that renders or
			// reads __typename). Recorded member-blind (positions under a `... on C` are skipped --
			// honest scope: the audit's shapes are unrefined positions); resolved after emitFields.
			if composite && !strings.Contains(childPath, memberMarkerPrefix) {
				needsConcrete := false
				for _, k := range children[cid] {
					kob := obs[k]
					if kob.Kind == obligation.Typename && kob.RespKey != internalTypenameKey {
						needsConcrete = true
					}
					if kob.Kind == obligation.Refine && (live[k] || subtreeHasTypename(obs, children, k)) {
						needsConcrete = true
					}
				}
				if needsConcrete {
					gb.discCandidates = append(gb.discCandidates, discCandidate{path: childPath, gi: cg, ob: cid})
				}
			}
			f := target.clientField(c.RespKey, c.Field, composite)
			// A covered typename-terminal composite (D3p/D3pp/D6pp-dead) resolves for its __typename
			// even when every child is exempt or non-live: print `{ __typename }` at its position.
			if f.sub != nil && gb.coveredComposite[cid] {
				f.sub.typename = true
			}
			// Carry the client field's arguments (and referenced variables) onto the fetch-side selection.
			// Same field NAME at one position with the SAME arguments dedups naturally (docSel.field returns
			// the existing docField); the requires-with-argument-conflict class -- same field, DIFFERENT
			// arguments at one position -- is an @requires concern handled at key injection, not here.
			if c.Arguments != "" {
				f.args = c.Arguments
				f.argVars = c.ArgVars
			}
			// Remember this obligation's docField so assignDocAliases can, after the whole document tree is
			// built, detect same-response-key output-type conflicts on the ACTUAL printed document and apply
			// an alias back onto the selection.
			if gb.obField != nil {
				gb.obField[cid] = f
			}
			if composite {
				gb.emitFields(obs, children, live, children[cid], childPath,
					append(ancestry, ancestorSeg{field: c.Field, respKey: c.RespKey, args: c.Arguments, argVars: c.ArgVars}),
					cg, cgBase, d, f.sub, f.sub)
			}
		}
	}
}

// ancestrySuffixBelowEntry returns the ancestry entries strictly below a jump group's entry depth
// (D11.13): the first entryFields FIELD entries -- and every member entry interleaved among them --
// belong to the path the entity fetch already re-roots at, so only what follows is re-selected
// inside the entity fragment. Empty for an at-entry boundary.
func ancestrySuffixBelowEntry(ancestry []ancestorSeg, entryFields int) []ancestorSeg {
	if entryFields == 0 {
		return ancestry // defensive: an entry at the operation root re-selects the whole chain
	}
	fields := 0
	for i, seg := range ancestry {
		if seg.member != "" {
			continue
		}
		fields++
		if fields == entryFields {
			return ancestry[i+1:]
		}
	}
	return nil
}

// trimTrailingMembers drops the trailing member entries of an ancestry chain (the `... on C` wrappers
// directly above the current position) -- the D11.9 flattened materialization form: the resolving
// subgraph serves the field on the interface type, so the member wrappers would name types it lacks.
// Field entries and any member entries BELOW the last field entry are kept.
func trimTrailingMembers(ancestry []ancestorSeg) []ancestorSeg {
	end := len(ancestry)
	for end > 0 && ancestry[end-1].member != "" {
		end--
	}
	return ancestry[:end]
}

// subtreeHasTypename reports whether obligation id's subtree (inclusive of nested Refines) contains a
// Typename obligation -- the "this fragment exists purely to read __typename" test for non-live member
// Refines in emitFields. Bounded by the obligation tree size.
func subtreeHasTypename(obs []obligation.Obligation, children map[obligation.ObID][]obligation.ObID, id obligation.ObID) bool {
	for _, cid := range children[id] {
		if obs[cid].Kind == obligation.Typename {
			return true
		}
		if obs[cid].Kind == obligation.Refine && subtreeHasTypename(obs, children, cid) {
			return true
		}
	}
	return false
}

// synthesizeDiscriminators mints the C-disc DISCRIMINATOR fetch for every candidate position whose
// object is resolved by an @interfaceObject subgraph and whose plan carries no other fetch that
// re-discriminates it: an entity jump through the ENTITY INTERFACE into a subgraph that declares
// the interface (and its concrete members), selecting only __typename -- the schema-computed
// concrete name merges over the interface name, so the client's __typename output and member gates
// see the concrete type (v1's interface-object discriminator; DIVERGENCES C-disc "the
// member-knowing discriminator jump is not yet synthesized"). Runs after emitFields (groups and
// posGroup are settled) and before the pipelines/injectKeys (the new group's key must be injected
// into its parent document like any jump group's).
func (gb *obGroupBuilder) synthesizeDiscriminators(h *hypergraph.Hypergraph,
	obs []obligation.Obligation, def *ast.Document) {

	for _, cand := range gb.discCandidates {
		if cand.gi < 0 || cand.gi >= len(gb.groups) {
			continue
		}
		g := gb.groups[cand.gi]
		ob := obs[cand.ob]
		t := namedFieldType(def, ob.Type, ob.Field)
		if t == "" || !h.IsInterfaceObject(t, g.key.subgraph) {
			continue // the position's data carries a trustworthy __typename already
		}
		// An existing jump at this position into a subgraph that re-discriminates (selects the
		// schema-computed concrete __typename) makes the synthesis redundant.
		already := false
		for _, og := range gb.groups {
			if og.jumpEdge != hypergraph.NoEdge && og.key.entryPath == cand.path &&
				entityFragmentNeedsTypename(h, def, og.key.subgraph, og.entryType) {
				already = true
				break
			}
		}
		if already {
			continue
		}
		e := findDiscriminatorJump(h, t, g.key.subgraph)
		if e == hypergraph.NoEdge {
			continue // no interface-declaring subgraph reachable: nothing sound to synthesize
		}
		segs := obligationChainFrom(obs, ob)
		memberScoped := false
		for _, seg := range segs {
			if len(seg.gates) > 0 {
				memberScoped = true // posKey would be member-qualified; honest scope -- skip
				break
			}
		}
		if memberScoped {
			continue
		}
		head := h.Node(h.EdgeHead(e))
		gi2 := gb.group(obGroupKey{entryPath: cand.path, subgraph: head.Subgraph, jump: e})
		grp := gb.groups[gi2]
		grp.entryType = head.Type
		grp.jumpEdge = e
		grp.srcType = t
		grp.synthetic = true
		if grp.hops == nil {
			grp.hops = attachHopsFromSegs(segs, len(segs), def)
		}
		grp.dependsOn[cand.gi] = true
		grp.sel.typename = true // the discriminator's whole payload
	}
}

// findDiscriminatorJump locates the entity jump that carries a discriminator fetch for interface t
// out of subgraph srcSg: an EntityJump headed at the INTERFACE node (type t) of a subgraph that
// declares t as an entity interface (never another @interfaceObject), whose key tails all live in
// srcSg. Deterministic: the smallest matching EdgeID. NoEdge when the graph has none.
func findDiscriminatorJump(h *hypergraph.Hypergraph, t string, srcSg hypergraph.SubgraphID) hypergraph.EdgeID {
	for i := 0; i < h.NumEdges(); i++ {
		e := hypergraph.EdgeID(i)
		if h.EdgeKind(e) != hypergraph.EdgeEntityJump {
			continue
		}
		head := h.Node(h.EdgeHead(e))
		if head.Type != t || head.Subgraph == srcSg || head.Scope != "" ||
			h.IsInterfaceObject(t, head.Subgraph) {
			continue
		}
		tailsOK := true
		for _, tail := range h.EdgeTails(e) {
			n := h.Node(tail)
			if n.Subgraph != srcSg || n.Type != t {
				tailsOK = false
				break
			}
		}
		if tailsOK {
			return e
		}
	}
	return hypergraph.NoEdge
}

// injectKeys adds each jump group's @key+@requires field selections into its parent group's document
// at the jump's entry position. These synthetic selections (id, organization { id }, dimensions { ... })
// are the representation inputs the entity fetch depends on; they are not client-requested fields, so
// the walk over the obligation tree never emits them.
func (gb *obGroupBuilder) injectKeys(h *hypergraph.Hypergraph, producedByAll map[hypergraph.NodeID]hypergraph.EdgeID, def *ast.Document) {
	for _, g := range gb.groups {
		if g.jumpEdge == hypergraph.NoEdge {
			continue
		}
		if gb.usesKeyPipeline(h, g) {
			continue // D11.11: the key-input pipeline placed this group's key (materializeRequiresPipelines)
		}
		parent := g.sourceParent()
		if parent == -1 {
			continue // defensive: a jump group with no producer
		}
		pg := gb.groups[parent]
		target := gb.entryTarget(pg, g, def)
		jump := h.Edge(g.jumpEdge)
		// @key tails inject argument-blind (keys carry no arguments). @requires tails are ALSO in
		// jump.Tails (search must produce them) but are rendered from jump.Requires by the D11.10
		// requires-input pipeline WITH their arguments, into the group that PRODUCES each coordinate --
		// skip them here so a same-named @requires field is not first inserted arg-blind
		// (docSel.field dedups by name, which would then drop the argument).
		keySet := map[hypergraph.NodeID]bool{}
		for _, t := range jump.KeyTails {
			keySet[t] = true
		}
		if g.synthetic {
			// No goal walk covered a synthesized discriminator's tails, so the tail up-walk is
			// undefined -- its key paths come from the edge's raw key structure instead.
			insertKeySelection(target, parseRequiresSelections(jump.KeySelection))
			continue
		}
		for _, tail := range jump.Tails {
			if len(jump.KeyTails) == 0 || keySet[tail] {
				insertFieldPath(target, tailFieldPath(h, producedByAll, tail, stopTypeOf(g)))
			}
		}
	}
}

// stopTypeOf is the representation/key anchor type of a jump group: the pre-jump entity type when the
// walk recorded one, else the entry type (the older behavior).
func stopTypeOf(g *obGroup) string {
	if g.srcType != "" {
		return g.srcType
	}
	return g.entryType
}

// entryTarget navigates parent group pg's document to group g's entry position and returns the
// selection set the jump's @key/@requires source fields are inserted into.
//
// The entryPath is relative to the root data buffer; the parent's own entryPath prefix is stripped so
// a nested parent group is handled too. The SCHEMA type is tracked during the descent so an abstract
// entry (union/interface) wraps the key in a `... on Member` fragment rather than selecting it
// directly on the abstract type. A member MARKER segment ("~Sale", D11.7) descends into the member's
// inline fragment -- marking the abstract position's __typename strongly on the way (the discriminator
// the representation's OnTypeNames gate reads) -- and pins the schema type to the member, so a key
// injected at a member-scoped entry lands inside `... on Member { ... }`, never flat on the abstract
// type.
//
// The key selection is typed on the PRE-JUMP entity type (srcType) -- what the SOURCE subgraph
// declares -- never the jump head's type. When the entry field's value type is abstract and the jump
// departs from a concrete member (a union/interface member jump), the key is a member field: emit
// `... on Member { id }` (plus __typename for resolution). When the source models the position as the
// abstract/@interfaceObject type itself (srcType == curType), the key is selected FLAT -- a member
// fragment would name a type the source subgraph does not declare (`... on Bread` against an
// @interfaceObject `Product`). The parent selection that produces the entity object(s) always selects
// __typename so each object (every array item, for a batch fetch) yields a typed representation the
// loader can gate on OnTypeNames -- v1 injects __typename into every entity representation's parent.
func (gb *obGroupBuilder) entryTarget(pg *obGroup, g *obGroup, def *ast.Document) *docSel {
	rel := relPathSegments(pg.key.entryPath, g.key.entryPath)
	curType := pg.entryType
	if pg.jumpEdge == hypergraph.NoEdge {
		curType = rootOperationType(def, rel)
	}
	sel := pg.sel
	for _, seg := range rel {
		if member, ok := strings.CutPrefix(seg, memberMarkerPrefix); ok {
			if member == curType {
				// Already inside this member: a jump group's document body IS its entry member's
				// fragment body, so a leading marker naming the entry type navigates nowhere.
				continue
			}
			sel.typename = true
			sel = sel.frag(member)
			curType = member
			continue
		}
		child := sel.field(seg, true) // seg is a RESPONSE key; an aliased client field is keyed by it
		if child.sub == nil {
			child.sub = newDocSel()
		}
		sel = child.sub
		curType = namedFieldType(def, curType, child.name) // schema name (differs from seg under an alias)
	}
	src := stopTypeOf(g)
	sel.typename = true
	target := sel
	if src != "" && src != curType && isAbstractType(def, curType) {
		target = sel.frag(src)
	}
	return target
}

// assignDocAliases handles output-type aliasing on this path's ACTUAL printed document. It walks each
// group's docSel and, at every selection-set scope (a docSel plus the member inline fragments that
// flatten into it), finds fields printed under the SAME response key whose subgraph output types
// conflict (the graphql-js "same response shape" rule, decided on Edge.OutputType). Each colliding field
// gets a unique `_planv2_<field>_<i>` fetch-side alias, mapped back to its client key via byOb so
// buildResponseObject reads the aliased value. Only client fields (those with a source obligation) are
// aliased -- injected @key/@requires selections are never renamed (the representation contract depends on
// their real names), so a scope whose colliding set includes such a field is left untouched.
func (gb *obGroupBuilder) assignDocAliases(obs []obligation.Obligation, obOutputTy map[obligation.ObID]string) *aliasMap {
	am := &aliasMap{byEdge: map[hypergraph.EdgeID]string{}, byOb: map[obligation.ObID]string{}, toResp: map[string]string{}}
	fieldToOb := make(map[*docField]obligation.ObID, len(gb.obField))
	for ob, f := range gb.obField {
		fieldToOb[f] = ob
	}
	for _, g := range gb.groups {
		gb.aliasScope(g.sel, obs, obOutputTy, am, fieldToOb)
	}
	return am
}

// aliasCand is one same-response-key selection in a scope, tagged with the abstract member fragment it
// was printed under ("" = a direct field of the scope object, not inside any `... on T`).
type aliasCand struct {
	f      *docField
	member string
}

// aliasScope processes one selection-set scope -- the docSel d plus the member fragments that flatten
// into it (nested refine members print into the same enclosing selection set, so they are siblings for
// the overlap rule) -- then recurses into every composite child's sub-selection (each a fresh scope).
//
// A response key selected under two or more DISTINCT abstract-member fragments (`... on User { id }`
// and `... on Admin { id }`) is aliased WHEN the members disagree on the field's SUBGRAPH-local output
// type (obOutputTy) -- the graphql-js "same response shape" conflict. That difference is invisible in the
// composed client schema (federation composes `ID!` and `ID` to a uniform `ID`), which is why the
// decision is made on Edge.OutputType, not the definition. Members that AGREE on the output type
// (`title: String` under Book/Movie/Song) are legal -- object parents can never co-apply -- and are left
// un-aliased. A key printed as a direct field (interface level) is never aliased: that is an
// abstract/refinement MERGE, not a member-vs-member conflict. The alias is harmless even where
// over-applied -- the response mapping restores the client key, and the audit's leaf-coverage reads the
// underlying field NAME, not the alias.
func (gb *obGroupBuilder) aliasScope(d *docSel, obs []obligation.Obligation, obOutputTy map[obligation.ObID]string,
	am *aliasMap, fieldToOb map[*docField]obligation.ObID) {
	if d == nil {
		return
	}
	byKey := map[string][]aliasCand{}
	var order []string
	var flatten func(s *docSel, member string)
	flatten = func(s *docSel, member string) {
		for _, f := range s.fields {
			if _, seen := byKey[f.name]; !seen {
				order = append(order, f.name)
			}
			byKey[f.name] = append(byKey[f.name], aliasCand{f: f, member: member})
		}
		for _, t := range s.fragOrder {
			flatten(s.frags[t], t)
		}
	}
	flatten(d, "")

	for _, name := range order {
		cands := byKey[name]
		if len(cands) < 2 {
			continue
		}
		// Alias only when every colliding selection is member-scoped (inside a `... on T`), the members
		// are pairwise distinct, AND they disagree on the subgraph output type. A direct (interface-level)
		// field, a duplicate member, or members that agree on output type is an interface-level merge, a dedup, or a legal
		// same-shape selection -- not a conflict -- and is left untouched.
		members := map[string]bool{}
		outTypes := map[string]bool{}
		ok := true
		for _, c := range cands {
			ob, hasOb := fieldToOb[c.f]
			if c.member == "" || members[c.member] || !hasOb {
				ok = false // direct field (interface-level merge), duplicate member, or injected key -- never alias
				break
			}
			members[c.member] = true
			outTypes[obOutputTy[ob]] = true
		}
		if ok && len(outTypes) < 2 {
			ok = false // members agree on the field's output type: legal, no conflict
		}
		if !ok {
			continue
		}
		sort.Slice(cands, func(i, j int) bool { return fieldToOb[cands[i].f] < fieldToOb[cands[j].f] })
		for i, c := range cands {
			ob := fieldToOb[c.f]
			a := fmt.Sprintf("_planv2_%s_%d", name, i)
			c.f.alias = a
			am.byOb[ob] = a
			am.toResp[a] = obs[ob].RespKey
		}
	}

	var recurse func(s *docSel)
	recurse = func(s *docSel) {
		for _, f := range s.fields {
			if f.sub != nil {
				gb.aliasScope(f.sub, obs, obOutputTy, am, fieldToOb)
			}
		}
		for _, t := range s.fragOrder {
			recurse(s.frags[t])
		}
	}
	recurse(d)
}

// rootOperationType returns the operation root type ("Query"/"Mutation"/"Subscription") that declares
// the first response-path segment -- used as the starting schema type when injecting keys into a root
// group's document (whose entry type is the operation root, not stored on the group). Member marker
// segments are skipped (defensive: a top-level selection is a field, never a refinement).
func rootOperationType(def *ast.Document, rel []string) string {
	first := ""
	for _, seg := range rel {
		if !strings.HasPrefix(seg, memberMarkerPrefix) {
			first = seg
			break
		}
	}
	if def == nil || first == "" {
		return "Query"
	}
	for _, t := range []string{"Query", "Mutation", "Subscription"} {
		node, ok := def.Index.FirstNodeByNameStr(t)
		if !ok {
			continue
		}
		if _, ok := def.NodeFieldDefinitionByName(node, ast.ByteSlice(first)); ok {
			return t
		}
	}
	return "Query"
}

// namedFieldType returns fieldName's named (List/NonNull-stripped) return type on parentType, or "".
func namedFieldType(def *ast.Document, parentType, fieldName string) string {
	if def == nil || parentType == "" {
		return ""
	}
	node, ok := def.Index.FirstNodeByNameStr(parentType)
	if !ok {
		return ""
	}
	fd, ok := def.NodeFieldDefinitionByName(node, ast.ByteSlice(fieldName))
	if !ok {
		return ""
	}
	return def.FieldDefinitionTypeNameString(fd)
}

// isAbstractType reports whether typeName is a union or interface in def.
func isAbstractType(def *ast.Document, typeName string) bool {
	if def == nil || typeName == "" {
		return false
	}
	node, ok := def.Index.FirstNodeByNameStr(typeName)
	if !ok {
		return false
	}
	return node.Kind == ast.NodeKindUnionTypeDefinition || node.Kind == ast.NodeKindInterfaceTypeDefinition
}

// insertFieldPath adds a nested field path (["organization","id"]) as a composite/leaf chain into sel.
func insertFieldPath(sel *docSel, fieldPath []string) {
	for i, f := range fieldPath {
		composite := i < len(fieldPath)-1
		child := sel.field(f, composite)
		if !composite {
			return
		}
		sel = child.sub
	}
}

// insertKeySelection adds a parsed key selection tree (Edge.KeySelection) into sel -- the synthetic
// discriminator's key injection, where no walk-recovered tail paths exist. Fragments cannot appear
// in keys (parseRequiresSelections tolerates them; skipped defensively).
func insertKeySelection(sel *docSel, nodes []*reqSelNode) {
	for _, n := range nodes {
		if n.frag != "" {
			continue
		}
		f := sel.field(n.name, len(n.sub) > 0)
		if len(n.sub) > 0 {
			if f.sub == nil {
				f.sub = newDocSel()
			}
			insertKeySelection(f.sub, n.sub)
		}
	}
}

// reqSelNode is a parsed @requires selection element carrying its field's rendered ARGUMENT body (for
// argument-bearing @requires). repNode/docField are otherwise argument-blind; this preserves the literal
// argument values (`currency: "USD"`, `limit: 3`) the source document and the entity fetch's
// representation must render -- the tails on the jump edge are argument-blind field nodes, so the values
// live only in the requires config carried on Edge.Requires.
type reqSelNode struct {
	name string
	args string        // rendered argument body without parens; "" if the field takes no arguments
	frag string        // inline-fragment type condition (`... on Bar`); name/args empty on a fragment node
	sub  []*reqSelNode // nested selection; nil for a leaf
}

// parseRequiresSelections parses one raw @requires selection-set string
// (`price(currency: "USD") weight category { averagePrice(currency: "USD") }`) into argument-bearing
// nodes. Argument VALUES are rendered via the parsed document's value printer, so string/enum/int
// literals round-trip exactly; JSON-escaping of any surviving string literal happens at input assembly
// (embedQuery).
func parseRequiresSelections(selectionSet string) []*reqSelNode {
	doc := unsafeparser.ParseGraphqlDocumentString("{ " + selectionSet + " }")
	for ref := range doc.OperationDefinitions {
		return parseReqSelectionSet(&doc, doc.OperationDefinitions[ref].SelectionSet)
	}
	return nil
}

func parseReqSelectionSet(doc *ast.Document, setRef int) []*reqSelNode {
	if setRef < 0 || setRef >= len(doc.SelectionSets) {
		return nil
	}
	var out []*reqSelNode
	for _, selRef := range doc.SelectionSets[setRef].SelectionRefs {
		sel := doc.Selections[selRef]
		switch sel.Kind {
		case ast.SelectionKindField:
			fRef := sel.Ref
			node := &reqSelNode{name: doc.FieldNameString(fRef), args: renderReqArgs(doc, fRef)}
			if doc.Fields[fRef].HasSelections {
				node.sub = parseReqSelectionSet(doc, doc.Fields[fRef].SelectionSet)
			}
			out = append(out, node)
		case ast.SelectionKindInlineFragment:
			// A @requires selection may refine an abstract input (`data { foo ... on Bar { bar } }`).
			// The fragment's coordinates are conditional inputs; the gather document must still
			// SELECT them (an abstract composite without subfields is an invalid document).
			cond := doc.InlineFragmentTypeConditionNameString(sel.Ref)
			if cond == "" {
				continue
			}
			node := &reqSelNode{frag: cond}
			if ss := doc.InlineFragments[sel.Ref].SelectionSet; doc.InlineFragments[sel.Ref].HasSelections {
				node.sub = parseReqSelectionSet(doc, ss)
			}
			out = append(out, node)
		}
	}
	return out
}

// renderReqArgs renders field fRef's argument list into the `name: value` body (no parens), the same
// form docField.args / repNode.args print.
func renderReqArgs(doc *ast.Document, fRef int) string {
	refs := doc.Fields[fRef].Arguments.Refs
	if len(refs) == 0 {
		return ""
	}
	var parts []string
	for _, aRef := range refs {
		name := doc.ArgumentNameString(aRef)
		val, err := doc.PrintValueBytes(doc.Arguments[aRef].Value, nil)
		if err != nil {
			continue
		}
		parts = append(parts, name+": "+string(val))
	}
	return strings.Join(parts, ", ")
}

// --- D11.10 requires-input pipeline ------------------------------------------------------------
//
// materializeRequiresPipelines places every jump group's @requires selection into the document of
// the group that PRODUCES each required coordinate, per the search's tail routes (FORMAL_SPEC
// D11.10; realizes D7pp at the wire). For a source-local requires (every coordinate resolved by the
// jump's source subgraph) every coordinate lands in the parent group's document at the jump's entry
// position -- byte-identical to the pre-D7pp parent-document rendering. A coordinate the source does
// NOT resolve was settled through its own producer route (the requires tail's walk); its producing
// jump gets a BRANCH fetch group at the position the requires path denotes, the coordinate is
// selected there, and the consuming group depends on it. Branch groups with their own @requires (a
// nested requires chain) are processed by the same loop -- appended groups are revisited.
func (gb *obGroupBuilder) materializeRequiresPipelines(h *hypergraph.Hypergraph,
	producedByAll map[hypergraph.NodeID]hypergraph.EdgeID, def *ast.Document) error {

	for gi := 0; gi < len(gb.groups); gi++ { // appended branch groups are processed too
		g := gb.groups[gi]
		if g.jumpEdge == hypergraph.NoEdge {
			continue
		}
		// D11.11 key-input pipeline: a distributed-key jump group (or a branch minted under one) has
		// no single source document for injectKeys -- place each key coordinate into the group that
		// produces it, exactly like the requires placement below. Runs first so the group's own key
		// hosts exist before its requires (if any) are placed.
		if gb.usesKeyPipeline(h, g) {
			if err := gb.placeKeyPipeline(h, producedByAll, def, gi); err != nil {
				return err
			}
			g = gb.groups[gi] // gb.groups may have been reallocated by group()
		}
		reqs := g.requiresOf(h)
		if len(reqs) == 0 {
			continue
		}
		jump := h.Edge(g.jumpEdge)
		// The requires tails by coordinate (the edge's non-key tails). A coordinate that is ALSO a
		// key field stays with the key machinery (injectKeys); its absence here routes it to the
		// source document, where the key injection already places it.
		keySet := map[hypergraph.NodeID]bool{}
		for _, t := range jump.KeyTails {
			keySet[t] = true
		}
		reqTailByCoord := map[string]hypergraph.NodeID{}
		for _, t := range jump.Tails {
			if keySet[t] {
				continue
			}
			n := h.Node(t)
			if n.Kind == hypergraph.NodeField {
				reqTailByCoord[n.Type+"."+n.Field] = t
			}
		}
		// The jump's pre-jump source object: a coordinate owned by it is source-local and lands in
		// the source parent's document (the pre-D7pp behavior, fragment wrapping included).
		srcObj := hypergraph.NodeID(0)
		haveSrc := false
		if pre, ok := preJumpObject(h, producedByAll, jump); ok {
			srcObj, haveSrc = pre, true
		}
		for _, reqStr := range reqs {
			nodes := parseRequiresSelections(reqStr)
			if err := gb.placeReqNodes(h, producedByAll, def, gi, reqTailByCoord, srcObj, haveSrc, nodes,
				stopTypeOf(g), g.key.entryPath, g.hops, "", gb.groups[gi].sourceParent(), map[int]*docSel{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// placeReqNodes places one level of a requires selection tree. curType is the schema type the
// siblings are selected on; curPos the response position of the enclosing object; curHops its attach
// hops; prefix the dotted requires path (for reqReadAlias keys); inheritGi the host group a
// coordinate WITHOUT its own tail inherits (the source parent at the top level; the enclosing path
// field's host below it). hostSels caches, per host group, the docSel the CURRENT level's fields
// insert into -- so a subtree whose host does not change descends through the actual created fields
// (aliased ones included) exactly like the old single-document merge did.
func (gb *obGroupBuilder) placeReqNodes(h *hypergraph.Hypergraph,
	producedByAll map[hypergraph.NodeID]hypergraph.EdgeID, def *ast.Document,
	consumerGi int, reqTailByCoord map[string]hypergraph.NodeID,
	srcObj hypergraph.NodeID, haveSrc bool, nodes []*reqSelNode,
	curType, curPos string, curHops []attachHop, prefix string, inheritGi int, hostSels map[int]*docSel) error {

	consumer := gb.groups[consumerGi]
	for _, n := range nodes {
		// Inline fragment: a refinement of the CURRENT position -- same host as the enclosing level,
		// rendered as `... on Cond { ... }` with the abstract position's __typename discriminator.
		if n.frag != "" {
			if inheritGi < 0 {
				continue
			}
			sel, ok := hostSels[inheritGi]
			if !ok {
				sel = gb.reqHostSel(inheritGi, consumerGi, curPos, def)
				hostSels[inheritGi] = sel
			}
			sel.typename = true
			if err := gb.placeReqNodes(h, producedByAll, def, consumerGi, reqTailByCoord, srcObj, haveSrc, n.sub,
				n.frag, curPos, curHops, prefix, inheritGi, map[int]*docSel{inheritGi: sel.frag(n.frag)}); err != nil {
				return err
			}
			continue
		}
		coord := n.name
		if prefix != "" {
			coord = prefix + "." + n.name
		}
		// Which group produces this coordinate? A tail on the consuming jump names the node the
		// search settled; its producer chain resolves to a group. No tail = a source-resolvable path
		// (or a key or fragment-conditioned coordinate): the inherited host owns it. A tail owned by
		// the jump's pre-jump SOURCE object is source-local -- the source parent's document, exactly
		// the pre-D7pp render.
		hostGi := inheritGi
		if tail, ok := reqTailByCoord[curType+"."+n.name]; ok {
			if fe, ok := producedByAll[tail]; ok && len(h.Edge(fe).Tails) > 0 {
				if owner := h.Edge(fe).Tails[0]; !haveSrc || owner != srcObj {
					var err error
					hostGi, err = gb.groupForObject(h, producedByAll, def, consumerGi, owner, curPos, curHops,
						gb.groups[consumerGi].key.entryPath, false, map[hypergraph.NodeID]bool{})
					if err != nil {
						return err
					}
				} else {
					hostGi = consumer.sourceParent()
				}
			}
		}
		if hostGi < 0 {
			continue // defensive: no producer known -- nothing to place the input into
		}
		consumer = gb.groups[consumerGi] // gb.groups may have been reallocated by group()
		sel, ok := hostSels[hostGi]
		if !ok {
			sel = gb.reqHostSel(hostGi, consumerGi, curPos, def)
			hostSels[hostGi] = sel
		}
		f := insertReqField(sel, n, coord, consumer)
		if hostGi != consumerGi && !consumer.dependsOn[hostGi] {
			// A NEW input dependency (the source-parent dep already exists from the spine chain).
			// Marked as a pipeline dep so the parent (source-group) derivation stays spine-only.
			consumer.dependsOn[hostGi] = true
			if consumer.pipeDeps == nil {
				consumer.pipeDeps = map[int]bool{}
			}
			consumer.pipeDeps[hostGi] = true
		}
		if len(n.sub) > 0 {
			if f.sub == nil {
				f.sub = newDocSel()
			}
			childType := namedFieldType(def, curType, n.name)
			// The children live under the field's RESPONSE key: when the requires path collided
			// with a client selection of the same field under different arguments, the gather was
			// aliased (`_planv2req_comments_1: comments(limit: 3)`), and the input pipeline --
			// branch fetches included -- attaches at the ALIASED position, which the consuming
			// representation reads back via reqReadAlias.
			respKey := n.name
			if f.alias != "" {
				respKey = f.alias
			}
			childPos := joinPath(curPos, respKey)
			childHops := append(append([]attachHop(nil), curHops...), attachHop{
				field:         respKey,
				enclosingType: curType,
				array:         curType != "" && listNesting(def, curType, n.name) > 0,
			})
			if err := gb.placeReqNodes(h, producedByAll, def, consumerGi, reqTailByCoord, srcObj, haveSrc, n.sub,
				childType, childPos, childHops, coord, hostGi, map[int]*docSel{hostGi: f.sub}); err != nil {
				return err
			}
		}
	}
	return nil
}

// reqHostSel navigates host group hostGi's document to position curPos and returns the selection set
// a requires coordinate inserts into. For the consumer's SOURCE PARENT the navigation goes through
// entryTarget (reproducing the abstract-entry `... on Src` wrapping the key injection uses) and then
// descends the requires path below the entry; for any other host (a branch group, or a spine group
// producing the input) it descends from that group's own entry.
func (gb *obGroupBuilder) reqHostSel(hostGi, consumerGi int, curPos string, def *ast.Document) *docSel {
	host := gb.groups[hostGi]
	consumer := gb.groups[consumerGi]
	var sel *docSel
	var base string
	if hostGi == consumer.sourceParent() {
		sel = gb.entryTarget(host, consumer, def)
		base = consumer.key.entryPath
	} else {
		sel = host.sel
		base = host.key.entryPath
	}
	for _, seg := range relPathSegments(base, curPos) {
		child := sel.field(seg, true)
		if child.sub == nil {
			child.sub = newDocSel()
		}
		sel = child.sub
	}
	return sel
}

// --- D11.11 key-input pipeline -------------------------------------------------------------------

// placeKeyPipeline places one distributed-key (or pipeline-branch) group's @key selection into the
// documents of the groups that PRODUCE its coordinates (FORMAL_SPEC D11.11; realizes D7ppp at the
// wire). The key's STRUCTURE comes from Edge.KeySelection -- the tails span subgraphs, so the
// tail-up-walk path recovery injectKeys uses is undefined here. Each coordinate instance is matched
// to its assignment tail by (type, field), preferring the enclosing path's assigned subgraph
// (path-coherence at placement: a repeated coordinate under two paths reads its own path's tail).
func (gb *obGroupBuilder) placeKeyPipeline(h *hypergraph.Hypergraph,
	producedByAll map[hypergraph.NodeID]hypergraph.EdgeID, def *ast.Document, consumerGi int) error {

	consumer := gb.groups[consumerGi]
	jump := h.Edge(consumer.jumpEdge)
	if jump.KeySelection == "" {
		return nil // defensive: hand-built graphs -- nothing to place
	}
	tailsByCoord := map[string][]hypergraph.NodeID{}
	for _, t := range jump.KeyTails {
		if n := h.Node(t); n.Kind == hypergraph.NodeField {
			c := n.Type + "." + n.Field
			tailsByCoord[c] = append(tailsByCoord[c], t)
		}
	}
	// The key selection is rooted on the jump head's type (the key's anchor; for the distributed +
	// @interfaceObject combination this is the interface -- untested territory, documented in the
	// spec's honest scope).
	anchorType := h.Node(jump.Head).Type
	nodes := parseRequiresSelections(jump.KeySelection)
	return gb.placeKeyNodes(h, producedByAll, def, consumerGi, tailsByCoord, nodes, anchorType,
		consumer.key.entryPath, consumer.hops, consumer.sourceParent(), 0)
}

// placeKeyNodes places one level of a key selection tree (see placeKeyPipeline). curType is the
// schema type the siblings are selected on, curPos the response position of the enclosing object,
// inheritGi the host a coordinate stays in unless its tail is genuinely foreign (the enclosing
// path's host; the consumer's source parent at the top level), ctxSg the subgraph the enclosing
// path was assigned to (0 at the top level -- no context).
func (gb *obGroupBuilder) placeKeyNodes(h *hypergraph.Hypergraph,
	producedByAll map[hypergraph.NodeID]hypergraph.EdgeID, def *ast.Document, consumerGi int,
	tailsByCoord map[string][]hypergraph.NodeID, nodes []*reqSelNode,
	curType, curPos string, curHops []attachHop, inheritGi int, ctxSg hypergraph.SubgraphID) error {

	for _, n := range nodes {
		if n.frag != "" {
			continue // keys never carry inline fragments (defensive)
		}
		// Match this coordinate instance to its assignment tail: prefer the enclosing path's
		// subgraph (path-coherence), else the first (deterministic -- builder tail order).
		var tail hypergraph.NodeID
		haveTail := false
		if cands := tailsByCoord[curType+"."+n.name]; len(cands) > 0 {
			tail, haveTail = cands[0], true
			if ctxSg != 0 {
				for _, t := range cands {
					if h.Node(t).Subgraph == ctxSg {
						tail, haveTail = t, true
						break
					}
				}
			}
		}
		hostGi := inheritGi
		tailSg := ctxSg
		if haveTail {
			tailSg = h.Node(tail).Subgraph
			inheritSg := hypergraph.SubgraphID(0)
			if inheritGi >= 0 {
				inheritSg = gb.groups[inheritGi].key.subgraph
			}
			if tailSg != inheritSg {
				// A genuinely foreign coordinate: the group its producer route names hosts it (a
				// branch group is minted when none exists -- flagged, so its own key goes through
				// this pipeline too).
				if fe, ok := producedByAll[tail]; ok && len(h.Edge(fe).Tails) > 0 {
					var err error
					hostGi, err = gb.groupForObject(h, producedByAll, def, consumerGi, h.Edge(fe).Tails[0],
						curPos, curHops, gb.groups[consumerGi].key.entryPath, true, map[hypergraph.NodeID]bool{})
					if err != nil {
						return err
					}
				}
			}
		}
		if hostGi < 0 {
			continue // defensive: no producer known -- nothing to place the input into
		}
		consumer := gb.groups[consumerGi] // gb.groups may have been reallocated by group()
		if hostGi != consumerGi && !consumer.dependsOn[hostGi] {
			consumer.dependsOn[hostGi] = true
			if consumer.pipeDeps == nil {
				consumer.pipeDeps = map[int]bool{}
			}
			consumer.pipeDeps[hostGi] = true
		}
		// Position-aware insertion (D11.11): a coordinate at-or-above the host's entry is never
		// re-selected in the host's document -- the host's document is rooted AT its entry (a gather
		// producer's entry may sit BELOW the consuming jump's entry, impossible under single-source
		// keys and routine under D7ppp).
		host := gb.groups[hostGi]
		fieldPos := joinPath(curPos, n.name)
		switch {
		case fieldPos == host.key.entryPath:
			// The coordinate IS the host's entry object: children print at the document root.
		case strings.HasPrefix(host.key.entryPath, fieldPos+"."):
			// Still above the host's entry: keep descending toward it.
		default:
			sel := gb.keyHostSel(hostGi, consumerGi, curPos, def)
			f := sel.field(n.name, len(n.sub) > 0)
			if len(n.sub) > 0 && f.sub == nil {
				f.sub = newDocSel()
			}
		}
		if len(n.sub) > 0 {
			childType := namedFieldType(def, curType, n.name)
			childHops := append(append([]attachHop(nil), curHops...), attachHop{
				field:         n.name,
				enclosingType: curType,
				array:         curType != "" && listNesting(def, curType, n.name) > 0,
			})
			if err := gb.placeKeyNodes(h, producedByAll, def, consumerGi, tailsByCoord, n.sub,
				childType, fieldPos, childHops, hostGi, tailSg); err != nil {
				return err
			}
		}
	}
	return nil
}

// keyHostSel navigates host group hostGi's document to position curPos and returns the selection
// set a key coordinate inserts into. Like reqHostSel, the consumer's source parent goes through
// entryTarget (the abstract-entry wrapping + __typename the representation gate reads) -- but ONLY
// when the consumer's entry is at-or-below the host's entry: a pipeline branch's source parent can
// sit BELOW the consumer (the D11.11 pre-jump anchor shape), where the plain from-the-host's-own-
// entry navigation is the defined one.
func (gb *obGroupBuilder) keyHostSel(hostGi, consumerGi int, curPos string, def *ast.Document) *docSel {
	host := gb.groups[hostGi]
	consumer := gb.groups[consumerGi]
	var sel *docSel
	var base string
	if hostGi == consumer.sourceParent() &&
		(host.key.entryPath == consumer.key.entryPath || strings.HasPrefix(consumer.key.entryPath, host.key.entryPath+".") || host.key.entryPath == "") {
		sel = gb.entryTarget(host, consumer, def)
		base = consumer.key.entryPath
	} else {
		sel = host.sel
		base = host.key.entryPath
	}
	for _, seg := range relPathSegments(base, curPos) {
		child := sel.field(seg, true)
		if child.sub == nil {
			child.sub = newDocSel()
		}
		sel = child.sub
	}
	return sel
}

// groupForObject resolves the fetch group that materializes object node obj at response position pos
// -- creating a BRANCH group for a producing entity jump that has none (D11.10). Local steps (Descent
// through a field, TypeMove through an abstract) walk up the producer chain, trimming the position
// segment the field contributed; an object with no known producer falls back to the consumer's
// source parent (its document is the root of everything gathered at the position).
//
// TERMINATION is grounded in the FINITE query structure, never the (cyclic) type graph. base is the
// consuming jump's entry position: a representation input lives at-or-below the entry by definition,
// so the Descent up-walk consumes position segments only while pos is strictly BELOW base -- reaching
// base resolves to the source parent (whose document materializes the entry), and no step ever
// climbs above it. This matters because producedByAll is a cross-goal, head-keyed union: a schema
// TYPE CYCLE (an entity nested inside itself -- V->M->P->E.node->V) collapses two response positions
// onto ONE object node, a repaired walk then records the DEEP descent as that node's producer, and
// the map becomes cyclic ((V,a)->(E,a)->(P,a)->(M,a)->(V,a)); an ungrounded walk over it never
// terminates (the M2 requires-chain regression -- customer-corpus stack overflow). visited is the
// defense-in-depth invariant for the same class: revisiting a node within one resolution means the
// producer map left the walk structure entirely -- FAIL LOUD (a typed plan error), never loop and
// never silently truncate.
func (gb *obGroupBuilder) groupForObject(h *hypergraph.Hypergraph,
	producedByAll map[hypergraph.NodeID]hypergraph.EdgeID, def *ast.Document, consumerGi int,
	obj hypergraph.NodeID, pos string, hops []attachHop,
	base string, viaKey bool, visited map[hypergraph.NodeID]bool) (int, error) {

	if visited[obj] {
		n := h.Node(obj)
		return -1, fmt.Errorf("planv2: lower: requires-input producer chain revisited node (%s, subgraph %d) at position %q -- cyclic producer map (self-nested entity type cycle); the gather cannot be attributed", n.Type, n.Subgraph, pos)
	}
	visited[obj] = true

	node := h.Node(obj)
	// A PLAIN object node (no requires/provides scope): a group already fetching that subgraph at
	// this position can select the coordinate (it is resolvable there input-free), so reuse it. This
	// sidesteps producedByAll's cross-goal last-write ambiguity -- a sibling goal's walk may have
	// recorded a root-descent producer for a node this position reaches by a jump -- and merges the
	// input into the shared fetch instead of minting a duplicate. Scope-carrying nodes never take
	// this shortcut: a requires-scoped field must land in the group of its OWN scoped jump (anything
	// else re-opens the bypass). A candidate that IS the consumer -- the consumer's own fetch enters
	// the subgraph at the position, but an input must be produced BEFORE it -- or that already
	// depends on the consumer (a cycle) is skipped.
	if node.Kind == hypergraph.NodeObject && node.Scope == "" {
		for gi, grp := range gb.groups {
			if grp.jumpEdge != hypergraph.NoEdge && grp.key.entryPath == pos && grp.key.subgraph == node.Subgraph &&
				gi != consumerGi && !gb.dependsTransitively(gi, consumerGi) {
				return gi, nil
			}
		}
	}
	e, ok := producedByAll[obj]
	if !ok {
		return gb.groups[consumerGi].sourceParent(), nil
	}
	edge := h.Edge(e)
	switch edge.Kind {
	case hypergraph.EdgeEntityJump:
		key := obGroupKey{entryPath: pos, subgraph: h.Node(obj).Subgraph, jump: e}
		if gi, ok := gb.byKey[key]; ok {
			if gi == consumerGi || gb.dependsTransitively(gi, consumerGi) {
				return gb.groups[consumerGi].sourceParent(), nil // defensive: never mint a cycle
			}
			return gi, nil
		}
		// The branch's own parent: the object its key tails hang off -- at the position the KEY
		// structure denotes (D11.11 pre-jump anchor positions): a nested key's pre-jump objects live
		// BELOW the branch's entry (`products{id pid}` -- the Product objects sit one response level
		// under the ProductList entity), so the parent is resolved at pos extended by the
		// key-selection path from the key's anchor type to the pre-jump object's type. For every
		// single-source shape the pre-jump object IS anchor-typed and the extension is empty --
		// byte-identical to the old same-position recursion. Without the extension the recursion
		// minted a second, mis-positioned group for a jump that already has a goal-attributed group
		// at the deeper position (the reverted D7ppp prototype's defect (b)).
		pgi := gb.groups[consumerGi].sourceParent()
		if pre, ok := jumpSpineParent(h, producedByAll, edge); ok {
			prePos, preHops := pos, hops
			if segs := keyAnchorPath(h.Edge(e).KeySelection, h.Node(h.EdgeHead(e)).Type, h.Node(pre).Type, def); len(segs) > 0 {
				cur := h.Node(h.EdgeHead(e)).Type
				preHops = append([]attachHop(nil), preHops...)
				for _, seg := range segs {
					preHops = append(preHops, attachHop{
						field:         seg,
						enclosingType: cur,
						array:         cur != "" && listNesting(def, cur, seg) > 0,
					})
					prePos = joinPath(prePos, seg)
					cur = namedFieldType(def, cur, seg)
				}
			}
			var err error
			pgi, err = gb.groupForObject(h, producedByAll, def, consumerGi, pre, prePos, preHops, base, viaKey, visited)
			if err != nil {
				return -1, err
			}
		}
		gi := gb.group(key)
		grp := gb.groups[gi]
		grp.entryType = h.Node(obj).Type
		grp.jumpEdge = e
		if viaKey {
			// Minted under a key-input placement: its own key goes through the D11.11 pipeline too
			// (its source objects may live below its entry, where entryTarget is undefined).
			grp.keyViaPipeline = true
		}
		if pre, ok := jumpSpineParent(h, producedByAll, edge); ok {
			grp.srcType = h.Node(pre).Type
		}
		grp.hops = append([]attachHop(nil), hops...)
		if pgi >= 0 {
			grp.dependsOn[pgi] = true
		}
		return gi, nil
	case hypergraph.EdgeDescent, hypergraph.EdgeTypeMove:
		t := edge.Tails[0]
		if h.Node(t).Kind == hypergraph.NodeField {
			// The primary grounding: a Descent through a field consumes one position segment of the
			// requires path. At base there is none left -- the entry object is materialized by the
			// source-parent chain (representation inputs never live above the entry).
			if pos == base {
				return gb.groups[consumerGi].sourceParent(), nil
			}
			fe, ok := producedByAll[t]
			if !ok || len(h.Edge(fe).Tails) == 0 {
				return gb.groups[consumerGi].sourceParent(), nil
			}
			trimmed := hops
			if len(trimmed) > 0 {
				trimmed = trimmed[:len(trimmed)-1]
			}
			return gb.groupForObject(h, producedByAll, def, consumerGi, h.Edge(fe).Tails[0], parentPath(pos), trimmed, base, viaKey, visited)
		}
		return gb.groupForObject(h, producedByAll, def, consumerGi, t, pos, hops, base, viaKey, visited)
	}
	return gb.groups[consumerGi].sourceParent(), nil
}

// keyAnchorPath returns the field-path segments from a key's anchor type to the first coordinate of
// keySelection whose (composite) type is preType -- the response-path suffix between a jump's entry
// object and its pre-jump objects (D11.11 pre-jump anchor positions). Empty when preType IS the
// anchor (every single-source key shape), when the selection is unknown (hand-built graphs), or when
// no path reaches preType (defensive -- the caller keeps the un-extended position).
func keyAnchorPath(keySelection, anchorType, preType string, def *ast.Document) []string {
	if keySelection == "" || preType == "" || preType == anchorType {
		return nil
	}
	nodes := parseRequiresSelections(keySelection)
	var walk func(nodes []*reqSelNode, curType string, prefix []string) []string
	walk = func(nodes []*reqSelNode, curType string, prefix []string) []string {
		for _, n := range nodes {
			if n.frag != "" || len(n.sub) == 0 {
				continue
			}
			childType := namedFieldType(def, curType, n.name)
			path := append(append([]string(nil), prefix...), n.name)
			if childType == preType {
				return path
			}
			if found := walk(n.sub, childType, path); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(nodes, anchorType, nil)
}

// dependsTransitively reports whether group `from` (transitively) depends on group `target` --
// the cycle guard for input-host resolution. Bounded DFS over dependsOn.
func (gb *obGroupBuilder) dependsTransitively(from, target int) bool {
	if from == target {
		return true
	}
	seen := map[int]bool{}
	var walk func(int) bool
	walk = func(g int) bool {
		if seen[g] {
			return false
		}
		seen[g] = true
		for d := range gb.groups[g].dependsOn {
			if d == target || walk(d) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

// parentPath strips the last dotted segment ("feed.author" -> "feed"; "feed" -> "").
func parentPath(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[:i]
	}
	return ""
}

// insertReqField inserts ONE requires coordinate into a document selection set with the
// argument-conflict aliasing contract of mergeRequiresIntoDocAliased (which see): plain insert,
// same-args dedup, or a `_planv2req_<name>_<n>` alias recorded on the CONSUMING group so its
// representation reads the aliased response key.
func insertReqField(sel *docSel, n *reqSelNode, coord string, consumer *obGroup) *docField {
	existing, ok := sel.byName[n.name]
	var f *docField
	switch {
	case !ok:
		f = sel.field(n.name, len(n.sub) > 0)
		if n.args != "" {
			f.args = n.args
		}
	case existing.args == n.args:
		f = existing
		if len(n.sub) > 0 && f.sub == nil {
			f.sub = newDocSel()
		}
	default:
		if reuse := aliasedSiblingWithArgs(sel, n.name, n.args); reuse != nil {
			f = reuse
		} else {
			alias := fmt.Sprintf("_planv2req_%s_%d", n.name, countFieldsNamed(sel, n.name))
			f = &docField{name: n.name, alias: alias, args: n.args}
			if len(n.sub) > 0 {
				f.sub = newDocSel()
			}
			sel.fields = append(sel.fields, f)
			// Registered in sel.byName under the ALIAS (the response key -- the same keying
			// clientField uses), never under the bare name (which maps to the first, unaliased
			// selection). Key injection and pipeline navigation address the aliased position by its
			// response key.
			sel.byName[alias] = f
		}
		if consumer != nil && f.alias != "" {
			if consumer.reqReadAlias == nil {
				consumer.reqReadAlias = map[string]string{}
			}
			consumer.reqReadAlias[coord] = f.alias
		}
	}
	return f
}

// countFieldsNamed counts the selections in sel whose schema field NAME is name (aliased siblings
// included) -- the next injective alias index for that coordinate.
func countFieldsNamed(sel *docSel, name string) int {
	n := 0
	for _, f := range sel.fields {
		if f.name == name {
			n++
		}
	}
	return n
}

// aliasedSiblingWithArgs returns an already-ALIASED selection of `name` in sel whose rendered argument
// body equals args (so a second requiring field needing the same argument variant reuses one aliased
// source selection instead of duplicating it), or nil.
func aliasedSiblingWithArgs(sel *docSel, name, args string) *docField {
	for _, f := range sel.fields {
		if f.name == name && f.alias != "" && f.args == args {
			return f
		}
	}
	return nil
}

// applyReadAs sets readAs on the representation-trie node at the dotted requires path, so its leaf reads
// the aliased response key. A no-op if the path is absent.
func applyReadAs(trie *repNode, path []string, alias string) {
	cur := trie
	for _, seg := range path {
		next := cur.children[seg]
		if next == nil {
			return
		}
		cur = next
	}
	cur.readAs = alias
}

// splitArgConflictGroups mirrors the argument-conflict check the v1 planner runs: when the requiring
// client fields resolved by one entity jump group call the SAME field with DIFFERENT argument values
// (`shippingEstimate` @requires `price(currency: "USD")`, `shippingEstimateEUR` @requires
// `price(currency: "EUR")`), a single entity representation cannot carry both `price` values. This
// partitions those requiring fields into separate entity fetch groups (greedy first-fit) so each group's
// representation carries a consistent binding. Group 0 REUSES the original group (its non-requiring
// client fields plus its assigned requiring fields stay); groups 1..N are fresh obGroups sharing the
// jump/entry/parent, with their requiring fields MOVED out of the original selection. Each split group's
// requires subset (reqStrings) is authoritative from here on (requiresOf). Runs before injectKeys; a
// no-op for every group without an internal argument conflict, so all non-conflict plans are
// byte-identical to the pre-split output.
func (gb *obGroupBuilder) splitArgConflictGroups(h *hypergraph.Hypergraph) {
	origLen := len(gb.groups) // snapshot: appended split groups are never re-examined
	for gi := 0; gi < origLen; gi++ {
		g := gb.groups[gi]
		if g.jumpEdge == hypergraph.NoEdge {
			continue
		}
		edge := h.Edge(g.jumpEdge)
		if _, conflict := requiresArgConflict(edge.Requires); !conflict {
			continue
		}
		if len(edge.RequiresBy) != len(edge.Requires) {
			continue // defensive: no field association available (pre-RequiresBy edge)
		}
		// field name -> its @requires selection (1:1 per @requires directive).
		reqByField := map[string]string{}
		for i, fn := range edge.RequiresBy {
			if fn != "" {
				reqByField[fn] = edge.Requires[i]
			}
		}

		// Greedy first-fit over the requiring fields ACTUALLY selected by this group (client-requested),
		// in selection (operation) order for determinism. A requiring field joins the first subgroup with
		// no argument conflict, else opens a new one.
		type subGroup struct {
			fields []string
			reqs   []string
			seen   map[string]string // coord -> argument body
		}
		var subs []*subGroup
		assign := func(rs string) int {
			coords := requiresCoordArgs(rs)
			for i, s := range subs {
				if !coordsConflict(s.seen, coords) {
					s.reqs = append(s.reqs, rs)
					for c, a := range coords {
						s.seen[c] = a
					}
					return i
				}
			}
			s := &subGroup{seen: map[string]string{}}
			s.reqs = append(s.reqs, rs)
			for c, a := range coords {
				s.seen[c] = a
			}
			subs = append(subs, s)
			return len(subs) - 1
		}
		// Attribute each requiring field to the subgroup index it landed in.
		fieldSub := map[string]int{}
		for _, df := range g.sel.fields {
			rs, ok := reqByField[df.name]
			if !ok {
				continue // not a requiring field (a plain client field on this entity)
			}
			fieldSub[df.name] = assign(rs)
		}
		if len(subs) < 2 {
			continue // the requested requiring fields do not actually conflict; leave the group intact
		}

		// Subgroup 0 reuses g: authoritative requires subset, requiring fields kept in place.
		g.isSplit = true
		g.reqStrings = subs[0].reqs
		// Subgroups 1...N: fresh groups; move their requiring fields out of g.sel.
		for si := 1; si < len(subs); si++ {
			ng := &obGroup{
				key: obGroupKey{
					entryPath: g.key.entryPath, subgraph: g.key.subgraph, jump: g.key.jump, split: si,
				},
				entryType:  g.entryType,
				jumpEdge:   g.jumpEdge,
				srcType:    g.srcType,
				dependsOn:  map[int]bool{},
				sel:        newDocSel(),
				hops:       g.hops,
				isSplit:    true,
				reqStrings: subs[si].reqs,
			}
			for d := range g.dependsOn {
				ng.dependsOn[d] = true
			}
			for name, si2 := range fieldSub {
				if si2 == si {
					moveTopField(g.sel, ng.sel, name)
				}
			}
			gb.groups = append(gb.groups, ng)
		}
	}
}

// requiresCoordArgs parses one @requires selection into a coord(dotted)->argument-body map for every
// field carrying arguments (nested included) -- the per-string form of requiresArgConflict's walk.
func requiresCoordArgs(reqStr string) map[string]string {
	out := map[string]string{}
	var walk func(prefix string, nodes []*reqSelNode)
	walk = func(prefix string, nodes []*reqSelNode) {
		for _, n := range nodes {
			coord := n.name
			if prefix != "" {
				coord = prefix + "." + n.name
			}
			if n.args != "" {
				out[coord] = n.args
			}
			walk(coord, n.sub)
		}
	}
	walk("", parseRequiresSelections(reqStr))
	return out
}

// coordsConflict reports whether any coordinate appears in both maps bound to a different argument body.
func coordsConflict(seen, coords map[string]string) bool {
	for c, a := range coords {
		if prev, ok := seen[c]; ok && prev != a {
			return true
		}
	}
	return false
}

// moveTopField relocates a top-level field (by name) from one selection set to another, preserving the
// docField pointer (so gb.obField/aliasing references stay valid). A no-op if `from` has no such field.
func moveTopField(from, to *docSel, name string) {
	f, ok := from.byName[name]
	if !ok {
		return
	}
	delete(from.byName, name)
	for i, ff := range from.fields {
		if ff == f {
			from.fields = append(from.fields[:i], from.fields[i+1:]...)
			break
		}
	}
	to.fields = append(to.fields, f)
	to.byName[name] = f
}

// requiresArgConflict reports the first coordinate that a set of @requires selection strings assigns
// two DIFFERENT rendered argument bodies -- the same argument-conflict check the v1 planner runs, applied
// to a single jump's bundled requires. Such a conflict has no single-representation encoding (see the
// guard in lowerObligationDriven).
func requiresArgConflict(reqStrings []string) (string, bool) {
	seen := map[string]string{}
	var walk func(prefix string, nodes []*reqSelNode) (string, bool)
	walk = func(prefix string, nodes []*reqSelNode) (string, bool) {
		for _, n := range nodes {
			coord := prefix + n.name
			if n.args != "" {
				if prev, ok := seen[coord]; ok && prev != n.args {
					return coord, true
				}
				seen[coord] = n.args
			}
			if c, ok := walk(coord+".", n.sub); ok {
				return c, true
			}
		}
		return "", false
	}
	for _, s := range reqStrings {
		if c, ok := walk("", parseRequiresSelections(s)); ok {
			return c, true
		}
	}
	return "", false
}

// mergeRequiresIntoTrie inserts argument-bearing @requires selections into the representation
// Requires-fragment trie (which the entity fetch declares as its @requires input contract).
func mergeRequiresIntoTrie(trie *repNode, nodes []*reqSelNode) {
	for _, n := range nodes {
		if n.frag != "" {
			// Fragment-conditioned requires coordinates print in the Requires fragment (the v1
			// contract text; also the assertion-6 mechanism signal), but are not built into the
			// representation VALUE (repNode.object is name-keyed; the enclosing composite reads as
			// a whole object). Registered residual (requires-fragment representation value).
			if trie.frags == nil {
				trie.frags = map[string]*repNode{}
			}
			child := trie.frags[n.frag]
			if child == nil {
				child = newRepNode()
				trie.frags[n.frag] = child
			}
			mergeRequiresIntoTrie(child, n.sub)
			continue
		}
		child := trie.children[n.name]
		if child == nil {
			child = newRepNode()
			trie.children[n.name] = child
		}
		if n.args != "" {
			child.args = n.args
		}
		mergeRequiresIntoTrie(child, n.sub)
	}
}

// mergeScopedTwins merges, per (entryPath, subgraph, defer scope), a requires-SCOPED jump group
// with its PLAIN twin (see the call site). The lower-indexed group survives: documents merge
// (response-key keyed), the requires bundle becomes the authoritative union (isSplit semantics),
// read-aliases and dependencies union, and every dependent of the absorbed group is redirected to
// the survivor; the absorbed group's selection empties so pruneEmptyGroups drops it. Guarded to
// exactly the twin shape: one side with @requires and one without, same entry/source types, same
// raw key structure, no argument-conflict split membership, no synthetic/distributed-key group,
// no transitive dependency between the two (a gather chain through the twin must keep its order),
// and no argument conflict across the merged requires.
func (gb *obGroupBuilder) mergeScopedTwins(h *hypergraph.Hypergraph) {
	for i := 0; i < len(gb.groups); i++ {
		g1 := gb.groups[i]
		if g1.jumpEdge == hypergraph.NoEdge || g1.synthetic || g1.key.split != 0 || gb.usesKeyPipeline(h, g1) {
			continue
		}
		for j := i + 1; j < len(gb.groups); j++ {
			g2 := gb.groups[j]
			if g2.jumpEdge == hypergraph.NoEdge || g2.synthetic || g2.key.split != 0 || gb.usesKeyPipeline(h, g2) {
				continue
			}
			if g2.key.entryPath != g1.key.entryPath || g2.key.subgraph != g1.key.subgraph ||
				g2.key.deferID != g1.key.deferID || g2.entryType != g1.entryType ||
				stopTypeOf(g2) != stopTypeOf(g1) {
				continue
			}
			if h.Edge(g1.jumpEdge).KeySelection != h.Edge(g2.jumpEdge).KeySelection {
				continue // different @key structures -- not the twin
			}
			r1, r2 := g1.requiresOf(h), g2.requiresOf(h)
			if (len(r1) == 0) == (len(r2) == 0) {
				continue // two plain (same-key, distinct-edge) or two scoped groups -- not the twin
			}
			merged := append(append([]string(nil), r1...), r2...)
			if _, conflict := requiresArgConflict(merged); conflict {
				continue // conflicting argument bindings need separate representations
			}
			if gb.dependsTransitively(i, j) || gb.dependsTransitively(j, i) {
				continue // an input pipeline threads through the twin -- order must survive
			}
			// Merge j into i.
			mergeDocSelInto(g1.sel, g2.sel)
			g1.isSplit = true
			g1.reqStrings = merged
			for path, alias := range g2.reqReadAlias {
				if g1.reqReadAlias == nil {
					g1.reqReadAlias = map[string]string{}
				}
				g1.reqReadAlias[path] = alias
			}
			for d := range g2.dependsOn {
				if d == i {
					continue
				}
				g1.dependsOn[d] = true
				if g2.pipeDeps[d] {
					if g1.pipeDeps == nil {
						g1.pipeDeps = map[int]bool{}
					}
					g1.pipeDeps[d] = true
				}
			}
			for k, gk := range gb.groups {
				if k == i || k == j || !gk.dependsOn[j] {
					continue
				}
				delete(gk.dependsOn, j)
				gk.dependsOn[i] = true
				if gk.pipeDeps[j] {
					delete(gk.pipeDeps, j)
					if gk.pipeDeps == nil {
						gk.pipeDeps = map[int]bool{}
					}
					gk.pipeDeps[i] = true
				}
			}
			delete(gb.byKey, g2.key)
			g2.sel = newDocSel() // empty selection -- pruneEmptyGroups drops the husk
			g2.dependsOn = map[int]bool{}
		}
	}
}

// pruneEmptyGroups drops every group whose printed selection body is empty and remaps the surviving
// groups' dependsOn indices (and byKey). Runs after injectKeys, so a jump group that only carries keys
// is kept (its body is non-empty). Deterministic: keep order is the original index order.
func (gb *obGroupBuilder) pruneEmptyGroups() {
	keep := make([]bool, len(gb.groups))
	oldToNew := make([]int, len(gb.groups))
	var kept []*obGroup
	for i, g := range gb.groups {
		if g.sel.print() == "" {
			oldToNew[i] = -1
			continue
		}
		keep[i] = true
		oldToNew[i] = len(kept)
		kept = append(kept, g)
	}
	if len(kept) == len(gb.groups) {
		return // nothing to prune
	}
	for _, g := range kept {
		remapped := make(map[int]bool, len(g.dependsOn))
		for d := range g.dependsOn {
			if d >= 0 && d < len(keep) && keep[d] {
				remapped[oldToNew[d]] = true
			}
		}
		g.dependsOn = remapped
	}
	byKey := make(map[obGroupKey]int, len(kept))
	for i, g := range kept {
		byKey[g.key] = i
	}
	gb.groups = kept
	gb.byKey = byKey
	// posGroup indices are no longer consulted after this point (emitFields already ran); leave as-is.
}

// topoOrder returns the groups in a stable topological order (every DependsOn strictly earlier),
// tie-breaking on original index -- the same contract as topoOrderGroups for the node-keyed path.
func (gb *obGroupBuilder) topoOrder() []*obGroup {
	n := len(gb.groups)
	indeg := make([]int, n)
	dependents := make([][]int, n)
	for i, g := range gb.groups {
		indeg[i] = len(g.dependsOn)
		for d := range g.dependsOn {
			dependents[d] = append(dependents[d], i)
		}
	}
	order := make([]int, 0, n)
	used := make([]bool, n)
	for len(order) < n {
		pick := -1
		for i := 0; i < n; i++ {
			if !used[i] && indeg[i] == 0 {
				pick = i
				break
			}
		}
		if pick == -1 {
			for i := 0; i < n; i++ {
				if !used[i] {
					used[i] = true
					order = append(order, i)
				}
			}
			break
		}
		used[pick] = true
		order = append(order, pick)
		for _, dep := range dependents[pick] {
			indeg[dep]--
		}
	}
	oldToNew := make([]int, n)
	for newIdx, oldIdx := range order {
		oldToNew[oldIdx] = newIdx
	}
	out := make([]*obGroup, n)
	for newIdx, oldIdx := range order {
		out[newIdx] = gb.groups[oldIdx]
	}
	gb.newIndex = oldToNew
	return out
}

// buildFetches emits one resolve.FetchItem per group in topological order, following buildRawFetches'
// input/representation/QueryPlan contract for postprocess but keyed on the obligation-driven groups and
// their per-position response path and fetch path.
func (gb *obGroupBuilder) buildFetches(h *hypergraph.Hypergraph, groups []*obGroup,
	producedByAll map[hypergraph.NodeID]hypergraph.EdgeID, def *ast.Document,
	varTypes map[string]string, opType ast.OperationType, transport TransportTable) ([]*resolve.FetchItem, error) {

	items := make([]*resolve.FetchItem, 0, len(groups))
	// Per-subgraph type knowledge for the abstract-fragment expansion below (one graph scan).
	sgIdx := buildSgTypeIndex(h)
	for gi, g := range groups {
		// Distributed interface membership (audit union-interface-distributed_0; v1 abstract
		// selection rewriter parity): `... on I { f }` printed into a subgraph whose LOCAL member
		// set for I is missing composed members resolves f only for the locally-declared members --
		// the foreign members' fields silently null although the subgraph resolves them as plain
		// member fields (e.g. a's Oven.id is its @key while only b declares `Oven implements
		// Node`). Expand such fragments per composed concrete member the subgraph can actually
		// serve. A subgraph with complete local membership keeps the interface fragment verbatim.
		expandForeignAbstractFrags(g.sel, def, g.key.subgraph, sgIdx)
		// C-disc re-discrimination: an entity fetch into an interface-DECLARING subgraph selects
		// __typename in its fragment body -- the schema-computed CONCRETE name merges over the
		// interface name an @interfaceObject source reported, so member gates and __typename
		// output see the concrete type. Never for fetches INTO @interfaceObject subgraphs (see
		// entityFragmentNeedsTypename). Set before print.
		if g.jumpEdge != hypergraph.NoEdge && entityFragmentNeedsTypename(h, def, g.key.subgraph, g.entryType) {
			g.sel.typename = true
		}
		body := g.sel.print()
		// The forwarded operation variables THIS document references (sorted, deduped) -- a fetch declares
		// and forwards only its own arguments' variables.
		docVars := collectDocVars(g.sel)
		var doc string
		if g.jumpEdge == hypergraph.NoEdge {
			// Root fetch. The operation keyword follows the operation type (D11 clause 7): `mutation`
			// for a mutation (its top-level fields are Mutation fields executed against the subgraph's
			// Mutation root), `subscription` for a subscription trigger document (D11.12); entity
			// fetches below always stay `query` (entity resolution is a query regardless of operation
			// type).
			kw := "query"
			switch opType {
			case ast.OperationTypeMutation:
				kw = "mutation"
			case ast.OperationTypeSubscription:
				kw = "subscription"
			}
			doc = kw + varDefsHeader(docVars, varTypes) + " { " + body + " }"
		} else {
			// Entity fetch. $representations is always declared; any client-argument variables the
			// re-rooted selection references are appended to the variable definition list.
			header := "query($representations: [_Any!]!"
			if len(docVars) > 0 {
				header += ", " + varDefsList(docVars, varTypes)
			}
			header += ")"
			doc = header + " { _entities(representations: $representations) { ... on " +
				g.entryType + " { " + body + " } } }"
		}
		// BATCH detection: an entity fetch whose attachment path crosses a list boundary (any list-typed
		// hop, top-level list included) must batch -- one representation per array item in a single
		// _entities call -- matching v1's batch-fetch rule. An object-only attachment path stays a single
		// entity fetch. RequiresEntityFetch and RequiresEntityBatchFetch are mutually exclusive
		// (postprocess checks batch first), matching v1.
		isEntity := g.jumpEdge != hypergraph.NoEdge
		isBatch := isEntity && attachCrossesList(g.hops)
		fc := resolve.FetchConfiguration{
			QueryPlan:                &resolve.QueryPlan{Query: doc},
			RequiresEntityFetch:      isEntity && !isBatch,
			RequiresEntityBatchFetch: isBatch,
			// v1 sets SetTemplateOutputToNullOnVariableNull for BOTH entity and batch fetches: a null
			// sibling item renders a null representation the batcher skips (SkipNullItems), and a single
			// entity fetch renders null (not the literal template) when its key variable is null.
			SetTemplateOutputToNullOnVariableNull: isEntity,
		}
		if g.jumpEdge == hypergraph.NoEdge {
			// Client-argument variables forward via ContextVariable (the v1 graphql_datasource contract):
			// each renders a $$N$$ segment reading the request variable at its name. A doc with no
			// variables keeps the exact byte-identical Input the pre-wave-2 root path emitted.
			if len(docVars) == 0 {
				fc.Input = `{"body":{"query":` + embedQuery(doc) + `}}`
			} else {
				segs, vars := forwardedVariables(docVars, 0)
				fc.Input = `{"body":{"query":` + embedQuery(doc) + `,"variables":{` + segs + `}}}`
				fc.Variables = vars
			}
			// v1 parity (graphql_datasource DefaultPostProcessingConfiguration): every fetch selects
			// the subgraph response's `errors` alongside `data` -- omitting it silently swallowed
			// subgraph errors (the audit's expected-subgraph-error class resolved leniently).
			fc.PostProcessing = resolve.PostProcessingConfiguration{
				SelectResponseDataPath:   []string{"data"},
				SelectResponseErrorsPath: []string{"errors"},
			}
		} else {
			headType := g.entryType
			// The representation's field paths are relative to the PRE-JUMP entity object in the source
			// subgraph (srcType); on an @interfaceObject / entity-interface jump that type differs from
			// the head, and stopping the walk-up on the head's type would overrun to the operation root.
			stopType := g.srcType
			if stopType == "" {
				stopType = headType
			}
			jump := h.Edge(g.jumpEdge)
			keySet := map[hypergraph.NodeID]bool{}
			for _, t := range jump.KeyTails {
				keySet[t] = true
			}
			keyTrie := newRepNode()
			reqTrie := newRepNode()
			if gb.usesKeyPipeline(h, g) || g.synthetic {
				// D11.11 / synthetic discriminator: the Key fragment renders from the raw key
				// selection -- distributed tails span subgraphs, and a synthesized group's tails
				// were never walk-covered, so the tail up-walk cannot reconstruct the paths.
				mergeRequiresIntoTrie(keyTrie, parseRequiresSelections(jump.KeySelection))
			} else {
				for _, tail := range jump.Tails {
					// @key tails build the argument-blind Key fragment; @requires tails are rendered from
					// jump.Requires (argument-bearing) into reqTrie below.
					if keySet[tail] || len(jump.KeyTails) == 0 {
						keyTrie.insert(tailFieldPath(h, producedByAll, tail, stopType))
					}
				}
			}
			for _, reqStr := range g.requiresOf(h) {
				mergeRequiresIntoTrie(reqTrie, parseRequiresSelections(reqStr))
			}
			// Value-path indirection: where the shared source document aliased a conflicting
			// requires selection, the representation must READ the value from that alias while still
			// presenting the field by its real name (the target's @requires contract).
			for path, alias := range g.reqReadAlias {
				applyReadAs(reqTrie, strings.Split(path, "."), alias)
			}
			// $$0$$ is the representations object; client-argument variables (if any) follow at $$1$$...
			vars := resolve.Variables{
				resolve.NewResolvableObjectVariable(representationObject(h, g.key.subgraph, headType, keyTrie, reqTrie, def)),
			}
			varsJSON := `"representations":[$$0$$]`
			if len(docVars) > 0 {
				segs, ctxVars := forwardedVariables(docVars, 1)
				varsJSON += "," + segs
				vars = append(vars, ctxVars...)
			}
			fc.Input = `{"body":{"query":` + embedQuery(doc) + `,"variables":{` + varsJSON + `}}}`
			fc.Variables = vars
			// v1 parity (graphql_datasource postProcessing selection): a BATCH entity fetch merges
			// the whole `_entities` array (one item per representation); a SINGLE entity fetch sends
			// exactly one representation and must select item 0 -- merging the one-element array
			// itself into the object position fails at execution ("differing types"), the defect the
			// defer executed-truth harness surfaced on the deferred single-entity re-entry.
			if isBatch {
				fc.PostProcessing = resolve.PostProcessingConfiguration{
					SelectResponseDataPath:   []string{"data", "_entities"},
					SelectResponseErrorsPath: []string{"errors"},
				}
			} else {
				fc.PostProcessing = resolve.PostProcessingConfiguration{
					SelectResponseDataPath:   []string{"data", "_entities", "0"},
					SelectResponseErrorsPath: []string{"errors"},
				}
			}
			fc.QueryPlan.DependsOnFields = []resolve.Representation{{
				Kind:     resolve.RepresentationKindKey,
				TypeName: headType,
				Fragment: "fragment Key on " + headType + " { __typename " + keyTrie.print() + " }",
			}}
			if len(reqTrie.children) > 0 {
				fc.QueryPlan.DependsOnFields = append(fc.QueryPlan.DependsOnFields, resolve.Representation{
					Kind:     resolve.RepresentationKindRequires,
					TypeName: headType,
					Fragment: "fragment Requires on " + headType + " { " + reqTrie.print() + " }",
				})
			}
		}
		deps := make([]int, 0, len(g.dependsOn))
		for d := range g.dependsOn {
			deps = append(deps, gb.newIndex[d])
		}
		sort.Ints(deps)
		// Attach the subgraph's executable transport (DataSource + url/method/header Input envelope) to
		// this fetch (root or entity). A nil/absent table leaves fc shape-only, preserving byte-identical
		// output for the plan-level harnesses that don't exercise transport. A SUBSCRIPTION operation's
		// root fetch is the trigger-to-be (D11.12): it must NOT get the HTTP wire fields -- the trigger's
		// subscription envelope (url/ws flags, no method) is spliced by lowerSubscription instead.
		if !(opType == ast.OperationTypeSubscription && g.jumpEdge == hypergraph.NoEdge) {
			if err := transport.lookup(h.SubgraphName(g.key.subgraph)).attach(&fc); err != nil {
				return nil, err
			}
		}
		sf := &resolve.SingleFetch{
			FetchConfiguration: fc,
			// DeferID (D11.13): the group's defer scope -- postprocess extract_defer_fetches
			// partitions the flat tree on it; 0 (the primary partition) for every fetch of an
			// undeferred operation. A cross-scope DependsOnFetchIDs entry (a deferred entity fetch
			// citing the primary fetch that produced its key) is ordering metadata: execution order
			// across scopes is structural (primary tree first, DeferTree sequencing after).
			FetchDependencies:    resolve.FetchDependencies{FetchID: gi, DependsOnFetchIDs: deps, DeferID: g.key.deferID},
			DataSourceIdentifier: []byte(h.SubgraphName(g.key.subgraph)),
		}
		// FetchInfo (v1 configureFetch parity, minus the per-field coordinate machinery planv2 does
		// not carry): the query-plan printer (resolve fetchtree queryPlan) dereferences Fetch.Info
		// unconditionally -- a nil Info is a router dev-mode panic, not a shape difference. Entity
		// fetches are queries regardless of operation type (D11 clause 7).
		infoOp := opType
		if g.jumpEdge != hypergraph.NoEdge {
			infoOp = ast.OperationTypeQuery
		}
		infoID := transport.lookup(h.SubgraphName(g.key.subgraph)).ID
		if infoID == "" {
			infoID = h.SubgraphName(g.key.subgraph)
		}
		sf.Info = &resolve.FetchInfo{
			DataSourceID:   infoID,
			DataSourceName: h.SubgraphName(g.key.subgraph),
			OperationType:  infoOp,
			QueryPlan:      fc.QueryPlan,
		}
		// M4.1 (info.go): the fetch's top-level coordinates with their authorization flags --
		// what postprocess's collectAuthorizationCoordinates reads on the fetch side, and what
		// the query-plan printer's RootFields block shows. Only under IncludeInfo (v1
		// DisableIncludeInfo parity).
		if gb.info.IncludeInfo {
			sf.Info.RootFields = gb.rootFieldCoords(g, def, opType)
		}
		respPath, fetchPath := renderAttachPath(reverseHops(g.hops))
		items = append(items, resolve.FetchItemWithPath(sf, respPath, fetchPath...))
	}
	return items, nil
}

// --- argument / variable lowering helpers ----------------------------------------------------

// operationVariableTypes maps every variable name declared in the operation to its printed GraphQL
// type ("ID!", "Int", "[String!]!", "UsersFilter!"). After normalization with WithExtractVariables
// every field argument is a variable reference, so this map types every argument a fetch forwards.
// Variable names are unique within an operation; the corpus is single-operation, so a document-wide
// scan is unambiguous.
func operationVariableTypes(operation *ast.Document) map[string]string {
	if operation == nil {
		return nil
	}
	out := make(map[string]string, len(operation.VariableDefinitions))
	for ref := range operation.VariableDefinitions {
		name := operation.VariableDefinitionNameString(ref)
		typeBytes, err := operation.PrintTypeBytes(operation.VariableDefinitions[ref].Type, nil)
		if err != nil {
			continue
		}
		out[name] = string(typeBytes)
	}
	return out
}

// operationResponseType returns the selected operation's type (Query, Mutation, or Subscription) for
// the resolver's GraphQLResponse.Info and the root-document keyword (D11 clause 7); an operation with
// no operation-definition root node defaults to Query. The resolver dereferences
// GraphQLResponse.Info.OperationType unconditionally (resolve.go), so lower MUST populate it -- a nil
// Info is a runtime nil-pointer panic, not just a shape difference.
func operationResponseType(operation *ast.Document) ast.OperationType {
	if operation == nil {
		return ast.OperationTypeQuery
	}
	for ref := range operation.RootNodes {
		if operation.RootNodes[ref].Kind != ast.NodeKindOperationDefinition {
			continue
		}
		if t := operation.OperationDefinitions[operation.RootNodes[ref].Ref].OperationType; t != ast.OperationTypeUnknown {
			return t
		}
	}
	return ast.OperationTypeQuery
}

// collectDocVars walks a group's selection tree and returns the operation variables it references
// (deduped, sorted for deterministic variable-definition headers and Input segments). A fetch declares
// and forwards ONLY the variables its own document uses.
func collectDocVars(sel *docSel) []string {
	seen := map[string]bool{}
	var walk func(s *docSel)
	walk = func(s *docSel) {
		if s == nil {
			return
		}
		for _, f := range s.fields {
			for _, v := range f.argVars {
				seen[v] = true
			}
			walk(f.sub)
		}
		for _, t := range s.fragOrder {
			walk(s.frags[t])
		}
	}
	walk(sel)
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// varDefsList renders "$a: ID!, $limit: Int" for the given variables (already ordered). A variable with
// no known type is skipped rather than emitting `$a: ` -- that surfaces as an invalid document a test
// catches, instead of a silently malformed one (fail loud, not wrong).
func varDefsList(vars []string, varTypes map[string]string) string {
	var parts []string
	for _, v := range vars {
		t := varTypes[v]
		if t == "" {
			continue
		}
		parts = append(parts, "$"+v+": "+t)
	}
	return strings.Join(parts, ", ")
}

// varDefsHeader renders the parenthesized variable-definition header ("($a: ID!)") for a root document,
// or "" when the document forwards no variables.
func varDefsHeader(vars []string, varTypes map[string]string) string {
	if len(vars) == 0 {
		return ""
	}
	return "(" + varDefsList(vars, varTypes) + ")"
}

// forwardedVariables builds the JSON variable segments ("a":$$1$$,"limit":$$2$$) and the matching
// resolve.Variables (a ContextVariable each, following v1's graphql_datasource contract: each reads the
// request variable at its own name). startIndex is the first $$N$$ slot -- 0 for a root fetch, 1 for an
// entity fetch (slot 0 is the representations object).
func forwardedVariables(vars []string, startIndex int) (string, resolve.Variables) {
	var segs []string
	out := make(resolve.Variables, 0, len(vars))
	for i, v := range vars {
		segs = append(segs, strconv.Quote(v)+":$$"+strconv.Itoa(startIndex+i)+"$$")
		out = append(out, &resolve.ContextVariable{
			Path:     []string{v},
			Renderer: resolve.NewJSONVariableRenderer(),
		})
	}
	return strings.Join(segs, ","), out
}

// embedQuery renders a fetch document as a JSON string value ready to splice after `"query":`. The
// common case -- a normalized document whose only argument values are variable references -- carries no
// characters needing JSON escaping, so it is embedded verbatim (keeping every existing golden Input
// stable). A document that DOES carry a string/enum literal (an @requires literal argument like
// `price(currency: "USD")`, or a non-extracted operation literal) is JSON-encoded so the embedded
// quotes/backslashes are escaped -- this is where that escaping happens, at Input assembly, where
// documents become JSON.
func embedQuery(doc string) string {
	if !strings.ContainsAny(doc, "\"\\\n\r\t") {
		return `"` + doc + `"`
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return `"` + doc + `"`
	}
	return string(b)
}

// --- small helpers ----------------------------------------------------------------------------

// joinPath appends a response-key segment to a dotted response path.
func joinPath(base, seg string) string {
	if base == "" {
		return seg
	}
	return base + "." + seg
}

// relPathSegments returns child's response path with parent's prefix stripped, split on ".". parent
// must be a prefix of child ("" is a prefix of everything). When parent == child -- a same-depth jump
// CHAIN, where the producer and jump groups share one entry object (e.g. an @override->@requires relay
// a->b->c all landing on the same response position, or a null-key upc->id relay) -- the relative path is
// empty and the keys inject directly into the parent's entry selection, never re-nested under a spurious
// repeat of the entry field.
func relPathSegments(parent, child string) []string {
	if parent == child {
		return nil
	}
	rest := child
	if parent != "" {
		rest = strings.TrimPrefix(child, parent+".")
	}
	if rest == "" {
		return nil
	}
	return strings.Split(rest, ".")
}

// attachCrossesList reports whether an entity fetch's response-attachment path crosses a list boundary:
// any hop is list-typed (the landing objects are array items, or are nested under a list). This is v1's
// batch-fetch condition: a list-crossing attachment resolves plural entity instances and must BATCH (one
// representation per item), while an object-only path resolves a single instance and stays a single
// entity fetch.
func attachCrossesList(hops []attachHop) bool {
	for _, hp := range hops {
		if hp.array {
			return true
		}
	}
	return false
}

// reverseHops copies hops so renderAttachPath (which reverses again, expecting leaf->root input) emits
// them root->leaf. walkSpine collects hops root->leaf, and renderAttachPath reverses its input, so this
// double-reverse leaves the wire order correct.
func reverseHops(hops []attachHop) []attachHop {
	out := make([]attachHop, len(hops))
	for i, hp := range hops {
		out[len(hops)-1-i] = hp
	}
	return out
}

// obligationChildren builds the parent->children index and the root list from the obligation slice
// (mirroring buildResponseObject's grouping so the two paths agree on the obligation-tree structure).
func obligationChildren(obs []obligation.Obligation) (map[obligation.ObID][]obligation.ObID, []obligation.ObID) {
	children := map[obligation.ObID][]obligation.ObID{}
	var roots []obligation.ObID
	for _, ob := range obs {
		if ob.Parent == obligation.NoParent || ob.Parent == ob.ID {
			roots = append(roots, ob.ID)
			continue
		}
		children[ob.Parent] = append(children[ob.Parent], ob.ID)
	}
	return children, roots
}

// liveObligations marks every obligation with a covered goal in its subtree -- the obligations that
// appear in a FETCH document. A narrowed-away (Cover.Nulls) member has no covered goal, so it is NOT
// live (no subgraph query selects it) yet still renders in the response object as a null (handled by
// buildResponseObject over the full tree).
func liveObligations(children map[obligation.ObID][]obligation.ObID,
	roots []obligation.ObID, res *search.Result, o *obligation.Tree) map[obligation.ObID]bool {

	coveredOb := map[obligation.ObID]bool{}
	for _, g := range o.Goals() {
		if _, ok := res.Cover.Selected[g]; ok {
			coveredOb[o.Ob(g).ID] = true
		}
	}
	live := map[obligation.ObID]bool{}
	var visit func(id obligation.ObID) bool
	visit = func(id obligation.ObID) bool {
		l := coveredOb[id]
		for _, c := range children[id] {
			if visit(c) {
				l = true
			}
		}
		live[id] = l
		return l
	}
	for _, r := range roots {
		visit(r)
	}
	return live
}

// --- distributed interface-membership expansion (see buildFetches) -------------------------------

// sgTypeIndex is the per-subgraph type knowledge the abstract-fragment expansion reads: each
// subgraph's locally-declared members per abstract type (TypeMove edges) and its resolvable
// field coordinates (Field nodes).
type sgTypeIndex struct {
	localMembers map[hypergraph.SubgraphID]map[string]map[string]bool
	hasField     map[hypergraph.SubgraphID]map[string]bool
}

func buildSgTypeIndex(h *hypergraph.Hypergraph) *sgTypeIndex {
	idx := &sgTypeIndex{
		localMembers: map[hypergraph.SubgraphID]map[string]map[string]bool{},
		hasField:     map[hypergraph.SubgraphID]map[string]bool{},
	}
	for i := 0; i < h.NumEdges(); i++ {
		e := h.Edge(hypergraph.EdgeID(i))
		if e.Kind != hypergraph.EdgeTypeMove || len(e.Tails) != 1 {
			continue
		}
		from := h.Node(e.Tails[0])
		byU := idx.localMembers[from.Subgraph]
		if byU == nil {
			byU = map[string]map[string]bool{}
			idx.localMembers[from.Subgraph] = byU
		}
		if byU[from.Type] == nil {
			byU[from.Type] = map[string]bool{}
		}
		for _, m := range e.Members {
			byU[from.Type][m] = true
		}
		byU[from.Type][h.Node(e.Head).Type] = true
	}
	for i := 0; i < h.NumNodes(); i++ {
		n := h.Node(hypergraph.NodeID(i))
		if n.Kind != hypergraph.NodeField || n.Scope != "" {
			continue
		}
		f := idx.hasField[n.Subgraph]
		if f == nil {
			f = map[string]bool{}
			idx.hasField[n.Subgraph] = f
		}
		f[n.Type+"."+n.Field] = true
	}
	return idx
}

// expandForeignAbstractFrags rewrites, in place, every inline fragment on an abstract type whose
// LOCAL member set in subgraph sg is missing composed members: the fragment's selection is
// re-emitted under each composed concrete member whose every top-level field the subgraph
// resolves; members it cannot serve are dropped (their data was never obtainable there -- exactly
// the silent null this rewrites away). Fragments the subgraph fully understands stay verbatim
// (byte-identical documents on every complete-membership path). Recurses into nested selections.
func expandForeignAbstractFrags(sel *docSel, def *ast.Document, sg hypergraph.SubgraphID, idx *sgTypeIndex) {
	if sel == nil {
		return
	}
	for _, f := range sel.fields {
		expandForeignAbstractFrags(f.sub, def, sg, idx)
	}
	for _, cond := range append([]string(nil), sel.fragOrder...) {
		fsel := sel.frags[cond]
		expandForeignAbstractFrags(fsel, def, sg, idx)
		if !isAbstractType(def, cond) {
			continue
		}
		var composed []string
		for _, m := range refinementGate(def, cond) {
			if string(m) != cond {
				composed = append(composed, string(m))
			}
		}
		if len(composed) == 0 {
			continue
		}
		local := idx.localMembers[sg][cond]
		complete := true
		for _, m := range composed {
			if !local[m] {
				complete = false
				break
			}
		}
		if complete {
			continue // the subgraph knows every composed member: the interface fragment is exact
		}
		// Remove `... on cond` and re-emit per serviceable concrete member.
		delete(sel.frags, cond)
		for i, c := range sel.fragOrder {
			if c == cond {
				sel.fragOrder = append(sel.fragOrder[:i], sel.fragOrder[i+1:]...)
				break
			}
		}
		for _, m := range composed {
			serviceable := true
			for _, f := range fsel.fields {
				if !idx.hasField[sg][m+"."+f.name] {
					serviceable = false
					break
				}
			}
			if !serviceable {
				continue
			}
			mergeDocSelInto(sel.frag(m), fsel)
		}
		sel.typenameWeak = true // the members need the discriminator the interface fragment implied
	}
}

// mergeDocSelInto copies src's selections into dst (response-key keyed, docField pointers shared --
// print-only reuse), merging nested selections and fragment sets recursively.
func mergeDocSelInto(dst, src *docSel) {
	if src.typename {
		dst.typename = true
	}
	if src.typenameWeak {
		dst.typenameWeak = true
	}
	for _, f := range src.fields {
		key := f.name
		if f.alias != "" {
			key = f.alias
		}
		if existing, ok := dst.byName[key]; ok {
			if f.sub != nil {
				if existing.sub == nil {
					existing.sub = newDocSel()
				}
				mergeDocSelInto(existing.sub, f.sub)
			}
			continue
		}
		dst.byName[key] = f
		dst.fields = append(dst.fields, f)
	}
	for _, t := range src.fragOrder {
		mergeDocSelInto(dst.frag(t), src.frags[t])
	}
}
