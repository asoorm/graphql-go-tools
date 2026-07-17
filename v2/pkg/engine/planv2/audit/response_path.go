package audit

// Assertion 7 -- response-path check. Closes the shared-instance gap left open by assertion 6's scope
// note.
//
// Assertion 6 works at the coordinate (Type.field) level and so is PATH-BLIND: it cannot see that one
// entity fetch serves the WRONG response position, or serves two sibling positions of the same
// coordinate with only one fetch. Its own scope note left that to response-path analysis, which needs
// lowering to assign each fetch a response path. Lowering now does exactly that: every fetch carries
// the `ResponsePath`/`FetchPath` of the position it serves, by default, on every plan. Assertion 7
// consumes it.
//
// For every ENTITY fetch (`_entities`, RequiresEntityFetch) the oracle checks that its attachment path is
// the position it actually serves:
//
//   (A) REQUESTED POSITION. The fetch's `ResponsePath` must denote a response position the client
//       operation actually requests: walking the operation selection tree along the path's response-key
//       segments (descending through inline fragments, honouring aliases) must reach a real field at each
//       hop. A fetch mis-attributed to a position the client never asked for -- the sibling-conflation
//       failure mode one level below assertion 5 -- is caught here.
//
//   (B) FETCHPATH <-> RESPONSEPATH CONSISTENCY. The `FetchPath` (the resolve-layer attach path the loader
//       walks) must carry the SAME response-key field sequence as `ResponsePath`, and each `@` array
//       marker in `ResponsePath` must sit on an Array-kind `FetchPath` element. A fetch whose two path
//       encodings disagree would attach its data at a different place than its stated position.
//
//   (C) MEMBER-TYPE COMPATIBILITY (conservative). The entity fetch's `... on T` member type must be
//       reachable at the landed position's type (equal, or an interface/union/implementer relationship,
//       or an @interfaceObject flattening where the position is abstract). Only a T that is an unrelated
//       CONCRETE object type at a CONCRETE landed position fails -- kept conservative so legitimate
//       @interfaceObject representation typing (the representation's `__typename` stays the member
//       type) is NOT flagged; that datum lives in the representation fragment, not the ResponsePath,
//       and this check reads the ResponsePath.
//
// SOUNDNESS. All three checks are necessary conditions on a correct plan; none rejects a legitimate one
// in the corpus. Root (non-entity) fetches are covered by assertion 5 (root-entry) and are out of scope
// here (they attach at ResponsePath "").

import (
	"fmt"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// validateResponsePaths implements assertion 7 over the plan's entity fetches.
func validateResponsePaths(fetches []*resolve.FetchItem, operation string, def *ast.Document) error {
	opDoc := unsafeparser.ParseGraphqlDocumentString(operation)
	rootType := operationRootType(&opDoc, def)
	rootSet := -1
	for ref := range opDoc.OperationDefinitions {
		rootSet = opDoc.OperationDefinitions[ref].SelectionSet
		break
	}

	for i, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		// Validate BOTH single (RequiresEntityFetch) and BATCH (RequiresEntityBatchFetch) entity
		// fetches -- a batch fetch under an array position must still attach at a requested response
		// position, and its `@` array markers must line up with Array-kind FetchPath elements.
		if !ok || sf == nil || (!sf.RequiresEntityFetch && !sf.RequiresEntityBatchFetch) {
			continue
		}
		p := item.ResponsePath
		if p == "" {
			return fmt.Errorf(
				"response-path (assertion 7): entity fetch %d (subgraph %q) has empty ResponsePath -- an `_entities` fetch must attach at the response position of the parent that carries its key",
				i, sf.DataSourceIdentifier)
		}
		// (A) the ResponsePath must denote a requested response position -- or a REQUIRES-MECHANISM
		// position (D11.10): a requires-input pipeline fetch attaches under a consuming fetch's
		// position along that fetch's `Requires` fragment path (`feed.@.author` gathering
		// `author { yearsOfExperience }` for a fetch at `feed`). Justified segments must continue a
		// declared Requires-fragment path rooted at the consuming fetch's own position; anything else
		// still fails (the sibling-conflation failure mode this assertion exists for).
		landed, err := walkRequestedResponsePath(&opDoc, def, rootType, rootSet, p, requiresPathRoots(fetches))
		if err != nil {
			return fmt.Errorf(
				"response-path (assertion 7): entity fetch %d (subgraph %q) ResponsePath %q is not a requested response position: %v",
				i, sf.DataSourceIdentifier, p, err)
		}
		// (B) FetchPath must agree with ResponsePath.
		if err := checkFetchPathConsistency(p, item.FetchPath); err != nil {
			return fmt.Errorf(
				"response-path (assertion 7): entity fetch %d (subgraph %q) FetchPath disagrees with ResponsePath %q: %v",
				i, sf.DataSourceIdentifier, p, err)
		}
		// (C) the member type resolved by the fetch must be reachable at the landed position.
		if err := checkEntityMemberType(sf, landed, def); err != nil {
			return fmt.Errorf(
				"response-path (assertion 7): entity fetch %d (subgraph %q) at position %q (type %q): %v",
				i, sf.DataSourceIdentifier, p, landed, err)
		}
	}
	return nil
}

// walkRequestedResponsePath descends the operation selection tree along p's dotted response-key segments
// (skipping `@` array markers, which do not change type or position) and returns the object type landed
// on. Each segment must be a field REQUESTED at the current position (by alias-or-name), resolved through
// inline fragments -- or, when the walk stalls, the REMAINING segments must spell a Requires-fragment path
// rooted at exactly the stalled position (a D11.10 requires-input pipeline fetch; reqRoots maps a
// consuming fetch's normalized position to its Requires selection trie). Errors when a segment is neither.
func walkRequestedResponsePath(opDoc, def *ast.Document, rootType string, rootSet int, p string,
	reqRoots map[string][]*reqPathNode) (string, error) {

	curType := rootType
	curSet := rootSet
	var consumed []string
	segs := strings.Split(p, ".")
	for i, seg := range segs {
		if seg == "" || seg == "@" {
			continue // array element marker: same type, same requested position
		}
		ft, childSet, found := findRequestedField(opDoc, def, curSet, curType, seg)
		if !found {
			// Requires-mechanism continuation: some fetch at THIS position declares a Requires
			// fragment whose path spells the remaining segments -- the gather fetch of a
			// requires-input pipeline. Type-walk the remainder against the composed definition.
			if landed, ok := walkRequiresMechanism(def, reqRoots[strings.Join(consumed, ".")], curType, segs[i:]); ok {
				return landed, nil
			}
			return "", fmt.Errorf("segment %q is not a field requested on type %q", seg, curType)
		}
		consumed = append(consumed, seg)
		curType = ft
		curSet = childSet
	}
	return curType, nil
}

// reqPathNode is one level of a fetch's Requires-fragment selection, used to justify a
// requires-mechanism attach position.
type reqPathNode struct {
	name string
	sub  []*reqPathNode
}

// requiresPathRoots collects, per entity fetch carrying a RepresentationKindRequires fragment, the
// fragment's selection tree keyed by the fetch's NORMALIZED position (ResponsePath with `@` markers
// dropped): the positions a requires-input pipeline fetch may legitimately extend.
func requiresPathRoots(fetches []*resolve.FetchItem) map[string][]*reqPathNode {
	var out map[string][]*reqPathNode
	for _, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok || sf == nil || sf.QueryPlan == nil {
			continue
		}
		for _, rep := range sf.QueryPlan.DependsOnFields {
			if rep.Kind != resolve.RepresentationKindRequires {
				continue
			}
			tree := parseRequiresFragmentTree(rep.Fragment)
			if len(tree) == 0 {
				continue
			}
			var norm []string
			for _, seg := range strings.Split(item.ResponsePath, ".") {
				if seg != "" && seg != "@" {
					norm = append(norm, seg)
				}
			}
			if out == nil {
				out = map[string][]*reqPathNode{}
			}
			key := strings.Join(norm, ".")
			out[key] = append(out[key], tree...)
		}
	}
	return out
}

// parseRequiresFragmentTree parses a `fragment Requires on T { ... }` body into reqPathNodes.
func parseRequiresFragmentTree(fragment string) []*reqPathNode {
	doc := unsafeparser.ParseGraphqlDocumentString(fragment)
	for ref := range doc.FragmentDefinitions {
		return reqPathSet(&doc, doc.FragmentDefinitions[ref].SelectionSet)
	}
	return nil
}

func reqPathSet(doc *ast.Document, setRef int) []*reqPathNode {
	if setRef < 0 || setRef >= len(doc.SelectionSets) {
		return nil
	}
	var out []*reqPathNode
	for _, selRef := range doc.SelectionSets[setRef].SelectionRefs {
		sel := doc.Selections[selRef]
		if sel.Kind != ast.SelectionKindField {
			continue
		}
		n := &reqPathNode{name: doc.FieldNameString(sel.Ref)}
		if doc.Fields[sel.Ref].HasSelections {
			n.sub = reqPathSet(doc, doc.Fields[sel.Ref].SelectionSet)
		}
		out = append(out, n)
	}
	return out
}

// walkRequiresMechanism consumes the remaining path segments against a Requires selection tree,
// resolving types on the composed definition. ok only when EVERY remaining segment is on a requires
// path -- a partial match is not a justification.
func walkRequiresMechanism(def *ast.Document, roots []*reqPathNode, curType string, remaining []string) (string, bool) {
	if len(roots) == 0 {
		return "", false
	}
	nodes := roots
	landed := curType
	for _, seg := range remaining {
		if seg == "" || seg == "@" {
			continue
		}
		// An argument-conflict gather position is ALIASED (`_planv2req_comments_1`); the requires
		// selection names the real field.
		name := reqMechanismFieldName(seg)
		var match *reqPathNode
		for _, n := range nodes {
			if n.name == name {
				match = n
				break
			}
		}
		if match == nil {
			return "", false
		}
		landed = namedFieldOutputType(def, landed, name)
		nodes = match.sub
	}
	return landed, true
}

// reqMechanismFieldName strips lowering's requires-gather alias (`_planv2req_<name>_<n>`) back to
// the schema field name; any other segment is returned unchanged.
func reqMechanismFieldName(seg string) string {
	rest, ok := strings.CutPrefix(seg, "_planv2req_")
	if !ok {
		return seg
	}
	i := strings.LastIndexByte(rest, '_')
	if i <= 0 {
		return seg
	}
	return rest[:i]
}

// findRequestedField searches selection set setRef (descending through inline fragments) for a field
// whose response key (alias-or-name) equals key, and returns its named output type (list/NonNull
// stripped, resolved on `enclosing`) plus its child selection set (-1 if a leaf). Under an inline
// fragment the field's owning type is the fragment's type condition.
func findRequestedField(opDoc, def *ast.Document, setRef int, enclosing, key string) (ftype string, child int, ok bool) {
	if setRef < 0 {
		return "", -1, false
	}
	for _, selRef := range opDoc.SelectionSets[setRef].SelectionRefs {
		sel := opDoc.Selections[selRef]
		switch sel.Kind {
		case ast.SelectionKindField:
			if opDoc.FieldAliasOrNameString(sel.Ref) != key {
				continue
			}
			fname := opDoc.FieldNameString(sel.Ref)
			ft := namedFieldOutputType(def, enclosing, fname)
			cs := -1
			if ss, has := opDoc.FieldSelectionSet(sel.Ref); has {
				cs = ss
			}
			return ft, cs, true
		case ast.SelectionKindInlineFragment:
			tc := opDoc.InlineFragmentTypeConditionNameString(sel.Ref)
			if tc == "" {
				tc = enclosing
			}
			if ft, cs, found := findRequestedField(opDoc, def, opDoc.InlineFragments[sel.Ref].SelectionSet, tc, key); found {
				return ft, cs, true
			}
		}
	}
	return "", -1, false
}

// namedFieldOutputType resolves fieldName's named output type on typeName against def (List/NonNull
// stripped). "" when unknown (halts the walk without a false landing type).
func namedFieldOutputType(def *ast.Document, typeName, fieldName string) string {
	if typeName == "" || def == nil {
		return ""
	}
	node, ok := def.Index.FirstNodeByNameStr(typeName)
	if !ok {
		return ""
	}
	fd, ok := def.NodeFieldDefinitionByName(node, ast.ByteSlice(fieldName))
	if !ok {
		return ""
	}
	return def.FieldDefinitionTypeNameString(fd)
}

// checkFetchPathConsistency verifies the FetchPath carries the same response-key field sequence as the
// dotted ResponsePath, and that every `@` array marker in ResponsePath sits on an Array-kind FetchPath
// element. The trailing `@` is dropped from the ResponsePath STRING (renderAttachPath), so a trailing
// array hop is validated by the field sequence alone -- not by a marker that no longer exists.
func checkFetchPathConsistency(responsePath string, fetchPath []resolve.FetchItemPathElement) error {
	tokens := strings.Split(responsePath, ".")
	fi := 0
	for ti := 0; ti < len(tokens); ti++ {
		tok := tokens[ti]
		if tok == "" {
			continue
		}
		if tok == "@" {
			// an array marker must follow a field that was just consumed as an Array element.
			if fi == 0 {
				return fmt.Errorf("array marker `@` with no preceding field")
			}
			if fetchPath[fi-1].Kind != resolve.FetchItemPathElementKindArray {
				return fmt.Errorf("`@` after %q but its FetchPath element is not Array-kind", fetchPath[fi-1].Path)
			}
			continue
		}
		if fi >= len(fetchPath) {
			return fmt.Errorf("ResponsePath field %q has no corresponding FetchPath element (FetchPath too short: %d)", tok, len(fetchPath))
		}
		el := fetchPath[fi]
		if len(el.Path) != 1 || el.Path[0] != tok {
			return fmt.Errorf("FetchPath element %d is %v, expected [%q]", fi, el.Path, tok)
		}
		fi++
	}
	if fi != len(fetchPath) {
		return fmt.Errorf("FetchPath has %d elements but ResponsePath encodes %d fields", len(fetchPath), fi)
	}
	return nil
}

// checkEntityMemberType validates that each `... on T` member the entity fetch resolves is reachable at
// the landed position's type. Conservative: only an unrelated pair of CONCRETE object types fails, so
// interface/union positions and @interfaceObject flattening (abstract landed type, or T abstract) pass.
func checkEntityMemberType(sf *resolve.SingleFetch, landedType string, def *ast.Document) error {
	if landedType == "" || def == nil {
		return nil // unknown landed type: (A) already halted; do not invent a failure
	}
	doc := fetchQuery(sf)
	if doc == "" {
		return nil
	}
	for _, member := range entitiesMemberTypes(doc) {
		if member == "" || member == landedType {
			continue
		}
		if typesRelated(def, member, landedType) {
			continue
		}
		if isAbstractType(def, member) || isAbstractType(def, landedType) {
			continue // @interfaceObject / entity-interface flattening: abstract on either side is admissible
		}
		return fmt.Errorf("resolves member type %q unrelated to the position type -- fetch attached at the wrong response position", member)
	}
	return nil
}

// entitiesMemberTypes returns the `... on T` type-condition names directly under the `_entities` field
// of an entity fetch document.
func entitiesMemberTypes(doc string) []string {
	parsed := unsafeparser.ParseGraphqlDocumentString(doc)
	var out []string
	var fromSet func(setRef int, underEntities bool)
	fromSet = func(setRef int, underEntities bool) {
		if setRef < 0 {
			return
		}
		for _, selRef := range parsed.SelectionSets[setRef].SelectionRefs {
			sel := parsed.Selections[selRef]
			switch sel.Kind {
			case ast.SelectionKindField:
				if parsed.FieldNameString(sel.Ref) == "_entities" {
					if ss, ok := parsed.FieldSelectionSet(sel.Ref); ok {
						fromSet(ss, true)
					}
				}
			case ast.SelectionKindInlineFragment:
				if underEntities {
					out = append(out, parsed.InlineFragmentTypeConditionNameString(sel.Ref))
				}
			}
		}
	}
	for ref := range parsed.OperationDefinitions {
		fromSet(parsed.OperationDefinitions[ref].SelectionSet, false)
		break
	}
	return out
}

// typesRelated reports whether a and b stand in an interface-implementation or union-membership relation
// (either direction) per def.
func typesRelated(def *ast.Document, a, b string) bool {
	aNode, aok := def.Index.FirstNodeByNameStr(a)
	bNode, bok := def.Index.FirstNodeByNameStr(b)
	if !aok || !bok {
		return false
	}
	if def.NodeImplementsInterface(aNode, ast.ByteSlice(b)) || def.NodeImplementsInterface(bNode, ast.ByteSlice(a)) {
		return true
	}
	if bNode.Kind == ast.NodeKindUnionTypeDefinition && def.NodeIsUnionMember(aNode, bNode) {
		return true
	}
	if aNode.Kind == ast.NodeKindUnionTypeDefinition && def.NodeIsUnionMember(bNode, aNode) {
		return true
	}
	return false
}

// isAbstractType reports whether name is an interface or union in def.
func isAbstractType(def *ast.Document, name string) bool {
	node, ok := def.Index.FirstNodeByNameStr(name)
	if !ok {
		return false
	}
	return node.Kind == ast.NodeKindInterfaceTypeDefinition || node.Kind == ast.NodeKindUnionTypeDefinition
}
