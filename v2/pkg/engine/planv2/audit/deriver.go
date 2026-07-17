package audit

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// overrideDecl records one `@override(from: "X")` occurrence: the owning subgraph takes the field,
// and the `from` subgraph must stop resolving it. Applied as a cross-subgraph post-pass in
// BuildDataSources (a single SDL cannot know the other subgraph's metadata).
type overrideDecl struct {
	TypeName  string
	FieldName string
	From      string // subgraph NAME the field is taken over from
}

// deriveMetadata derives a plan.DataSourceMetadata from a Federation-v2 subgraph SDL, the way a
// composition step would: it walks object/interface type definitions and their directives to produce
// the RootNodes/ChildNodes capability lists (with @external field partitioning) and the
// Keys/Requires/Provides/EntityInterfaces/InterfaceObjects federation configurations the hypergraph
// builder reads.
//
// implementers maps interface name -> concrete object types implementing it IN THE COMPOSED
// SUPERGRAPH. It is needed for @interfaceObject: a subgraph declaring `type X @key @interfaceObject`
// does not know X's concrete types -- composition does. Following the v1 config convention
// (entity_interfaces_engine_config.go), the interface-object type's fields AND keys are propagated
// to every concrete implementer.
//
// Supported: @key (incl. resolvable:false), @external, @requires (incl. argument capture into
// RequiredFieldArguments), @provides, @shareable (no metadata effect), entity interfaces
// (interface X @key), @interfaceObject, @override (returned as decls; applied cross-subgraph),
// fed-v1 `extend type` blocks. @inaccessible fields stay in the capability lists (they remain
// subgraph-resolvable; only the client schema hides them).
func deriveMetadata(sdl string, implementers map[string][]string) (*plan.DataSourceMetadata, []overrideDecl, error) {
	doc := unsafeparser.ParseGraphqlDocumentString(sdl)

	meta := &plan.DataSourceMetadata{}
	var overrides []overrideDecl
	rootTypes := map[string]bool{"Query": true, "Mutation": true, "Subscription": true}
	// canonicalRoot maps a RENAMED root operation type name (schema { query: AcmeQuery }) to its
	// composed canonical name -- composition lists a subgraph's root fields under the COMPOSED root
	// type name ("Query") even when the subgraph SDL renames it, and planv2's hypergraph builder
	// consumes exactly that convention (sg.rootAlias resolves the SDL-side name). Deriving the
	// metadata under the SDL name would leave every renamed-root field unreachable.
	canonicalRoot := map[string]string{}
	for ref := range doc.RootOperationTypeDefinitions {
		rd := doc.RootOperationTypeDefinitions[ref]
		name := doc.Input.ByteSliceString(rd.NamedType.Name)
		if name == "" {
			continue
		}
		rootTypes[name] = true
		var canon string
		switch rd.OperationType {
		case ast.OperationTypeQuery:
			canon = "Query"
		case ast.OperationTypeMutation:
			canon = "Mutation"
		case ast.OperationTypeSubscription:
			canon = "Subscription"
		}
		if canon != "" && name != canon {
			canonicalRoot[name] = canon
		}
	}

	// localImplementers: interface -> object types implementing it IN THIS SDL (entity interfaces).
	localImplementers := map[string][]string{}
	for ref := range doc.ObjectTypeDefinitions {
		objName := doc.ObjectTypeDefinitionNameString(ref)
		node, ok := doc.Index.FirstNodeByNameStr(objName)
		if !ok {
			continue
		}
		for ifaceRef := range doc.InterfaceTypeDefinitions {
			iface := doc.InterfaceTypeDefinitionNameString(ifaceRef)
			if doc.NodeImplementsInterface(node, ast.ByteSlice(iface)) {
				localImplementers[iface] = append(localImplementers[iface], objName)
			}
		}
	}

	walk := func(typeName string, fieldRefs []int, typeDirectives []int) (plan.TypeField, bool) {
		if canon, ok := canonicalRoot[typeName]; ok {
			typeName = canon // renamed root: metadata under the composed name (see canonicalRoot)
		}
		keys := readKeys(&doc, typeName, typeDirectives)
		meta.Keys = append(meta.Keys, keys...)
		isEntity := len(keys) > 0
		isRoot := rootTypes[typeName]

		var fieldNames, externalNames []string
		for _, fr := range fieldRefs {
			fname := doc.FieldDefinitionNameString(fr)
			if fname == "" || fname == "_entities" || fname == "_service" {
				continue
			}
			directives := doc.FieldDefinitions[fr].Directives.Refs
			if hasDirective(&doc, directives, "external") {
				externalNames = append(externalNames, fname)
			} else {
				fieldNames = append(fieldNames, fname)
			}
			if sel, ok := fieldSelectionArg(&doc, directives, "requires"); ok {
				meta.Requires = append(meta.Requires, plan.FederationFieldConfiguration{
					TypeName: typeName, FieldName: fname, SelectionSet: sel,
					RequiredFieldArguments: captureArguments(typeName, sel),
				})
			}
			if sel, ok := fieldSelectionArg(&doc, directives, "provides"); ok {
				meta.Provides = append(meta.Provides, plan.FederationFieldConfiguration{
					TypeName: typeName, FieldName: fname, SelectionSet: sel,
				})
			}
			for _, d := range directives {
				if doc.DirectiveNameString(d) == "override" {
					if from, ok := stringArgNamed(&doc, d, "from"); ok {
						overrides = append(overrides, overrideDecl{TypeName: typeName, FieldName: fname, From: from})
					}
				}
			}
		}

		tf := plan.TypeField{TypeName: typeName, FieldNames: fieldNames, ExternalFieldNames: externalNames}
		return tf, isRoot || isEntity
	}

	appendNode := func(tf plan.TypeField, root bool) {
		if root {
			meta.RootNodes = append(meta.RootNodes, tf)
		} else {
			meta.ChildNodes = append(meta.ChildNodes, tf)
		}
	}

	for ref := range doc.ObjectTypeDefinitions {
		name := doc.ObjectTypeDefinitionNameString(ref)
		directives := doc.ObjectTypeDefinitions[ref].Directives.Refs
		tf, root := walk(name, doc.ObjectTypeDefinitions[ref].FieldsDefinition.Refs, directives)

		if hasDirective(&doc, directives, "interfaceObject") {
			// @interfaceObject: this subgraph resolves the INTERFACE as a plain object. Composition
			// propagates its fields and keys to every concrete implementer in the supergraph -- mirror
			// the v1 config convention (entity_interfaces_engine_config.go): RootNodes + Keys entries
			// for the interface name AND each implementer, and an InterfaceObjects configuration.
			concrete := append([]string(nil), implementers[name]...)
			sort.Strings(concrete)
			meta.InterfaceObjects = append(meta.InterfaceObjects, plan.EntityInterfaceConfiguration{
				InterfaceTypeName: name, ConcreteTypeNames: concrete,
			})
			appendNode(tf, true)
			typeKeys := meta.Keys.FilterByTypeAndResolvability(name, false)
			for _, c := range concrete {
				appendNode(plan.TypeField{TypeName: c, FieldNames: tf.FieldNames, ExternalFieldNames: tf.ExternalFieldNames}, true)
				for _, k := range typeKeys {
					meta.Keys = append(meta.Keys, plan.FederationFieldConfiguration{
						TypeName: c, SelectionSet: k.SelectionSet, DisableEntityResolver: k.DisableEntityResolver,
					})
				}
			}
			continue
		}
		appendNode(tf, root)
	}

	for ref := range doc.InterfaceTypeDefinitions {
		name := doc.InterfaceTypeDefinitionNameString(ref)
		directives := doc.InterfaceTypeDefinitions[ref].Directives.Refs
		tf, root := walk(name, doc.InterfaceTypeDefinitions[ref].FieldsDefinition.Refs, directives)
		if root && !rootTypes[name] {
			// interface X @key -- an ENTITY INTERFACE: record the local concrete implementers.
			concrete := append([]string(nil), localImplementers[name]...)
			sort.Strings(concrete)
			meta.EntityInterfaces = append(meta.EntityInterfaces, plan.EntityInterfaceConfiguration{
				InterfaceTypeName: name, ConcreteTypeNames: concrete,
			})
		}
		appendNode(tf, root)
	}

	// Fed-v1 `extend type X` blocks: fold extension fields into the same TypeField shape.
	for ref := range doc.ObjectTypeExtensions {
		name := doc.ObjectTypeExtensionNameString(ref)
		tf, root := walk(name, doc.ObjectTypeExtensions[ref].FieldsDefinition.Refs,
			doc.ObjectTypeExtensions[ref].Directives.Refs)
		appendNode(tf, root || rootTypes[name])
	}

	if len(meta.RootNodes) == 0 && len(meta.ChildNodes) == 0 {
		return nil, nil, fmt.Errorf("no type definitions found")
	}
	return meta, overrides, nil
}

// applyOverrides removes each overridden field from the `from` subgraph's resolvable field lists
// (the overriding subgraph is now the owner -- the overridden subgraph must not emit a Field edge).
// An @override whose `from` subgraph is not in the config (the `unavailable-override` scenario) is a
// no-op: there is nothing to take the field from.
func applyOverrides(metas map[string]*plan.DataSourceMetadata, overrides []overrideDecl) {
	for _, o := range overrides {
		meta, ok := metas[o.From]
		if !ok {
			continue
		}
		for _, nodes := range []*plan.TypeFields{&meta.RootNodes, &meta.ChildNodes} {
			for i := range *nodes {
				if (*nodes)[i].TypeName != o.TypeName {
					continue
				}
				(*nodes)[i].FieldNames = slices.DeleteFunc((*nodes)[i].FieldNames, func(f string) bool {
					return f == o.FieldName
				})
			}
		}
	}
}

// captureArguments records the literal arguments a @requires selection set carries (e.g.
// `comments(limit: 3) { authorId }` -> {Path: "comments", Value: "3"}), populating the same
// RequiredFieldArguments shape the v1 config loader produces. planv2 does not yet consume
// argument-carrying @requires (its selection parser reads field names only) -- such cases surface as
// typed plan errors and are skipped; the capture keeps the derived metadata faithful for when
// argument lowering lands.
func captureArguments(typeName, selectionSet string) []plan.RequiredFieldArgumentInfo {
	if !strings.Contains(selectionSet, "(") {
		return nil
	}
	doc := unsafeparser.ParseGraphqlDocumentString("{ " + selectionSet + " }")
	var out []plan.RequiredFieldArgumentInfo
	for ref := range doc.Fields {
		if !doc.FieldHasArguments(ref) {
			continue
		}
		fieldName := doc.FieldNameString(ref)
		for _, argRef := range doc.Fields[ref].Arguments.Refs {
			value := doc.ValueContentString(doc.Arguments[argRef].Value)
			out = append(out, plan.RequiredFieldArgumentInfo{
				TypeName: typeName,
				Path:     fieldName,
				Value:    value,
			})
		}
	}
	return out
}

// readKeys reads every @key directive on a type into a FederationFieldConfiguration, honoring
// resolvable:false (-> DisableEntityResolver).
func readKeys(doc *ast.Document, typeName string, directives []int) plan.FederationFieldConfigurations {
	var out plan.FederationFieldConfigurations
	for _, d := range directives {
		if doc.DirectiveNameString(d) != "key" {
			continue
		}
		sel, ok := stringArgNamed(doc, d, "fields")
		if !ok {
			continue
		}
		cfg := plan.FederationFieldConfiguration{TypeName: typeName, SelectionSet: sel}
		if v, ok := doc.DirectiveArgumentValueByName(d, ast.ByteSlice("resolvable")); ok {
			if v.Kind == ast.ValueKindBoolean && !bool(doc.BooleanValue(v.Ref)) {
				cfg.DisableEntityResolver = true
			}
		}
		out = append(out, cfg)
	}
	return out
}

func hasDirective(doc *ast.Document, directives []int, name string) bool {
	for _, d := range directives {
		if doc.DirectiveNameString(d) == name {
			return true
		}
	}
	return false
}

// fieldSelectionArg returns the `fields` selection-set argument of the named directive if present.
func fieldSelectionArg(doc *ast.Document, directives []int, name string) (string, bool) {
	for _, d := range directives {
		if doc.DirectiveNameString(d) != name {
			continue
		}
		return stringArgNamed(doc, d, "fields")
	}
	return "", false
}

func stringArgNamed(doc *ast.Document, directive int, arg string) (string, bool) {
	v, ok := doc.DirectiveArgumentValueByName(directive, ast.ByteSlice(arg))
	if !ok || v.Kind != ast.ValueKindString {
		return "", false
	}
	return doc.StringValueContentString(v.Ref), true
}
