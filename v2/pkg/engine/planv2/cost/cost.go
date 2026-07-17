// Package cost honors plan.Configuration.ComputeCosts for planner-v2 (M4.5). IBM @cost / @listSize
// cost is POST-PLAN: it never steers plan selection (that is the search layer's tree-cost, a distinct
// concept -- search/cost.go). Instead the router computes a static/actual cost from a CostCalculator
// attached to the finished plan, and enforces cost limits against it (execution_engine.go). If planv2
// accepted ComputeCosts but attached a nil calculator, ValidateSliceArguments and every cost-limit
// check would silently do nothing -- a security-grade defect. This package builds the same calculator
// v1 builds so the router enforces limits identically.
//
// Reuse over reimplementation: the cost tree, the weight/list-size/multiplier math, and
// Estimate/Actual/ValidateSliceArguments all live in the v1 plan package and are byte-for-byte
// reused via plan.BuildCostCalculator. The ONLY planner-specific input is which datasource(s) resolve
// each field -- v1 reads that from its internal fieldPlanners map; planv2 supplies it from the route
// the search chose, attributed here.
//
// Attribution (DV-012, DIVERGENCES.md): v1 lists EVERY planner that touched a field and sums their
// cost configs; planv2 attributes each field to the ONE route the search selected -- a non-empty
// subset, identical to the FieldInfo.Source subset already documented for M4.1. For the common case
// (a field's weight defined on the subgraph that resolves it) the estimate is identical; the subset
// only differs when a field is co-planned on multiple subgraphs whose cost configs disagree.
package cost

import (
	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// BuildCalculator returns the CostCalculator planner-v2 attaches to a plan when ComputeCosts is set,
// or nil when the operation prices to nothing. config carries the per-datasource CostConfig, the
// default list size, and the implementing-type-weight toggle (read by plan.NewCostCalculator).
// dsHashByName maps each subgraph name to its datasource hash (built once at NewPlanner). h/o/res are
// the built graph, obligation tree, and search result; operation/definition/operationName are the
// normalized inputs the facade already holds.
func BuildCalculator(
	config plan.Configuration,
	dsHashByName map[string]plan.DSHash,
	h *hypergraph.Hypergraph,
	o *obligation.Tree,
	res *search.Result,
	operation, definition *ast.Document,
	operationName string,
) *plan.CostCalculator {
	pathHashes := attributeDataSources(dsHashByName, h, o, res)
	resolver := func(fieldPath, _, _ string) []plan.DSHash {
		return pathHashes[fieldPath]
	}
	return plan.BuildCostCalculator(config, operation, definition, operationName, resolver)
}

// attributeDataSources maps each field's response path (e.g. "Query.hero.name") to the datasource
// hashes whose cost config applies -- the route the search chose to resolve it. It mirrors info.go's
// obligation->subgraph attribution: leaf goals take the search-selected node's subgraph, and
// unattributed obligations (composite objects, refinements, typenames, narrowed-away nulls) inherit
// from the nearest attributed descendant, then the nearest attributed ancestor.
func attributeDataSources(
	dsHashByName map[string]plan.DSHash,
	h *hypergraph.Hypergraph,
	o *obligation.Tree,
	res *search.Result,
) map[string][]plan.DSHash {
	obs := o.Obligations()

	// 1. Seed: each leaf goal takes the subgraph of the node the search selected for it.
	subgraphOf := make(map[obligation.ObID]string, len(obs))
	if res != nil && res.Cover != nil {
		for _, g := range o.Goals() {
			node, ok := res.Cover.Selected[g]
			if !ok {
				continue
			}
			name := h.SubgraphName(h.Node(node).Subgraph)
			if name != "" {
				subgraphOf[o.Ob(g).ID] = name
			}
		}
	}

	// 2. Reverse pass: an unattributed obligation inherits from its first attributed child
	//    (obligations are in document order, parents before children).
	for i := len(obs) - 1; i >= 0; i-- {
		ob := obs[i]
		if _, ok := subgraphOf[ob.ID]; ok {
			continue
		}
		for j := i + 1; j < len(obs); j++ {
			if obs[j].Parent == ob.ID {
				if name, ok := subgraphOf[obs[j].ID]; ok {
					subgraphOf[ob.ID] = name
					break
				}
			}
		}
	}
	// 3. Forward pass: still-unattributed obligations inherit from their parent.
	for _, ob := range obs {
		if _, ok := subgraphOf[ob.ID]; ok {
			continue
		}
		if ob.Parent != obligation.NoParent && ob.Parent != ob.ID {
			if name, ok := subgraphOf[ob.Parent]; ok {
				subgraphOf[ob.ID] = name
			}
		}
	}

	// 4. Response path per obligation, then map to datasource hashes.
	root := operationTypeLiteral(o)
	byID := make(map[obligation.ObID]obligation.Obligation, len(obs))
	for _, ob := range obs {
		byID[ob.ID] = ob
	}
	out := make(map[string][]plan.DSHash, len(obs))
	for _, ob := range obs {
		if ob.Kind != obligation.Field && ob.Kind != obligation.Typename {
			continue
		}
		name, ok := subgraphOf[ob.ID]
		if !ok {
			continue
		}
		hash, ok := dsHashByName[name]
		if !ok {
			continue
		}
		out[responsePath(byID, ob, root)] = []plan.DSHash{hash}
	}
	return out
}

// responsePath builds a field's response path the way the v1 CostVisitor seeds it: the literal
// operation-type name ("Query"/"Mutation"/"Subscription") followed by the response key of every
// field ancestor, root to leaf. Refinement obligations carry an empty RespKey and contribute no
// segment -- matching v1, where an inline fragment adds no field-path segment.
func responsePath(byID map[obligation.ObID]obligation.Obligation, ob obligation.Obligation, root string) string {
	var keys []string
	for cur := ob; ; {
		if cur.RespKey != "" {
			keys = append(keys, cur.RespKey)
		}
		if cur.Parent == obligation.NoParent || cur.Parent == cur.ID {
			break
		}
		parent, ok := byID[cur.Parent]
		if !ok {
			break
		}
		cur = parent
	}
	// keys are leaf->root; assemble root->leaf.
	path := root
	for i := len(keys) - 1; i >= 0; i-- {
		path += "." + keys[i]
	}
	return path
}

// operationTypeLiteral returns the literal operation-type name the v1 CostVisitor uses to seed the
// root of the cost tree's response paths (CostVisitor.operationTypeName). It is the operation KIND
// name, never the schema's (possibly renamed) root object type.
func operationTypeLiteral(o *obligation.Tree) string {
	switch {
	case o.SubscriptionOperation():
		return "Subscription"
	case o.MutationOperation():
		return "Mutation"
	default:
		return "Query"
	}
}
