package document

import "github.com/jensneuse/graphql-go-tools/pkg/lexing/position"

// FieldDefinition as specified in:
// http://facebook.github.io/graphql/draft/#FieldDefinition
type FieldDefinition struct {
	Description         ByteSliceReference
	Name                ByteSliceReference
	ArgumentsDefinition int
	Type                int
	DirectiveSet        int
	Position            position.Position
	NextRef             int
}

func (f FieldDefinition) NodeSelectionSet() int {
	panic("implement me")
}

func (f FieldDefinition) NodeInputFieldsDefinition() int {
	panic("implement me")
}

func (f FieldDefinition) NodeInputValueDefinitions() InputValueDefinitions {
	panic("implement me")
}

func (f FieldDefinition) NodePosition() position.Position {
	return f.Position
}

func (f FieldDefinition) NodeValueType() ValueType {
	panic("implement me")
}

func (f FieldDefinition) NodeValueReference() int {
	panic("implement me")
}

func (f FieldDefinition) NodeUnionMemberTypes() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeSchemaDefinition() SchemaDefinition {
	panic("implement me")
}

func (f FieldDefinition) NodeScalarTypeDefinitions() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeObjectTypeDefinitions() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeInterfaceTypeDefinitions() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeUnionTypeDefinitions() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeEnumTypeDefinitions() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeInputObjectTypeDefinitions() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeDirectiveDefinitions() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeImplementsInterfaces() ByteSliceReferences {
	panic("implement me")
}

func (f FieldDefinition) NodeValue() int {
	panic("implement me")
}

func (f FieldDefinition) NodeDefaultValue() int {
	panic("implement me")
}

func (f FieldDefinition) NodeFieldsDefinition() FieldDefinitions {
	panic("implement me")
}

func (f FieldDefinition) NodeArgumentsDefinition() int {
	return f.ArgumentsDefinition
}

func (f FieldDefinition) NodeName() ByteSliceReference {
	return f.Name
}

func (f FieldDefinition) NodeAlias() ByteSliceReference {
	panic("implement me")
}

func (f FieldDefinition) NodeDescription() ByteSliceReference {
	return f.Description
}

func (f FieldDefinition) NodeArgumentSet() int {
	panic("implement me")
}

func (f FieldDefinition) NodeDirectiveSet() int {
	return f.DirectiveSet
}

func (f FieldDefinition) NodeEnumValuesDefinition() EnumValueDefinitions {
	panic("implement me")
}

func (f FieldDefinition) NodeFields() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeFragmentSpreads() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeInlineFragments() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeVariableDefinitions() []int {
	panic("implement me")
}

func (f FieldDefinition) NodeType() int {
	return f.Type
}

func (f FieldDefinition) NodeOperationType() OperationType {
	panic("implement me")
}

type FieldDefinitionGetter interface {
	FieldDefinition(ref int) FieldDefinition
}

// FieldDefinitions as specified in:
// http://facebook.github.io/graphql/draft/#FieldsDefinition
type FieldDefinitions struct {
	nextRef    int
	currentRef int
	current    FieldDefinition
}

func NewFieldDefinitions(nextRef int) FieldDefinitions {
	return FieldDefinitions{
		nextRef: nextRef,
	}
}

func (i *FieldDefinitions) HasNext() bool {
	return i.nextRef != -1
}

func (i *FieldDefinitions) Next(getter FieldDefinitionGetter) bool {
	if i.nextRef == -1 {
		return false
	}

	i.currentRef = i.nextRef
	i.current = getter.FieldDefinition(i.nextRef)
	i.nextRef = i.current.NextRef
	return true
}

func (i *FieldDefinitions) Value() (FieldDefinition, int) {
	return i.current, i.currentRef
}
