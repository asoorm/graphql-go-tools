package lower

// subfilter.go realizes @openfed__subscriptionFilter emission (M4.4; PARITY.md Section 7): it turns
// the root field's plan.FieldConfiguration.SubscriptionFilterCondition into the
// resolve.SubscriptionFilter the resolver's SkipEvent consults per event. This is SECURITY-GRADE
// parity: a filter condition says which events MUST be skipped, so a dropped or mis-built filter
// SILENTLY DELIVERS events that must never reach the client (a data-exposure defect). The build
// mirrors v1's pathBuilderVisitor.buildSubscriptionFilterCondition / buildSubscriptionFieldFilter
// EXACTLY -- the same And/Or/Not/In recursion and the same argument-template resolution
// ({{ args.path }} -> a ContextVariable segment) -- but reads the operation/definition documents
// directly instead of off a walker (lowering has no walker). v1 sets the built filter on
// resolve.GraphQLSubscription.Filter (visitor.go configureSubscription); lowering does the same.
//
// Loud, never silent (Section 6.3 no silent degrade): where v1 halts planning with StopWithInternalErr
// on a malformed template (undefined argument, missing variable definition, non-variable argument),
// lowering captures the first such error and fails the whole LowerSubscription call. v1's ONE
// tolerated malformation -- a value carrying more than one template, or a single non-conforming
// template match -- is mirrored as a nil filter value (which collapses the condition to no filter),
// matching v1's "invalid filter multiple templates" behavior exactly.

import (
	"fmt"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/argument_templates"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// subFilterBuilder carries the operation/definition context v1's pathBuilderVisitor reads off the
// walker, so the build can mirror v1 without one. fieldRef is the subscription root field in the
// operation (v1's c.fieldRef -- the source of argument values); enclosingTypeRef is the Subscription
// object type definition (v1's walker EnclosingTypeDefinition, the field-definition owner); opDefRef
// is the operation definition (v1's walker Ancestors[0], for variable-definition validation).
type subFilterBuilder struct {
	operation        *ast.Document
	definition       *ast.Document
	fieldRef         int
	enclosingTypeRef int
	opDefRef         int
	err              error // first template-resolution error; halts the build loudly (v1: StopWithInternalErr)
}

// buildSubscriptionFilter builds the resolve.SubscriptionFilter for the subscription root field, or
// nil when the field carries no filter condition. A non-nil error is a malformed-template failure
// that must fail lowering rather than silently drop the filter.
func buildSubscriptionFilter(operation, definition *ast.Document, info InfoConfig,
	rootTypeName, rootFieldName string, fieldRef, opDefRef int) (*resolve.SubscriptionFilter, error) {

	fc := info.Fields.ForTypeField(rootTypeName, rootFieldName)
	if fc == nil || fc.SubscriptionFilterCondition == nil {
		return nil, nil
	}
	enclosing, ok := definition.Index.FirstNodeByNameStr(rootTypeName)
	if !ok || enclosing.Kind != ast.NodeKindObjectTypeDefinition {
		return nil, fmt.Errorf("planv2: subscription filter: root type %q is not an object type in the composed schema", rootTypeName)
	}
	b := &subFilterBuilder{
		operation:        operation,
		definition:       definition,
		fieldRef:         fieldRef,
		enclosingTypeRef: enclosing.Ref,
		opDefRef:         opDefRef,
	}
	filter := b.condition(*fc.SubscriptionFilterCondition)
	if b.err != nil {
		return nil, b.err
	}
	return filter, nil
}

// condition mirrors v1's buildSubscriptionFilterCondition: And/Or recurse and collect non-nil
// children, Not recurses once, In builds the field filter. A condition that yields none of the four
// collapses to nil (no filter), exactly as v1 does.
func (b *subFilterBuilder) condition(condition plan.SubscriptionFilterCondition) *resolve.SubscriptionFilter {
	filter := &resolve.SubscriptionFilter{}
	if condition.And != nil {
		for _, andCondition := range condition.And {
			and := b.condition(andCondition)
			if and != nil {
				filter.And = append(filter.And, *and)
			}
		}
	}
	if condition.Or != nil {
		for _, orCondition := range condition.Or {
			or := b.condition(orCondition)
			if or != nil {
				filter.Or = append(filter.Or, *or)
			}
		}
	}
	if condition.Not != nil {
		filter.Not = b.condition(*condition.Not)
	}
	if condition.In != nil {
		filter.In = b.fieldFilter(condition.In)
	}
	if filter.And == nil && filter.Or == nil && filter.Not == nil && filter.In == nil {
		return nil
	}
	return filter
}

// fieldFilter mirrors v1's buildSubscriptionFieldFilter: each value is either a static segment or a
// prefix/variable/suffix template resolved against the root field's arguments. A value carrying more
// than one template (or a single non-conforming match) returns nil for the whole field filter --
// v1's tolerated "invalid filter multiple templates" case. A malformed single template (undefined
// argument, missing variable definition, non-variable argument) records b.err and halts.
func (b *subFilterBuilder) fieldFilter(condition *plan.SubscriptionFieldCondition) *resolve.SubscriptionFieldFilter {
	filter := &resolve.SubscriptionFieldFilter{}
	filter.FieldPath = condition.FieldPath
	filter.Values = make([]resolve.InputTemplate, len(condition.Values))
	for i, value := range condition.Values {
		matches := argument_templates.ArgumentTemplateRegex.FindAllStringSubmatchIndex(value, -1)
		if len(matches) == 0 {
			filter.Values[i].Segments = []resolve.TemplateSegment{
				{
					SegmentType: resolve.StaticSegmentType,
					Data:        []byte(value),
				},
			}
			continue
		}
		fieldNameBytes := b.operation.FieldNameBytes(b.fieldRef)
		fieldDefinitionRef, ok := b.definition.ObjectTypeDefinitionFieldWithName(b.enclosingTypeRef, fieldNameBytes)
		if !ok {
			b.err = fmt.Errorf(`planv2: subscription filter: expected field definition to exist for field "%s"`, fieldNameBytes)
			return nil
		}
		groups := matches[0]
		/* The range value[0:groups[0]] is a prefix (if any -- an empty prefix still provides an index)
		 * The range value[groups[1]:groups[2]] is the whole argument template
		 * The range value[groups[2]:groups[3]] is the argument path
		 * The range groups[1] to the end of value is the suffix (if any)
		 */
		if len(matches) != 1 || len(groups) != 4 {
			return nil
		}
		argumentPathGroup := value[groups[2]:groups[3]]
		validationResult, err := argument_templates.ValidateArgumentPath(b.definition, argumentPathGroup, fieldDefinitionRef)
		if err != nil {
			b.err = fmt.Errorf(`planv2: subscription filter: argument template defined on field "%s" is invalid: %w`, fieldNameBytes, err)
			return nil
		}
		prefix := value[:groups[0]]
		hasPrefix := len(prefix) > 0
		argumentNameBytes := []byte(validationResult.ArgumentPath[0])
		argumentRef, ok := b.operation.FieldArgument(b.fieldRef, argumentNameBytes)
		if !ok {
			b.err = fmt.Errorf(`planv2: subscription filter: operation field "%s" does not define argument "%s"`, fieldNameBytes, argumentNameBytes)
			return nil
		}
		variablePath, err := b.operation.VariablePathByArgumentRefAndArgumentPath(argumentRef, validationResult.ArgumentPath, b.opDefRef)
		if err != nil {
			b.err = fmt.Errorf(`planv2: subscription filter: failed to create template segment for argument "%s" on field "%s": %w`, argumentNameBytes, fieldNameBytes, err)
			return nil
		}
		suffix := value[groups[1]:]
		hasSuffix := len(suffix) > 0
		size := 1
		if hasPrefix {
			size++
		}
		if hasSuffix {
			size++
		}
		filter.Values[i].Segments = make([]resolve.TemplateSegment, size)
		idx := 0
		if hasPrefix {
			filter.Values[i].Segments[idx] = resolve.TemplateSegment{
				SegmentType: resolve.StaticSegmentType,
				Data:        []byte(prefix),
			}
			idx++
		}
		filter.Values[i].Segments[idx] = resolve.TemplateSegment{
			SegmentType:        resolve.VariableSegmentType,
			VariableKind:       resolve.ContextVariableKind,
			Renderer:           resolve.NewPlainVariableRenderer(),
			VariableSourcePath: variablePath,
		}
		if hasSuffix {
			filter.Values[i].Segments[idx+1] = resolve.TemplateSegment{
				SegmentType: resolve.StaticSegmentType,
				Data:        []byte(suffix),
			}
		}
	}
	return filter
}

// subscriptionRootFieldRef finds the operation field ref of the subscription root field plus its
// operation-definition ref. A subscription has exactly one root field (validated on the obligation
// tree before this is called); the scan honors operationName so a multi-operation document resolves
// the same operation obligation.Build selected. An empty operationName matches the first subscription
// operation (the single-operation contract the plain Lower entry points use).
func subscriptionRootFieldRef(operation *ast.Document, operationName string) (fieldRef, opDefRef int, ok bool) {
	for ref := range operation.OperationDefinitions {
		if operation.OperationDefinitions[ref].OperationType != ast.OperationTypeSubscription {
			continue
		}
		if operationName != "" && operation.OperationDefinitionNameString(ref) != operationName {
			continue
		}
		if !operation.OperationDefinitions[ref].HasSelections {
			return ast.InvalidRef, ast.InvalidRef, false
		}
		ssRef := operation.OperationDefinitions[ref].SelectionSet
		for _, selRef := range operation.SelectionSets[ssRef].SelectionRefs {
			sel := operation.Selections[selRef]
			if sel.Kind == ast.SelectionKindField {
				return sel.Ref, ref, true
			}
		}
		return ast.InvalidRef, ast.InvalidRef, false
	}
	return ast.InvalidRef, ast.InvalidRef, false
}
