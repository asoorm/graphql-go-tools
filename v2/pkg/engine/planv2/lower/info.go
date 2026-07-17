package lower

// info.go emits the v1 planner's per-field and per-fetch metadata surfaces (M4.1 adoption safety;
// PARITY.md Section 6): resolve.FieldInfo on every response field (Name, ExactParentTypeName,
// ParentTypeNames, NamedType, Source IDs/Names, HasAuthorizationRule) and FetchInfo.RootFields on
// every fetch (the fetch's top-level GraphCoordinates, each carrying its authorization flag).
// Downstream, postprocess's collectAuthorizationCoordinates derives the pre-fetch authorization
// coordinate set from exactly these two surfaces -- without them a @authenticated/@requiresScopes
// field resolves UNAUTHORIZED silently -- and the router's schema-usage telemetry reads the
// Source attribution.
//
// v1 semantics mirrored (plan/visitor.go resolveFieldInfo, plan/path_builder_visitor.go
// addRootField):
//   - ExactParentTypeName is the type the field is selected on (the obligation's owner type --
//     under `... on C` that is C, matching v1's walker enclosing type).
//   - ParentTypeNames is the owner type plus, when the owner is an interface, its object
//     implementers in the composed schema (sorted, deduplicated).
//   - NamedType is the field's List/NonNull-stripped named type ("String" for __typename).
//   - HasAuthorizationRule comes from plan.FieldConfigurations.ForTypeField -- the compiled form of
//     @authenticated/@requiresScopes.
//   - Source lists the datasource that RESOLVES the field. planv2 attributes each response
//     position to the one route the search chose, so Source is a single-element, non-empty SUBSET
//     of v1's list (v1 lists every planner that touched the field); the differential FieldInfo
//     oracle pins the subset contract.
//   - RootFields are the fetch's top-level coordinates: fields selected directly on the fetch's
//     entry type (operation root or entity type), plus fields directly inside top-level member
//     fragments (typed on the member) -- v1's fieldIsChildNode boundary.
//
// FieldInfo.FetchID and IndirectInterfaceNames are deliberately not populated: v1's visitor never
// sets FetchID either (postprocess reads its zero value), and IndirectInterfaceNames has no
// consumer in this repository (registered in PARITY.md Section 6).

import (
	"slices"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// InfoConfig carries what lowering needs to emit the v1 Info surfaces. The zero value emits
// nothing -- byte-identical to the pre-M4.1 output -- which is what the plain Lower/LowerExecutable
// entry points pass; the facade passes IncludeInfo = !plan.Configuration.DisableIncludeInfo plus
// the config's field table.
type InfoConfig struct {
	// Fields is the v1 per-field configuration table; only HasAuthorizationRule is consulted here
	// (argument routing stays operation-derived; the unsupported field-config capabilities are
	// refused typed at the facade -- see planv2 refuse.go).
	Fields plan.FieldConfigurations
	// IncludeInfo emits resolve.FieldInfo per response field and FetchInfo.RootFields per fetch.
	IncludeInfo bool
}

// hasAuthRule is v1's fieldHasAuthorizationRule (visitor.go / path_builder_visitor.go).
func (c InfoConfig) hasAuthRule(typeName, fieldName string) bool {
	fc := c.Fields.ForTypeField(typeName, fieldName)
	return fc != nil && fc.HasAuthorizationRule
}

// infoEmission is the per-plan emission state renderFields consumes: the FieldInfo each obligation's
// response field carries. nil = no emission (the zero-InfoConfig path).
type infoEmission struct {
	infos map[obligation.ObID]*resolve.FieldInfo
}

// fieldInfoFor returns the FieldInfo for one obligation's response field, nil when emission is off
// or the obligation has none (Refine obligations render no field of their own).
func (em *infoEmission) fieldInfoFor(id obligation.ObID) *resolve.FieldInfo {
	if em == nil {
		return nil
	}
	return em.infos[id]
}

// buildFieldInfos derives the per-obligation FieldInfo map. Source attribution comes from the
// group each field was emitted into (obSubgraph, recorded during emitFields); obligations no walk
// attributed (a composite whose children all re-rooted elsewhere) inherit the nearest attributed
// descendant's source, then the nearest ancestor's -- the group whose document materialized their
// selection chain.
func (gb *obGroupBuilder) buildFieldInfos(h *hypergraph.Hypergraph, obs []obligation.Obligation,
	def *ast.Document, transport TransportTable) map[obligation.ObID]*resolve.FieldInfo {

	// Close the attribution gaps: children precede nothing (obligations are in document order,
	// parents before children), so a reverse pass inherits from the first attributed child, and a
	// forward pass inherits from the parent.
	sub := make(map[obligation.ObID]hypergraph.SubgraphID, len(obs))
	for id, sg := range gb.obSubgraph {
		sub[id] = sg
	}
	for i := len(obs) - 1; i >= 0; i-- {
		ob := obs[i]
		if _, ok := sub[ob.ID]; ok {
			continue
		}
		for j := i + 1; j < len(obs); j++ {
			if obs[j].Parent == ob.ID {
				if sg, ok := sub[obs[j].ID]; ok {
					sub[ob.ID] = sg
					break
				}
			}
		}
	}
	for _, ob := range obs {
		if _, ok := sub[ob.ID]; ok {
			continue
		}
		if ob.Parent != obligation.NoParent && ob.Parent != ob.ID {
			if sg, ok := sub[ob.Parent]; ok {
				sub[ob.ID] = sg
			}
		}
	}

	out := make(map[obligation.ObID]*resolve.FieldInfo, len(obs))
	for _, ob := range obs {
		if ob.Kind != obligation.Field && ob.Kind != obligation.Typename {
			continue
		}
		info := &resolve.FieldInfo{
			Name:                 ob.Field,
			ExactParentTypeName:  ob.Type,
			ParentTypeNames:      parentTypeNamesFor(def, ob.Type),
			NamedType:            fieldNamedType(def, ob.Type, ob.Field),
			HasAuthorizationRule: gb.info.hasAuthRule(ob.Type, ob.Field),
		}
		if sg, ok := sub[ob.ID]; ok {
			name := h.SubgraphName(sg)
			id := transport.lookup(name).ID
			if id == "" {
				id = name
			}
			info.Source = resolve.TypeFieldSource{IDs: []string{id}, Names: []string{name}}
		}
		out[ob.ID] = info
	}
	return out
}

// parentTypeNamesFor mirrors v1's parent-type-name derivation: the owner type itself plus, when
// the owner is an interface in the composed schema, every object type implementing it. Sorted and
// deduplicated, like v1.
func parentTypeNamesFor(def *ast.Document, typeName string) []string {
	names := []string{typeName}
	if def != nil {
		if node, ok := def.Index.FirstNodeByNameStr(typeName); ok && node.Kind == ast.NodeKindInterfaceTypeDefinition {
			if impls, ok := def.InterfaceTypeDefinitionImplementedByObjectWithNames(node.Ref); ok {
				names = append(names, impls...)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// fieldNamedType resolves the field's List/NonNull-stripped named type on its owner in the
// composed schema; "__typename" is the spec meta-field of type String.
func fieldNamedType(def *ast.Document, parentType, fieldName string) string {
	if fieldName == "__typename" {
		return "String"
	}
	return namedFieldType(def, parentType, fieldName)
}

// rootFieldCoords collects a fetch group's RootFields: the top-level fields of its printed
// selection, typed on the group's entry type (operation root for a root group, the entity type for
// a jump group), plus the fields directly inside top-level member fragments, typed on the member --
// the same boundary v1's addRootField draws with fieldIsChildNode. __typename is not a coordinate
// (v1 never records it). Deduplicated, insertion-ordered (v1 appends in planning order).
func (gb *obGroupBuilder) rootFieldCoords(g *obGroup, def *ast.Document, opType ast.OperationType) []resolve.GraphCoordinate {
	typeName := g.entryType
	if g.jumpEdge == hypergraph.NoEdge {
		typeName = operationRootTypeName(def, opType)
	}
	var out []resolve.GraphCoordinate
	add := func(tn string, fields []*docField) {
		for _, f := range fields {
			coord := resolve.GraphCoordinate{
				TypeName:             tn,
				FieldName:            f.name,
				HasAuthorizationRule: gb.info.hasAuthRule(tn, f.name),
			}
			if !slices.Contains(out, coord) {
				out = append(out, coord)
			}
		}
	}
	add(typeName, g.sel.fields)
	frags := append([]string(nil), g.sel.fragOrder...)
	slices.Sort(frags)
	for _, t := range frags {
		add(t, g.sel.frags[t].fields)
	}
	return out
}

// operationRootTypeName names the composed schema's root type for the operation kind ("Query"
// fallback for a nil/rootless definition, matching rootOperationType's lenient contract).
func operationRootTypeName(def *ast.Document, opType ast.OperationType) string {
	if def == nil {
		return "Query"
	}
	var name ast.ByteSlice
	switch opType {
	case ast.OperationTypeMutation:
		name = def.Index.MutationTypeName
	case ast.OperationTypeSubscription:
		name = def.Index.SubscriptionTypeName
	default:
		name = def.Index.QueryTypeName
	}
	if len(name) == 0 {
		return "Query"
	}
	return string(name)
}
