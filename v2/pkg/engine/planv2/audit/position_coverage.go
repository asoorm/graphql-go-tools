package audit

// Assertion 6p -- position-aware forward coverage. Tightens assertion 6's forward direction from
// Type.field COORDINATES to response POSITIONS, using the same position vocabulary the response-path
// oracle (assertion 7) established on every fetch.
//
// Two blind spots of the coordinate-level check, both measured by the anti-cheat mutation audit
// (report blind spots 2 and 3):
//
//	(2) COORDINATE MASKING. A field dropped at one response position is invisible while the same
//	    Type.field is selected at ANOTHER position (`Product.id` under `category.mainProduct` masked
//	    by `products.id` in the same document; `User.name` under `users` masked by `accounts`' name).
//	    This check demands a covering fetch PER POSITION: some fetch must attach at a prefix of the
//	    position and select the field along the remaining response-key path.
//
//	(3) MEMBER-REFINED EXEMPTION. Assertion 6 exempts coordinates reached only under `... on M` --
//	    a member narrowed to a response-only null is legitimately unfetched. But the blanket
//	    exemption also hid drops inside members the plan DOES fetch. Narrowed here: a member-refined
//	    position is exempt only while NO fetch materializes its refinement member at that position;
//	    once any fetch resolves `... on M` (or a related type) there, every client-requested field
//	    under that refinement must be covered.
//
// MATCHING RULES. Positions are response-key paths (the assertion-7 vocabulary; `@` array markers
// dropped). A fetch document is walked from its attachment (root documents from the operation root;
// `_entities` documents from their `... on T` member fragments), matching each hop by response key --
// fetch documents print client aliases -- with two mechanism allowances: a member-variant alias
// (`_planv2_<name>_<n>`) matches its schema field name, while a requires-gather alias
// (`_planv2req_...`) never provides CLIENT coverage (it serves a requires binding, not the client's
// position). At a refined hop, the document-side type context (innermost `... on` condition, or the
// entity member) must be compatible with the client's refinement: equal, interface/union-related, or
// either side abstract -- only an unrelated pair of CONCRETE types is rejected, the assertion-7(C)
// conservatism, so interface-level (and @interfaceObject-flattened) supply of a member field never
// false-fails while a wrong-member supply does.
//
// SCOPE. Forward direction only (the backward direction stays with assertion 6). An UNREFINED
// abstract-position field remains satisfiable by any one member's selection (member explosion --
// the adjudicated assertion-6 leniency; per-member completeness stays executed-truth's business).
// Consequently a dropped WHOLE member branch of an unrefined abstract field, and a drop whose
// refinement member no fetch materializes, remain out of plan-level reach -- documented intentional
// survivors in the mutation suite.

import (
	"fmt"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// posSeg is one hop of a client response position.
type posSeg struct {
	key       string // response key (alias-or-name)
	fieldName string // schema field name
	typeCond  string // innermost `... on` condition this hop is selected under ("" when ungated)
}

// posOccurrence is one client-requested field occurrence: the full response-key path to it.
type posOccurrence struct {
	segs []posSeg
}

func (o posOccurrence) String() string {
	keys := make([]string, len(o.segs))
	for i, s := range o.segs {
		keys[i] = s.key
	}
	return strings.Join(keys, ".")
}

// docState is a cursor into a fetch document: a selection set plus the type context it is read at.
type docState struct {
	set int    // selection set ref
	ctx string // type context ("" = unknown; never used to reject)
	// member marks an ENTITY MEMBER context (the `... on M` directly under `_entities`): the one
	// context whose type itself proves the member is materialized. A field's OUTPUT type context
	// must not -- an abstract output type is `typesRelated` to every member, which would falsely
	// materialize members the plan narrowed away.
	member bool
}

// fetchWalker is one fetch document prepared for position walks.
type fetchWalker struct {
	idx    int
	attach []string // normalized attachment position (ResponsePath, `@` markers dropped)
	doc    *ast.Document
	starts []docState
}

// validatePositionCoverage implements assertion 6p.
func validatePositionCoverage(fetches []*resolve.FetchItem, operation string, def *ast.Document) error {
	opDoc := unsafeparser.ParseGraphqlDocumentString(operation)
	rootType := operationRootType(&opDoc, def)

	var walkers []*fetchWalker
	for i, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok || sf == nil {
			continue
		}
		raw := fetchQuery(sf)
		if raw == "" {
			continue
		}
		parsed := unsafeparser.ParseGraphqlDocumentString(raw)
		w := &fetchWalker{idx: i, doc: &parsed}
		if sf.RequiresEntityFetch || sf.RequiresEntityBatchFetch {
			for _, seg := range strings.Split(item.ResponsePath, ".") {
				if seg != "" && seg != "@" {
					w.attach = append(w.attach, seg)
				}
			}
			w.starts = entityMemberStates(&parsed)
		} else {
			// A plain document reads from the operation root; its absolute positions start at "".
			for ref := range parsed.OperationDefinitions {
				w.starts = []docState{{set: parsed.OperationDefinitions[ref].SelectionSet, ctx: rootType}}
				break
			}
		}
		if len(w.starts) > 0 {
			walkers = append(walkers, w)
		}
	}

	occs := clientPositions(&opDoc, def, rootType)
	for _, occ := range occs {
		if positionCovered(walkers, occ, def) {
			continue
		}
		if refinementNarrowed(walkers, occ, def) {
			continue // a refinement member no fetch materializes: legitimately unfetched
		}
		last := occ.segs[len(occ.segs)-1]
		return fmt.Errorf(
			"position-coverage (assertion 6p): requested field %q (response position %q%s) is not selected by any fetch document serving that position -- a same-coordinate selection elsewhere does not serve this position",
			last.fieldName, occ.String(), refinementNote(occ))
	}
	return nil
}

func refinementNote(occ posOccurrence) string {
	for _, s := range occ.segs {
		if s.typeCond != "" {
			return fmt.Sprintf(", refined via `... on %s` which the plan materializes at that position", s.typeCond)
		}
	}
	return ""
}

// clientPositions enumerates every client-requested field occurrence with its response-key path.
func clientPositions(opDoc, def *ast.Document, rootType string) []posOccurrence {
	var out []posOccurrence
	var walk func(setRef int, enclosing, typeCond string, prefix []posSeg, depth int)
	walk = func(setRef int, enclosing, typeCond string, prefix []posSeg, depth int) {
		if setRef < 0 || depth > 32 {
			return
		}
		for _, selRef := range opDoc.SelectionSets[setRef].SelectionRefs {
			sel := opDoc.Selections[selRef]
			switch sel.Kind {
			case ast.SelectionKindField:
				name := opDoc.FieldNameString(sel.Ref)
				if name == "__typename" {
					continue
				}
				on := typeCond
				if on == "" {
					on = enclosing
				}
				seg := posSeg{key: opDoc.FieldAliasOrNameString(sel.Ref), fieldName: name, typeCond: typeCond}
				occ := make([]posSeg, len(prefix)+1)
				copy(occ, prefix)
				occ[len(prefix)] = seg
				out = append(out, posOccurrence{segs: occ})
				if ss, has := opDoc.FieldSelectionSet(sel.Ref); has {
					walk(ss, namedFieldOutputType(def, on, name), "", occ, depth+1)
				}
			case ast.SelectionKindInlineFragment:
				tc := opDoc.InlineFragmentTypeConditionNameString(sel.Ref)
				if tc == "" {
					tc = typeCond // condition-less fragment keeps the current context
				}
				walk(opDoc.InlineFragments[sel.Ref].SelectionSet, enclosing, tc, prefix, depth+1)
			}
		}
	}
	for ref := range opDoc.OperationDefinitions {
		walk(opDoc.OperationDefinitions[ref].SelectionSet, rootType, "", nil, 0)
		break // audit operations are single-operation
	}
	return out
}

// entityMemberStates returns the `... on T` member selection sets directly under `_entities`.
func entityMemberStates(doc *ast.Document) []docState {
	var out []docState
	var scan func(setRef int, underEntities bool)
	scan = func(setRef int, underEntities bool) {
		if setRef < 0 {
			return
		}
		for _, selRef := range doc.SelectionSets[setRef].SelectionRefs {
			sel := doc.Selections[selRef]
			switch sel.Kind {
			case ast.SelectionKindField:
				if doc.FieldNameString(sel.Ref) == "_entities" {
					if ss, ok := doc.FieldSelectionSet(sel.Ref); ok {
						scan(ss, true)
					}
				}
			case ast.SelectionKindInlineFragment:
				if underEntities {
					out = append(out, docState{
						set:    doc.InlineFragments[sel.Ref].SelectionSet,
						ctx:    doc.InlineFragmentTypeConditionNameString(sel.Ref),
						member: true,
					})
				}
			}
		}
	}
	for ref := range doc.OperationDefinitions {
		scan(doc.OperationDefinitions[ref].SelectionSet, false)
		break
	}
	return out
}

// positionCovered reports whether some fetch document selects occ's field at occ's position.
func positionCovered(walkers []*fetchWalker, occ posOccurrence, def *ast.Document) bool {
	for _, w := range walkers {
		if walkerCovers(w, occ, def) {
			return true
		}
	}
	return false
}

// walkerCovers walks w's document along occ's segments below w's attachment.
func walkerCovers(w *fetchWalker, occ posOccurrence, def *ast.Document) bool {
	if len(w.attach) >= len(occ.segs) || !attachIsPrefix(w.attach, occ.segs) {
		return false
	}
	states := w.starts
	rem := occ.segs[len(w.attach):]
	for i, seg := range rem {
		var next []docState
		for _, st := range states {
			next = append(next, matchDocField(w.doc, def, st.set, st.ctx, seg)...)
		}
		if len(next) == 0 {
			return false
		}
		if i == len(rem)-1 {
			return true
		}
		states = next
	}
	return false
}

// attachIsPrefix reports whether the fetch attachment position is a prefix of occ's key path.
func attachIsPrefix(attach []string, segs []posSeg) bool {
	for j, a := range attach {
		if a == segs[j].key {
			continue
		}
		if reqMechanismFieldName(a) == segs[j].fieldName {
			continue // mechanism-aliased attach position of the same schema field
		}
		return false
	}
	return true
}

// matchDocField finds fields in selection set setRef (descending inline fragments, ctx tracking the
// innermost type condition) matching client segment seg, and returns their child cursors.
func matchDocField(doc, def *ast.Document, setRef int, ctx string, seg posSeg) []docState {
	var out []docState
	var scan func(setRef int, ctx string, depth int)
	scan = func(setRef int, ctx string, depth int) {
		if setRef < 0 || depth > 32 {
			return
		}
		for _, selRef := range doc.SelectionSets[setRef].SelectionRefs {
			sel := doc.Selections[selRef]
			switch sel.Kind {
			case ast.SelectionKindField:
				if !docFieldMatches(doc, sel.Ref, seg) {
					continue
				}
				if seg.typeCond != "" && !typeContextCompatible(def, ctx, seg.typeCond) {
					continue // wrong-member supply is not coverage
				}
				child := -1
				if ss, has := doc.FieldSelectionSet(sel.Ref); has {
					child = ss
				}
				out = append(out, docState{set: child, ctx: namedFieldOutputType(def, ctx, doc.FieldNameString(sel.Ref))})
			case ast.SelectionKindInlineFragment:
				tc := doc.InlineFragmentTypeConditionNameString(sel.Ref)
				if tc == "" {
					tc = ctx
				}
				scan(doc.InlineFragments[sel.Ref].SelectionSet, tc, depth+1)
			}
		}
	}
	scan(setRef, ctx, 0)
	return out
}

// docFieldMatches matches one document field against a client segment: by response key (documents
// print client aliases), or -- for lowering's member-variant alias `_planv2_...` -- by schema field
// name. A requires-gather alias (`_planv2req_...`) serves a requires binding, never client coverage.
func docFieldMatches(doc *ast.Document, fieldRef int, seg posSeg) bool {
	key := doc.FieldAliasOrNameString(fieldRef)
	if key == seg.key {
		return true
	}
	if strings.HasPrefix(key, "_planv2req_") {
		return false
	}
	if strings.HasPrefix(key, "_planv2_") {
		return doc.FieldNameString(fieldRef) == seg.fieldName
	}
	return false
}

// typeContextCompatible applies the assertion-7(C) conservatism to a refined hop: only an unrelated
// pair of CONCRETE types is incompatible; unknown context ("") never rejects.
func typeContextCompatible(def *ast.Document, ctx, cond string) bool {
	if ctx == "" || cond == "" || ctx == cond {
		return true
	}
	if typesRelated(def, ctx, cond) {
		return true
	}
	return isAbstractType(def, ctx) || isAbstractType(def, cond)
}

// refinementNarrowed reports whether occ sits under a refinement member NO fetch materializes at the
// refinement's position -- the narrowed remainder of assertion 6's member exemption.
func refinementNarrowed(walkers []*fetchWalker, occ posOccurrence, def *ast.Document) bool {
	for k, seg := range occ.segs {
		if seg.typeCond == "" {
			continue
		}
		if !memberMaterialized(walkers, occ.segs[:k], seg.typeCond, def) {
			return true
		}
	}
	return false
}

// memberMaterialized reports whether any fetch resolves member type cond (or a related type) at the
// response position given by parent segs: either an entity fetch attached there whose member set
// includes it, or a document whose walk to that position lands in a compatible fragment.
func memberMaterialized(walkers []*fetchWalker, parent []posSeg, cond string, def *ast.Document) bool {
	for _, w := range walkers {
		if len(w.attach) > len(parent) || !attachIsPrefix(w.attach, parent) {
			continue
		}
		states := w.starts
		ok := true
		for _, seg := range parent[len(w.attach):] {
			var next []docState
			for _, st := range states {
				next = append(next, matchDocField(w.doc, def, st.set, st.ctx, seg)...)
			}
			if len(next) == 0 {
				ok = false
				break
			}
			states = next
		}
		if !ok {
			continue
		}
		for _, st := range states {
			if stateHasFragment(w.doc, def, st, cond) {
				return true
			}
		}
	}
	return false
}

// stateHasFragment reports whether the cursor's level carries an inline fragment TOKEN
// (transitively) -- or IS an entity member context -- compatible with cond in the strict direction:
// equal or interface/union-related. Only actual `... on T` conditions (and the entity member's own
// type) count: a field's abstract OUTPUT type is related to every member and must not materialize
// anything (blanket "either abstract" is likewise deliberately NOT enough).
func stateHasFragment(doc, def *ast.Document, st docState, cond string) bool {
	if st.member && st.ctx != "" && (st.ctx == cond || typesRelated(def, st.ctx, cond)) {
		return true
	}
	var scan func(setRef, depth int) bool
	scan = func(setRef, depth int) bool {
		if setRef < 0 || depth > 32 {
			return false
		}
		for _, selRef := range doc.SelectionSets[setRef].SelectionRefs {
			sel := doc.Selections[selRef]
			if sel.Kind != ast.SelectionKindInlineFragment {
				continue
			}
			tc := doc.InlineFragmentTypeConditionNameString(sel.Ref)
			if tc == cond || typesRelated(def, tc, cond) {
				return true
			}
			if scan(doc.InlineFragments[sel.Ref].SelectionSet, depth+1) {
				return true
			}
		}
		return false
	}
	return scan(st.set, 0)
}
