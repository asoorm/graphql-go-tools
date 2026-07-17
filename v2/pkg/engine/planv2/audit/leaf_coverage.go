package audit

// Assertion 6 -- leaf coverage and path correspondence.
//
// Assertions 1-5 miss a whole class of wrong data: a leaf in the response fetched from the wrong
// place, or a fetch selection that is missing or misplaced BELOW the top level of a fetch document
// (assertion 5 only checks a root fetch's TOP-LEVEL fields). Two real bugs look like this: for
// `order { buyer { rating } seller { rating } }` a plan can emit a root document selecting only
// `buyer` (seller dropped); and another case can select un-requested sibling fields (`aMedia`/`bMedia`)
// while never selecting the requested ones (`media`/`song`). Both kinds of plan pass 1-5.
//
// This check looks in the two directions the earlier ones miss. It works at the Type.field COORDINATE
// level (which needs no per-request response-path data) and recurses below the top level:
//
//   FORWARD (leaf coverage): every field coordinate the client requests, unless it is reached only
//   through an abstract refinement (`... on C`), must be selected by SOME fetch document. A dropped
//   sibling (`Order.seller`, `Viewer.media`, `Viewer.song`) is caught here -- no fetch selects it.
//   Coordinates reached ONLY under an inline fragment are excluded: a member that was narrowed to a
//   response-only null is legitimately never fetched. So the check stays a sound necessary condition
//   on the fields the client always requests.
//
//   BACKWARD (path correspondence -- the same idea as assertion 5, but below the top level): every
//   field coordinate a fetch document selects must correspond to a client request, __typename, or a
//   legitimate mechanism -- a @key/@requires representation field (read off each entity fetch's
//   QueryPlan Key/Requires fragments). An un-requested sibling (`Viewer.aMedia`/`bMedia`) is caught here.
//
// Scope. Coordinate-level correspondence catches the dropped-field and un-requested-field cases (both
// examples above). It does NOT catch the remaining shared-instance case: one entity fetch serving two
// sibling response positions of the SAME coordinate -- e.g. both `buyer` and `seller` resolved by a
// single `_entities` fetch at one response path. That needs response-path analysis, which requires
// lowering to assign each fetch a response path (assertion 7 covers it). This check is intentionally
// sound (it never fails a legitimate plan) rather than complete.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// validateLeafCoverage implements assertion 6. def is the composed client-facing definition (already
// base-merged) used to resolve each field's output type so the coordinate walk can descend.
func validateLeafCoverage(fetches []*resolve.FetchItem, operation string, def *ast.Document) error {
	opDoc := unsafeparser.ParseGraphqlDocumentString(operation)
	rootType := operationRootType(&opDoc, def)

	client := newCoordWalk(&opDoc, def)
	for ref := range opDoc.OperationDefinitions {
		client.walkSet(opDoc.OperationDefinitions[ref].SelectionSet, rootType, false, 0)
		break // audit operations are single-operation
	}

	fetchAll := map[string]bool{} // every coordinate any fetch document selects
	allowed := map[string]bool{}  // client-requested  union  @key/@requires mechanism coordinates
	for c := range client.all {
		allowed[c] = true
	}

	for _, item := range fetches {
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
			start = "" // the `_entities` wrapper (single or batch) sets member types via `... on Type`
		}
		for ref := range fdoc.OperationDefinitions {
			fw.walkSet(fdoc.OperationDefinitions[ref].SelectionSet, start, false, 0)
			break
		}
		for c := range fw.all {
			fetchAll[c] = true
		}
		// @key/@requires representation fields are legitimate non-client selections.
		if sf.QueryPlan != nil {
			for _, rep := range sf.QueryPlan.DependsOnFields {
				for c := range fragmentCoords(rep.Fragment, def) {
					allowed[c] = true
				}
			}
		}
	}

	// FORWARD: every unconditionally-requested client coordinate must be selected somewhere. An
	// ABSTRACT coordinate U.f (U an interface/union in the composed schema) is additionally satisfied
	// by MEMBER coordinates: under member expansion (FORMAL_SPEC D3pppp -- an interface field locally
	// unresolvable at its position is planned as one `... on C { f }` branch per position-possible
	// member) the fetch documents legitimately select C.f per concrete member and never U.f itself --
	// the same plan shape the reference gateway emits for `products { reviews }`. Requiring at least
	// one member's C.f keeps the check a sound necessary condition (a wholly-dropped field still
	// fails); per-member completeness is the response-shape oracle's and the differential harness's
	// business (position-possible member sets are not visible at coordinate level).
	implementers := abstractImplementers(def)
	var missing []string
	for c := range client.concrete {
		if fetchAll[c] {
			continue
		}
		if u, f, ok := splitCoord(c); ok {
			covered := false
			for _, impl := range implementers[u] {
				if fetchAll[impl+"."+f] {
					covered = true
					break
				}
			}
			// UPCAST (the reverse direction -- D3io/D11.9 flattened placement): a CONCRETE
			// coordinate C.f is additionally satisfied by an ABSTRACT coordinate I.f for an
			// interface I that C implements. An @interfaceObject subgraph resolves f on the
			// interface FOR EVERY implementer (the Fed 2.3 wire contract), so the fetch document
			// legitimately selects `... on I { f }` and never C.f -- the same mechanism the
			// backward check below already names for its field-name matching ("an interface field
			// satisfying a concrete request"). Witness:
			// conformance FS-IFO-1/interface-object/contributed-field-concrete
			// (`usersConcrete: [User]` selecting the ifo-contributed field fragment-free).
			if !covered {
				for iface, impls := range implementers {
					if !fetchAll[iface+"."+f] {
						continue
					}
					for _, impl := range impls {
						if impl == u {
							covered = true
							break
						}
					}
					if covered {
						break
					}
				}
			}
			if covered {
				continue
			}
		}
		missing = append(missing, c)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf(
			"leaf-coverage (assertion 6): requested field(s) never selected by any fetch document: %v -- a covering walk that does not factor through its obligation path (sibling-conflation / dropped-field residual)",
			missing)
	}

	// BACKWARD (recurses assertion 5 below top level): every selected coordinate must correspond to a
	// request or a key/requires/__typename mechanism. Matched at FIELD-NAME level (not the full
	// Type.field coordinate) so an interface/concrete-type split on a legitimate mechanism field does
	// not false-FAIL: a @key `id` selected on an interface node (`NodeWithName.id`) while the jump keys
	// the concrete type (`User.id`), or an interface field satisfying a concrete request
	// (`Product.id` covering `Bread.id`), are real mechanisms -- their NAME is requested-or-key even
	// when the exact type differs. A genuinely-foreign sibling (`aMedia`/`bMedia`), whose NAME appears
	// nowhere in the request and in no key/requires fragment, is still caught.
	allowedNames := map[string]bool{}
	for c := range allowed {
		if _, f, ok := splitCoord(c); ok {
			allowedNames[f] = true
		}
	}
	var foreign []string
	for c := range fetchAll {
		_, f, ok := splitCoord(c)
		if ok && allowedNames[f] {
			continue
		}
		foreign = append(foreign, c)
	}
	if len(foreign) > 0 {
		sort.Strings(foreign)
		return fmt.Errorf(
			"path-correspondence (assertion 6): fetch document selects field(s) not requested and not a key/requires/__typename mechanism: %v -- an un-requested sibling selection (sibling-conflation)",
			foreign)
	}
	return nil
}

// abstractImplementers maps every abstract type of the composed definition to its concrete member
// type names (object types implementing an interface; union member types) -- the member-coordinate
// acceptance set for the forward check above.
func abstractImplementers(def *ast.Document) map[string][]string {
	out := map[string][]string{}
	if def == nil {
		return out
	}
	for ref := range def.InterfaceTypeDefinitions {
		iface := def.InterfaceTypeDefinitionNameString(ref)
		for objRef := range def.ObjectTypeDefinitions {
			objName := def.ObjectTypeDefinitionNameString(objRef)
			if node, ok := def.Index.FirstNodeByNameStr(objName); ok &&
				def.NodeImplementsInterface(node, ast.ByteSlice(iface)) {
				out[iface] = append(out[iface], objName)
			}
		}
	}
	for ref := range def.UnionTypeDefinitions {
		u := def.UnionTypeDefinitionNameString(ref)
		if members, ok := def.UnionTypeDefinitionMemberTypeNames(ref); ok {
			out[u] = append(out[u], members...)
		}
	}
	return out
}

// splitCoord splits a "Type.field" coordinate into its parts; ok is false if it is not a coordinate.
func splitCoord(c string) (typ, field string, ok bool) {
	i := strings.LastIndexByte(c, '.')
	if i <= 0 || i == len(c)-1 {
		return "", "", false
	}
	return c[:i], c[i+1:], true
}

// coordWalk collects Type.field coordinates from a document's selection tree, resolving each field's
// output type against def to descend. `all` is every coordinate seen; `concrete` is the subset reached
// WITHOUT passing through an inline fragment (so abstract-refinement members, which may be narrowed to
// a response-only null and legitimately unfetched, are excluded from the forward coverage requirement).
type coordWalk struct {
	doc      *ast.Document
	def      *ast.Document
	all      map[string]bool
	concrete map[string]bool
}

func newCoordWalk(doc, def *ast.Document) *coordWalk {
	return &coordWalk{doc: doc, def: def, all: map[string]bool{}, concrete: map[string]bool{}}
}

func (w *coordWalk) walkSet(setRef int, enclosing string, underRefine bool, depth int) {
	if setRef < 0 || depth > 32 {
		return
	}
	for _, selRef := range w.doc.SelectionSets[setRef].SelectionRefs {
		sel := w.doc.Selections[selRef]
		switch sel.Kind {
		case ast.SelectionKindField:
			fname := w.doc.FieldNameString(sel.Ref)
			if fname == "__typename" {
				continue // meta field: synthesized, never an obligation coordinate
			}
			if fname == "_entities" {
				// federation meta field: descend into its `... on Type` fragments (type set there).
				if ss, ok := w.doc.FieldSelectionSet(sel.Ref); ok {
					w.walkSet(ss, "", underRefine, depth+1)
				}
				continue
			}
			if enclosing != "" {
				coord := enclosing + "." + fname
				w.all[coord] = true
				if !underRefine {
					w.concrete[coord] = true
				}
			}
			if ss, ok := w.doc.FieldSelectionSet(sel.Ref); ok {
				w.walkSet(ss, w.fieldType(enclosing, fname), underRefine, depth+1)
			}
		case ast.SelectionKindInlineFragment:
			tc := w.doc.InlineFragmentTypeConditionNameString(sel.Ref)
			if tc == "" {
				tc = enclosing
			}
			w.walkSet(w.doc.InlineFragments[sel.Ref].SelectionSet, tc, true, depth+1)
		}
	}
}

// fieldType resolves fname's named output type on enclosing against def (List/NonNull stripped).
// Returns "" when the type is unknown, which halts descent (no false coordinates).
func (w *coordWalk) fieldType(enclosing, fname string) string {
	if enclosing == "" || w.def == nil {
		return ""
	}
	node, ok := w.def.Index.FirstNodeByNameStr(enclosing)
	if !ok {
		return ""
	}
	fd, ok := w.def.NodeFieldDefinitionByName(node, ast.ByteSlice(fname))
	if !ok {
		return ""
	}
	return w.def.FieldDefinitionTypeNameString(fd)
}

// fragmentCoords parses a printed representation fragment (`fragment Key on User { __typename id }`)
// and returns its Type.field coordinates (nested @requires selections included).
func fragmentCoords(fragment string, def *ast.Document) map[string]bool {
	out := map[string]bool{}
	if fragment == "" {
		return out
	}
	fdoc := unsafeparser.ParseGraphqlDocumentString(fragment)
	w := newCoordWalk(&fdoc, def)
	for ref := range fdoc.FragmentDefinitions {
		tc := fdoc.FragmentDefinitionTypeNameString(ref)
		w.walkSet(fdoc.FragmentDefinitions[ref].SelectionSet, tc, false, 0)
	}
	for c := range w.all {
		out[c] = true
	}
	return out
}

// operationRootType returns the root object type name for the operation's kind, resolved against the
// definition's schema roots (default Query/Mutation names when the schema omits an explicit block).
func operationRootType(opDoc, def *ast.Document) string {
	opType := ast.OperationTypeQuery
	for ref := range opDoc.OperationDefinitions {
		opType = opDoc.OperationDefinitions[ref].OperationType
		break
	}
	if opType == ast.OperationTypeMutation {
		if len(def.Index.MutationTypeName) > 0 {
			return string(def.Index.MutationTypeName)
		}
		return "Mutation"
	}
	if len(def.Index.QueryTypeName) > 0 {
		return string(def.Index.QueryTypeName)
	}
	return "Query"
}
