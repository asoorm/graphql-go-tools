// Package lower turns the plan the search produced (a "cover": the set of graph edges chosen to
// answer the query) into the v1 plan output the resolver runs: fetch groups, entity representations,
// the exact client response shape, cross-subgraph output-type aliasing, __typename gating, and
// response-only nulls for fields the query narrowed away. It is the boundary layer -- the one planv2
// package allowed to import the v1 `plan`/`resolve` types -- and it must NOT become a second planner:
// search has already fixed every routing and cost decision; lowering only re-expresses that cover in
// the resolver's fetch-tree shape and reuses the existing postprocess step untouched to schedule it.
//
// The subtle risk here is a lowering bug that passes plan-level tests yet corrupts real responses.
// Two things guard against it: the response tree mirrors the client's selection tree verbatim, and
// response-only nulls and __typename gates are emitted exactly as the resolver renders them (a member
// field that is present but gated on a concrete __typename the fetch never returns comes out null).
// Spec: FORMAL_SPEC Section 7.
package lower

import (
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// fetchGroup is one fetch's worth of the plan: the largest connected run of the plan that stays in a
// single subgraph, bounded by entity jumps and operation roots. Each group becomes exactly one fetch.
type fetchGroup struct {
	Subgraph  hypergraph.SubgraphID
	Edges     []hypergraph.EdgeID // Field/Descent/TypeMove edges inside the group
	Jump      hypergraph.EdgeID   // the EntityJump opening this group; hypergraph.NoEdge = root group
	Entry     hypergraph.NodeID   // object/root node the group's selection set is printed from
	RepKeys   []string            // representation key coordinates "Type.field", sorted (from Jump tails)
	DependsOn []int               // indices of groups that resolve this group's tails
}

// buildGroups partitions the cover into fetch groups and wires up their ordering: group u must run
// before group v when an edge in v's group depends on a value produced by an edge in u's group. The
// returned groupOf maps every non-jump cover edge to its group index -- the per-document scope the
// aliaser needs to spot response-key collisions.
func buildGroups(h *hypergraph.Hypergraph, cover *search.Cover) ([]*fetchGroup, map[hypergraph.EdgeID]int) {
	producedBy := map[hypergraph.NodeID]hypergraph.EdgeID{} // head -> producing cover edge
	for _, e := range cover.Edges {
		producedBy[h.Edge(e).Head] = e
	}
	groupOf := map[hypergraph.EdgeID]int{}  // ownership memo (also grows during pass 2/3 walks)
	opening := map[hypergraph.EdgeID]bool{} // pass-1 boundary edges, already placed in their group
	var groups []*fetchGroup
	// Pass 1: every EntityJump opens a group; every root-entering Field edge opens a group.
	for _, e := range cover.Edges {
		edge := h.Edge(e)
		isRootEntering := edge.Kind == hypergraph.EdgeField &&
			h.Node(edge.Tails[0]).Kind == hypergraph.NodeRoot
		if edge.Kind == hypergraph.EdgeEntityJump || isRootEntering {
			g := &fetchGroup{Subgraph: h.Node(edge.Head).Subgraph, Jump: hypergraph.NoEdge}
			if edge.Kind == hypergraph.EdgeEntityJump {
				g.Jump = e
				g.Entry = edge.Head // the entity object the jump lands on
				for _, t := range edge.Tails {
					n := h.Node(t) // the jump's tails become the entity representation
					g.RepKeys = append(g.RepKeys, n.Type+"."+n.Field)
				}
				sort.Strings(g.RepKeys)
			} else {
				g.Entry = edge.Tails[0] // the operation root the fetch selects off of
				g.Edges = append(g.Edges, e)
			}
			groupOf[e] = len(groups)
			opening[e] = true
			groups = append(groups, g)
		}
	}
	// Pass 2: assign every in-subgraph edge to the group whose walk produced its tail --
	// follow producedBy upward until an opening edge (jump or root-entering Field) is hit.
	var owner func(e hypergraph.EdgeID) int
	owner = func(e hypergraph.EdgeID) int {
		if gi, ok := groupOf[e]; ok {
			return gi
		}
		tail := h.Edge(e).Tails[0] // in-subgraph edges have a single tail
		gi := owner(producedBy[tail])
		groupOf[e] = gi
		return gi
	}
	for _, e := range cover.Edges {
		// Skip only the pass-1 boundary edges (already placed) -- NOT everything in groupOf: the
		// owner memo can pre-mark an edge while resolving a consumer that sorts before it (e.g. a
		// TypeMove whose Field-edge consumer has a lower EdgeID), and such an edge must still be
		// appended to its group here or its selection silently vanishes from the fetch document.
		if opening[e] || h.Edge(e).Kind == hypergraph.EdgeEntityJump {
			continue
		}
		gi := owner(e)
		groups[gi].Edges = append(groups[gi].Edges, e)
	}
	// Pass 3: dependencies -- a jump group depends on whichever group(s) resolve its tails.
	for gi, g := range groups {
		if g.Jump == hypergraph.NoEdge {
			continue
		}
		seen := map[int]bool{}
		for _, t := range h.Edge(g.Jump).Tails {
			pg := owner(producedBy[t])
			if pg != gi && !seen[pg] {
				seen[pg] = true
				g.DependsOn = append(g.DependsOn, pg)
			}
		}
		sort.Ints(g.DependsOn)
	}
	return groups, groupOf
}

// merge (dedup identical fetches + a bounded co-location pass) lives in merge.go.

// topoOrderGroups reorders fetch groups so every group comes before the groups that depend on it,
// remapping each group's DependsOn indices and the groupOf edge->index map into the new order. It
// uses Kahn's algorithm, breaking ties by original index so the emission order is deterministic and,
// for input that is already in order, unchanged. This is what lets buildRawFetches emit fetch IDs
// where every dependency refers to a strictly earlier fetch. Any leftover cycle (not expected -- the
// dependency graph is a DAG by construction) is appended in original order as a safety net.
func topoOrderGroups(groups []*fetchGroup, groupOf map[hypergraph.EdgeID]int) ([]*fetchGroup, map[hypergraph.EdgeID]int) {
	n := len(groups)
	indeg := make([]int, n)        // number of not-yet-emitted dependencies of each group
	dependents := make([][]int, n) // dependents[d] = groups that DependsOn d
	for i, g := range groups {
		indeg[i] = len(g.DependsOn)
		for _, d := range g.DependsOn {
			dependents[d] = append(dependents[d], i)
		}
	}
	order := make([]int, 0, n) // order[newIdx] = old index
	used := make([]bool, n)
	for len(order) < n {
		pick := -1
		for i := 0; i < n; i++ {
			if !used[i] && indeg[i] == 0 {
				pick = i
				break // smallest ready original index -> deterministic, stable for topological inputs
			}
		}
		if pick == -1 { // cycle safety: emit the rest in original order
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
	out := make([]*fetchGroup, n)
	for newIdx, oldIdx := range order {
		g := groups[oldIdx]
		nd := make([]int, len(g.DependsOn))
		for j, d := range g.DependsOn {
			nd[j] = oldToNew[d]
		}
		sort.Ints(nd)
		g.DependsOn = nd
		out[newIdx] = g
	}
	remapped := make(map[hypergraph.EdgeID]int, len(groupOf))
	for e, gi := range groupOf {
		remapped[e] = oldToNew[gi]
	}
	return out, remapped
}

// LowerConfig selects which of two lowering paths runs. The default (zero value) is the
// obligation-driven path, which drives grouping and document printing from the obligation tree and
// the per-field routes, so sibling and self-referential response positions each get their own fetch.
// See obligation_driven.go.
//
// LegacyNodeKeyedGrouping is an emergency escape hatch kept for one release cycle: it routes back
// through the original grouping (buildGroups + merge), which collapses two response positions that
// resolve to the same node onto one route -- the exact bug the default path fixes. It exists only so a
// caller can fall back if the new default causes an unforeseen regression, and it is scheduled for
// removal.
type LowerConfig struct {
	// LegacyNodeKeyedGrouping routes Lower through the old node-keyed grouping instead of the
	// obligation-driven path. Default (false) is the obligation-driven path. Escape hatch only -- see
	// the type doc.
	LegacyNodeKeyedGrouping bool
}

// Lower maps a search Result to the v1 plan output: grouping, ordering, the response shape (including
// response-only nulls), and cross-subgraph aliasing. It runs the obligation-driven path (the
// zero-value config); lowerWithConfig selects the legacy escape hatch under
// LowerConfig{LegacyNodeKeyedGrouping: true}.
func Lower(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document) (*plan.SynchronousResponsePlan, error) {
	return lowerWithConfig(h, o, res, operation, definition, LowerConfig{}, nil, InfoConfig{})
}

// LowerExecutable is Lower plus transport: it attaches each fetch's HTTP details (the source plus the
// url/method/header envelope) from the per-subgraph TransportTable, producing an executable plan
// rather than a shape-only one. planv2.Plan calls this with a table derived from the configuration; a
// nil table behaves exactly like Lower (so the plan-level audit/differential harnesses stay
// transport-free and byte-identical).
func LowerExecutable(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable) (*plan.SynchronousResponsePlan, error) {
	return lowerWithConfig(h, o, res, operation, definition, LowerConfig{}, transport, InfoConfig{})
}

// LowerExecutableWithInfo is LowerExecutable plus the M4.1 Info emission (info.go): per-field
// resolve.FieldInfo on the response tree and FetchInfo.RootFields on fetches, per the InfoConfig.
// The facade calls this with IncludeInfo = !config.DisableIncludeInfo; a zero InfoConfig behaves
// exactly like LowerExecutable.
func LowerExecutableWithInfo(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable, info InfoConfig) (*plan.SynchronousResponsePlan, error) {
	return lowerWithConfig(h, o, res, operation, definition, LowerConfig{}, transport, info)
}

// internalTypenameKey is the engine-internal response key the defer normalization aliases its
// placeholder `__typename` to (`__internal_typename: __typename`, literal.INTERNAL_TYPENAME). The
// double-underscore prefix is spec-reserved, so no client field can legitimately carry the key; it
// is excluded from the client response shape (D11.13, v1 skipFieldRefs parity).
const internalTypenameKey = "__internal_typename"

// LowerDeferExecutable is LowerExecutable for a query whose obligation tree carries @defer records
// (FORMAL_SPEC D11.13): the same obligation-driven lowering runs -- scope variants partition the
// fetches, the response tree carries the DeferField stamps -- and the result is wrapped in v1's
// DeferResponsePlan encoding with the tree's defer records re-encoded as resolve.DeferDescriptors,
// so the untouched postprocess pipeline (extract_defer_fetches, build_defer_tree) and the resolve
// incremental-delivery machinery execute it unchanged. The facade routes here when o.Defers() is
// non-empty; calling it on a defer-free tree is a caller bug (the plan would announce nothing).
func LowerDeferExecutable(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable) (*plan.DeferResponsePlan, error) {
	return LowerDeferExecutableWithInfo(h, o, res, operation, definition, transport, InfoConfig{})
}

// LowerDeferExecutableWithInfo is LowerDeferExecutable plus the M4.1 Info emission (see
// LowerExecutableWithInfo); a zero InfoConfig behaves exactly like LowerDeferExecutable.
func LowerDeferExecutableWithInfo(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable, info InfoConfig) (*plan.DeferResponsePlan, error) {
	sp, err := lowerObligationDriven(h, o, res, operation, definition, transport, info)
	if err != nil {
		return nil, err
	}
	descriptors := make(map[int]resolve.DeferDescriptor, len(o.Defers()))
	for id, d := range o.Defers() {
		descriptors[id] = resolve.DeferDescriptor{ID: d.ID, ParentID: d.ParentID, Label: d.Label, Path: d.Path}
	}
	return &plan.DeferResponsePlan{
		Response: &resolve.GraphQLDeferResponse{
			Response:         sp.Response,
			DeferDescriptors: descriptors,
		},
	}, nil
}

// LowerWithConfig is Lower with an explicit LowerConfig, exported only so tests can select the legacy
// escape hatch on real fixtures and pin its byte-identical output. Production code calls Lower. This
// is not a stable API -- it just keeps the escape hatch testable from an external test package without
// an import cycle for the one release cycle it survives.
func LowerWithConfig(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, cfg LowerConfig) (*plan.SynchronousResponsePlan, error) {
	return lowerWithConfig(h, o, res, operation, definition, cfg, nil, InfoConfig{})
}

// lowerWithConfig chooses between the obligation-driven path (the default) and the legacy escape
// hatch. The escape hatch is kept for one release cycle and is exercised only by its byte-identical
// guard test; it never emits the M4.1 Info surfaces (the facade always runs the default path).
func lowerWithConfig(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, cfg LowerConfig, transport TransportTable, info InfoConfig) (*plan.SynchronousResponsePlan, error) {

	if !cfg.LegacyNodeKeyedGrouping {
		// The obligation-driven path consults the operation for two things the response tree doesn't
		// carry: the operation type (query vs mutation, for the root fetch keyword) and each variable's
		// type (to declare `query($v: T)` headers in fetch documents that forward arguments).
		return lowerObligationDriven(h, o, res, operation, definition, transport, info)
	}

	_ = operation // legacy path: the response tree comes from the obligation tree; the raw AST is unused.

	groups, _ := buildGroups(h, res.Cover)
	// Merge: drop identical fetches, then one bounded co-location pass. Returns the merged groups AND a
	// fresh edge->group map over the MERGED documents -- the aliaser must decide response-key collisions
	// against the post-merge documents, not the pre-merge grouping.
	groups, groupOf := merge(h, o, res, groups)
	// Fetches must be emitted in dependency order (a fetch may only depend on earlier fetches).
	// buildGroups/merge index groups in cover-edge order, which is only topological by coincidence -- an
	// entity-jump chain whose deeper fetch has a higher index (me -> reviews -> products -> inventory,
	// where reviews sorts last) breaks it. Reorder into a stable topological order and remap the maps.
	groups, groupOf = topoOrderGroups(groups, groupOf)

	// Aliasing (alias.go): give each colliding same-document sibling selection a fetch-side alias, plus
	// a map from alias back to the client key that buildResponseObject reads (the field's client name
	// stays put; only its value path points at the alias, so the aliased data lands at the client key).
	aliases := assignAliases(h, o, res.Cover, definition, groupOf)

	// The response object tree is exactly the client's selection tree -- walk the obligation tree, whose
	// response keys and nesting mirror the query. A covered field and a narrowed-away field produce the
	// same shape node; the narrowed one is present, gated on its concrete __typename, with no fetch
	// producing it, so the resolver renders it as null. The composed definition types each scalar leaf
	// (String/Int/Float/Boolean/custom) so the resolver reads it with the matching typed walker rather
	// than the string walker (which errors on non-string JSON -- a Float leaf would otherwise fail).
	data := buildResponseObject(h, o, aliases, definition, nil)

	// One fetch per group, in dependency order. A jump group carries its representation both as the
	// resolvable-object input variable (what the loader renders into `representations`) and as the
	// split @key/@requires query-plan fragments; @requires fields ride as fetch inputs and are never
	// visible to the client.
	raw, err := buildRawFetches(h, res.Cover, groups, aliases, definition, transport)
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

// buildResponseObject renders the response object tree from the obligation tree, matching the client's
// selection exactly: each obligation's response key becomes the field's client key, the nesting
// mirrors the query, and every abstract-refinement obligation is flattened into a __typename gate on
// the fields selected under it. A covered field and a narrowed-away field render to the same shape
// node; the only difference is whether a fetch produces the value -- a narrowed field has none, so it
// comes out null.
//
// Top-level obligations are those whose parent is the NoParent sentinel (a self-loop is also honored
// defensively) -- NOT `Parent == 0`, which is also every child of obligation 0: the old conflation
// misfiled every top-level field after the first under the first root (`{ a { x } b { y } }` became
// `a { x b { y } }`).
//
// The alias map supplies the response mapping: an aliased field keeps its client key as the field
// name but reads its value at the alias path -- the fetch returns the data under the alias.
// h supplies the entity-interface fact for possible-type completion (nil tolerated: no completion).
// em carries the M4.1 per-obligation FieldInfo emission (info.go); nil emits none (the legacy path
// and the zero-InfoConfig entry points).
func buildResponseObject(h *hypergraph.Hypergraph, o *obligation.Tree, am *aliasMap, def *ast.Document, em *infoEmission) *resolve.Object {
	obs := o.Obligations()
	children := map[obligation.ObID][]obligation.ObID{}
	var roots []obligation.ObID
	for _, ob := range obs {
		if ob.Parent == obligation.NoParent || ob.Parent == ob.ID {
			roots = append(roots, ob.ID)
			continue
		}
		children[ob.Parent] = append(children[ob.Parent], ob.ID)
	}
	return &resolve.Object{Fields: renderFields(h, obs, children, am, roots, nil, def, em)}
}

// renderFields turns a list of sibling obligations into response fields under an optional __typename
// gate (OnTypeNames). Refine obligations do not emit a node of their own; they contribute their
// concrete type as the gate for the fields nested inside them.
func renderFields(h *hypergraph.Hypergraph, obs []obligation.Obligation, children map[obligation.ObID][]obligation.ObID,
	am *aliasMap, ids []obligation.ObID, gate [][]byte, def *ast.Document, em *infoEmission) []*resolve.Field {

	var out []*resolve.Field
	for _, id := range ids {
		ob := obs[id]
		switch ob.Kind {
		case obligation.Refine:
			// A refinement (`... on C`) gates the fields under it on the concrete type C's __typename.
			//
			// When C is itself abstract (an interface refinement under an abstract parent), the gate has
			// to be C's concrete implementers, not C: the resolver matches the gate by exact equality
			// against the runtime __typename, which is always a concrete type name, so a gate naming the
			// interface can never match and the whole subtree silently nulls. `... on C` means "applies
			// to any object whose type implements C", which is exactly this expanded member gate (v1
			// expands interface fragments to their implementers the same way).
			childGate := refinementGate(def, ob.Concrete)
			out = append(out, renderFields(h, obs, children, am, children[id], childGate, def, em)...)
		case obligation.Typename:
			// The engine-internal defer placeholder (`__internal_typename: __typename`, injected by
			// the defer normalization so an all-deferred selection set still fetches non-empty) is
			// excluded from the client response shape -- v1 skipFieldRefs parity (D11.13). Its
			// SELECTION still rides the owning scope's document via emitFields' typename marking.
			if ob.RespKey == internalTypenameKey {
				continue
			}
			// The response key is the client key (RespKey) -- the alias when one is given
			// (`typename: __typename`), the meta-field name otherwise. Emitting ob.Field ("__typename")
			// here dropped the alias, so an aliased __typename rendered under the wrong key and vanished
			// from the response (the Field branch already keys Name on RespKey).
			//
			// v1 parity (visitor.go EnterField TYPENAME branch): __typename selected DIRECTLY on a
			// root operation type (Query/Mutation/Subscription) is a compile-time constant -- the root
			// type name never varies at runtime -- so v1 emits resolve.StaticString{Value: <TypeName>}.
			// Everywhere else (object/interface/union positions) the value is read from the fetch
			// response at the client key, so v1 emits resolve.String{IsTypeName: true}. Mirror both.
			out = append(out, &resolve.Field{
				Name:        []byte(ob.RespKey),
				OnTypeNames: gate,
				Value:       typenameLeaf(def, ob.Type, ob.RespKey),
				Info:        em.fieldInfoFor(ob.ID),
			})
		case obligation.Field:
			// The client key is always the field name; an aliased field reads its value at the alias
			// instead.
			path := ob.RespKey
			if a := am.byOb[ob.ID]; a != "" {
				path = a
			}
			f := &resolve.Field{Name: []byte(ob.RespKey), OnTypeNames: gate, Info: em.fieldInfoFor(ob.ID)}
			// D11.13: a deferred field carries its scope as a DeferField stamp -- the renderer (not
			// the shape) skips it in the initial response and emits it in the matching increment.
			// DeferID is only ever non-zero on trees built from query operations (FS-DEF-7 gate in
			// obligation.Build), so mutation/subscription plans never carry stamps.
			if ob.DeferID != 0 {
				f.Defer = &resolve.DeferField{DeferID: ob.DeferID}
			}
			kids := children[id]
			// A list-typed field lowers to a resolve.Array wrapping the item node (object or typed
			// scalar), one Array per list dimension. The outermost Array carries the field's path; inner
			// arrays and the item read the enclosing element directly (nil path), matching v1. Without
			// this a list leaf flattens to a bare scalar and the resolver renders a single value where
			// the client expects a JSON array.
			depth := listNesting(def, ob.Type, ob.Field)
			innerPath := []string{path}
			if depth > 0 {
				innerPath = nil // the element value reads its array item directly
			}
			var val resolve.Node
			if hasSelectableChild(obs, kids) {
				childObj := &resolve.Object{
					Path:     innerPath,
					Nullable: true,
					Fields:   renderFields(h, obs, children, am, kids, nil, def, em),
				}
				// v1 parity (visitor resolveFieldValue): every composite response object carries its
				// schema type name and the set of CLIENT-visible possible runtime types -- the
				// resolver COMPLETES a value whose runtime __typename is not possible (an
				// @inaccessible member, or a corrupted subgraph value) to null instead of leaking it.
				if tn := namedFieldType(def, ob.Type, ob.Field); tn != "" {
					childObj.TypeName = tn
					childObj.PossibleTypes = possibleTypes(def, h, tn)
				}
				val = childObj
			} else {
				// Typed scalar leaf: the resolver dispatches on node kind, so a Float/Int/Boolean
				// leaf MUST be the matching typed node or walkString hard-errors on its JSON value.
				val = leafValue(def, ob.Type, ob.Field, innerPath)
			}
			for i := 0; i < depth; i++ {
				p := []string(nil)
				if i == depth-1 {
					p = []string{path} // outermost Array carries the field path
				}
				val = &resolve.Array{Path: p, Nullable: true, Item: val}
			}
			f.Value = val
			out = append(out, f)
		}
	}
	// D11.8: same-key member variants stay SIBLING fields with their own gates -- exactly v1's
	// visitor output. The depth-correct fold is postprocess merge_fields' (it propagates a gate onto
	// folded children as ParentOnTypeNames records carrying the DEPTH of the discriminating
	// ancestor, which the resolver evaluates against the right object). An earlier lowering-side
	// merge folded gated composites into the ungated sibling with the gate at the child's own depth,
	// where it matched the child's enclosing object instead of the discriminating parent -- silently
	// dropping every folded member field at runtime (the executed-truth Abstract_object family).
	return out
}

// possibleTypes returns the CLIENT-visible possible runtime type names of typeName in the composed
// definition (v1 visitor parity): an object type is possible as itself; an interface/union expands
// to its non-@inaccessible implementers/members, plus the interface itself when it is an entity
// interface or @interfaceObject interface in any subgraph (h; nil-tolerant) -- an interface-object
// subgraph legitimately reports the interface name as the runtime __typename. Returns nil for an
// unknown type (no completion -- hand-built fixtures without schema entries).
func possibleTypes(def *ast.Document, h *hypergraph.Hypergraph, typeName string) map[string]struct{} {
	if def == nil {
		return nil
	}
	node, ok := def.Index.FirstNodeByNameStr(typeName)
	if !ok {
		return nil
	}
	out := map[string]struct{}{}
	switch node.Kind {
	case ast.NodeKindObjectTypeDefinition:
		out[typeName] = struct{}{}
	case ast.NodeKindInterfaceTypeDefinition:
		for objRef := range def.ObjectTypeDefinitions {
			name := def.ObjectTypeDefinitionNameString(objRef)
			if n, ok := def.Index.FirstNodeByNameStr(name); ok && def.NodeImplementsInterface(n, ast.ByteSlice(typeName)) {
				if typeInaccessible(def, name) {
					continue
				}
				out[name] = struct{}{}
			}
		}
		if h != nil && h.IsEntityInterface(typeName) {
			out[typeName] = struct{}{}
		}
	case ast.NodeKindUnionTypeDefinition:
		if names, ok := def.UnionTypeDefinitionMemberTypeNames(node.Ref); ok {
			for _, name := range names {
				if typeInaccessible(def, name) {
					continue
				}
				out[name] = struct{}{}
			}
		}
	default:
		return nil // scalar/enum/unknown -- not a composite position
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// typeInaccessible reports whether an object type carries @inaccessible in the composed definition
// (v1 isInaccesibleType parity; a client schema usually omits such types entirely, making this a
// defensive second check).
func typeInaccessible(def *ast.Document, typeName string) bool {
	node, ok := def.Index.FirstNodeByNameStr(typeName)
	if !ok || node.Kind != ast.NodeKindObjectTypeDefinition {
		return false
	}
	if !def.ObjectTypeDefinitions[node.Ref].HasDirectives {
		return false
	}
	return def.ObjectTypeDefinitions[node.Ref].Directives.HasDirectiveByName(def, "inaccessible")
}

// refinementGate renders the __typename gate for a refinement `... on C`. A concrete C gates on
// itself. An abstract C (interface/union) gates on its concrete implementers/members in the composed
// definition, in definition order -- the resolver matches gates against the runtime __typename by
// exact equality, and `... on C` applies to every object type that implements C. Falls back to [C]
// when C is not in the definition.
func refinementGate(def *ast.Document, concrete string) [][]byte {
	if def == nil {
		return [][]byte{[]byte(concrete)}
	}
	node, ok := def.Index.FirstNodeByNameStr(concrete)
	if !ok {
		return [][]byte{[]byte(concrete)}
	}
	var members []string
	switch node.Kind {
	case ast.NodeKindUnionTypeDefinition:
		if names, ok := def.UnionTypeDefinitionMemberTypeNames(node.Ref); ok {
			members = names
		}
	case ast.NodeKindInterfaceTypeDefinition:
		for objRef := range def.ObjectTypeDefinitions {
			name := def.ObjectTypeDefinitionNameString(objRef)
			if n, ok := def.Index.FirstNodeByNameStr(name); ok && def.NodeImplementsInterface(n, ast.ByteSlice(concrete)) {
				members = append(members, name)
			}
		}
	default:
		return [][]byte{[]byte(concrete)} // concrete object type -- gate on itself (the common case)
	}
	if len(members) == 0 {
		return [][]byte{[]byte(concrete)} // defensive: an abstract with no modelled members
	}
	out := make([][]byte, 0, len(members))
	for _, m := range members {
		out = append(out, []byte(m))
	}
	return out
}

// NOTE (D11.8): the former mergeAbstractRefinementFields/mergeFieldInto lowering-side merge of
// same-key gated/ungated sibling fields was retired. It propagated the member gate onto folded
// children at the CHILD's own depth (OnTypeNames), where the resolver evaluates it against the
// child's enclosing object -- never the discriminating parent -- so every folded member field
// silently dropped at runtime. Same-key member variants now stay sibling fields (v1 parity) and
// postprocess merge_fields performs the depth-correct fold via ParentOnTypeNames. Plan-level
// oracles union same-key siblings for presence (audit runner planFieldsByKey).

// typenameLeaf returns the resolve node for a __typename selection at respKey, selected on parentType.
// v1 parity: on a root operation type the value is the static type-name literal (resolve.StaticString);
// elsewhere it is read from the response (resolve.String with IsTypeName). parentType is the type the
// __typename is selected on (Typename obligation's Type).
//
// The VALUE is always read at the `__typename` key of the merged object, never at respKey: an alias
// (`typename: __typename`) is by definition the same value, and planv2's documents select the ONE
// plain `__typename` per position (emitFields) -- v1 instead prints the alias in its document and
// reads it back at the alias key; both encodings render identical client JSON.
func typenameLeaf(def *ast.Document, parentType, respKey string) resolve.Node {
	if def != nil && def.Index.IsRootOperationTypeNameString(parentType) {
		return &resolve.StaticString{Path: []string{respKey}, Value: parentType}
	}
	return &resolve.String{Path: []string{"__typename"}, Nullable: false, IsTypeName: true}
}

// leafValue returns the resolve node for a scalar/enum leaf, keyed on the field's named output type in
// the composed definition, so the resolver reads each leaf with the matching typed walker (integer,
// float, boolean, ...) rather than the string walker -- matching v1. An unknown type (a hand-built
// fixture with no schema entry) falls back to String, which is safe for string-valued leaves.
// Response-side leaves are always nullable (planv2's lenient response shape); REPRESENTATION leaves
// carry the schema's real nullability via leafValueN -- see repNode.object.
func leafValue(def *ast.Document, parentType, fieldName string, path []string) resolve.Node {
	return leafValueN(def, parentType, fieldName, path, true)
}

// leafValueN is leafValue with explicit nullability (v1 representation-visitor parity: a null value
// at a non-nullable node errors the render, which is what lets a null-keyed representation item be
// SKIPPED instead of sent upstream -- the real subgraph rejects it with a reference error).
func leafValueN(def *ast.Document, parentType, fieldName string, path []string, nullable bool) resolve.Node {
	typeName, kind := leafTypeName(def, parentType, fieldName)
	switch kind {
	case ast.NodeKindScalarTypeDefinition:
		switch typeName {
		case "String":
			return &resolve.String{Path: path, Nullable: nullable}
		case "Boolean":
			return &resolve.Boolean{Path: path, Nullable: nullable}
		case "Int":
			return &resolve.Integer{Path: path, Nullable: nullable}
		case "Float":
			return &resolve.Float{Path: path, Nullable: nullable}
		case "BigInt":
			return &resolve.BigInt{Path: path, Nullable: nullable}
		default:
			// ID and custom scalars: any JSON shape -- resolve.Scalar copies the raw value, matching v1.
			return &resolve.Scalar{Path: path, Nullable: nullable}
		}
	case ast.NodeKindEnumTypeDefinition:
		// An enum leaf lowers to resolve.Enum carrying the enum's type name and its accepted values
		// (with @inaccessible values recorded separately) so the resolver validates and serializes the
		// value through the enum walker rather than the generic string walker -- matching v1. Values are
		// read off the composed definition.
		values, inaccessible := enumLeafValues(def, typeName)
		return &resolve.Enum{
			Path:               path,
			Nullable:           nullable,
			TypeName:           typeName,
			Values:             values,
			InaccessibleValues: inaccessible,
		}
	default:
		return &resolve.String{Path: path, Nullable: nullable}
	}
}

// typeShape returns a field's list depth and per-level nullability in the composed definition:
// levels[0] is the field value itself (outermost wrapper), levels[depth] the innermost item. A
// missing definition entry reports (0, [true]) -- the lenient default.
func typeShape(def *ast.Document, parentType, fieldName string) (int, []bool) {
	fallback := []bool{true}
	if def == nil {
		return 0, fallback
	}
	node, ok := def.Index.FirstNodeByNameStr(parentType)
	if !ok {
		return 0, fallback
	}
	fieldDef, ok := def.NodeFieldDefinitionByName(node, ast.ByteSlice(fieldName))
	if !ok {
		return 0, fallback
	}
	var levels []bool
	depth := 0
	cur := true
	for typeRef := def.FieldDefinitionType(fieldDef); typeRef != ast.InvalidRef; {
		switch def.Types[typeRef].TypeKind {
		case ast.TypeKindNonNull:
			cur = false
			typeRef = def.Types[typeRef].OfType
		case ast.TypeKindList:
			levels = append(levels, cur)
			cur = true
			depth++
			typeRef = def.Types[typeRef].OfType
		default: // TypeKindNamed -- bottom
			return depth, append(levels, cur)
		}
	}
	return depth, append(levels, cur)
}

// enumLeafValues returns the accepted value names of an enum in the composed definition and,
// separately, those carrying @inaccessible -- matching v1's enum-node construction. Returns (nil, nil)
// when the type is absent (a hand-built fixture with no schema entry).
func enumLeafValues(def *ast.Document, typeName string) (values, inaccessible []string) {
	if def == nil {
		return nil, nil
	}
	node, ok := def.Index.FirstNodeByNameStr(typeName)
	if !ok || node.Kind != ast.NodeKindEnumTypeDefinition {
		return nil, nil
	}
	refs := def.EnumTypeDefinitions[node.Ref].EnumValuesDefinition.Refs
	values = make([]string, 0, len(refs))
	inaccessible = make([]string, 0)
	for _, valueRef := range refs {
		valueName := def.EnumValueDefinitionNameString(valueRef)
		values = append(values, valueName)
		if _, isInaccessible := def.EnumValueDefinitionDirectiveByName(valueRef, []byte("inaccessible")); isInaccessible {
			inaccessible = append(inaccessible, valueName)
		}
	}
	return values, inaccessible
}

// leafTypeName resolves fieldName's named output type on parentType in the composed definition and
// returns it with the defining node's kind (scalar/enum/unknown). It strips List/NonNull wrappers by
// reading the field definition's underlying named type (FieldDefinitionTypeNameString).
func leafTypeName(def *ast.Document, parentType, fieldName string) (string, ast.NodeKind) {
	if def == nil {
		return "", ast.NodeKindUnknown
	}
	node, ok := def.Index.FirstNodeByNameStr(parentType)
	if !ok {
		return "", ast.NodeKindUnknown
	}
	fieldDef, ok := def.NodeFieldDefinitionByName(node, ast.ByteSlice(fieldName))
	if !ok {
		return "", ast.NodeKindUnknown
	}
	typeName := def.FieldDefinitionTypeNameString(fieldDef)
	typeNode, ok := def.Index.FirstNodeByNameStr(typeName)
	if !ok {
		return typeName, ast.NodeKindUnknown
	}
	return typeName, typeNode.Kind
}

// listNesting counts the number of list wrappers on a field's type in the composed definition
// (`[User]` -> 1, `[[Int!]]` -> 2, `User` -> 0), stripping non-null wrappers as it descends. lower wraps
// a field value in that many resolve.Array nodes.
func listNesting(def *ast.Document, parentType, fieldName string) int {
	if def == nil {
		return 0
	}
	node, ok := def.Index.FirstNodeByNameStr(parentType)
	if !ok {
		return 0
	}
	fieldDef, ok := def.NodeFieldDefinitionByName(node, ast.ByteSlice(fieldName))
	if !ok {
		return 0
	}
	depth := 0
	for typeRef := def.FieldDefinitionType(fieldDef); typeRef != ast.InvalidRef; {
		switch def.Types[typeRef].TypeKind {
		case ast.TypeKindList:
			depth++
			typeRef = def.Types[typeRef].OfType
		case ast.TypeKindNonNull:
			typeRef = def.Types[typeRef].OfType
		default: // TypeKindNamed -- bottom
			return depth
		}
	}
	return depth
}

// hasSelectableChild reports whether an obligation has any child that contributes to the response
// shape (a Field, Typename, or Refine) -- i.e. whether it is a composite selection rather than a leaf.
func hasSelectableChild(obs []obligation.Obligation, ids []obligation.ObID) bool {
	for _, id := range ids {
		switch obs[id].Kind {
		case obligation.Field, obligation.Typename, obligation.Refine:
			return true
		}
	}
	return false
}

// buildRawFetches emits one fetch per group, in dependency order. A root group selects its subgraph
// document off the operation root. A jump group is an `_entities` fetch: its Input carries the `$$0$$`
// variable placeholder inside the `representations` array and its Variables carry a resolvable-object
// variable that renders the representation object (the @key fields AND the @requires fields both ride
// in the representation the resolver sends) -- exactly the shape v1's postprocess step expects, since
// that step is reused untouched here. QueryPlan.Query keeps the printed document; DependsOnFields
// splits the representation into its @key and @requires fragments the way v1 does.
func buildRawFetches(h *hypergraph.Hypergraph, cover *search.Cover, groups []*fetchGroup, aliases *aliasMap, def *ast.Document, transport TransportTable) ([]*resolve.FetchItem, error) {
	producedBy := map[hypergraph.NodeID]hypergraph.EdgeID{}
	for _, e := range cover.Edges {
		producedBy[h.Edge(e).Head] = e
	}
	items := make([]*resolve.FetchItem, 0, len(groups))
	for gi, g := range groups {
		doc := groupDocument(h, g, aliases)
		fc := resolve.FetchConfiguration{
			QueryPlan:           &resolve.QueryPlan{Query: doc},
			RequiresEntityFetch: g.Jump != hypergraph.NoEdge,
		}
		if g.Jump == hypergraph.NoEdge {
			fc.Input = `{"body":{"query":"` + doc + `"}}`
			fc.PostProcessing = resolve.PostProcessingConfiguration{
				SelectResponseDataPath:   []string{"data"},
				SelectResponseErrorsPath: []string{"errors"}, // v1 parity: surface subgraph errors
			}
		} else {
			headType := h.Node(g.Entry).Type
			jump := h.Edge(g.Jump)
			keySet := map[hypergraph.NodeID]bool{}
			for _, t := range jump.KeyTails {
				keySet[t] = true
			}
			keyTrie := newRepNode()
			reqTrie := newRepNode()
			for _, tail := range jump.Tails {
				trie := reqTrie
				if keySet[tail] || len(jump.KeyTails) == 0 { // no recorded split -> treat all as key
					trie = keyTrie
				}
				trie.insert(tailFieldPath(h, producedBy, tail, headType))
			}
			// The representation input object -- @key + @requires fields, gated on the entity type --
			// rendered by the loader into the `representations` variable.
			fc.Input = `{"body":{"query":"` + doc + `","variables":{"representations":[$$0$$]}}}`
			fc.Variables = resolve.Variables{
				resolve.NewResolvableObjectVariable(representationObject(h, g.Subgraph, headType, keyTrie, reqTrie, def)),
			}
			fc.PostProcessing = resolve.PostProcessingConfiguration{
				SelectResponseDataPath:   []string{"data", "_entities"},
				SelectResponseErrorsPath: []string{"errors"}, // v1 parity: surface subgraph errors
			}
			// Split query-plan fragments -- a key-only representation and a separate @requires one.
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
		// Attach the subgraph's executable transport (HTTP source + url/method/header envelope, or a
		// per-fetch gRPC DataSource). A nil/absent table leaves the fetch shape-only, byte-identical to
		// the pre-transport output. A gRPC DataSource-construction error aborts lowering (never silent).
		if err := transport.lookup(h.SubgraphName(g.Subgraph)).attach(&fc); err != nil {
			return nil, err
		}
		sf := &resolve.SingleFetch{
			FetchConfiguration: fc,
			FetchDependencies: resolve.FetchDependencies{
				FetchID:           gi,
				DependsOnFetchIDs: g.DependsOn,
			},
			DataSourceIdentifier: []byte(h.SubgraphName(g.Subgraph)),
		}
		// FetchInfo: the query-plan printer dereferences Fetch.Info unconditionally (see the
		// obligation-driven path). The legacy hatch carries the minimal truthful record.
		infoID := transport.lookup(h.SubgraphName(g.Subgraph)).ID
		if infoID == "" {
			infoID = h.SubgraphName(g.Subgraph)
		}
		sf.Info = &resolve.FetchInfo{
			DataSourceID:   infoID,
			DataSourceName: h.SubgraphName(g.Subgraph),
			OperationType:  ast.OperationTypeQuery,
			QueryPlan:      fc.QueryPlan,
		}
		// Emit the fetch's response path and fetch path so the loader merges an `_entities` result into
		// the nested response object(s) it extends, not the root data buffer. A root group's entry is
		// the operation root (no producer) -> empty path, the correct root attachment; a jump group's
		// entry is the landed entity object, whose response path is recovered by walking back up to the
		// root. See fetchAttachPath.
		respPath, fetchPath := fetchAttachPath(h, producedBy, g.Entry, def)
		items = append(items, resolve.FetchItemWithPath(sf, respPath, fetchPath...))
	}
	return items, nil
}

// fetchAttachPath recovers the response position an entity fetch's `_entities` result merges into, as
// both the dotted response-path string and the list of path elements the loader walks. It walks back
// up from the group's entry object node to the root, collecting one hop per response level, then
// renders them root-to-leaf like v1 does: a list-typed hop is an array element (field name) plus an
// "@" marker in the dotted path; an object hop carries the enclosing object type as its __typename
// gate. A root-group entry (the operation root, with no producer) yields the empty path -- the correct
// root attachment. Object nodes are reached via a descent (field -> object), an entity jump (keys ->
// object, a same-position subgraph hop that adds no response level), or a member edge (abstract ->
// member, same-position refinement); the walk handles all three.
func fetchAttachPath(h *hypergraph.Hypergraph, producedBy map[hypergraph.NodeID]hypergraph.EdgeID,
	entry hypergraph.NodeID, def *ast.Document) (string, []resolve.FetchItemPathElement) {

	var hops []attachHop
	cur := entry
	for guard := 0; guard < 1<<16; guard++ {
		pe, ok := producedBy[cur]
		if !ok {
			break // reached the operation root (or a severed chain) -- done
		}
		edge := h.Edge(pe)
		switch edge.Kind {
		case hypergraph.EdgeEntityJump:
			prev, ok := preJumpObject(h, producedBy, edge)
			if !ok || prev == cur {
				return renderAttachPath(hops)
			}
			cur = prev
			continue
		case hypergraph.EdgeTypeMove:
			cur = edge.Tails[0] // abstract object at the same response position
			continue
		case hypergraph.EdgeDescent:
			fieldNode := edge.Tails[0]
			fe, ok := producedBy[fieldNode]
			if !ok {
				return renderAttachPath(hops) // defensive: severed -- emit what we have
			}
			fedge := h.Edge(fe)
			enclosing := fedge.Tails[0]
			encType := h.Node(enclosing).Type // "" for the operation root
			hops = append(hops, attachHop{
				field:         fedge.Label,
				enclosingType: encType,
				array:         encType != "" && listNesting(def, encType, fedge.Label) > 0,
			})
			cur = enclosing
			continue
		default:
			return renderAttachPath(hops) // a Field edge does not produce an object; stop defensively
		}
	}
	return renderAttachPath(hops)
}

// attachHop is one response level recovered by fetchAttachPath: the field name, the type of the
// object that field is selected on (the loader's __typename gate), and whether it is list-typed.
type attachHop struct {
	field, enclosingType string
	array                bool
}

// preJumpObject returns the entity object in the SOURCE subgraph an EntityJump departed from -- the
// enclosing object of one of the jump's @key tails (the representation is anchored there). It is the
// same response position as the jump's head, so the attach-path walk continues from it without adding
// a response level. ok is false when no key tail's producer is known.
func preJumpObject(h *hypergraph.Hypergraph, producedBy map[hypergraph.NodeID]hypergraph.EdgeID, jump hypergraph.Edge) (hypergraph.NodeID, bool) {
	keyTails := jump.KeyTails
	if len(keyTails) == 0 {
		keyTails = jump.Tails
	}
	headType := h.Node(jump.Head).Type
	for _, t := range keyTails {
		fe, ok := producedBy[t]
		if !ok {
			continue
		}
		obj := h.Edge(fe).Tails[0]
		if h.Node(obj).Type == headType { // the entity object the key is read off of
			return obj, true
		}
	}
	// fall back to any key tail's enclosing object
	for _, t := range keyTails {
		if fe, ok := producedBy[t]; ok {
			return h.Edge(fe).Tails[0], true
		}
	}
	return 0, false
}

// renderAttachPath turns the root->leaf hop list into the dotted ResponsePath string and the
// []FetchItemPathElement, mirroring v1's per-hop emission (Array hop => "@" marker + Array element;
// object hop => Object element gated on the enclosing type's __typename). The hops are collected
// leaf->root by the caller, so they are reversed here.
func renderAttachPath(hops []attachHop) (string, []resolve.FetchItemPathElement) {
	if len(hops) == 0 {
		return "", nil
	}
	var elems []string
	var fp []resolve.FetchItemPathElement
	for i := len(hops) - 1; i >= 0; i-- {
		hp := hops[i]
		if hp.array {
			fp = append(fp, resolve.FetchItemPathElement{
				Kind: resolve.FetchItemPathElementKindArray,
				Path: []string{hp.field},
			})
			elems = append(elems, hp.field, "@")
			continue
		}
		var typeNames []string
		if hp.enclosingType != "" {
			typeNames = []string{hp.enclosingType}
		}
		fp = append(fp, resolve.FetchItemPathElement{
			Kind:      resolve.FetchItemPathElementKindObject,
			Path:      []string{hp.field},
			TypeNames: typeNames,
		})
		elems = append(elems, hp.field)
	}
	// v1's responsePath() drops only a TRAILING "@"; intermediate markers stay in the dotted string.
	if len(elems) > 0 && elems[len(elems)-1] == "@" {
		elems = elems[:len(elems)-1]
	}
	return strings.Join(elems, "."), fp
}

// tailFieldPath recovers a jump tail's field path by walking producedBy upward to the entity object
// (a NodeObject of the jump's head type) -- e.g. (Organization,A).id -> ["organization", "id"].
func tailFieldPath(h *hypergraph.Hypergraph, producedBy map[hypergraph.NodeID]hypergraph.EdgeID, tail hypergraph.NodeID, headType string) []string {
	var path []string
	n := tail
	// producedBy is a cross-goal, head-keyed union; a self-nested entity type cycle can make it
	// CYCLIC (see groupForObject). If the up-walk revisits a node before reaching the entity object,
	// the chain has left the walk structure -- stop with the same defensive contract as a severed
	// chain (the guard prevents a hang, exactly like the missing-producer case below).
	visited := map[hypergraph.NodeID]bool{}
	for !visited[n] {
		visited[n] = true
		node := h.Node(n)
		if node.Kind == hypergraph.NodeObject && node.Type == headType {
			return path // reached the entity object the representation is keyed on
		}
		e, ok := producedBy[n]
		if !ok {
			return path // defensive: severed chain -- emit what we have
		}
		edge := h.Edge(e)
		if edge.Kind == hypergraph.EdgeField {
			path = append([]string{edge.Label}, path...)
		}
		n = edge.Tails[0]
	}
	return path
}

// repNode is a selection trie that merges the tails' field paths into one nested selection with a
// deterministic (sorted) sibling order. args holds a field's rendered argument body for an
// argument-bearing @requires (e.g. `currency: "USD"`); "" for @key fields and any field with no
// arguments. args only affects the printed selection -- the value is read by the bare field name, so
// object() ignores it.
type repNode struct {
	children map[string]*repNode
	// frags holds fragment-conditioned @requires sub-selections (`... on Bar { bar }`) keyed by type
	// condition. They PRINT in the Requires fragment (v1's contract text, and the assertion-6
	// mechanism signal) but are not built into the representation VALUE (object() is name-keyed) --
	// the registered requires-fragment representation residual. nil except under such a @requires.
	frags map[string]*repNode
	args  string
	// readAs is the response key this field's VALUE is read from when it differs from the field name.
	// When two requiring fields on one entity require the same coordinate with different argument
	// values, the source document aliases the second one (`_planv2req_price_0: price(currency: "EUR")`),
	// so its value lands in the prior fetch's response under the alias, not `price`. The representation
	// must still PRESENT the field as `price` (its real name, which the target's @requires contract
	// expects) while READING from the alias. print() and the field name stay the real name; only
	// object()'s read path switches to readAs. "" = read by the field name (the usual case).
	readAs string
}

func newRepNode() *repNode { return &repNode{children: map[string]*repNode{}} }

func (n *repNode) insert(path []string) {
	cur := n
	for _, f := range path {
		next := cur.children[f]
		if next == nil {
			next = newRepNode()
			cur.children[f] = next
		}
		cur = next
	}
}

func (n *repNode) sortedKeys() []string {
	keys := make([]string, 0, len(n.children))
	for k := range n.children {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (n *repNode) print() string {
	var parts []string
	for _, k := range n.sortedKeys() {
		c := n.children[k]
		label := k
		if c.args != "" {
			label = k + "(" + c.args + ")"
		}
		if len(c.children) == 0 && len(c.frags) == 0 {
			parts = append(parts, label)
			continue
		}
		parts = append(parts, label+" { "+c.print()+" }")
	}
	if len(n.frags) > 0 {
		conds := make([]string, 0, len(n.frags))
		for cond := range n.frags {
			conds = append(conds, cond)
		}
		sort.Strings(conds)
		for _, cond := range conds {
			parts = append(parts, "... on "+cond+" { "+n.frags[cond].print()+" }")
		}
	}
	return joinNonEmpty(parts)
}

// object renders the trie as resolve nodes for the resolvable-object representation variable: leaves
// read the parent object's data at their field name; composites nest. Each leaf is typed against the
// composed definition (starting from currentType) so a Float/Int @requires input reads through its
// typed walker rather than the string walker -- matching v1. A LIST-typed field (of any depth) wraps
// its value in resolve.Array nodes exactly like v1's representation visitor: the walker refuses an
// array value at an Object/scalar node, so an unwrapped list rendered the whole representation null
// (the audit's list-valued @requires/@key gather class). Fragment-conditioned branches (frags) render
// as member-gated sibling fields (OnTypeNames on the member's implementers) -- the walker skips them
// when the runtime __typename does not match, which is exactly the conditional-input contract.
func (n *repNode) object(def *ast.Document, currentType string, path []string) *resolve.Object {
	obj := &resolve.Object{Nullable: true, Path: path}
	for _, k := range n.sortedKeys() {
		c := n.children[k]
		// Read from the aliased response key when one is set, but keep the representation field
		// NAME as the real coordinate (the target's @requires contract). Applies to composites too:
		// an argument-conflict alias lands the WHOLE gathered subtree at the aliased key.
		readKey := k
		if c.readAs != "" {
			readKey = c.readAs
		}
		depth, levels := typeShape(def, currentType, k)
		innerPath := []string{readKey}
		if depth > 0 {
			innerPath = nil // the element value reads its array item directly
		}
		// Representation nodes carry the schema's REAL nullability (v1 representation-visitor
		// parity): rendering a null against a non-nullable node errors the item, and the batch
		// renderer's SkipErrItems DROPS it -- the null-keyed entity completes to null instead of
		// being sent upstream, where the subgraph rejects the unresolvable reference.
		var val resolve.Node
		if len(c.children) == 0 && len(c.frags) == 0 {
			val = leafValueN(def, currentType, k, innerPath, levels[depth])
		} else {
			childType, _ := leafTypeName(def, currentType, k) // composite output type of k
			val = c.object(def, childType, innerPath)
			if o, ok := val.(*resolve.Object); ok {
				o.Nullable = levels[depth]
			}
		}
		for i := 0; i < depth; i++ {
			p := []string(nil)
			if i == depth-1 {
				p = []string{readKey} // outermost Array carries the read path
			}
			val = &resolve.Array{Path: p, Nullable: levels[depth-1-i], Item: val}
		}
		obj.Fields = append(obj.Fields, &resolve.Field{Name: []byte(k), Value: val})
	}
	if len(n.frags) > 0 {
		conds := make([]string, 0, len(n.frags))
		for cond := range n.frags {
			conds = append(conds, cond)
		}
		sort.Strings(conds)
		for _, cond := range conds {
			gate := refinementGate(def, cond)
			for _, f := range n.frags[cond].object(def, cond, nil).Fields {
				if f.OnTypeNames == nil {
					f.OnTypeNames = gate
				}
				obj.Fields = append(obj.Fields, f)
			}
		}
	}
	return obj
}

// representationObject builds the resolve node the ResolvableObjectVariable renders into each
// `representations` array item: `{ __typename, <key fields>, <requires fields> }`, every top-level
// field gated on the entity type (v1's representation_variable.go structure: __typename String +
// per-field OnTypeNames). Leaves are typed against the composed definition, rooted at the entity type.
//
// Interface-object semantics (C-disc; v1 representation_variable.go parity):
//   - The GATE is every runtime __typename the SOURCE data may legitimately carry
//     (representationGate) -- a concrete head admits the entity-interface name (the source position
//     may only know the interface, when its data came from an @interfaceObject subgraph), and an
//     interface head admits its concrete implementers (the source usually knows the concrete type).
//     The old headType-only gate rendered the representation null whenever the runtime name
//     differed, silently skipping the whole fetch.
//   - The __typename VALUE presented to an @interfaceObject TARGET is the STATIC interface name --
//     the target subgraph declares no concrete member types, so the runtime concrete name is
//     unresolvable there. Every other target reads the runtime value.
//
// h/targetSg supply the interface-object facts; a nil h (hand-built fixtures) keeps the old
// headType-only behavior byte-identically.
func representationObject(h *hypergraph.Hypergraph, targetSg hypergraph.SubgraphID,
	headType string, keyTrie, reqTrie *repNode, def *ast.Document) *resolve.Object {
	gate := representationGate(h, def, targetSg, headType)
	var typenameValue resolve.Node = &resolve.String{Path: []string{"__typename"}}
	if h != nil && h.IsInterfaceObject(headType, targetSg) {
		typenameValue = &resolve.StaticString{Path: []string{"__typename"}, Value: headType}
	}
	fields := []*resolve.Field{{
		Name:        []byte("__typename"),
		Value:       typenameValue,
		OnTypeNames: gate,
	}}
	for _, trie := range []*repNode{keyTrie, reqTrie} {
		for _, f := range trie.object(def, headType, nil).Fields {
			f.OnTypeNames = gate
			fields = append(fields, f)
		}
	}
	return &resolve.Object{Nullable: true, Fields: fields}
}

// representationGate returns the OnTypeNames gate for a representation whose jump lands on
// headType in subgraph targetSg: the set of runtime __typename values the source data may carry.
// The head type leads (v1's [concreteType, interfaceTypeName] order); nil h or def degrades to the
// bare head type (the pre-interface-object behavior).
func representationGate(h *hypergraph.Hypergraph, def *ast.Document, targetSg hypergraph.SubgraphID, headType string) [][]byte {
	gate := [][]byte{[]byte(headType)}
	if h == nil || def == nil {
		return gate
	}
	if isAbstractType(def, headType) {
		// Interface head (@interfaceObject or entity-interface jump): admit the concrete
		// implementers -- the source position usually carries a concrete runtime name.
		for _, m := range refinementGate(def, headType) {
			if string(m) != headType {
				gate = append(gate, m)
			}
		}
		return gate
	}
	// Concrete head: admit each entity-interface it implements -- a source fed by an
	// @interfaceObject subgraph reports the interface name.
	for _, iface := range implementedInterfaces(def, headType) {
		if h.IsEntityInterface(iface) {
			gate = append(gate, []byte(iface))
		}
	}
	return gate
}

// implementedInterfaces returns the interface names object type typeName declares in def.
func implementedInterfaces(def *ast.Document, typeName string) []string {
	node, ok := def.Index.FirstNodeByNameStr(typeName)
	if !ok || node.Kind != ast.NodeKindObjectTypeDefinition {
		return nil
	}
	refs := def.ObjectTypeDefinitions[node.Ref].ImplementsInterfaces.Refs
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, def.TypeNameString(r))
	}
	return out
}

// entityFragmentNeedsTypename reports whether an entity fetch's fragment body must select
// __typename to RE-DISCRIMINATE the concrete type (C-disc): a jump into an interface-DECLARING
// subgraph -- the head is a concrete member of an entity interface, or the entity interface itself --
// returns the schema-computed CONCRETE __typename, which merges over the interface name an
// @interfaceObject source reported, so the response's member gates apply. A jump INTO an
// @interfaceObject subgraph must NOT select it: that subgraph reports the interface name, which
// would clobber the concrete discriminator other fetches supplied.
func entityFragmentNeedsTypename(h *hypergraph.Hypergraph, def *ast.Document,
	targetSg hypergraph.SubgraphID, entryType string) bool {
	if h == nil || def == nil || h.IsInterfaceObject(entryType, targetSg) {
		return false
	}
	if isAbstractType(def, entryType) {
		return h.IsEntityInterface(entryType)
	}
	for _, iface := range implementedInterfaces(def, entryType) {
		if h.IsEntityInterface(iface) {
			return true
		}
	}
	return false
}

// groupEdges indexes a group's Field/Descent/TypeMove edges by their (single) tail so the document
// printer can walk object -> field -> child-object -> member structurally.
type groupEdges struct {
	fieldsByObj  map[hypergraph.NodeID][]hypergraph.EdgeID // Field edges, keyed by parent object/root
	descentByFld map[hypergraph.NodeID]hypergraph.EdgeID   // Descent edge, keyed by field node
	movesByObj   map[hypergraph.NodeID][]hypergraph.EdgeID // TypeMove edges, keyed by abstract object
}

func indexGroup(h *hypergraph.Hypergraph, g *fetchGroup) groupEdges {
	ge := groupEdges{
		fieldsByObj:  map[hypergraph.NodeID][]hypergraph.EdgeID{},
		descentByFld: map[hypergraph.NodeID]hypergraph.EdgeID{},
		movesByObj:   map[hypergraph.NodeID][]hypergraph.EdgeID{},
	}
	for _, e := range g.Edges {
		edge := h.Edge(e)
		tail := edge.Tails[0]
		switch edge.Kind {
		case hypergraph.EdgeField:
			ge.fieldsByObj[tail] = append(ge.fieldsByObj[tail], e)
		case hypergraph.EdgeDescent:
			ge.descentByFld[tail] = e
		case hypergraph.EdgeTypeMove:
			ge.movesByObj[tail] = append(ge.movesByObj[tail], e)
		}
	}
	return ge
}

// groupDocument prints the subgraph query for a group. Root groups print `{ ... }` off the operation
// root; jump groups print an `_entities` selection under the jump's head type with the representation
// variable.
func groupDocument(h *hypergraph.Hypergraph, g *fetchGroup, aliases *aliasMap) string {
	ge := indexGroup(h, g)
	sel := printSelection(h, ge, g.Entry, aliases)
	if g.Jump == hypergraph.NoEdge {
		return "query { " + sel + " }"
	}
	headType := h.Node(g.Entry).Type
	return "query($representations: [_Any!]!) { _entities(representations: $representations) { ... on " +
		headType + " { " + sel + " } } }"
}

// printSelection renders the selection set (without the enclosing braces) reachable from object node
// obj within the group. Fields are emitted in ascending field-node order for determinism. A field
// whose edge carries an alias is printed `alias: name` (fetch-side); leaves print their label.
func printSelection(h *hypergraph.Hypergraph, ge groupEdges, obj hypergraph.NodeID, aliases *aliasMap) string {
	fields := append([]hypergraph.EdgeID(nil), ge.fieldsByObj[obj]...)
	sort.Slice(fields, func(i, j int) bool { return h.Edge(fields[i]).Head < h.Edge(fields[j]).Head })

	var parts []string
	for _, e := range fields {
		edge := h.Edge(e)
		name := edge.Label
		if a := aliases.byEdge[e]; a != "" {
			name = a + ": " + edge.Label
		}
		child, hasChild := ge.descentByFld[edge.Head]
		if !hasChild {
			parts = append(parts, name)
			continue
		}
		childObj := h.Edge(child).Head
		var inner string
		if moves := ge.movesByObj[childObj]; len(moves) > 0 {
			// abstract child: one inline fragment per covered member, plus __typename for resolution.
			sort.Slice(moves, func(i, j int) bool { return h.Edge(moves[i]).Head < h.Edge(moves[j]).Head })
			frags := []string{"__typename"}
			for _, m := range moves {
				member := h.Edge(m).Head
				frags = append(frags, "... on "+h.Node(member).Type+" { "+printSelection(h, ge, member, aliases)+" }")
			}
			inner = joinNonEmpty(frags)
		} else {
			inner = printSelection(h, ge, childObj, aliases)
		}
		parts = append(parts, name+" { "+inner+" }")
	}
	return joinNonEmpty(parts)
}

func joinNonEmpty(parts []string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += p
	}
	return out
}
