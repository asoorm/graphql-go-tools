package obligation

import (
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astvisitor"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// typenameField is the GraphQL meta field __typename. No subgraph declares it, so it has no
// field-resolution node and is filled in later at lowering -- the search never sees it. EnterField
// records it with Kind Typename so lowering can synthesize the selection; it is never a goal and
// never gets candidate nodes.
const typenameField = "__typename"

// visitor walks the normalized operation and appends one Obligation per selected field (a Field
// obligation, or Kind Typename for the __typename meta field) and one per inline refinement (a
// Refine obligation). It keeps the current obligation ancestry on a stack so each new obligation is
// parented by the one it nests under.
type visitor struct {
	walker *astvisitor.Walker
	op     *ast.Document
	def    *ast.Document
	tree   *Tree
	stack  []ObID // current obligation ancestry; stack[len-1] is the parent

	operationName string
	// readDefer enables the D11.13 defer-scope carry: EnterField reads each field's
	// @__defer_internal stamp onto Obligation.DeferID and records the per-id DeferInfo. Set only
	// for query operations -- a mutation is never deferred and a subscription must not honor @defer
	// (FS-DEF-7), so on those every scope stays 0 and the plan flattens (FS-DEF-1 conforming).
	readDefer bool
}

// Build re-expresses the normalized operation as the obligation tree, with each goal's candidate
// nodes resolved against the graph H. It produces only IDs -- the search package never sees the
// parsed operation.
//
// Input contract: the operation MUST already be normalized -- fragment spreads inlined, @skip/@include
// resolved, aliases assigned. Fragment definitions may either be removed by the normalizer or left in
// the document: the walker visits every top-level node including fragment definitions, so leftover
// (already-inlined) fragment bodies would otherwise be walked a second time and emit spurious
// obligations. Build guards against that by skipping fragment definitions, which also makes plain
// NormalizeOperation (which inlines spreads but does NOT remove the definitions) safe input -- the
// guard is defense in depth, not an excuse to skip pinning the normalization config.
// Spec: FORMAL_SPEC D2, D3.
func Build(operation, definition *ast.Document, opName string, h *hypergraph.Hypergraph) (*Tree, error) {
	report := &operationreport.Report{}
	operationName := selectOperationName(operation, opName, report)
	if report.HasErrors() {
		return nil, report
	}

	// Record the selected operation's type (D11.12 root scoping): reachability, narrowing, and the
	// search's root participation are scoped to this kind's roots, so a schema that merely declares
	// a Subscription type cannot perturb query/mutation planning (and vice versa for the roots a
	// subscription may enter through). Resolved BEFORE the walk because the D11.13 defer-scope
	// carry is gated on it (query operations only).
	opKind := selectedOperationType(operation, operationName)

	walker := astvisitor.NewWalker(48)
	t := &Tree{}
	v := &visitor{walker: &walker, op: operation, def: definition, tree: t, operationName: operationName,
		readDefer: opKind == ast.OperationTypeQuery}
	walker.RegisterEnterOperationVisitor(v)
	walker.RegisterEnterFragmentDefinitionVisitor(v)
	walker.RegisterEnterFieldVisitor(v)
	walker.RegisterLeaveFieldVisitor(v)
	walker.RegisterEnterInlineFragmentVisitor(v)
	walker.RegisterLeaveInlineFragmentVisitor(v)
	walker.Walk(operation, definition, report)
	if report.HasErrors() {
		return nil, report
	}

	t.opKind = opKind

	t.def = definition // composed-schema knowledge for the D6pp classification (type kinds, implementers)
	// Abstract-position member expansion (D3pppp): a bare interface/union field locally unresolvable at
	// its position is rewritten into per-member refinements ahead of goal resolution, so each member
	// travels to its declaring subgraph via its own entity jump (the class-C provider-split family).
	// A pure tree rewrite; runs to fixpoint before any goal exists.
	t.expandAbstractPositionMembers(h)
	t.resolveGoalsAndCand(h) // pick out goals + their candidate nodes, in document order
	// Compute the member-narrowing verdicts; on by default. Build owns this: the search must not call
	// ClassifyNarrowing itself (it would mutate the tree, breaking the search's purity), and a tree
	// without it would silently plan partial unions as if every member were resolvable everywhere --
	// the exact bug narrowing exists to close. ClassifyNarrowing stays exported for re-classification
	// against a rebuilt H.
	t.ClassifyNarrowing(h)
	reachable := reachableFor(h, t.opKind)
	// Interface-refinement fallback: for a non-exempt field goal on an abstract "mixin" type whose own
	// candidate nodes are all unreachable, add the concrete members' reachable field nodes -- turning a
	// hard "no valid plan" error into a routed plan. Only fires when the goal's own candidates are all
	// unreachable, so no working interface case is disturbed.
	t.expandInterfaceRefinementGoals(h, reachable)
	// @interfaceObject member flattening (D3io): the reverse direction -- a member-refined field goal
	// (`... on C { f }`) whose concrete candidates are all unreachable, but where an @interfaceObject /
	// entity-interface subgraph serves f on an interface C implements, gains the interface-flattened
	// candidates. Conditional like the interface-refinement fallback above; lowering places the covered
	// field at the interface level (D11.9).
	t.expandInterfaceObjectFlattening(h, reachable)
	// Exempt-terminal composite coverage: after narrowing, add a coverage goal for any composite whose
	// whole field-subtree is a correct value-type null, so the router still resolves it (for the
	// surviving members' __typename) instead of dropping the whole subtree. Gated on the exempt members
	// being reachable, so it never turns a real gap into a silently-wrong plan; see
	// promoteExemptTerminalGoals.
	t.promoteExemptTerminalGoals(h, reachable)
	return t, nil
}

// selectedOperationType returns the operation type of the named operation (the one Build selected).
// The scan mirrors the facade's isSubscription: with a name given, the matching operation decides;
// an empty name is only valid for single-operation documents, so the first operation decides.
func selectedOperationType(operation *ast.Document, operationName string) ast.OperationType {
	for ref := range operation.OperationDefinitions {
		name := operation.OperationDefinitionNameString(ref)
		if operationName == "" || name == operationName {
			return operation.OperationDefinitions[ref].OperationType
		}
	}
	return ast.OperationTypeUnknown
}

// selectOperationName mirrors plan.Planner.selectOperation: an empty opName is only valid when the
// document has exactly one operation (its name is then used, possibly ""); otherwise a name must
// be given and must exist.
func selectOperationName(operation *ast.Document, opName string, report *operationreport.Report) string {
	numOps := operation.NumOfOperationDefinitions()
	opName = strings.TrimSpace(opName)
	if opName == "" && numOps > 1 {
		report.AddExternalError(operationreport.ErrRequiredOperationNameIsMissing())
		return ""
	}
	if opName == "" && numOps == 1 {
		opName = operation.OperationDefinitionNameString(0)
	}
	if !operation.OperationNameExists(opName) {
		report.AddExternalError(operationreport.ErrOperationWithProvidedOperationNameNotFound(opName))
		return ""
	}
	return opName
}

// EnterOperationDefinition skips every operation but the selected one, so a multi-operation
// document only contributes obligations from the operation the caller asked to plan.
func (v *visitor) EnterOperationDefinition(ref int) {
	if v.op.OperationDefinitionNameString(ref) != v.operationName {
		v.walker.SkipNode()
	}
}

// EnterFragmentDefinition skips top-level fragment definitions entirely. Normalization inlines every
// spread into the operation body, but plain NormalizeOperation leaves the now-redundant fragment
// definitions in the document, and the walker would walk their bodies after the operation -- with an
// empty obligation stack, emitting spurious top-level obligations. See Build's input-contract godoc.
func (v *visitor) EnterFragmentDefinition(_ int) {
	v.walker.SkipNode()
}

func (v *visitor) EnterField(ref int) {
	parentType := v.walker.EnclosingTypeDefinition.NameString(v.def)
	fieldName := v.op.FieldNameString(ref)
	kind := Field
	if fieldName == typenameField {
		kind = Typename // response-shape bookkeeping only; never a goal -- see Kind Typename
	}
	args, argVars := v.renderArguments(ref)
	ob := Obligation{
		ID:        ObID(len(v.tree.obligations)),
		Kind:      kind,
		Parent:    v.currentParent(),
		Type:      parentType,
		Field:     fieldName,
		RespKey:   v.op.FieldAliasOrNameString(ref), // client response key, preserved for lowering
		Arguments: args,
		ArgVars:   argVars,
	}
	// D11.13 defer-scope carry (query operations only): read the field's @__defer_internal stamp,
	// and record the fragment's DeferInfo on first sight of its id. The mount path is the response
	// path of the ENCLOSING selection set -- the ancestry's field response keys, which at this point
	// is exactly the current stack's Field obligations (the deferred field itself not yet pushed).
	if v.readDefer {
		if id, label, parentID, ok := v.op.FieldDeferInfo(ref); ok && id != 0 {
			ob.DeferID = id
			if _, seen := v.tree.defers[id]; !seen {
				if v.tree.defers == nil {
					v.tree.defers = map[int]DeferInfo{}
				}
				v.tree.defers[id] = DeferInfo{ID: id, ParentID: parentID, Label: label, Path: v.mountPath()}
			}
		}
	}
	v.tree.obligations = append(v.tree.obligations, ob)
	v.stack = append(v.stack, ob.ID)
}

// mountPath renders the current ancestry as a response path: the stack's Field obligations'
// response keys, root->parent. Refine ancestors contribute no segment (a fragment adds no response
// level), matching v1's DeferDescriptor.Path ("where the fragment is mounted").
func (v *visitor) mountPath() []string {
	var path []string
	for _, id := range v.stack {
		if ob := v.tree.obligations[id]; ob.Kind == Field {
			path = append(path, ob.RespKey)
		}
	}
	return path
}

func (v *visitor) LeaveField(_ int) { v.stack = v.stack[:len(v.stack)-1] }

// renderArguments renders field ref's argument list into the form the obligation carries: the body
// WITHOUT the enclosing parens ("id: $a, limit: $limit"), plus the deduped variable names it
// references (document order). It prints every argument value kind via the AST value printer -- the
// operation is normalized with variables extracted, so each value is a variable reference, but a
// non-extracted literal still prints faithfully, keeping the capture correct if extraction is ever
// disabled. Returns ("", nil) for an argument-free field.
func (v *visitor) renderArguments(ref int) (string, []string) {
	argRefs := v.op.Fields[ref].Arguments.Refs
	if len(argRefs) == 0 {
		return "", nil
	}
	var parts []string
	var vars []string
	seen := map[string]bool{}
	for _, argRef := range argRefs {
		name := v.op.ArgumentNameString(argRef)
		val := v.op.Arguments[argRef].Value
		rendered, _ := v.op.PrintValueBytes(val, nil)
		parts = append(parts, name+": "+string(rendered))
		collectValueVariables(v.op, val, seen, &vars)
	}
	return strings.Join(parts, ", "), vars
}

// collectValueVariables appends (deduped, in-order) every operation variable name referenced by an
// argument value, recursing through object fields and list elements so a variable nested inside an
// input object (`{filter: {ids: $ids}}`) or list is still collected for the fetch's variable set.
func collectValueVariables(op *ast.Document, val ast.Value, seen map[string]bool, out *[]string) {
	switch val.Kind {
	case ast.ValueKindVariable:
		name := op.VariableValueNameString(val.Ref)
		if !seen[name] {
			seen[name] = true
			*out = append(*out, name)
		}
	case ast.ValueKindObject:
		for _, fieldRef := range op.ObjectValues[val.Ref].Refs {
			collectValueVariables(op, op.ObjectFieldValue(fieldRef), seen, out)
		}
	case ast.ValueKindList:
		for _, elemRef := range op.ListValues[val.Ref].Refs {
			collectValueVariables(op, op.Value(elemRef), seen, out)
		}
	}
}

func (v *visitor) EnterInlineFragment(ref int) {
	// `... on C` under an abstract parent U becomes a Refine obligation. At this point the walker has
	// not yet descended into the fragment, so EnclosingTypeDefinition is still U (the enclosing type),
	// not C.
	ob := Obligation{
		ID:       ObID(len(v.tree.obligations)),
		Kind:     Refine,
		Parent:   v.currentParent(),
		Type:     v.walker.EnclosingTypeDefinition.NameString(v.def), // U
		Concrete: v.op.InlineFragmentTypeConditionNameString(ref),    // C
	}
	v.tree.obligations = append(v.tree.obligations, ob)
	v.stack = append(v.stack, ob.ID)
}

func (v *visitor) LeaveInlineFragment(_ int) { v.stack = v.stack[:len(v.stack)-1] }

func (v *visitor) currentParent() ObID {
	if len(v.stack) == 0 {
		return NoParent // top-level obligation -- no enclosing obligation
	}
	return v.stack[len(v.stack)-1]
}

// candFor computes the candidate nodes for the obligation a goal maps to. Typename obligations never
// reach here (they are excluded from the goal set).
//
//   - A field obligation (field f on type T): every field node for T.f -- one per candidate subgraph,
//     sorted by node id (the scan below runs over ascending node ids, so the result is sorted for free).
//   - A leaf refinement obligation (a `... on C` fragment with no field selected under it): can't
//     occur in a normalized operation -- GraphQL requires a non-empty selection set under a type
//     condition -- but handled defensively via the object nodes for C, so Cand never panics.
func candFor(h *hypergraph.Hypergraph, ob Obligation) []hypergraph.NodeID {
	typ := ob.Type
	byObject := false
	if ob.Kind == Refine {
		typ = ob.Concrete
		byObject = true
	}

	var out []hypergraph.NodeID
	for id := hypergraph.NodeID(0); id < hypergraph.NodeID(h.NumNodes()); id++ {
		n := h.Node(id)
		if byObject {
			if n.Kind == hypergraph.NodeObject && n.Type == typ {
				out = append(out, id)
			}
			continue
		}
		if n.Kind == hypergraph.NodeField && n.Type == typ && n.Field == ob.Field {
			out = append(out, id)
		}
	}
	return out
}
