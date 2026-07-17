package audit

// Assertion 8 -- key-supply completeness.
//
// An entity fetch declares what its runtime representation reads via its QueryPlan Key/Requires
// fragments (`fragment Key on User { __typename email }`): the loader builds the `_entities`
// representation object from data that EARLIER fetches placed at the fetch's position. Assertions
// 1-7 never connect those two ends: a source document that silently drops a key field (`email` from
// `{ user { __typename id email } }`) still validates against its subgraph (assertion 2), still
// covers every CLIENT-requested coordinate (assertion 6 -- a key field is a mechanism, not a client
// request), and leaves the dependent fetch's paths intact (assertion 7) -- yet the runtime jump would
// send `null` key values and the entity fetch would return nothing. The anti-cheat mutation audit
// measured exactly this blind spot (M5 key-drop survivors; report blind spot 1).
//
// This assertion closes it: for every entity fetch, every field coordinate its representation
// fragments require must be SELECTED by the fetches it (transitively) depends on. Coordinates are
// the same Type.field vocabulary as assertion 6 (fragmentCoords / coordWalk); a requirement is
// satisfied by an exact coordinate or by a related-type coordinate of the same field name (interface
// supply for a concrete requirement, member supply for an abstract one -- e.g. an @interfaceObject
// representation on `NodeWithName` supplied under `... on User`, or vice versa). A requires-gather
// alias (`_planv2req_<field>_<n>`) is normalized back to its schema field name on the requirement
// side; suppliers are read by field NAME, so source-document aliasing never false-fails.
//
// SOUNDNESS. The fragments are the plan's own declaration of its runtime inputs (they render from
// the same tries as the wire representation -- verified by the anti-cheat audit's pre-check), so
// requiring their supply is a necessary condition on a correct plan. A corrupted fragment now also
// fails here (it declares a requirement nobody supplies) -- the fragment is assertion 6/7's
// admissibility input, so a lying fragment is itself a defect worth failing.

import (
	"fmt"
	"sort"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// validateKeySupply implements assertion 8. operation is the normalized client operation (used only
// to resolve the root type for supplier-document coordinate walks); def is the composed client-facing
// definition.
func validateKeySupply(fetches []*resolve.FetchItem, operation string, def *ast.Document) error {
	opDoc := unsafeparser.ParseGraphqlDocumentString(operation)
	rootType := operationRootType(&opDoc, def)

	// Each fetch document's selected coordinates, computed once (same walk as assertion 6).
	selected := make([]map[string]bool, len(fetches))
	for i, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok || sf == nil {
			continue
		}
		doc := fetchQuery(sf)
		if doc == "" {
			continue
		}
		fdoc := unsafeparser.ParseGraphqlDocumentString(doc)
		fw := newCoordWalk(&fdoc, def)
		start := rootType
		if sf.RequiresEntityFetch || sf.RequiresEntityBatchFetch {
			start = "" // the `_entities` wrapper sets member types via `... on Type`
		}
		for ref := range fdoc.OperationDefinitions {
			fw.walkSet(fdoc.OperationDefinitions[ref].SelectionSet, start, false, 0)
			break
		}
		selected[i] = fw.all
	}

	for i, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok || sf == nil || sf.QueryPlan == nil {
			continue
		}
		if !sf.RequiresEntityFetch && !sf.RequiresEntityBatchFetch {
			continue // only `_entities` fetches consume a representation
		}
		if len(sf.QueryPlan.DependsOnFields) == 0 {
			continue
		}

		suppliers := transitiveDeps(fetches, i)
		if len(suppliers) == 0 {
			// Defensive: an entity fetch with no recorded dependencies still reads data some earlier
			// fetch produced -- measure against everything before it rather than nothing.
			for j := 0; j < i; j++ {
				suppliers[j] = true
			}
		}
		supplied := map[string]bool{}
		for j := range suppliers {
			for c := range selected[j] {
				supplied[c] = true
			}
		}

		var missing []string
		for _, rep := range sf.QueryPlan.DependsOnFields {
			for coord := range fragmentCoords(rep.Fragment, def) {
				if !coordSupplied(coord, supplied, def) {
					missing = append(missing, string(rep.Kind)+" "+coord)
				}
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf(
				"key-supply (assertion 8): entity fetch %d (subgraph %q, position %q) requires representation field(s) no dependency fetch selects: %v -- the runtime entity jump would be built from data its source fetches never fetched",
				i, sf.DataSourceIdentifier, item.ResponsePath, missing)
		}
	}
	return nil
}

// transitiveDeps returns the transitive closure of fetch i's DependsOnFetchIDs (dependencies are
// indices of earlier fetches -- assertion 3 enforces the ordering, so the closure terminates).
func transitiveDeps(fetches []*resolve.FetchItem, i int) map[int]bool {
	out := map[int]bool{}
	var visit func(idx int)
	visit = func(idx int) {
		sf, ok := fetches[idx].Fetch.(*resolve.SingleFetch)
		if !ok || sf == nil {
			return
		}
		for _, dep := range sf.DependsOnFetchIDs {
			if dep < 0 || dep >= len(fetches) || out[dep] {
				continue
			}
			out[dep] = true
			visit(dep)
		}
	}
	visit(i)
	return out
}

// coordSupplied reports whether required coordinate `T.f` is satisfied by the supplied set: exactly,
// or by a RELATED type's same-named field (interface/union relationship in either direction, or
// either side abstract) -- the same conservative type-relatedness assertion 7(C) uses, so legitimate
// interface-level supply of a concrete requirement (and @interfaceObject flattening) never
// false-fails while an unrelated concrete supplier still does.
func coordSupplied(coord string, supplied map[string]bool, def *ast.Document) bool {
	typ, field, ok := splitCoord(coord)
	if !ok {
		return supplied[coord]
	}
	// A requires-gather alias reads the aliased position back; the schema field is what suppliers
	// select.
	field = reqMechanismFieldName(field)
	if supplied[typ+"."+field] {
		return true
	}
	for c := range supplied {
		st, sf, ok := splitCoord(c)
		if !ok || sf != field || st == typ {
			continue
		}
		if typesRelated(def, st, typ) || isAbstractType(def, st) || isAbstractType(def, typ) {
			return true
		}
	}
	return false
}
