package lower

import (
	"fmt"
	"sort"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// aliasMap holds both halves of the field-aliasing fix for response-key collisions. When two
// selections in the same fetch document would print under the same response key but disagree on
// output type, each is renamed to a unique alias so the subgraph accepts the document. byEdge is the
// fetch side (the alias printed as `alias: field` in the subgraph query); byOb and toResp are the
// response side (which client key the aliased value belongs to). Both sides must move together: the
// response tree keeps the client's key as the field name but reads the value at the alias path
// (byOb, consumed by buildResponseObject), so a fetch's aliased result lands back at the client's
// key. An alias without that path rewrite would null the field, which is why both are minted here in
// one collision pass.
type aliasMap struct {
	byEdge map[hypergraph.EdgeID]string // alias per fetch edge; "" = no alias
	byOb   map[obligation.ObID]string   // alias per covered obligation (the response path to read from)
	toResp map[string]string            // alias -> client response key (for tooling/audit)
}

// assignAliases finds response-key collisions and mints an alias for each colliding field, using
// GraphQL's own conflict rule to decide what actually collides. Two selections collide only if they
// would print as siblings in the same fetch document: same fetch group AND same selection-set scope
// (the shared abstract object for member fragments, or the shared parent object otherwise). The same
// key at a different response position, or in a different fetch document, does not collide --
// including the cross-subgraph case, whose selections stay in separate documents until the merge
// pass combines them.
//
// Within one sibling scope, mutually exclusive `... on C { f }` fragments that select the same key
// are legal GraphQL as long as their output types agree: object parent types that differ can never
// apply to the same runtime object, so only the response shape has to match -- no alias is minted for
// them. An alias is minted only when the printed output types differ (a conflict the subgraph would
// reject): each colliding edge gets a unique `_planv2_<key>_<i>` alias, all mapped back to the
// client key.
func assignAliases(h *hypergraph.Hypergraph, o *obligation.Tree, cover *search.Cover,
	definition *ast.Document, groupOf map[hypergraph.EdgeID]int) *aliasMap {

	am := &aliasMap{
		byEdge: map[hypergraph.EdgeID]string{},
		byOb:   map[obligation.ObID]string{},
		toResp: map[string]string{},
	}

	producedBy := map[hypergraph.NodeID]hypergraph.EdgeID{}
	for _, e := range cover.Edges {
		producedBy[h.Edge(e).Head] = e
	}

	type domain struct {
		group int               // fetch document
		scope hypergraph.NodeID // sibling selection-set scope within the document
		rho   string            // client response key
	}
	buckets := map[domain][]aliasCandidate{}
	var keys []domain

	// Iterate goals in GoalID order (deterministic) so bucket member order is stable.
	for _, g := range o.Goals() {
		v, covered := cover.Selected[g]
		if !covered {
			continue
		}
		ob := o.Ob(g)
		if ob.RespKey == "" {
			continue
		}
		e := coveringFieldEdge(h, cover, v)
		if e == hypergraph.NoEdge {
			continue
		}
		d := domain{group: groupOf[e], scope: siblingScope(h, producedBy, e), rho: ob.RespKey}
		if _, seen := buckets[d]; !seen {
			keys = append(keys, d)
		}
		buckets[d] = append(buckets[d], aliasCandidate{edge: e, ob: ob.ID})
	}

	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.group != b.group {
			return a.group < b.group
		}
		if a.scope != b.scope {
			return a.scope < b.scope
		}
		return a.rho < b.rho
	})

	for _, d := range keys {
		ms := buckets[d]
		if len(ms) < 2 || !outputTypesConflict(h, definition, ms) {
			continue
		}
		for i, m := range ms {
			a := fmt.Sprintf("_planv2_%s_%d", d.rho, i) // unique within any document (counter suffix)
			am.byEdge[m.edge] = a
			am.byOb[m.ob] = a
			am.toResp[a] = d.rho
		}
	}
	return am
}

// siblingScope returns the node identifying the selection-set scope an edge's field is printed
// into: the abstract object when the field's parent object is a TypeMove head (member fragments are
// siblings of one another under the abstract parent), else the parent object itself.
func siblingScope(h *hypergraph.Hypergraph, producedBy map[hypergraph.NodeID]hypergraph.EdgeID, e hypergraph.EdgeID) hypergraph.NodeID {
	parent := h.Edge(e).Tails[0]
	if pe, ok := producedBy[parent]; ok && h.Edge(pe).Kind == hypergraph.EdgeTypeMove {
		return h.Edge(pe).Tails[0]
	}
	return parent
}

// aliasCandidate is one same-response-key selection inside a collision domain: the covering Field
// edge (fetch side) and the obligation it covers (response side).
type aliasCandidate struct {
	edge hypergraph.EdgeID
	ob   obligation.ObID
}

// outputTypesConflict reports whether the bucket's covered fields disagree on their printed output
// type, including list and non-null wrappers. A field whose type can't be resolved from the
// definition is treated as conflicting -- the safe choice, since aliasing a legal selection is
// harmless (the response mapping restores the client key) while missing a real conflict would
// produce an invalid subgraph query.
func outputTypesConflict(h *hypergraph.Hypergraph, definition *ast.Document, ms []aliasCandidate) bool {
	first := ""
	for i, m := range ms {
		head := h.Node(h.Edge(m.edge).Head)
		printed, ok := printedFieldType(definition, head.Type, head.Field)
		if !ok {
			return true
		}
		if i == 0 {
			first = printed
			continue
		}
		if printed != first {
			return true
		}
	}
	return false
}

// printedFieldType resolves and prints the full output type (with wrappers) of Type.field in the
// composed definition.
func printedFieldType(definition *ast.Document, typeName, fieldName string) (string, bool) {
	node, ok := definition.Index.FirstNodeByNameStr(typeName)
	if !ok {
		return "", false
	}
	fieldDef, ok := definition.NodeFieldDefinitionByName(node, []byte(fieldName))
	if !ok {
		return "", false
	}
	printed, err := definition.PrintTypeBytes(definition.FieldDefinitionType(fieldDef), nil)
	if err != nil {
		return "", false
	}
	return string(printed), true
}

// coveringFieldEdge returns the cover edge whose head is the goal's selected node -- the Field edge
// that resolves the goal. A covered field goal maps to exactly one field-producing edge in the plan.
// Returns hypergraph.NoEdge when v is not a field-producing node in the cover (e.g. a leaf Refine
// object candidate, which carries no response key of its own).
func coveringFieldEdge(h *hypergraph.Hypergraph, cover *search.Cover, v hypergraph.NodeID) hypergraph.EdgeID {
	for _, e := range cover.Edges {
		edge := h.Edge(e)
		if edge.Kind == hypergraph.EdgeField && edge.Head == v {
			return e
		}
	}
	return hypergraph.NoEdge
}
