package lookup

import (
	"fmt"
	"testing"

	"github.com/jensneuse/graphql-go-tools/pkg/parser"
)

func TestWalker(t *testing.T) {

	type check func(w *Walker)

	run := func(schema, input string, checks ...check) {
		p := parser.NewParser()
		err := p.ParseTypeSystemDefinition([]byte(schema))
		if err != nil {
			panic(err)
		}

		l := New(p)

		err = p.ParseExecutableDefinition([]byte(input))
		if err != nil {
			panic(err)
		}

		walker := NewWalker(1024, 8)
		walker.SetLookup(l)
		walker.WalkExecutable()

		for i := range checks {
			checks[i](walker)
		}
	}

	mustPanic := func(wrapped check) check {
		return func(w *Walker) {
			defer func() {
				err := recover()
				if err == nil {
					panic(fmt.Errorf("want panic, got nothing"))
				}
			}()

			wrapped(w)
		}
	}

	mustGetSelectionSetTypeName := func(fieldName, wantTypeName string) check {
		return func(w *Walker) {
			fields := w.FieldsIterable()
			for fields.Next() {

				field, _, parent := fields.Value()
				if string(w.l.ByteSlice(field.Name)) != fieldName {
					continue
				}
				typeName := w.SelectionSetTypeName(w.l.SelectionSet(field.SelectionSet), parent)
				gotTypeName := string(w.l.ByteSlice(typeName))

				if wantTypeName != gotTypeName {
					panic(fmt.Errorf("mustGetSelectionSetTypeName: want: %s got: %s", wantTypeName, gotTypeName))
				}
			}
		}
	}

	argumentUsedInOperations := func(argumentName string, operationNames ...string) check {
		return func(w *Walker) {
			argSets := w.ArgumentSetIterable()
			for argSets.Next() {
				set, _ := argSets.Value()
				args := w.l.ArgumentsIterable(set)
				for args.Next() {
					arg, ref := args.Value()
					if string(w.l.p.ByteSlice(arg.Name)) == argumentName {

						operations := w.NodeUsageInOperationsIterator(ref)
						for i := range operationNames {
							wantName := operationNames[i]
							if !operations.Next() {
								panic(fmt.Errorf("argumentUsedInOperations: want next root operation with name '%s' for argument with name '%s', got nothing", wantName, argumentName))
							}
							ref := operations.Value()
							operationDefinition := w.l.OperationDefinition(ref)
							gotName := string(w.l.p.ByteSlice(operationDefinition.Name))
							if wantName != gotName {
								panic(fmt.Errorf("argumentUsedInOperations: want operation name: '%s', got: '%s'", wantName, gotName))
							}
						}

						return
					}
				}
			}
		}
	}

	wantFieldPath := func(forNamedField string, wantPath ...string) check {
		return func(w *Walker) {
			fields := w.FieldsIterable()
			for fields.Next() {
				field, _, parent := fields.Value()
				fieldName := string(w.l.ByteSlice(field.Name))
				if fieldName != forNamedField {
					continue
				}

				gotPath := w.FieldPath(parent)
				if len(wantPath) != len(gotPath) {
					panic(fmt.Errorf("wantFieldPath: want path with len: %d, got: %d", len(wantPath), len(gotPath)))
				}
				for i, wantName := range wantPath {
					gotName := string(w.l.ByteSlice(gotPath[len(gotPath)-1-i]))
					if gotName != wantName {
						panic(fmt.Errorf("wantFieldPath: want path field name: %s, got: %s (pos: %d)", wantName, gotName, i))
					}
				}
			}
		}
	}

	t.Run("argumentUsedInOperations", func(t *testing.T) {
		t.Run("get argument root from inside operation definition", func(t *testing.T) {
			run(testDefinition, `	query argOnRequiredArg($booleanArg: Boolean) {
						dog {
							isHousetrained(atOtherHomes: $booleanArg) @include(if: $booleanArg)
						}
					}`, argumentUsedInOperations("atOtherHomes", "argOnRequiredArg"))
		})
		t.Run("get argument root from inside fragment", func(t *testing.T) {
			run(testDefinition, `	query argOnRequiredArg($booleanArg: Boolean) {
						dog {
							...argOnOptional
						}
					}
					fragment argOnOptional on Dog {
						isHousetrained(atOtherHomes: $booleanArg) @include(if: $booleanArg)
					}`, argumentUsedInOperations("atOtherHomes", "argOnRequiredArg"))
		})
		t.Run("get argument root from inside fragment multiple times", func(t *testing.T) {
			run(testDefinition, `	query argOnRequiredArg($booleanArg: Boolean) {
						dog {
							...argOnOptional
							...argOnOptional
							...argOnOptional
						}
					}
					fragment argOnOptional on Dog {
						isHousetrained(atOtherHomes: $booleanArg) @include(if: $booleanArg)
					}`, argumentUsedInOperations("atOtherHomes", "argOnRequiredArg"))
		})
		t.Run("get argument root from inside fragment multiple times (check de-duplicating)", func(t *testing.T) {
			run(testDefinition, `	query argOnRequiredArg($booleanArg: Boolean) {
						dog {
							...argOnOptional
							...argOnOptional
							...argOnOptional
						}
					}
					fragment argOnOptional on Dog {
						isHousetrained(atOtherHomes: $booleanArg) @include(if: $booleanArg)
					}`, mustPanic(argumentUsedInOperations("atOtherHomes", "argOnRequiredArg", "argOnRequiredArg")))
		})
		t.Run("get argument root from inside nested fragment", func(t *testing.T) {
			run(testDefinition, `	query argOnRequiredArg($booleanArg: Boolean) {
						dog {
							...argOnOptional1
						}
					}
					fragment argOnOptional1 on Dog {
						... {
							...on Dog {
								...argOnOptional2
							}
						}
					}
					fragment argOnOptional2 on Dog {
						isHousetrained(atOtherHomes: $booleanArg) @include(if: $booleanArg)
					}`, argumentUsedInOperations("atOtherHomes", "argOnRequiredArg"))
		})
		t.Run("get argument root from inside fragment used in multiple operations", func(t *testing.T) {
			run(testDefinition, `	query argOnRequiredArg1($booleanArg: Boolean) {
						dog {
							...argOnOptional
						}
					}
					query argOnRequiredArg2($booleanArg: Boolean) {
						dog {
							...argOnOptional
						}
					}
					fragment argOnOptional on Dog {
						isHousetrained(atOtherHomes: $booleanArg) @include(if: $booleanArg)
					}`, argumentUsedInOperations("atOtherHomes", "argOnRequiredArg1", "argOnRequiredArg2"))
		})
	})
	t.Run("fieldPath", func(t *testing.T) {
		t.Run("nested 2 levels", func(t *testing.T) {
			run(testDefinition, `{dog{owner{name}}}`, wantFieldPath("name", "dog", "owner"))
		})
		t.Run("nested 3 levels", func(t *testing.T) {
			run(testDefinition, `{dog{owner{another{name}}}}`, wantFieldPath("name", "dog", "owner", "another"))
		})
		t.Run("with inline fragment", func(t *testing.T) {
			run(testDefinition, `{ dog { ... on Dog { owner { name } } } }`, wantFieldPath("name", "dog", "owner"))
		})
		t.Run("with nested inline fragments", func(t *testing.T) {
			run(testDefinition, `{ dog { ... on Dog { ... { owner { name } } } } }`, wantFieldPath("name", "dog", "owner"))
		})
		t.Run("with alias", func(t *testing.T) {
			run(testDefinition, `{dog{renamed:owner{name}}}`, wantFieldPath("name", "dog", "renamed"))
		})
	})
	t.Run("SelectionSetTypeName", func(t *testing.T) {
		t.Run("assets query", func(t *testing.T) {
			run(bigSchema, `{assets{id}}`, mustGetSelectionSetTypeName("assets", "Query"))
		})
		t.Run("assets query (id)", func(t *testing.T) {
			run(bigSchema, `{assets{id}}`, mustGetSelectionSetTypeName("id", "Asset"))
		})
	})
}

const bigSchema = `type AggregateAsset {
	count: Int!
  }
  
  type AggregateColor {
	count: Int!
  }
  
  type AggregateLocal {
	count: Int!
  }
  
  type AggregateLocation {
	count: Int!
  }
  
  type Asset implements Node {
	status: Status!
	updatedAt: DateTime!
	createdAt: DateTime!
	id: ID!
	handle: String!
	fileName: String!
	height: Float
	width: Float
	size: Float
	mimeType: String
  }
  
  """A connection to a list of items."""
  type AssetConnection {
	"""Information to aid in pagination."""
	pageInfo: PageInfo!
  
	"""A list of edges."""
	edges: [AssetEdge]!
	aggregate: AggregateAsset!
  }
  
  input AssetCreateInput {
	status: Status
	handle: String!
	fileName: String!
	height: Float
	width: Float
	size: Float
	mimeType: String
  }
  
  """An edge in a connection."""
  type AssetEdge {
	"""The item at the end of the edge."""
	node: Asset!
  
	"""A cursor for use in pagination."""
	cursor: String!
  }
  
  enum AssetOrderByInput {
	status_ASC
	status_DESC
	updatedAt_ASC
	updatedAt_DESC
	createdAt_ASC
	createdAt_DESC
	id_ASC
	id_DESC
	handle_ASC
	handle_DESC
	fileName_ASC
	fileName_DESC
	height_ASC
	height_DESC
	width_ASC
	width_DESC
	size_ASC
	size_DESC
	mimeType_ASC
	mimeType_DESC
  }
  
  type AssetPreviousValues {
	status: Status!
	updatedAt: DateTime!
	createdAt: DateTime!
	id: ID!
	handle: String!
	fileName: String!
	height: Float
	width: Float
	size: Float
	mimeType: String
  }
  
  type AssetSubscriptionPayload {
	mutation: MutationType!
	node: Asset
	updatedFields: [String!]
	previousValues: AssetPreviousValues
  }
  
  input AssetSubscriptionWhereInput {
	"""Logical AND on all given filters."""
	AND: [AssetSubscriptionWhereInput!]
  
	"""Logical OR on all given filters."""
	OR: [AssetSubscriptionWhereInput!]
  
	"""Logical NOT on all given filters combined by AND."""
	NOT: [AssetSubscriptionWhereInput!]
  
	"""
	The subscription event gets dispatched when it's listed in mutation_in
	"""
	mutation_in: [MutationType!]
  
	"""
	The subscription event gets only dispatched when one of the updated fields names is included in this list
	"""
	updatedFields_contains: String
  
	"""
	The subscription event gets only dispatched when all of the field names included in this list have been updated
	"""
	updatedFields_contains_every: [String!]
  
	"""
	The subscription event gets only dispatched when some of the field names included in this list have been updated
	"""
	updatedFields_contains_some: [String!]
	node: AssetWhereInput
  }
  
  input AssetUpdateInput {
	status: Status
	handle: String
	fileName: String
	height: Float
	width: Float
	size: Float
	mimeType: String
  }
  
  input AssetUpdateManyMutationInput {
	status: Status
	handle: String
	fileName: String
	height: Float
	width: Float
	size: Float
	mimeType: String
  }
  
  input AssetWhereInput {
	"""Logical AND on all given filters."""
	AND: [AssetWhereInput!]
  
	"""Logical OR on all given filters."""
	OR: [AssetWhereInput!]
  
	"""Logical NOT on all given filters combined by AND."""
	NOT: [AssetWhereInput!]
	status: Status
  
	"""All values that are not equal to given value."""
	status_not: Status
  
	"""All values that are contained in given list."""
	status_in: [Status!]
  
	"""All values that are not contained in given list."""
	status_not_in: [Status!]
	updatedAt: DateTime
  
	"""All values that are not equal to given value."""
	updatedAt_not: DateTime
  
	"""All values that are contained in given list."""
	updatedAt_in: [DateTime!]
  
	"""All values that are not contained in given list."""
	updatedAt_not_in: [DateTime!]
  
	"""All values less than the given value."""
	updatedAt_lt: DateTime
  
	"""All values less than or equal the given value."""
	updatedAt_lte: DateTime
  
	"""All values greater than the given value."""
	updatedAt_gt: DateTime
  
	"""All values greater than or equal the given value."""
	updatedAt_gte: DateTime
	createdAt: DateTime
  
	"""All values that are not equal to given value."""
	createdAt_not: DateTime
  
	"""All values that are contained in given list."""
	createdAt_in: [DateTime!]
  
	"""All values that are not contained in given list."""
	createdAt_not_in: [DateTime!]
  
	"""All values less than the given value."""
	createdAt_lt: DateTime
  
	"""All values less than or equal the given value."""
	createdAt_lte: DateTime
  
	"""All values greater than the given value."""
	createdAt_gt: DateTime
  
	"""All values greater than or equal the given value."""
	createdAt_gte: DateTime
	id: ID
  
	"""All values that are not equal to given value."""
	id_not: ID
  
	"""All values that are contained in given list."""
	id_in: [ID!]
  
	"""All values that are not contained in given list."""
	id_not_in: [ID!]
  
	"""All values less than the given value."""
	id_lt: ID
  
	"""All values less than or equal the given value."""
	id_lte: ID
  
	"""All values greater than the given value."""
	id_gt: ID
  
	"""All values greater than or equal the given value."""
	id_gte: ID
  
	"""All values containing the given string."""
	id_contains: ID
  
	"""All values not containing the given string."""
	id_not_contains: ID
  
	"""All values starting with the given string."""
	id_starts_with: ID
  
	"""All values not starting with the given string."""
	id_not_starts_with: ID
  
	"""All values ending with the given string."""
	id_ends_with: ID
  
	"""All values not ending with the given string."""
	id_not_ends_with: ID
	handle: String
  
	"""All values that are not equal to given value."""
	handle_not: String
  
	"""All values that are contained in given list."""
	handle_in: [String!]
  
	"""All values that are not contained in given list."""
	handle_not_in: [String!]
  
	"""All values less than the given value."""
	handle_lt: String
  
	"""All values less than or equal the given value."""
	handle_lte: String
  
	"""All values greater than the given value."""
	handle_gt: String
  
	"""All values greater than or equal the given value."""
	handle_gte: String
  
	"""All values containing the given string."""
	handle_contains: String
  
	"""All values not containing the given string."""
	handle_not_contains: String
  
	"""All values starting with the given string."""
	handle_starts_with: String
  
	"""All values not starting with the given string."""
	handle_not_starts_with: String
  
	"""All values ending with the given string."""
	handle_ends_with: String
  
	"""All values not ending with the given string."""
	handle_not_ends_with: String
	fileName: String
  
	"""All values that are not equal to given value."""
	fileName_not: String
  
	"""All values that are contained in given list."""
	fileName_in: [String!]
  
	"""All values that are not contained in given list."""
	fileName_not_in: [String!]
  
	"""All values less than the given value."""
	fileName_lt: String
  
	"""All values less than or equal the given value."""
	fileName_lte: String
  
	"""All values greater than the given value."""
	fileName_gt: String
  
	"""All values greater than or equal the given value."""
	fileName_gte: String
  
	"""All values containing the given string."""
	fileName_contains: String
  
	"""All values not containing the given string."""
	fileName_not_contains: String
  
	"""All values starting with the given string."""
	fileName_starts_with: String
  
	"""All values not starting with the given string."""
	fileName_not_starts_with: String
  
	"""All values ending with the given string."""
	fileName_ends_with: String
  
	"""All values not ending with the given string."""
	fileName_not_ends_with: String
	height: Float
  
	"""All values that are not equal to given value."""
	height_not: Float
  
	"""All values that are contained in given list."""
	height_in: [Float!]
  
	"""All values that are not contained in given list."""
	height_not_in: [Float!]
  
	"""All values less than the given value."""
	height_lt: Float
  
	"""All values less than or equal the given value."""
	height_lte: Float
  
	"""All values greater than the given value."""
	height_gt: Float
  
	"""All values greater than or equal the given value."""
	height_gte: Float
	width: Float
  
	"""All values that are not equal to given value."""
	width_not: Float
  
	"""All values that are contained in given list."""
	width_in: [Float!]
  
	"""All values that are not contained in given list."""
	width_not_in: [Float!]
  
	"""All values less than the given value."""
	width_lt: Float
  
	"""All values less than or equal the given value."""
	width_lte: Float
  
	"""All values greater than the given value."""
	width_gt: Float
  
	"""All values greater than or equal the given value."""
	width_gte: Float
	size: Float
  
	"""All values that are not equal to given value."""
	size_not: Float
  
	"""All values that are contained in given list."""
	size_in: [Float!]
  
	"""All values that are not contained in given list."""
	size_not_in: [Float!]
  
	"""All values less than the given value."""
	size_lt: Float
  
	"""All values less than or equal the given value."""
	size_lte: Float
  
	"""All values greater than the given value."""
	size_gt: Float
  
	"""All values greater than or equal the given value."""
	size_gte: Float
	mimeType: String
  
	"""All values that are not equal to given value."""
	mimeType_not: String
  
	"""All values that are contained in given list."""
	mimeType_in: [String!]
  
	"""All values that are not contained in given list."""
	mimeType_not_in: [String!]
  
	"""All values less than the given value."""
	mimeType_lt: String
  
	"""All values less than or equal the given value."""
	mimeType_lte: String
  
	"""All values greater than the given value."""
	mimeType_gt: String
  
	"""All values greater than or equal the given value."""
	mimeType_gte: String
  
	"""All values containing the given string."""
	mimeType_contains: String
  
	"""All values not containing the given string."""
	mimeType_not_contains: String
  
	"""All values starting with the given string."""
	mimeType_starts_with: String
  
	"""All values not starting with the given string."""
	mimeType_not_starts_with: String
  
	"""All values ending with the given string."""
	mimeType_ends_with: String
  
	"""All values not ending with the given string."""
	mimeType_not_ends_with: String
  }
  
  input AssetWhereUniqueInput {
	id: ID
	handle: String
  }
  
  type BatchPayload {
	"""The number of nodes that have been affected by the Batch operation."""
	count: Long!
  }
  
  type Color implements Node {
	updatedAt: DateTime!
	createdAt: DateTime!
	id: ID!
  }
  
  """A connection to a list of items."""
  type ColorConnection {
	"""Information to aid in pagination."""
	pageInfo: PageInfo!
  
	"""A list of edges."""
	edges: [ColorEdge]!
	aggregate: AggregateColor!
  }
  
  """An edge in a connection."""
  type ColorEdge {
	"""The item at the end of the edge."""
	node: Color!
  
	"""A cursor for use in pagination."""
	cursor: String!
  }
  
  enum ColorOrderByInput {
	updatedAt_ASC
	updatedAt_DESC
	createdAt_ASC
	createdAt_DESC
	id_ASC
	id_DESC
  }
  
  type ColorPreviousValues {
	updatedAt: DateTime!
	createdAt: DateTime!
	id: ID!
  }
  
  type ColorSubscriptionPayload {
	mutation: MutationType!
	node: Color
	updatedFields: [String!]
	previousValues: ColorPreviousValues
  }
  
  input ColorSubscriptionWhereInput {
	"""Logical AND on all given filters."""
	AND: [ColorSubscriptionWhereInput!]
  
	"""Logical OR on all given filters."""
	OR: [ColorSubscriptionWhereInput!]
  
	"""Logical NOT on all given filters combined by AND."""
	NOT: [ColorSubscriptionWhereInput!]
  
	"""
	The subscription event gets dispatched when it's listed in mutation_in
	"""
	mutation_in: [MutationType!]
  
	"""
	The subscription event gets only dispatched when one of the updated fields names is included in this list
	"""
	updatedFields_contains: String
  
	"""
	The subscription event gets only dispatched when all of the field names included in this list have been updated
	"""
	updatedFields_contains_every: [String!]
  
	"""
	The subscription event gets only dispatched when some of the field names included in this list have been updated
	"""
	updatedFields_contains_some: [String!]
	node: ColorWhereInput
  }
  
  input ColorWhereInput {
	"""Logical AND on all given filters."""
	AND: [ColorWhereInput!]
  
	"""Logical OR on all given filters."""
	OR: [ColorWhereInput!]
  
	"""Logical NOT on all given filters combined by AND."""
	NOT: [ColorWhereInput!]
	updatedAt: DateTime
  
	"""All values that are not equal to given value."""
	updatedAt_not: DateTime
  
	"""All values that are contained in given list."""
	updatedAt_in: [DateTime!]
  
	"""All values that are not contained in given list."""
	updatedAt_not_in: [DateTime!]
  
	"""All values less than the given value."""
	updatedAt_lt: DateTime
  
	"""All values less than or equal the given value."""
	updatedAt_lte: DateTime
  
	"""All values greater than the given value."""
	updatedAt_gt: DateTime
  
	"""All values greater than or equal the given value."""
	updatedAt_gte: DateTime
	createdAt: DateTime
  
	"""All values that are not equal to given value."""
	createdAt_not: DateTime
  
	"""All values that are contained in given list."""
	createdAt_in: [DateTime!]
  
	"""All values that are not contained in given list."""
	createdAt_not_in: [DateTime!]
  
	"""All values less than the given value."""
	createdAt_lt: DateTime
  
	"""All values less than or equal the given value."""
	createdAt_lte: DateTime
  
	"""All values greater than the given value."""
	createdAt_gt: DateTime
  
	"""All values greater than or equal the given value."""
	createdAt_gte: DateTime
	id: ID
  
	"""All values that are not equal to given value."""
	id_not: ID
  
	"""All values that are contained in given list."""
	id_in: [ID!]
  
	"""All values that are not contained in given list."""
	id_not_in: [ID!]
  
	"""All values less than the given value."""
	id_lt: ID
  
	"""All values less than or equal the given value."""
	id_lte: ID
  
	"""All values greater than the given value."""
	id_gt: ID
  
	"""All values greater than or equal the given value."""
	id_gte: ID
  
	"""All values containing the given string."""
	id_contains: ID
  
	"""All values not containing the given string."""
	id_not_contains: ID
  
	"""All values starting with the given string."""
	id_starts_with: ID
  
	"""All values not starting with the given string."""
	id_not_starts_with: ID
  
	"""All values ending with the given string."""
	id_ends_with: ID
  
	"""All values not ending with the given string."""
	id_not_ends_with: ID
  }
  
  input ColorWhereUniqueInput {
	id: ID
  }
  
  scalar DateTime
  
  type Local implements Node {
	status: Status!
	updatedAt: DateTime!
	createdAt: DateTime!
	id: ID!
	dataXyzy: String
  }
  
  """A connection to a list of items."""
  type LocalConnection {
	"""Information to aid in pagination."""
	pageInfo: PageInfo!
  
	"""A list of edges."""
	edges: [LocalEdge]!
	aggregate: AggregateLocal!
  }
  
  input LocalCreateInput {
	status: Status
	dataXyzy: String
  }
  
  """An edge in a connection."""
  type LocalEdge {
	"""The item at the end of the edge."""
	node: Local!
  
	"""A cursor for use in pagination."""
	cursor: String!
  }
  
  enum LocalOrderByInput {
	status_ASC
	status_DESC
	updatedAt_ASC
	updatedAt_DESC
	createdAt_ASC
	createdAt_DESC
	id_ASC
	id_DESC
	dataXyzy_ASC
	dataXyzy_DESC
  }
  
  type LocalPreviousValues {
	status: Status!
	updatedAt: DateTime!
	createdAt: DateTime!
	id: ID!
	dataXyzy: String
  }
  
  type LocalSubscriptionPayload {
	mutation: MutationType!
	node: Local
	updatedFields: [String!]
	previousValues: LocalPreviousValues
  }
  
  input LocalSubscriptionWhereInput {
	"""Logical AND on all given filters."""
	AND: [LocalSubscriptionWhereInput!]
  
	"""Logical OR on all given filters."""
	OR: [LocalSubscriptionWhereInput!]
  
	"""Logical NOT on all given filters combined by AND."""
	NOT: [LocalSubscriptionWhereInput!]
  
	"""
	The subscription event gets dispatched when it's listed in mutation_in
	"""
	mutation_in: [MutationType!]
  
	"""
	The subscription event gets only dispatched when one of the updated fields names is included in this list
	"""
	updatedFields_contains: String
  
	"""
	The subscription event gets only dispatched when all of the field names included in this list have been updated
	"""
	updatedFields_contains_every: [String!]
  
	"""
	The subscription event gets only dispatched when some of the field names included in this list have been updated
	"""
	updatedFields_contains_some: [String!]
	node: LocalWhereInput
  }
  
  input LocalUpdateInput {
	status: Status
	dataXyzy: String
  }
  
  input LocalUpdateManyMutationInput {
	status: Status
	dataXyzy: String
  }
  
  input LocalWhereInput {
	"""Logical AND on all given filters."""
	AND: [LocalWhereInput!]
  
	"""Logical OR on all given filters."""
	OR: [LocalWhereInput!]
  
	"""Logical NOT on all given filters combined by AND."""
	NOT: [LocalWhereInput!]
	status: Status
  
	"""All values that are not equal to given value."""
	status_not: Status
  
	"""All values that are contained in given list."""
	status_in: [Status!]
  
	"""All values that are not contained in given list."""
	status_not_in: [Status!]
	updatedAt: DateTime
  
	"""All values that are not equal to given value."""
	updatedAt_not: DateTime
  
	"""All values that are contained in given list."""
	updatedAt_in: [DateTime!]
  
	"""All values that are not contained in given list."""
	updatedAt_not_in: [DateTime!]
  
	"""All values less than the given value."""
	updatedAt_lt: DateTime
  
	"""All values less than or equal the given value."""
	updatedAt_lte: DateTime
  
	"""All values greater than the given value."""
	updatedAt_gt: DateTime
  
	"""All values greater than or equal the given value."""
	updatedAt_gte: DateTime
	createdAt: DateTime
  
	"""All values that are not equal to given value."""
	createdAt_not: DateTime
  
	"""All values that are contained in given list."""
	createdAt_in: [DateTime!]
  
	"""All values that are not contained in given list."""
	createdAt_not_in: [DateTime!]
  
	"""All values less than the given value."""
	createdAt_lt: DateTime
  
	"""All values less than or equal the given value."""
	createdAt_lte: DateTime
  
	"""All values greater than the given value."""
	createdAt_gt: DateTime
  
	"""All values greater than or equal the given value."""
	createdAt_gte: DateTime
	id: ID
  
	"""All values that are not equal to given value."""
	id_not: ID
  
	"""All values that are contained in given list."""
	id_in: [ID!]
  
	"""All values that are not contained in given list."""
	id_not_in: [ID!]
  
	"""All values less than the given value."""
	id_lt: ID
  
	"""All values less than or equal the given value."""
	id_lte: ID
  
	"""All values greater than the given value."""
	id_gt: ID
  
	"""All values greater than or equal the given value."""
	id_gte: ID
  
	"""All values containing the given string."""
	id_contains: ID
  
	"""All values not containing the given string."""
	id_not_contains: ID
  
	"""All values starting with the given string."""
	id_starts_with: ID
  
	"""All values not starting with the given string."""
	id_not_starts_with: ID
  
	"""All values ending with the given string."""
	id_ends_with: ID
  
	"""All values not ending with the given string."""
	id_not_ends_with: ID
	dataXyzy: String
  
	"""All values that are not equal to given value."""
	dataXyzy_not: String
  
	"""All values that are contained in given list."""
	dataXyzy_in: [String!]
  
	"""All values that are not contained in given list."""
	dataXyzy_not_in: [String!]
  
	"""All values less than the given value."""
	dataXyzy_lt: String
  
	"""All values less than or equal the given value."""
	dataXyzy_lte: String
  
	"""All values greater than the given value."""
	dataXyzy_gt: String
  
	"""All values greater than or equal the given value."""
	dataXyzy_gte: String
  
	"""All values containing the given string."""
	dataXyzy_contains: String
  
	"""All values not containing the given string."""
	dataXyzy_not_contains: String
  
	"""All values starting with the given string."""
	dataXyzy_starts_with: String
  
	"""All values not starting with the given string."""
	dataXyzy_not_starts_with: String
  
	"""All values ending with the given string."""
	dataXyzy_ends_with: String
  
	"""All values not ending with the given string."""
	dataXyzy_not_ends_with: String
  }
  
  input LocalWhereUniqueInput {
	id: ID
  }
  
  type Location implements Node {
	updatedAt: DateTime!
	createdAt: DateTime!
	id: ID!
  }
  
  """A connection to a list of items."""
  type LocationConnection {
	"""Information to aid in pagination."""
	pageInfo: PageInfo!
  
	"""A list of edges."""
	edges: [LocationEdge]!
	aggregate: AggregateLocation!
  }
  
  """An edge in a connection."""
  type LocationEdge {
	"""The item at the end of the edge."""
	node: Location!
  
	"""A cursor for use in pagination."""
	cursor: String!
  }
  
  enum LocationOrderByInput {
	updatedAt_ASC
	updatedAt_DESC
	createdAt_ASC
	createdAt_DESC
	id_ASC
	id_DESC
  }
  
  type LocationPreviousValues {
	updatedAt: DateTime!
	createdAt: DateTime!
	id: ID!
  }
  
  type LocationSubscriptionPayload {
	mutation: MutationType!
	node: Location
	updatedFields: [String!]
	previousValues: LocationPreviousValues
  }
  
  input LocationSubscriptionWhereInput {
	"""Logical AND on all given filters."""
	AND: [LocationSubscriptionWhereInput!]
  
	"""Logical OR on all given filters."""
	OR: [LocationSubscriptionWhereInput!]
  
	"""Logical NOT on all given filters combined by AND."""
	NOT: [LocationSubscriptionWhereInput!]
  
	"""
	The subscription event gets dispatched when it's listed in mutation_in
	"""
	mutation_in: [MutationType!]
  
	"""
	The subscription event gets only dispatched when one of the updated fields names is included in this list
	"""
	updatedFields_contains: String
  
	"""
	The subscription event gets only dispatched when all of the field names included in this list have been updated
	"""
	updatedFields_contains_every: [String!]
  
	"""
	The subscription event gets only dispatched when some of the field names included in this list have been updated
	"""
	updatedFields_contains_some: [String!]
	node: LocationWhereInput
  }
  
  input LocationWhereInput {
	"""Logical AND on all given filters."""
	AND: [LocationWhereInput!]
  
	"""Logical OR on all given filters."""
	OR: [LocationWhereInput!]
  
	"""Logical NOT on all given filters combined by AND."""
	NOT: [LocationWhereInput!]
	updatedAt: DateTime
  
	"""All values that are not equal to given value."""
	updatedAt_not: DateTime
  
	"""All values that are contained in given list."""
	updatedAt_in: [DateTime!]
  
	"""All values that are not contained in given list."""
	updatedAt_not_in: [DateTime!]
  
	"""All values less than the given value."""
	updatedAt_lt: DateTime
  
	"""All values less than or equal the given value."""
	updatedAt_lte: DateTime
  
	"""All values greater than the given value."""
	updatedAt_gt: DateTime
  
	"""All values greater than or equal the given value."""
	updatedAt_gte: DateTime
	createdAt: DateTime
  
	"""All values that are not equal to given value."""
	createdAt_not: DateTime
  
	"""All values that are contained in given list."""
	createdAt_in: [DateTime!]
  
	"""All values that are not contained in given list."""
	createdAt_not_in: [DateTime!]
  
	"""All values less than the given value."""
	createdAt_lt: DateTime
  
	"""All values less than or equal the given value."""
	createdAt_lte: DateTime
  
	"""All values greater than the given value."""
	createdAt_gt: DateTime
  
	"""All values greater than or equal the given value."""
	createdAt_gte: DateTime
	id: ID
  
	"""All values that are not equal to given value."""
	id_not: ID
  
	"""All values that are contained in given list."""
	id_in: [ID!]
  
	"""All values that are not contained in given list."""
	id_not_in: [ID!]
  
	"""All values less than the given value."""
	id_lt: ID
  
	"""All values less than or equal the given value."""
	id_lte: ID
  
	"""All values greater than the given value."""
	id_gt: ID
  
	"""All values greater than or equal the given value."""
	id_gte: ID
  
	"""All values containing the given string."""
	id_contains: ID
  
	"""All values not containing the given string."""
	id_not_contains: ID
  
	"""All values starting with the given string."""
	id_starts_with: ID
  
	"""All values not starting with the given string."""
	id_not_starts_with: ID
  
	"""All values ending with the given string."""
	id_ends_with: ID
  
	"""All values not ending with the given string."""
	id_not_ends_with: ID
  }
  
  input LocationWhereUniqueInput {
	id: ID
  }
  
  """
  The Long scalar type represents non-fractional signed whole numeric values.
  Long can represent values between -(2^63) and 2^63 - 1.
  """
  scalar Long
  
  type Mutation {
	createAsset(data: AssetCreateInput!): Asset!
	createColor: Color!
	createLocation: Location!
	createLocal(data: LocalCreateInput!): Local!
	updateAsset(data: AssetUpdateInput!, where: AssetWhereUniqueInput!): Asset
	updateLocal(data: LocalUpdateInput!, where: LocalWhereUniqueInput!): Local
	deleteAsset(where: AssetWhereUniqueInput!): Asset
	deleteColor(where: ColorWhereUniqueInput!): Color
	deleteLocation(where: LocationWhereUniqueInput!): Location
	deleteLocal(where: LocalWhereUniqueInput!): Local
	upsertAsset(where: AssetWhereUniqueInput!, create: AssetCreateInput!, update: AssetUpdateInput!): Asset!
	upsertLocal(where: LocalWhereUniqueInput!, create: LocalCreateInput!, update: LocalUpdateInput!): Local!
	updateManyAssets(data: AssetUpdateManyMutationInput!, where: AssetWhereInput): BatchPayload!
	updateManyLocals(data: LocalUpdateManyMutationInput!, where: LocalWhereInput): BatchPayload!
	deleteManyAssets(where: AssetWhereInput): BatchPayload!
	deleteManyColors(where: ColorWhereInput): BatchPayload!
	deleteManyLocations(where: LocationWhereInput): BatchPayload!
	deleteManyLocals(where: LocalWhereInput): BatchPayload!
  }
  
  enum MutationType {
	CREATED
	UPDATED
	DELETED
  }
  
  """An object with an ID"""
  interface Node {
	"""The id of the object."""
	id: ID!
  }
  
  """Information about pagination in a connection."""
  type PageInfo {
	"""When paginating forwards, are there more items?"""
	hasNextPage: Boolean!
  
	"""When paginating backwards, are there more items?"""
	hasPreviousPage: Boolean!
  
	"""When paginating backwards, the cursor to continue."""
	startCursor: String
  
	"""When paginating forwards, the cursor to continue."""
	endCursor: String
  }
  
  type Query {
	assets(where: AssetWhereInput, orderBy: AssetOrderByInput, skip: Int, after: String, before: String, first: Int, last: Int): [Asset]!
	colors(where: ColorWhereInput, orderBy: ColorOrderByInput, skip: Int, after: String, before: String, first: Int, last: Int): [Color]!
	locations(where: LocationWhereInput, orderBy: LocationOrderByInput, skip: Int, after: String, before: String, first: Int, last: Int): [Location]!
	locals(where: LocalWhereInput, orderBy: LocalOrderByInput, skip: Int, after: String, before: String, first: Int, last: Int): [Local]!
	asset(where: AssetWhereUniqueInput!): Asset
	color(where: ColorWhereUniqueInput!): Color
	location(where: LocationWhereUniqueInput!): Location
	local(where: LocalWhereUniqueInput!): Local
	assetsConnection(where: AssetWhereInput, orderBy: AssetOrderByInput, skip: Int, after: String, before: String, first: Int, last: Int): AssetConnection!
	colorsConnection(where: ColorWhereInput, orderBy: ColorOrderByInput, skip: Int, after: String, before: String, first: Int, last: Int): ColorConnection!
	locationsConnection(where: LocationWhereInput, orderBy: LocationOrderByInput, skip: Int, after: String, before: String, first: Int, last: Int): LocationConnection!
	localsConnection(where: LocalWhereInput, orderBy: LocalOrderByInput, skip: Int, after: String, before: String, first: Int, last: Int): LocalConnection!
  
	"""Fetches an object given its ID"""
	node(
	  """The ID of an object"""
	  id: ID!
	): Node
  }
  
  enum Status {
	DRAFT
	PUBLISHED
	ARCHIVED
  }
  
  type Subscription {
	asset(where: AssetSubscriptionWhereInput): AssetSubscriptionPayload
	color(where: ColorSubscriptionWhereInput): ColorSubscriptionPayload
	location(where: LocationSubscriptionWhereInput): LocationSubscriptionPayload
	local(where: LocalSubscriptionWhereInput): LocalSubscriptionPayload
  }`
