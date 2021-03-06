package document

import "github.com/jensneuse/graphql-go-tools/pkg/lexing/position"

// ScalarTypeDefinition as specified in:
// http://facebook.github.io/graphql/draft/#sec-Scalars
type ScalarTypeDefinition struct {
	Description  ByteSliceReference
	Name         ByteSliceReference
	DirectiveSet int
	Position     position.Position
	IsExtend     bool
}

func (s ScalarTypeDefinition) NodeSelectionSet() int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeInputFieldsDefinition() int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeInputValueDefinitions() InputValueDefinitions {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodePosition() position.Position {
	return s.Position
}

func (s ScalarTypeDefinition) NodeValueType() ValueType {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeValueReference() int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeUnionMemberTypes() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeSchemaDefinition() SchemaDefinition {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeScalarTypeDefinitions() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeObjectTypeDefinitions() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeInterfaceTypeDefinitions() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeUnionTypeDefinitions() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeEnumTypeDefinitions() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeInputObjectTypeDefinitions() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeDirectiveDefinitions() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeName() ByteSliceReference {
	return s.Name
}

func (s ScalarTypeDefinition) NodeAlias() ByteSliceReference {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeDescription() ByteSliceReference {
	return s.Description
}

func (s ScalarTypeDefinition) NodeArgumentSet() int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeArgumentsDefinition() int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeDirectiveSet() int {
	return s.DirectiveSet
}

func (s ScalarTypeDefinition) NodeEnumValuesDefinition() EnumValueDefinitions {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeFields() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeFieldsDefinition() FieldDefinitions {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeFragmentSpreads() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeInlineFragments() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeVariableDefinitions() []int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeType() int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeOperationType() OperationType {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeValue() int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeDefaultValue() int {
	panic("implement me")
}

func (s ScalarTypeDefinition) NodeImplementsInterfaces() ByteSliceReferences {
	panic("implement me")
}

// ScalarTypeDefinitions is the plural of ScalarTypeDefinition
type ScalarTypeDefinitions []ScalarTypeDefinition
