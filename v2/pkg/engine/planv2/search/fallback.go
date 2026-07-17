package search

// fallback.go types the D10 completeness-preserving fall-back (FORMAL_SPEC D10 amendment --
// typed-loud fall-back). The fall-back itself lives at exactly two sites -- the goal loop's
// root-pin reversion (search.go) and the per-field scoped-walk reversion (scope.go) -- and its
// route-selection behavior is UNCHANGED by this file: recording a RouteFallback is observability
// only, part of the Result contract, so callers (the planv2 facade, the audit runner) can see
// every plan that was served through a path-inconsistent route instead of failing. Retirement
// gate: when the audit-corpus AND customer-corpus fallback counts are both zero, the fall-back
// branches are deleted and unroutable goals fail loud (ErrNoValidPlan); this file goes with them.

import (
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// RouteFallbackKind names which of the two fall-back branches fired.
type RouteFallbackKind string

const (
	// RouteFallbackRootPin is the goal-loop branch (search.go): a root-pinnable goal whose pinned
	// table reaches no candidate reverts to the plain settle tables.
	RouteFallbackRootPin RouteFallbackKind = "root-pin"
	// RouteFallbackScopedWalk is the per-field kappa branch (scope.go): a covered field whose masked
	// (path-scoped) table does not reach its serving node traces its walk over the plain back-edges.
	RouteFallbackScopedWalk RouteFallbackKind = "scoped-walk"
)

// RouteFallback records one firing of the D10 fall-back: the goal that could not be served
// path-consistently, which branch fired, and the (path-inconsistent) route that was taken
// instead. A goal can appear twice -- once per branch -- when both fire for it.
type RouteFallback struct {
	Goal       obligation.GoalID
	Kind       RouteFallbackKind
	Coordinate string              // the goal's obligation coordinate, "Type.field" ("Type on Concrete" for a refinement goal)
	RootField  string              // the operation root field the goal is anchored to ("" if none)
	Node       hypergraph.NodeID   // the serving node the goal resolved to
	Subgraph   string              // the serving node's subgraph name
	Route      []hypergraph.EdgeID // the route actually taken (traced over the unpinned/unmasked tables)
}

// goalCoordinate renders a goal's obligation coordinate for the fallback record: "Type.field" for
// a field (or typename-terminal) goal, "Type on Concrete" for a refinement goal (which has no
// field of its own).
func goalCoordinate(o *obligation.Tree, g obligation.GoalID) string {
	ob := o.Ob(g)
	if ob.Kind == obligation.Refine {
		return ob.Type + " on " + ob.Concrete
	}
	return ob.Type + "." + ob.Field
}
