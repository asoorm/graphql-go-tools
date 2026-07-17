package conformance

// spec.go -- the tiny schema model the scenario families synthesize cases from. A family builds
// SubgraphSpec values (per-subgraph federation shapes) and the SAME specs render both the
// per-subgraph federation SDLs and the composed client-facing supergraph SDL, so the two can never
// drift (the scale_export_test.go lockstep pattern). This is NOT a general composer: it supports
// exactly the composition rules the generated families exercise, and families are written against
// its documented behavior.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
)

// Key is one @key declaration on a type.
type Key struct {
	Fields        string
	NonResolvable bool // renders resolvable: false
}

// Field is one field of an object/interface type.
type Field struct {
	Name         string
	Type         string // rendered GraphQL type, e.g. "ID!", "[Review]"
	Args         string // rendered argument list WITHOUT parens, e.g. `currency: String!` ("" = none)
	External     bool
	Shareable    bool
	Inaccessible bool
	Requires     string // @requires(fields: ...) selection set
	Provides     string // @provides(fields: ...) selection set
	OverrideFrom string // @override(from: ...)
}

// TypeKind enumerates the type-definition kinds the families need.
type TypeKind int

const (
	KindObject TypeKind = iota
	KindInterface
	KindUnion
)

// Type is one type definition inside a subgraph.
type Type struct {
	Name            string
	Kind            TypeKind
	Keys            []Key
	InterfaceObject bool // object rendered with @interfaceObject
	Implements      []string
	Members         []string // union members
	Fields          []Field
}

// SubgraphSpec is one subgraph's schema. Root type names default to Query/Mutation/Subscription;
// a non-empty rename emits a schema block binding the renamed type (FS-ROOT-4 territory).
type SubgraphSpec struct {
	Name             string
	QueryName        string // "" = "Query" if such a type exists
	MutationName     string
	SubscriptionName string
	Types            []*Type
}

// Schema groups the subgraphs of one generated case.
type Schema struct {
	Subgraphs []SubgraphSpec
	// ComposedFieldTypes overrides the composed output type of "Type.field" when subgraph
	// declarations legitimately diverge (FS-ABS-12 per-subgraph output types).
	ComposedFieldTypes map[string]string
	// OmitSubscriptionRoot suppresses the composed schema's `subscription:` binding even though a
	// type named Subscription exists -- the billing/commerce war-story shape where `Subscription`
	// is an ordinary DATA entity and no subgraph declares a subscription operation root.
	OmitSubscriptionRoot bool
}

func (s SubgraphSpec) rootName(kind string) string {
	switch kind {
	case "query":
		if s.QueryName != "" {
			return s.QueryName
		}
		return "Query"
	case "mutation":
		if s.MutationName != "" {
			return s.MutationName
		}
		return "Mutation"
	default:
		if s.SubscriptionName != "" {
			return s.SubscriptionName
		}
		return "Subscription"
	}
}

// typ returns the named type of the subgraph, or nil.
func (s SubgraphSpec) typ(name string) *Type {
	for _, t := range s.Types {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// hasExplicitRootBinding reports whether the subgraph binds kind to a NON-DEFAULT type name, or a
// default-named type serves a different role.
func (s SubgraphSpec) needsSchemaBlock() bool {
	return s.QueryName != "" || s.MutationName != "" || s.SubscriptionName != ""
}

// gqlStr renders a GraphQL string literal; FieldSets containing quotes (argument literals) render
// as block strings, matching the corpus fixtures' convention.
func gqlStr(s string) string {
	if strings.Contains(s, `"`) {
		return `"""` + s + `"""`
	}
	return fmt.Sprintf("%q", s)
}

// renderField renders one field definition line (subgraph side, with federation directives).
func renderField(f Field, federation bool) string {
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(f.Name)
	if f.Args != "" {
		b.WriteString("(" + f.Args + ")")
	}
	b.WriteString(": " + f.Type)
	if federation {
		if f.External {
			b.WriteString(" @external")
		}
		if f.Shareable {
			b.WriteString(" @shareable")
		}
		if f.Inaccessible {
			b.WriteString(" @inaccessible")
		}
		if f.Requires != "" {
			b.WriteString(" @requires(fields: " + gqlStr(f.Requires) + ")")
		}
		if f.Provides != "" {
			b.WriteString(" @provides(fields: " + gqlStr(f.Provides) + ")")
		}
		if f.OverrideFrom != "" {
			b.WriteString(fmt.Sprintf(" @override(from: %q)", f.OverrideFrom))
		}
	}
	return b.String()
}

// renderType renders one type definition (subgraph side when federation=true).
func renderType(t *Type, federation bool) string {
	var b strings.Builder
	switch t.Kind {
	case KindUnion:
		b.WriteString("union " + t.Name + " = " + strings.Join(t.Members, " | ") + "\n")
		return b.String()
	case KindInterface:
		b.WriteString("interface " + t.Name)
	default:
		b.WriteString("type " + t.Name)
	}
	if len(t.Implements) > 0 {
		b.WriteString(" implements " + strings.Join(t.Implements, " & "))
	}
	if federation {
		for _, k := range t.Keys {
			if k.NonResolvable {
				b.WriteString(fmt.Sprintf(" @key(fields: %q, resolvable: false)", k.Fields))
			} else {
				b.WriteString(fmt.Sprintf(" @key(fields: %q)", k.Fields))
			}
		}
		if t.InterfaceObject {
			b.WriteString(" @interfaceObject")
		}
	}
	b.WriteString(" {\n")
	for _, f := range t.Fields {
		b.WriteString(renderField(f, federation) + "\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// RenderSubgraphSDL renders the federation SDL of one subgraph. The audit runner prepends the
// federation @link boilerplate when absent, so none is emitted here.
func RenderSubgraphSDL(s SubgraphSpec) string {
	var b strings.Builder
	if s.needsSchemaBlock() {
		b.WriteString("schema {")
		if q := s.QueryName; q != "" || s.typ("Query") != nil {
			b.WriteString(" query: " + s.rootName("query"))
		}
		if m := s.MutationName; m != "" || s.typ("Mutation") != nil {
			b.WriteString(" mutation: " + s.rootName("mutation"))
		}
		if sub := s.SubscriptionName; sub != "" || s.typ("Subscription") != nil {
			b.WriteString(" subscription: " + s.rootName("subscription"))
		}
		b.WriteString(" }\n\n")
	}
	for _, t := range s.Types {
		b.WriteString(renderType(t, true) + "\n")
	}
	return b.String()
}

// canonicalTypeName maps a subgraph's (possibly renamed) root type name to the composed canonical
// name; every other type keeps its name.
func canonicalTypeName(s SubgraphSpec, name string) string {
	switch name {
	case s.rootName("query"):
		if s.QueryName != "" || name == "Query" {
			return "Query"
		}
	case s.rootName("mutation"):
		if s.MutationName != "" || name == "Mutation" {
			return "Mutation"
		}
	case s.rootName("subscription"):
		if s.SubscriptionName != "" || name == "Subscription" {
			return "Subscription"
		}
	}
	return name
}

// isRenamedRoot reports whether name is a renamed root type of s (so member/implements references
// never need mapping -- root types are never members).
func rootRole(s SubgraphSpec, name string) (string, bool) {
	for _, kind := range []string{"query", "mutation", "subscription"} {
		bound := ""
		switch kind {
		case "query":
			bound = s.QueryName
		case "mutation":
			bound = s.MutationName
		case "subscription":
			bound = s.SubscriptionName
		}
		if bound != "" && name == bound {
			return kind, true
		}
	}
	return "", false
}

// ComposeSupergraphSDL merges the subgraph specs into the client-facing composed schema:
//   - types merged by (canonical) name; field sets unioned by field name (first signature wins,
//     unless ComposedFieldTypes overrides the output type -- FS-ABS-12);
//   - @inaccessible fields omitted (the API schema hides them; subgraph SDLs keep them);
//   - union member sets unioned; implements sets unioned;
//   - @interfaceObject object types merge INTO the interface declared elsewhere, and their
//     non-key fields propagate to every composed implementer of that interface (the pinned
//     @interfaceObject composition semantics);
//   - a schema block is emitted naming the roots that exist.
func ComposeSupergraphSDL(schema Schema) string {
	type composedField struct {
		Field
		order int
	}
	type composedType struct {
		name       string
		kind       TypeKind
		implements map[string]bool
		members    map[string]bool
		fields     map[string]*composedField
		order      int
		isIfaceObj bool // some subgraph declared it @interfaceObject
	}
	types := map[string]*composedType{}
	var typeOrder int
	get := func(name string, kind TypeKind) *composedType {
		ct, ok := types[name]
		if !ok {
			ct = &composedType{
				name: name, kind: kind,
				implements: map[string]bool{}, members: map[string]bool{},
				fields: map[string]*composedField{}, order: typeOrder,
			}
			typeOrder++
			types[name] = ct
		}
		if kind == KindInterface {
			// interface declaration wins over an @interfaceObject object twin
			ct.kind = KindInterface
		}
		return ct
	}

	fieldOrder := 0
	for _, sg := range schema.Subgraphs {
		for _, t := range sg.Types {
			name := canonicalTypeName(sg, t.Name)
			kind := t.Kind
			if t.InterfaceObject {
				kind = KindInterface // composes into the entity interface
			}
			ct := get(name, kind)
			if t.InterfaceObject {
				ct.isIfaceObj = true
			}
			for _, impl := range t.Implements {
				ct.implements[impl] = true
			}
			for _, m := range t.Members {
				ct.members[m] = true
			}
			for _, f := range t.Fields {
				if f.Inaccessible {
					continue
				}
				cf, ok := ct.fields[f.Name]
				if !ok {
					cf = &composedField{Field: Field{Name: f.Name, Type: f.Type, Args: f.Args}, order: fieldOrder}
					fieldOrder++
					ct.fields[f.Name] = cf
				}
				if override, ok := schema.ComposedFieldTypes[name+"."+f.Name]; ok {
					cf.Type = override
				}
			}
		}
	}

	ordered := make([]*composedType, 0, len(types))
	for _, ct := range types {
		ordered = append(ordered, ct)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].order < ordered[j].order })

	// @interfaceObject propagation: contributed fields land on every composed implementer.
	// Iteration runs over the ORDERED slices (maps would leak iteration nondeterminism into the
	// propagated fields' rendering order -- the generator's determinism contract forbids that).
	for _, ct := range ordered {
		if ct.kind != KindInterface {
			continue
		}
		ifaceFields := make([]*composedField, 0, len(ct.fields))
		for _, f := range ct.fields {
			ifaceFields = append(ifaceFields, f)
		}
		sort.Slice(ifaceFields, func(i, j int) bool { return ifaceFields[i].order < ifaceFields[j].order })
		for _, impl := range ordered {
			if impl.kind != KindObject || !impl.implements[ct.name] {
				continue
			}
			for _, f := range ifaceFields {
				if _, ok := impl.fields[f.Name]; !ok {
					cp := *f
					cp.order = fieldOrder
					fieldOrder++
					impl.fields[f.Name] = &cp
				}
			}
		}
	}

	var b strings.Builder
	var roots []string
	for _, kind := range []string{"query", "mutation", "subscription"} {
		name := strings.ToUpper(kind[:1]) + kind[1:]
		if kind == "subscription" && schema.OmitSubscriptionRoot {
			continue
		}
		if _, ok := types[name]; ok {
			roots = append(roots, kind+": "+name)
		}
	}
	if len(roots) > 0 {
		b.WriteString("schema { " + strings.Join(roots, " ") + " }\n\n")
	}
	for _, ct := range ordered {
		switch ct.kind {
		case KindUnion:
			members := make([]string, 0, len(ct.members))
			for m := range ct.members {
				members = append(members, m)
			}
			sort.Strings(members)
			b.WriteString("union " + ct.name + " = " + strings.Join(members, " | ") + "\n\n")
			continue
		case KindInterface:
			b.WriteString("interface " + ct.name)
		default:
			b.WriteString("type " + ct.name)
		}
		if len(ct.implements) > 0 {
			impls := make([]string, 0, len(ct.implements))
			for i := range ct.implements {
				impls = append(impls, i)
			}
			sort.Strings(impls)
			b.WriteString(" implements " + strings.Join(impls, " & "))
		}
		b.WriteString(" {\n")
		fields := make([]*composedField, 0, len(ct.fields))
		for _, f := range ct.fields {
			fields = append(fields, f)
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].order < fields[j].order })
		for _, f := range fields {
			b.WriteString(renderField(f.Field, false) + "\n")
		}
		b.WriteString("}\n\n")
	}
	return b.String()
}

// BuildAuditCase assembles the audit.Case fixture from a Schema + operation (+ optional expected
// response JSON).
func BuildAuditCase(name, family string, schema Schema, operation, expected string) audit.Case {
	subs := make([]audit.Subgraph, 0, len(schema.Subgraphs))
	for _, sg := range schema.Subgraphs {
		subs = append(subs, audit.Subgraph{Name: sg.Name, SDL: RenderSubgraphSDL(sg)})
	}
	c := audit.Case{
		Name:       name,
		Suite:      family,
		Subgraphs:  subs,
		Definition: ComposeSupergraphSDL(schema),
		Operation:  operation,
	}
	if expected != "" {
		c.Expected = []byte(expected)
	}
	return c
}
