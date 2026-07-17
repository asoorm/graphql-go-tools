package hypergraph

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
)

// Weights are the per-step costs the search adds up. The defaults make a network fetch far more
// expensive than descending into a nested object, which in turn costs more than reading one more
// field from a subgraph already being queried.
type Weights struct{ Fetch, Field, Depth int64 } // fetch, in-subgraph field, descent

func DefaultWeights() Weights { return Weights{Fetch: 1000, Field: 1, Depth: 10} }

// BuildConfig carries what Build needs beyond the datasource list.
type BuildConfig struct {
	Weights  Weights
	RootType map[string]string // operation kind -> root type name, e.g. {"query": "Query"}
}

// sg bundles everything Build reads per subgraph.
type sg struct {
	id     SubgraphID
	ds     plan.DataSource
	na     plan.NodesAccess // lists the root and child nodes this subgraph can resolve
	schema *ast.Document    // the subgraph's own parsed SDL; source of truth for its types and members
	fed    plan.FederationMetaData
	// rootAlias maps a composed-schema root type name (e.g. "Query") to this subgraph's OWN root
	// operation type name in its SDL (e.g. "DataRoomTeamQuery"). Federation subgraphs often rename
	// their root operation types with a `schema { query: ... }` definition; the router's node metadata
	// still lists their root fields under the composed name ("Query"), so looking a root field's
	// output type up by the composed name would miss it and silently drop the descent into the object
	// it returns. This map lets that lookup use the subgraph's real root type name. A missing key
	// means "no rename" -- the composed name is used directly.
	rootAlias map[string]string
}

// Build compiles the supergraph configuration into the immutable graph the search runs over. Each
// datasource gets a stable subgraph id (its index + 1); its types and members are read from its
// UpstreamSchema. Spec: FORMAL_SPEC Section 6.
func Build(dataSources []plan.DataSource, cfg BuildConfig) (*Hypergraph, error) {
	w := cfg.Weights
	if w == (Weights{}) {
		w = DefaultWeights()
	}
	b := NewBuilder()

	// One synthetic root node per operation kind, mapping root type name -> root node. Iterate in
	// sorted-key order so node-id assignment is reproducible with multiple roots (Go map iteration
	// order is randomized).
	roots := map[string]NodeID{}
	ops := make([]string, 0, len(cfg.RootType))
	for op := range cfg.RootType {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		roots[cfg.RootType[op]] = b.AddNode(Node{Kind: NodeRoot, Field: op})
	}

	// DUAL-ROLE split (D11.12 dual role): the subscription root is an ANCHOR, never a
	// classification of a type. The roots map handed to emitFieldEdges keeps only the
	// query/mutation entries, so a type named like the subscription root retains its ordinary DATA
	// role -- object-tailed field edges, for every operation kind, exactly as a graph without a
	// subscription root would emit them. The subscription ANCHOR edges are emitted ADDITIVELY per
	// subgraph (subscriptionAnchorNames) alongside the data edges: one composed type may be
	// SIMULTANEOUSLY the subscription operation root and a data object referenced by payload
	// fields (the verified billing+realtime merge), and either/or classification can never
	// represent that. Operation-kind masking (search/obligation root scoping) applies to the
	// anchor edges only -- it can never sever data routing.
	subscriptionRoot := NodeID(0)
	hasSubscriptionRoot := false
	if name, ok := cfg.RootType["subscription"]; ok {
		if r, ok := roots[name]; ok {
			subscriptionRoot, hasSubscriptionRoot = r, true
			delete(roots, name)
		}
	}

	subgraphs := make([]sg, 0, len(dataSources))
	for i, ds := range dataSources {
		na, ok := ds.(plan.NodesAccess) // concrete dataSourceConfiguration embeds DataSourceMetadata
		if !ok {
			return nil, fmt.Errorf("planv2: datasource %q does not expose NodesAccess", ds.Id())
		}
		schema, ok := ds.UpstreamSchema()
		if !ok || schema == nil {
			return nil, fmt.Errorf("planv2: datasource %q has no upstream schema", ds.Id())
		}
		id := SubgraphID(i + 1)
		b.SetSubgraphName(id, ds.Name())
		subgraphs = append(subgraphs, sg{id: id, ds: ds, na: na, schema: schema, fed: ds.FederationConfiguration(),
			rootAlias: rootTypeAliases(schema, cfg.RootType)})
	}

	keyCoordsBySg := map[SubgraphID]map[string]struct{}{}
	for _, s := range subgraphs { // field + descent edges (an @external field emits no edge)
		keyCoords := collectKeyCoords(s) // @external KEY fields are an exception -- they still resolve
		keyCoordsBySg[s.id] = keyCoords
		reqScopes := requiresScopes(s) // D7pp: @requires fields re-root onto their requires-scope node
		// Which type names ANCHOR onto the subscription root for this subgraph (D5pp reverse
		// direction, dual role): the subgraph's declared operation-root identity aims the anchor;
		// data routing below is unconditional either way.
		var anchors map[string]bool
		if hasSubscriptionRoot {
			anchors = subscriptionAnchorNames(s, cfg.RootType)
		}
		for _, tf := range s.na.ListRootNodes() {
			emitFieldEdges(b, w, roots, s, tf, keyCoords, reqScopes, anchors, subscriptionRoot)
		}
		for _, tf := range s.na.ListChildNodes() {
			emitFieldEdges(b, w, roots, s, tf, keyCoords, reqScopes, anchors, subscriptionRoot)
		}
		emitTypeMoves(b, w, s) // union/interface member edges, per subgraph
		emitProvides(b, w, s)  // @provides scope nodes + edges
	}
	emitEntityJumps(b, w, subgraphs, keyCoordsBySg) // entity jumps (keys + requires scopes) between subgraph pairs

	// D10 narrowing metadata (FORMAL_SPEC D10 amendment -- provable-non-resolvability narrowing):
	// record, per subgraph, every type a @key declaration HEADS and whether any heading declaration
	// is resolvable. The search's fall-back guard reads this to prove a goal non-resolvable -- a type
	// whose every heading key is resolvable:false can never be entity-jumped into, by this or any
	// future model amendment. jumpHeadTypes mirrors emitEntityJumps' head mapping, so entity-interface
	// and @interfaceObject heads carry the same verdict as the jumps they would head. Metadata only:
	// nodes, edges, and every cost are untouched.
	for _, s := range subgraphs {
		for _, k := range s.fed.Keys {
			for _, headType := range jumpHeadTypes(s, k.TypeName) {
				b.MarkKeyHead(s.id, headType, !k.DisableEntityResolver)
			}
		}
	}

	// Interface-object metadata (metadata only -- nodes, edges, costs untouched): the entity
	// interfaces and @interfaceObject interfaces, for response completion (a runtime __typename at
	// such a position may BE the interface name) and for lowering's representation __typename
	// rewrite (a jump INTO an @interfaceObject subgraph presents the interface name).
	for _, s := range subgraphs {
		for _, ei := range s.fed.EntityInterfaces {
			b.MarkEntityInterface(ei.InterfaceTypeName)
		}
		for _, io := range s.fed.InterfaceObjects {
			b.MarkEntityInterface(io.InterfaceTypeName)
			b.MarkInterfaceObject(s.id, io.InterfaceTypeName)
		}
	}

	return b.Build(), nil // edges were deduped as they were added; Build prunes unreachable nodes
}

// --- Field and descent edges ------------------------------------------------------------

// requiresScopes maps each @requires-bearing coordinate ("Type.field") in this subgraph to its
// requires-scope tag (D7pp): the field's Field edge is re-rooted onto the scope node so the field
// resolves only behind a jump carrying its requires -- never from a plain/local position (the
// requires-bypass this removes).
func requiresScopes(s sg) map[string]string {
	if len(s.fed.Requires) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.fed.Requires))
	for _, req := range s.fed.Requires {
		out[req.TypeName+"."+req.FieldName] = requiresScopeTag(req.TypeName, req.FieldName)
	}
	return out
}

// requiresScopeTag is the Node.Scope tag of a requires-scope node. The "req:" prefix keeps it
// disjoint from @provides scope tags (which are bare "Type.field").
func requiresScopeTag(typeName, fieldName string) string { return "req:" + typeName + "." + fieldName }

func emitFieldEdges(b *Builder, w Weights, roots map[string]NodeID, s sg, tf plan.TypeField,
	keyCoords map[string]struct{}, reqScopes map[string]string,
	subscriptionAnchors map[string]bool, subscriptionRoot NodeID) {
	external := map[string]struct{}{}
	for _, f := range tf.ExternalFieldNames {
		external[f] = struct{}{}
	}
	// A field is resolvable here if it is not @external, OR it is @external but is a key field of its
	// type in this subgraph. An @external key field is still resolvable here because the subgraph's
	// resolver returns the entity's key in the representation, so the key value rides along with the
	// object. An @external field that is NOT a key (a pure @requires/@provides input another subgraph
	// owns) emits no edge. Making key fields resolvable is what lets an entity jump's key tails be
	// reached from the source subgraph rather than stuck unreachable, so the jump can fire. External
	// key fields live in ExternalFieldNames, not FieldNames, so they are appended explicitly.
	fields := make([]string, 0, len(tf.FieldNames)+len(tf.ExternalFieldNames))
	fields = append(fields, tf.FieldNames...)
	for _, f := range tf.ExternalFieldNames {
		if _, isKey := keyCoords[tf.TypeName+"."+f]; isKey {
			fields = append(fields, f)
		}
	}
	for _, f := range fields {
		if _, ext := external[f]; ext {
			if _, isKey := keyCoords[tf.TypeName+"."+f]; !isKey {
				continue // @external non-key field: no edge
			}
		}
		head := b.AddNode(Node{Kind: NodeField, Type: tf.TypeName, Subgraph: s.id, Field: f})
		var tail NodeID
		var weight int64
		requiresScoped := false
		if r, isOpRoot := roots[tf.TypeName]; isOpRoot {
			tail, weight = r, w.Fetch // a field that enters the subgraph costs a fetch
		} else if scope, isReq := reqScopes[tf.TypeName+"."+f]; isReq {
			// D7pp(1): a @requires field resolves only from its requires-scope node, whose sole
			// in-edges are the requires-scoped jumps carrying this field's inputs. The field NODE
			// stays plain (cand/tail identity preserved); only the edge's tail moves.
			tail = b.AddNode(Node{Kind: NodeObject, Type: tf.TypeName, Subgraph: s.id, Scope: scope})
			weight = w.Field
			requiresScoped = true
		} else {
			tail = b.AddNode(Node{Kind: NodeObject, Type: tf.TypeName, Subgraph: s.id})
			weight = w.Field // an already-in-subgraph field costs one field read
		}
		// Resolving a field's output type reads the subgraph's own SDL, so a field on a renamed root
		// operation type has to be looked up under the subgraph's real root type name (see
		// sg.rootAlias): the node metadata lists it as "Query" but the SDL declares it on, say,
		// "DataRoomTeamQuery". A non-root type (or a subgraph that doesn't rename) resolves to itself.
		sdlType := tf.TypeName
		if alias, ok := s.rootAlias[tf.TypeName]; ok {
			sdlType = alias
		}
		b.AddEdge(Edge{Kind: EdgeField, Label: f, Head: head, Tails: []NodeID{tail}, Weight: weight,
			OutputType: printedFieldOutputType(s.schema, sdlType, f)})
		// DUAL-ROLE anchor (D11.12 dual role): when this type name anchors onto the subscription
		// root for this subgraph, ALSO emit the operation-entry edge -- additive, never replacing
		// the data edge above (which stays object-tailed for every operation kind; the two roles
		// coexist on the merged billing+realtime shape). Search/obligation kind-scoping masks this
		// edge for query/mutation operations. A requires-scoped field gets no anchor: an unscoped
		// entry would reintroduce the D7pp requires bypass.
		if subscriptionAnchors[tf.TypeName] && !requiresScoped {
			b.AddEdge(Edge{Kind: EdgeField, Label: f, Head: head, Tails: []NodeID{subscriptionRoot}, Weight: w.Fetch,
				OutputType: printedFieldOutputType(s.schema, sdlType, f)})
		}
		if out, composite := compositeOutputType(s.schema, sdlType, f); composite {
			obj := b.AddNode(Node{Kind: NodeObject, Type: out, Subgraph: s.id})
			b.AddEdge(Edge{Kind: EdgeDescent, Head: obj, Tails: []NodeID{head}, Weight: 0}) // descent is free
		}
	}
}

// subscriptionAnchorNames returns the type names that ANCHOR onto the subscription root for ONE
// subgraph -- the operation-root identity the subgraph declares (D5pp reverse direction), used ONLY
// to aim the additive anchor edges; data routing is unconditional regardless (see the dual-role
// note in Build):
//
//   - explicit SDL `schema { ... }` block WITH a subscription binding X: {X, composed default} --
//     node metadata may list the root fields under either name (the composed name per the D5pp
//     forward convention, or the subgraph's own name);
//   - explicit block WITHOUT a subscription binding: none -- this subgraph declares no subscription
//     root, so an anchor would be spurious (its same-named types still route as data, as always);
//   - no block: the composed default name (GraphQL default naming).
//
// The view is per subgraph on purpose: one subgraph's subscription root name may be an ordinary
// object type in another subgraph -- and, per the dual role, even in the SAME subgraph.
func subscriptionAnchorNames(s sg, cfgRootType map[string]string) map[string]bool {
	cfgName, ok := cfgRootType["subscription"]
	if !ok {
		return nil
	}
	if s.schema != nil && len(s.schema.RootOperationTypeDefinitions) > 0 {
		for i := range s.schema.RootOperationTypeDefinitions {
			rd := s.schema.RootOperationTypeDefinitions[i]
			if rd.OperationType != ast.OperationTypeSubscription {
				continue
			}
			sdlName := s.schema.Input.ByteSliceString(rd.NamedType.Name)
			if sdlName == "" || sdlName == cfgName {
				return map[string]bool{cfgName: true}
			}
			return map[string]bool{cfgName: true, sdlName: true}
		}
		return nil // explicit block, no subscription binding
	}
	return map[string]bool{cfgName: true}
}

// rootTypeAliases maps each configured root type name (e.g. "Query") to this subgraph's own root
// operation type name in its SDL, for subgraphs that rename their roots via a `schema { query: ... }`
// definition. Returns nil when the subgraph declares no explicit root operation types (the default
// Query/Mutation/Subscription names already match the config) -- callers then use the config name as
// is.
func rootTypeAliases(schema *ast.Document, cfgRootType map[string]string) map[string]string {
	if schema == nil || len(schema.RootOperationTypeDefinitions) == 0 {
		return nil
	}
	byOp := map[ast.OperationType]string{}
	for i := range schema.RootOperationTypeDefinitions {
		rd := schema.RootOperationTypeDefinitions[i]
		byOp[rd.OperationType] = schema.Input.ByteSliceString(rd.NamedType.Name)
	}
	var out map[string]string
	for opKind, cfgName := range cfgRootType {
		var ot ast.OperationType
		switch opKind {
		case "query":
			ot = ast.OperationTypeQuery
		case "mutation":
			ot = ast.OperationTypeMutation
		case "subscription":
			ot = ast.OperationTypeSubscription
		default:
			continue
		}
		if sdlName, ok := byOp[ot]; ok && sdlName != "" && sdlName != cfgName {
			if out == nil {
				out = map[string]string{}
			}
			out[cfgName] = sdlName
		}
	}
	return out
}

// printedFieldOutputType returns a field's full printed return type (with list/non-null wrappers) on
// the given type -- the subgraph-local type that the composed schema's nullability merge would erase.
// Empty when the field or type is absent or can't be printed (both harmless: the aliaser treats an
// unresolved output type conservatively).
func printedFieldOutputType(schema *ast.Document, typeName, fieldName string) string {
	node, ok := schema.Index.FirstNodeByNameStr(typeName)
	if !ok {
		return ""
	}
	def, ok := schema.NodeFieldDefinitionByName(node, ast.ByteSlice(fieldName))
	if !ok {
		return ""
	}
	printed, err := schema.PrintTypeBytes(schema.FieldDefinitionType(def), nil)
	if err != nil {
		return ""
	}
	return string(printed)
}

// compositeOutputType resolves a field's named output type and reports whether it is composite
// (object/interface/union). Scalars and enums are not composite, so they get no descent edge.
func compositeOutputType(schema *ast.Document, typeName, fieldName string) (string, bool) {
	node, ok := schema.Index.FirstNodeByNameStr(typeName)
	if !ok {
		return "", false
	}
	def, ok := schema.NodeFieldDefinitionByName(node, ast.ByteSlice(fieldName))
	if !ok {
		return "", false
	}
	out := schema.FieldDefinitionTypeNameString(def)
	outNode, ok := schema.Index.FirstNodeByNameStr(out)
	if !ok {
		return "", false
	}
	switch outNode.Kind {
	case ast.NodeKindObjectTypeDefinition, ast.NodeKindInterfaceTypeDefinition, ast.NodeKindUnionTypeDefinition:
		return out, true
	}
	return "", false
}

// --- Union and interface member edges ---------------------------------------------------

func emitTypeMoves(b *Builder, w Weights, s sg) {
	// For a union, the member set is exactly what this subgraph's own schema declares (each subgraph
	// can declare a different subset).
	for ref := range s.schema.UnionTypeDefinitions {
		u := s.schema.UnionTypeDefinitionNameString(ref)
		if strings.HasPrefix(u, "_") {
			// A federation/introspection meta type (e.g. `union _Entity` in a merged schema). GraphQL
			// reserves the "_" prefix; it is never a client-facing abstract type, so it gets no member
			// edges.
			continue
		}
		members, ok := s.schema.UnionTypeDefinitionMemberTypeNames(ref)
		if !ok {
			continue
		}
		addTypeMoves(b, w, s.id, u, members)
	}
	// For an interface, the member set is the object types in this subgraph's schema that implement it.
	for ref := range s.schema.InterfaceTypeDefinitions {
		iface := s.schema.InterfaceTypeDefinitionNameString(ref)
		if strings.HasPrefix(iface, "_") {
			continue // meta type -- same reserved-prefix rule as unions above
		}
		var members []string
		for objRef := range s.schema.ObjectTypeDefinitions {
			objName := s.schema.ObjectTypeDefinitionNameString(objRef)
			if node, ok := s.schema.Index.FirstNodeByNameStr(objName); ok &&
				s.schema.NodeImplementsInterface(node, ast.ByteSlice(iface)) {
				members = append(members, objName)
			}
		}
		addTypeMoves(b, w, s.id, iface, members)
	}
}

func addTypeMoves(b *Builder, w Weights, sid SubgraphID, u string, members []string) {
	if len(members) == 0 {
		return
	}
	sort.Strings(members)
	from := b.AddNode(Node{Kind: NodeObject, Type: u, Subgraph: sid})
	for _, c := range members {
		to := b.AddNode(Node{Kind: NodeObject, Type: c, Subgraph: sid})
		// One edge per member, each with a single head -- never one edge fanning out to many.
		b.AddEdge(Edge{Kind: EdgeTypeMove, Label: c, Head: to, Tails: []NodeID{from},
			Weight: w.Field, Members: append([]string(nil), members...)})
	}
}

// --- Entity jumps ------------------------------------------------------------------------

// keyField is one node of a parsed key/requires selection ("id organization { id }"). A @requires
// selection may also carry INLINE FRAGMENTS (`data { foo ... on Bar { bar } }`); a fragment node has
// Frag set (the type condition) and Name empty. Keys never carry fragments. Args carries the field's
// raw argument group verbatim, parens included (`(currency: "USD")`), "" when the field has none --
// node IDENTITY stays argument-blind (A-3); the raw text exists only so a filtered selection can be
// re-rendered without losing literals (D7pp(4) fragment pruning, filterConditionalRequires).
type keyField struct {
	Name string
	Frag string // inline-fragment type condition; "" for a plain field
	Args string // raw "(...)" argument group, verbatim; "" when absent
	Sub  []keyField
}

// parseSelection parses a federation key/requires selection set into a tree of keyField.
// Grammar: sel := field* ; field := NAME ("(" args ")")? ("{" sel "}")? -- keys and requires may carry
// field arguments (`price(currency: "USD")`, `comments(limit: 3)`). Argument values stay outside a
// node's identity, so `price(currency:"USD")` and `price(currency:"EUR")` are the same field node;
// the raw group is carried on keyField.Args for re-rendering only (the literal values used in
// representations are still recovered at lowering from the requires config).
func parseSelection(s string) []keyField {
	fields, _ := parseFields(tokenizeSelection(s), 0)
	return fields
}

// tokenizeSelection splits a key/requires selection into NAME / "{" / "}" / "(...)" tokens; an
// argument group `(...)` is captured whole as one token (quote- and nesting-aware, so a `)` inside a
// string literal or a nested object/list argument does not terminate the group prematurely).
func tokenizeSelection(s string) []string {
	var toks []string
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '{' || c == '}':
			toks = append(toks, string(c))
			i++
		case c == '(':
			start := i
			depth, inStr := 0, false
			for i < len(s) {
				ch := s[i]
				if inStr {
					if ch == '"' {
						inStr = false
					}
					i++
					continue
				}
				switch ch {
				case '"':
					inStr = true
				case '(':
					depth++
				case ')':
					depth--
				}
				i++
				if depth == 0 {
					break
				}
			}
			toks = append(toks, s[start:i]) // the raw "(...)" group, one token
		case isSelDelim(c):
			i++
		default:
			start := i
			for i < len(s) && !isSelDelim(s[i]) && s[i] != '{' && s[i] != '}' && s[i] != '(' {
				i++
			}
			if name := s[start:i]; name != "" {
				toks = append(toks, name)
			}
		}
	}
	return toks
}

// isSelDelim reports whether c separates tokens in a selection set (whitespace, commas, or the `:` of
// an argument/alias colon -- argument bodies are skipped as a group so a bare colon is only a separator).
func isSelDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', ',', ':':
		return true
	}
	return false
}

func parseFields(toks []string, i int) ([]keyField, int) {
	var out []keyField
	for i < len(toks) {
		switch toks[i] {
		case "}":
			return out, i + 1
		case "{":
			sub, next := parseFields(toks, i+1)
			out[len(out)-1].Sub = sub
			i = next
		case "...":
			// Inline fragment: `... on TypeName { ... }` (a @requires selection shape). The tokenizer
			// yields "...", "on", the type name, then the braced sub-selection handled by the "{"
			// case above.
			if i+2 < len(toks) && toks[i+1] == "on" {
				out = append(out, keyField{Frag: toks[i+2]})
				i += 3
				continue
			}
			i++ // malformed: skip the token rather than emit a bogus field
		case "...on":
			// The unspaced spelling of the same inline fragment.
			if i+1 < len(toks) {
				out = append(out, keyField{Frag: toks[i+1]})
				i += 2
				continue
			}
			i++
		default:
			if toks[i][0] == '(' {
				// A raw argument group: attach it to the field it follows (Args is re-render-only
				// carry; identity stays argument-blind). A leading group with no preceding field is
				// malformed input -- skip it rather than emit a bogus field.
				if len(out) > 0 {
					out[len(out)-1].Args = toks[i]
				}
				i++
				continue
			}
			out = append(out, keyField{Name: toks[i]})
			i++
		}
	}
	return out, i
}

// renderSelection re-renders a parsed key/requires selection tree back to its string form,
// argument groups included -- the inverse of parseSelection up to whitespace normalization. Used
// only when filterConditionalRequires actually pruned something; an untouched selection keeps its
// original string byte-identical.
func renderSelection(fields []keyField) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(' ')
		}
		if f.Frag != "" {
			b.WriteString("... on " + f.Frag + " { " + renderSelection(f.Sub) + " }")
			continue
		}
		b.WriteString(f.Name)
		if f.Args != "" {
			b.WriteString(f.Args)
		}
		if len(f.Sub) > 0 {
			b.WriteString(" { " + renderSelection(f.Sub) + " }")
		}
	}
	return b.String()
}

// filterConditionalRequires realizes the D7pp(4) fragment-conditioned rendering clause (AX-REQ-COND,
// MG-2 closure): a coordinate of a @requires FieldSet under an inline fragment is a CONDITIONAL
// input, and the gathering document may render it only where it is resolvable -- in particular a
// conditioned coordinate resolvable NOWHERE renders NOWHERE, or the gather fetch is an invalid
// document (FS-PLAN-1 dominates; conformance witness
// FS-REQ-9/requires-conditional/probe-unresolvable). The returned string is what Edge.Requires
// carries (the source lowering renders from): fragment branches whose coordinates the `resolvable`
// predicate rejects are pruned (per coordinate, recursively; an emptied fragment drops whole), and
// an UNCONDITIONAL composite left childless by pruning renders `{ __typename }` -- the document
// stays valid and the enclosing object still rides the representation. Unconditional coordinates
// are never pruned; a selection with no fragments (or with nothing to prune) is returned
// BYTE-IDENTICAL, argument groups included. typeName is the FieldSet's root type; schema is the
// TARGET subgraph's SDL (the target declares the requires structure by definition).
func filterConditionalRequires(sel string, schema *ast.Document, typeName string,
	resolvable func(typeName, fieldName string) bool) string {

	if !strings.Contains(sel, "...") {
		return sel // no fragments -- nothing conditional to filter
	}
	out, changed := pruneReqFields(parseSelection(sel), schema, typeName, false, resolvable)
	if !changed {
		return sel
	}
	if len(out) == 0 {
		return "__typename" // defensive: the whole FieldSet was conditional and unresolvable
	}
	return renderSelection(out)
}

// pruneReqFields walks one selection level for filterConditionalRequires. conditional is true
// inside any fragment scope (only conditional coordinates are ever pruned). Returns the surviving
// fields and whether anything anywhere was pruned.
func pruneReqFields(fields []keyField, schema *ast.Document, typeName string, conditional bool,
	resolvable func(typeName, fieldName string) bool) ([]keyField, bool) {

	var out []keyField
	changed := false
	for _, f := range fields {
		if f.Frag != "" {
			sub, ch := pruneReqFields(f.Sub, schema, f.Frag, true, resolvable)
			changed = changed || ch
			if len(sub) == 0 {
				changed = true
				continue // the whole conditioned branch resolves nowhere -- render it nowhere
			}
			nf := f
			nf.Sub = sub
			out = append(out, nf)
			continue
		}
		if conditional && f.Name != typenameFieldName && !resolvable(typeName, f.Name) {
			changed = true
			continue
		}
		nf := f
		if len(f.Sub) > 0 {
			nested, ok := compositeOutputType(schema, typeName, f.Name)
			if ok {
				sub, ch := pruneReqFields(f.Sub, schema, nested, conditional, resolvable)
				changed = changed || ch
				if len(sub) == 0 {
					if conditional {
						changed = true
						continue // a conditional composite emptied -- prune it whole
					}
					sub = []keyField{{Name: typenameFieldName}} // unconditional composite stays, validly
				}
				nf.Sub = sub
			}
		}
		out = append(out, nf)
	}
	return out, changed
}

// typenameFieldName is the __typename meta field, resolvable on every composite position.
const typenameFieldName = "__typename"

// resolvableAnywhere returns the resolvability predicate filterConditionalRequires prunes against:
// a coordinate is kept iff SOME subgraph resolves it (resolvesField -- non-external listing, or an
// external key field per D5p). The judgment is global on purpose: pruning only the
// resolvable-NOWHERE branches is assignment-independent (one filtered string per requires config)
// and can never remove a branch any gathering host could validly render -- the conservative
// realization the D7pp(4) honest-scope note records.
func resolvableAnywhere(subgraphs []sg, kc map[SubgraphID]map[string]struct{}) func(string, string) bool {
	return func(typeName, fieldName string) bool {
		for _, sx := range subgraphs {
			if resolvesField(sx, kc[sx.id], typeName, fieldName) {
				return true
			}
		}
		return false
	}
}

// collectKeyCoords returns the set of "Type.field" coordinates that appear in any @key selection of
// the subgraph, recursing through nested key selections ("id organization { id }" contributes User.id,
// User.organization, and Organization.id). emitFieldEdges uses it to decide which @external fields are
// nonetheless resolvable here (they ride in the entity representation the subgraph supplies as a key).
// Keys marked non-resolvable are still included: the key value is still carried in the representation
// even when the subgraph itself can't be entered by that key.
func collectKeyCoords(s sg) map[string]struct{} {
	out := map[string]struct{}{}
	var walk func(t string, fields []keyField)
	walk = func(t string, fields []keyField) {
		for _, f := range fields {
			if f.Frag != "" {
				continue // keys never carry inline fragments
			}
			out[t+"."+f.Name] = struct{}{}
			if len(f.Sub) == 0 {
				continue
			}
			nested, ok := compositeOutputType(s.schema, t, f.Name)
			if !ok {
				continue
			}
			walk(nested, f.Sub)
		}
	}
	for _, k := range s.fed.Keys {
		walk(k.TypeName, parseSelection(k.SelectionSet))
	}
	return out
}

func emitEntityJumps(b *Builder, w Weights, subgraphs []sg, keyCoordsBySg map[SubgraphID]map[string]struct{}) {
	for _, s2 := range subgraphs { // jump target
		for _, k := range s2.fed.Keys {
			if k.DisableEntityResolver {
				continue // @key(resolvable: false): no jump into s2 via this key
			}
			keySel := parseSelection(k.SelectionSet)
			for _, s1 := range subgraphs { // jump source
				tails, ok := keyTails(b, s1, k.TypeName, keySel)
				if !ok {
					continue // the source subgraph can't supply this key's fields
				}
				// PLAIN jump -- D7pp(2): key tails only, no @requires ride-along. It resolves the
				// target's non-requires capabilities; requires-bearing fields resolve only behind the
				// scoped jumps below. Never same-subgraph (without a requires scope it adds nothing).
				//
				// Jump variants: an entity-interface key jumps to each concrete implementer; an
				// @interfaceObject jump produces the interface node and lets member edges take it from
				// there.
				if s1.id != s2.id {
					for _, headType := range jumpHeadTypes(s2, k.TypeName) {
						head := b.AddNode(Node{Kind: NodeObject, Type: headType, Subgraph: s2.id})
						b.AddEdge(Edge{
							Kind: EdgeEntityJump, Head: head, Tails: append([]NodeID(nil), tails...),
							Weight:       w.Fetch + w.Depth, // a jump costs a fetch plus a descent
							Conditions:   mapConditions(k.Conditions),
							KeyTails:     append([]NodeID(nil), tails...),
							KeySelection: k.SelectionSet, // D7ppp(5): raw key structure for lowering
						})
					}
				}
				// REQUIRES-SCOPED jumps -- D7pp(3,4): one per @requires field on the key's type, headed
				// at the field's requires-scope node, tails = key  union  that field's requires selection.
				// Same-subgraph (s1 == s2) scoped jumps are legitimate -- the relay re-enters the
				// subgraph with the gathered inputs (v1's b->a->b shape). A requires coordinate the
				// source does not resolve is assigned to a subgraph that does (distributed tails);
				// each complete assignment vector is its own edge.
				for _, req := range s2.fed.Requires {
					if req.TypeName != k.TypeName {
						continue
					}
					reqSel := parseSelection(req.SelectionSet)
					// D7pp(4) fragment-conditioned rendering clause (AX-REQ-COND): the carried
					// selection prunes conditioned branches resolvable NOWHERE, so lowering never
					// renders them into any gathering document (the tails are unaffected -- fragment
					// coordinates contribute no static tail either way).
					reqRendered := filterConditionalRequires(req.SelectionSet, s2.schema, req.TypeName,
						resolvableAnywhere(subgraphs, keyCoordsBySg))
					for _, reqTails := range requiresTailAssignments(b, subgraphs, keyCoordsBySg, s1, req.TypeName, reqSel) {
						scopedTails := append(append([]NodeID(nil), tails...), reqTails...)
						for _, headType := range jumpHeadTypes(s2, k.TypeName) {
							head := b.AddNode(Node{Kind: NodeObject, Type: headType, Subgraph: s2.id,
								Scope: requiresScopeTag(req.TypeName, req.FieldName)})
							b.AddEdge(Edge{
								Kind: EdgeEntityJump, Head: head, Tails: scopedTails,
								Weight:       w.Fetch + w.Depth,
								Conditions:   mapConditions(k.Conditions),
								KeyTails:     append([]NodeID(nil), tails...),
								KeySelection: k.SelectionSet,
								// The raw requires selection (with arguments) for lowering: the tail
								// nodes ignore argument values, so the literal values only survive
								// here, tied to the requiring field.
								Requires:   []string{reqRendered},
								RequiresBy: []string{req.FieldName},
							})
						}
					}
				}
			}
			// DISTRIBUTED KEY -- D7ppp: a key NO single foreign source supplies emits per-assignment
			// jumps instead (gated here so every single-source key keeps the byte-identical base-D7
			// edges -- zero drift). Every coordinate -- composite paths included -- contributes an
			// explicit tail in its assigned subgraph; the target itself is never assigned (D7ppp(3)).
			if !keyFullySupplied(subgraphs, s2, k.TypeName, keySel) {
				emitDistributedKeyJumps(b, w, subgraphs, keyCoordsBySg, s2, k, keySel)
			}
		}
	}
}

// keyFullySupplied reports whether any FOREIGN source subgraph carries every coordinate of the key
// (the keyTails premise, checked without mutating the builder): true means base D7/D7pp already
// models the key and D7ppp must stay out (the zero-drift gate). The target trivially carries its own
// key fields, so s2 is excluded.
func keyFullySupplied(subgraphs []sg, s2 sg, t string, fields []keyField) bool {
	for _, s1 := range subgraphs {
		if s1.id == s2.id {
			continue
		}
		if sourceCarriesKey(s1, t, fields) {
			return true
		}
	}
	return false
}

// sourceCarriesKey is keyTails' premise as a pure predicate: subgraph s1 carries (possibly as
// @external inputs) every field coordinate of the selection rooted at t.
func sourceCarriesKey(s1 sg, t string, fields []keyField) bool {
	for _, f := range fields {
		if f.Frag != "" {
			continue // keys never carry inline fragments (defensive, mirrors keyTails)
		}
		if !s1.ds.HasRootNode(t, f.Name) && !s1.ds.HasChildNode(t, f.Name) &&
			!s1.ds.HasExternalRootNode(t, f.Name) && !s1.ds.HasExternalChildNode(t, f.Name) {
			return false
		}
		if len(f.Sub) == 0 {
			continue
		}
		nested, ok := compositeOutputType(s1.schema, t, f.Name)
		if !ok {
			return false
		}
		if !sourceCarriesKey(s1, nested, f.Sub) {
			return false
		}
	}
	return true
}

// emitDistributedKeyJumps emits the D7ppp edges for one distributed key: one plain jump per complete
// assignment vector, plus -- D7ppp(6) -- the requires-scoped variants for each @requires field on the
// key's type, with the requirement coordinates assigned by the same no-source machinery.
func emitDistributedKeyJumps(b *Builder, w Weights, subgraphs []sg, kc map[SubgraphID]map[string]struct{},
	s2 sg, k plan.FederationFieldConfiguration, keySel []keyField) {

	for _, tailsVec := range distKeyAssignments(b, subgraphs, kc, s2, k.TypeName, keySel, 0) {
		for _, headType := range jumpHeadTypes(s2, k.TypeName) {
			head := b.AddNode(Node{Kind: NodeObject, Type: headType, Subgraph: s2.id})
			b.AddEdge(Edge{
				Kind: EdgeEntityJump, Head: head, Tails: tailsVec,
				Weight:         w.Fetch + w.Depth,
				Conditions:     mapConditions(k.Conditions),
				KeyTails:       tailsVec,
				KeySelection:   k.SelectionSet,
				KeyDistributed: true,
			})
		}
		for _, req := range s2.fed.Requires {
			if req.TypeName != k.TypeName {
				continue
			}
			reqSel := parseSelection(req.SelectionSet)
			// D7pp(4) fragment-conditioned rendering clause -- same pruning as the base requires-scoped
			// jumps: a conditioned branch resolvable NOWHERE never reaches a gathering document.
			reqRendered := filterConditionalRequires(req.SelectionSet, s2.schema, req.TypeName,
				resolvableAnywhere(subgraphs, kc))
			for _, reqTails := range distKeyAssignments(b, subgraphs, kc, s2, req.TypeName, reqSel, 0) {
				scopedTails := append(append([]NodeID(nil), tailsVec...), reqTails...)
				for _, headType := range jumpHeadTypes(s2, k.TypeName) {
					head := b.AddNode(Node{Kind: NodeObject, Type: headType, Subgraph: s2.id,
						Scope: requiresScopeTag(req.TypeName, req.FieldName)})
					b.AddEdge(Edge{
						Kind: EdgeEntityJump, Head: head, Tails: scopedTails,
						Weight:         w.Fetch + w.Depth,
						Conditions:     mapConditions(k.Conditions),
						KeyTails:       tailsVec,
						KeySelection:   k.SelectionSet,
						KeyDistributed: true,
						Requires:       []string{reqRendered},
						RequiresBy:     []string{req.FieldName},
					})
				}
			}
		}
	}
}

// distKeyAssignments returns the per-assignment tail vectors for one distributed-key (or no-source
// requires) selection level (D7ppp(2,3)): every coordinate contributes an EXPLICIT tail in its
// assigned subgraph -- composite paths included, which forces the gathering walk through a fetch
// that can actually select the path. Assignment is path-coherent: a coordinate nested under a path
// assigned to ctx is assigned to ctx when ctx resolves it (single choice -- the source-local
// analogue); otherwise it ranges over every resolving subgraph EXCEPT the target s2 (D7ppp(3):
// an edge whose tail its own head must produce can never fire). ctx == 0 means no context (a
// top-level coordinate). Returns nil when some coordinate resolves nowhere outside the target --
// the key is unsatisfiable from outside, no edge (honest ErrNoValidPlan downstream). Deterministic
// (subgraph declaration order), bounded by maxRequiresAssignments per level.
func distKeyAssignments(b *Builder, subgraphs []sg, kc map[SubgraphID]map[string]struct{},
	s2 sg, t string, fields []keyField, ctx SubgraphID) [][]NodeID {

	acc := [][]NodeID{nil}
	for _, f := range fields {
		choices := distCoordChoices(b, subgraphs, kc, s2, t, f, ctx)
		if len(choices) == 0 {
			return nil
		}
		var next [][]NodeID
		for _, base := range acc {
			for _, ch := range choices {
				next = append(next, append(append([]NodeID(nil), base...), ch...))
				if len(next) >= maxRequiresAssignments {
					break
				}
			}
			if len(next) >= maxRequiresAssignments {
				break
			}
		}
		acc = next
	}
	return acc
}

// distCoordChoices returns the tail-node alternatives one distributed-key coordinate contributes
// (each alternative covers the coordinate's whole subtree). The nested type is read from the
// TARGET's schema -- the target declares the full key structure by definition.
func distCoordChoices(b *Builder, subgraphs []sg, kc map[SubgraphID]map[string]struct{},
	s2 sg, t string, f keyField, ctx SubgraphID) [][]NodeID {

	if f.Frag != "" {
		return [][]NodeID{nil} // keys never carry inline fragments (defensive, mirrors reqCoordChoices)
	}
	// Candidate subgraphs: the enclosing path's assignment when it resolves the coordinate
	// (path-coherent single choice), else every resolving subgraph except the target.
	var cands []sg
	for _, sx := range subgraphs {
		if ctx != 0 {
			if sx.id != ctx {
				continue
			}
		} else if sx.id == s2.id {
			continue
		}
		if resolvesField(sx, kc[sx.id], t, f.Name) {
			cands = append(cands, sx)
		}
	}
	if ctx != 0 && len(cands) == 0 {
		// The context subgraph does not resolve the coordinate: fall back to the foreign range.
		return distCoordChoices(b, subgraphs, kc, s2, t, f, 0)
	}
	if len(f.Sub) == 0 { // leaf coordinate
		var out [][]NodeID
		for _, sx := range cands {
			out = append(out, []NodeID{b.AddNode(Node{Kind: NodeField, Type: t, Subgraph: sx.id, Field: f.Name})})
		}
		return out
	}
	nested, ok := compositeOutputType(s2.schema, t, f.Name)
	if !ok {
		return nil
	}
	var out [][]NodeID
	for _, sx := range cands {
		pathNode := b.AddNode(Node{Kind: NodeField, Type: t, Subgraph: sx.id, Field: f.Name})
		for _, sub := range distKeyAssignments(b, subgraphs, kc, s2, nested, f.Sub, sx.id) {
			out = append(out, append([]NodeID{pathNode}, sub...))
			if len(out) >= maxRequiresAssignments {
				return out
			}
		}
	}
	return out
}

// maxRequiresAssignments bounds the distributed-tail cross product per (key, requires, source)
// triple. Requires selections are tiny (a handful of coordinates) and resolving-subgraph choice
// sets small, so the bound exists for pathological inputs only; truncation is deterministic
// (subgraph declaration order).
const maxRequiresAssignments = 8

// resolvesField reports whether subgraph s RESOLVES (t, f) locally: it lists the field non-external,
// or the field is @external but a key field of its type there (D5p -- it rides in the entity
// representation). An @external non-key listing is an input the subgraph reads, never a resolution.
func resolvesField(s sg, keyCoords map[string]struct{}, t, f string) bool {
	if s.ds.HasExternalRootNode(t, f) || s.ds.HasExternalChildNode(t, f) {
		_, isKey := keyCoords[t+"."+f]
		return isKey
	}
	return s.ds.HasRootNode(t, f) || s.ds.HasChildNode(t, f)
}

// requiresTailAssignments returns the requires-tail sets for one @requires selection from source s1
// (D7pp(4)) -- one set per complete assignment of each coordinate to a subgraph that resolves it:
//
//   - a coordinate s1 resolves is source-local (single choice, byte-identical to base D7 semantics);
//   - a LEAF s1 does not resolve is assigned, one choice per resolving subgraph, to that subgraph's
//     field node -- the search gathers it at the position via that subgraph's own edges and jumps;
//   - a source-resolvable COMPOSITE path contributes no tail of its own (as in base D7's keyTails);
//     a path s1 does not resolve contributes the resolving subgraph's path node as a tail, forcing
//     the gathering walk through a fetch that can actually select the path.
//
// Returns nil when some coordinate resolves nowhere (the requires is unsatisfiable from s1 in any
// assignment -- no edge). Deterministic (subgraph declaration order), bounded by
// maxRequiresAssignments.
func requiresTailAssignments(b *Builder, subgraphs []sg, keyCoordsBySg map[SubgraphID]map[string]struct{},
	s1 sg, t string, fields []keyField) [][]NodeID {
	return crossReqFields(b, subgraphs, keyCoordsBySg, s1, t, fields)
}

// crossReqFields builds the assignment cross product over the sibling coordinates of one selection
// level. Each field contributes a choice list; the product is truncated at maxRequiresAssignments.
func crossReqFields(b *Builder, subgraphs []sg, kc map[SubgraphID]map[string]struct{},
	s1 sg, t string, fields []keyField) [][]NodeID {

	acc := [][]NodeID{nil} // one empty assignment to start the product
	for _, f := range fields {
		choices := reqCoordChoices(b, subgraphs, kc, s1, t, f)
		if len(choices) == 0 {
			return nil // the coordinate resolves nowhere: no assignment exists
		}
		var next [][]NodeID
		for _, base := range acc {
			for _, ch := range choices {
				merged := append(append([]NodeID(nil), base...), ch...)
				next = append(next, merged)
				if len(next) >= maxRequiresAssignments {
					break
				}
			}
			if len(next) >= maxRequiresAssignments {
				break
			}
		}
		acc = next
	}
	return acc
}

// reqCoordChoices returns the tail-node alternatives one requires coordinate contributes (each
// alternative is the node set for that coordinate's whole subtree).
func reqCoordChoices(b *Builder, subgraphs []sg, kc map[SubgraphID]map[string]struct{},
	s1 sg, t string, f keyField) [][]NodeID {

	if f.Frag != "" {
		// Inline fragment in a @requires selection: its coordinates are CONDITIONAL inputs (present
		// only for instances of the fragment's member), so they contribute no static AND-tail --
		// requiring them statically would make the whole requires unsatisfiable for a mixed
		// population. The gather fetch still SELECTS them (lowering renders the full selection);
		// only the unconditional coordinates gate the jump. Completeness-favoring; the residual
		// (fragment coordinates not statically checked) is registered.
		return [][]NodeID{nil}
	}
	if len(f.Sub) == 0 { // leaf coordinate
		if resolvesField(s1, kc[s1.id], t, f.Name) {
			return [][]NodeID{{b.AddNode(Node{Kind: NodeField, Type: t, Subgraph: s1.id, Field: f.Name})}}
		}
		var out [][]NodeID
		for _, sr := range subgraphs {
			if sr.id == s1.id || !resolvesField(sr, kc[sr.id], t, f.Name) {
				continue
			}
			out = append(out, []NodeID{b.AddNode(Node{Kind: NodeField, Type: t, Subgraph: sr.id, Field: f.Name})})
		}
		return out
	}
	// composite path coordinate
	if resolvesField(s1, kc[s1.id], t, f.Name) {
		nested, ok := compositeOutputType(s1.schema, t, f.Name)
		if !ok {
			return nil
		}
		// Source-resolvable path: no tail of its own (base D7's keyTails shape); recurse below.
		return crossReqFields(b, subgraphs, kc, s1, nested, f.Sub)
	}
	var out [][]NodeID
	for _, sx := range subgraphs {
		if sx.id == s1.id || !resolvesField(sx, kc[sx.id], t, f.Name) {
			continue
		}
		nested, ok := compositeOutputType(sx.schema, t, f.Name)
		if !ok {
			continue
		}
		pathNode := b.AddNode(Node{Kind: NodeField, Type: t, Subgraph: sx.id, Field: f.Name})
		for _, sub := range crossReqFields(b, subgraphs, kc, s1, nested, f.Sub) {
			out = append(out, append([]NodeID{pathNode}, sub...))
			if len(out) >= maxRequiresAssignments {
				return out
			}
		}
	}
	return out
}

// keyTails maps a parsed key/requires selection rooted at type t to field nodes in the source
// subgraph, recursing through nested selections: "id organization { id }" yields t.id and
// Organization.id -- leaf fields only. If the source subgraph doesn't carry a field at all (not even as
// an @external input), the jump can't be made from it.
func keyTails(b *Builder, s1 sg, t string, fields []keyField) ([]NodeID, bool) {
	var out []NodeID
	for _, f := range fields {
		if f.Frag != "" {
			continue // keys never carry inline fragments; tolerate and skip (defensive)
		}
		if !s1.ds.HasRootNode(t, f.Name) && !s1.ds.HasChildNode(t, f.Name) &&
			!s1.ds.HasExternalRootNode(t, f.Name) && !s1.ds.HasExternalChildNode(t, f.Name) {
			return nil, false
		}
		if len(f.Sub) == 0 {
			// The tail node exists even for @external inputs; if nothing in the source subgraph
			// resolves it, it stays unreachable and the jump never fires -- the search handles that.
			out = append(out, b.AddNode(Node{Kind: NodeField, Type: t, Subgraph: s1.id, Field: f.Name}))
			continue
		}
		nested, ok := compositeOutputType(s1.schema, t, f.Name)
		if !ok {
			return nil, false
		}
		sub, ok := keyTails(b, s1, nested, f.Sub)
		if !ok {
			return nil, false
		}
		out = append(out, sub...)
	}
	return out, true
}

// jumpHeadTypes returns the head type(s) a key on t jumps to in the target subgraph: an
// entity-interface key -> the interface node itself PLUS each concrete implementer; a concrete member
// of an @interfaceObject config -> the interface node (member edges reach the members from there); a
// plain entity -> just t.
//
// The interface node head (D7p) is what makes an interface-level entity jump routable: a subgraph that
// declares `interface I @key(...)` resolves `_entities` representations typed on I itself (the Fed 2.3
// entity-interface contract), so a source position that only knows the interface (an @interfaceObject
// subgraph, which cannot name concrete types) can still move an instance into the interface-declaring
// subgraph and resolve I's own fields there. Heads on the concrete implementers are kept unchanged --
// a source that knows the concrete type jumps straight to the member. Pure edge addition.
func jumpHeadTypes(s2 sg, t string) []string {
	for _, ei := range s2.fed.EntityInterfaces {
		if ei.InterfaceTypeName == t {
			return append([]string{ei.InterfaceTypeName}, ei.ConcreteTypeNames...)
		}
	}
	for _, io := range s2.fed.InterfaceObjects {
		if slices.Contains(io.ConcreteTypeNames, t) {
			return []string{io.InterfaceTypeName}
		}
	}
	return []string{t}
}

func mapConditions(in []plan.KeyCondition) []KeyCondition {
	if len(in) == 0 {
		return nil
	}
	out := make([]KeyCondition, len(in))
	for i, c := range in {
		coords := make([]string, len(c.Coordinates))
		for j, fc := range c.Coordinates {
			coords[j] = fc.String() // "TypeName.FieldName"
		}
		out[i] = KeyCondition{Coordinates: coords, FieldPath: append([]string(nil), c.FieldPath...)}
	}
	return out
}

// --- @provides scopes --------------------------------------------------------------------

func emitProvides(b *Builder, w Weights, s sg) {
	for _, p := range s.fed.Provides {
		out, ok := compositeOutputType(s.schema, p.TypeName, p.FieldName)
		if !ok {
			continue // @provides only applies to composite (entity) outputs
		}
		scope := p.TypeName + "." + p.FieldName // tags the nodes reachable only via this @provides
		providing := b.AddNode(Node{Kind: NodeField, Type: p.TypeName, Subgraph: s.id, Field: p.FieldName})
		scopeObj := b.AddNode(Node{Kind: NodeObject, Type: out, Subgraph: s.id, Scope: scope})
		// Reachable only by descending through the providing field: the same object type reached any
		// other way does not get the provided selection for free.
		b.AddEdge(Edge{Kind: EdgeDescent, Head: scopeObj, Tails: []NodeID{providing}, Weight: 0, Scope: scope})
		emitProvidedFields(b, w, s, scopeObj, out, scope, parseSelection(p.SelectionSet))
	}
}

func emitProvidedFields(b *Builder, w Weights, s sg, parent NodeID, typeName, scope string, sel []keyField) {
	for _, f := range sel {
		fieldNode := b.AddNode(Node{Kind: NodeField, Type: typeName, Subgraph: s.id, Field: f.Name, Scope: scope})
		b.AddEdge(Edge{Kind: EdgeField, Label: f.Name, Head: fieldNode, Tails: []NodeID{parent},
			Weight: w.Field, Scope: scope}) // a cheap in-scope alternative to fetching the field
		if len(f.Sub) == 0 {
			continue
		}
		nested, ok := compositeOutputType(s.schema, typeName, f.Name)
		if !ok {
			continue
		}
		nestedObj := b.AddNode(Node{Kind: NodeObject, Type: nested, Subgraph: s.id, Scope: scope})
		b.AddEdge(Edge{Kind: EdgeDescent, Head: nestedObj, Tails: []NodeID{fieldNode}, Weight: 0, Scope: scope})
		emitProvidedFields(b, w, s, nestedObj, nested, scope, f.Sub)
	}
}
