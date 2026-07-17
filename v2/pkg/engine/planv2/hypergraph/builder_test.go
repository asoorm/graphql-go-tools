package hypergraph

import (
	"testing"

	. "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
)

func partialUnionBuildConfig() BuildConfig {
	return BuildConfig{Weights: DefaultWeights(), RootType: map[string]string{"query": "Query"}}
}

func entityJumpBuildConfig() BuildConfig {
	return BuildConfig{Weights: DefaultWeights(), RootType: map[string]string{"query": "Query"}}
}

func TestBuildPartialUnion_TypeMoveMemberSetsPerSubgraph(t *testing.T) {
	h, err := Build(PartialUnionConfig(), partialUnionBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	// D6: Action in A moves to {Common, OnlyA}; in B to {Common, OnlyB}. Per-subgraph.
	memA := typeMoveMembers(t, h, "Action", subgraphIDByName(t, h, "A"))
	memB := typeMoveMembers(t, h, "Action", subgraphIDByName(t, h, "B"))
	assertEqualStrings(t, memA, []string{"Common", "OnlyA"})
	assertEqualStrings(t, memB, []string{"Common", "OnlyB"})
	// Value types have NO EntityJump between A and B; Wrapper (entity) does.
	if hasEntityJump(h, "OnlyA") || hasEntityJump(h, "Common") {
		t.Fatal("value types must have no D7 edge")
	}
	if !hasEntityJump(h, "Wrapper") {
		t.Fatal("Wrapper is an entity and must have a D7 edge between A and B")
	}
}

func TestBuildEntityJump_NestedKeyAndRequiresTails(t *testing.T) {
	h, err := Build(EntityJumpConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	// D7 + D7pp: the PLAIN EntityJump into (Product,B) carries the nested key tails only (Product.id,
	// Organization.id); the @requires selection (Dimensions.length/width/height) rides on the
	// REQUIRES-SCOPED jump into (Product,B | req:Product.shippingEstimate) instead -- the ride-along
	// is gone.
	jump := findEntityJump(t, h, "Product", "B")
	tailLabels := tailFieldLabels(h, jump)
	for _, want := range []string{"Product.id", "Organization.id"} {
		if !contains(tailLabels, want) {
			t.Fatalf("plain EntityJump tail missing %q; got %v", want, tailLabels)
		}
	}
	for _, reqOnly := range []string{"Dimensions.length", "Dimensions.width", "Dimensions.height"} {
		if contains(tailLabels, reqOnly) {
			t.Fatalf("D7pp: plain EntityJump must not carry the @requires ride-along tail %q; got %v", reqOnly, tailLabels)
		}
	}
	// C.1: EntityJump weight is w_f + w_d = 1010 under defaults.
	if w := h.Edge(jump).Weight; w != 1010 {
		t.Fatalf("EntityJump weight want 1010 (w_f+w_d), got %d", w)
	}
	// The scoped jump carries key AND requires tails.
	b := subgraphIDByName(t, h, "B")
	scoped := findScopedJumps(h, "Product", b, "req:Product.shippingEstimate")
	if len(scoped) == 0 {
		t.Fatal("D7pp: no requires-scoped jump into (Product,B | shippingEstimate)")
	}
	tailLabels = tailFieldLabels(h, scoped[0])
	for _, want := range []string{"Product.id", "Organization.id", "Dimensions.length", "Dimensions.width", "Dimensions.height"} {
		if !contains(tailLabels, want) {
			t.Fatalf("scoped EntityJump tail missing %q; got %v", want, tailLabels)
		}
	}
}

func TestBuildExternalFieldEmitsNoEdge(t *testing.T) {
	// A field flagged external (ExternalFieldNames) is a key/requires input, never a resolvable
	// head here (D5). Assert no Field edge is emitted for it.
	h, err := Build(EntityJumpConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	if fieldEdgeExists(h, "Product", "B", "dimensions") {
		t.Fatal("external @requires input must emit no resolvable Field edge in B")
	}
}

func TestBuildExternalKeyFieldEmitsEdge(t *testing.T) {
	// D5p (class A1): an @external field that is a KEY field of its type is still locally producible
	// (it rides in the entity representation the subgraph supplies), so it emits a Field edge -- unlike
	// a non-key @external input, which emits none. In EntityJumpConfig's subgraph B, Product.id and
	// Organization.id are @external AND part of @key(fields: "id organization { id }"), whereas
	// Product.dimensions is @external but only a @requires input.
	h, err := Build(EntityJumpConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !fieldEdgeExists(h, "Product", "B", "id") {
		t.Fatal("D5p: @external key field Product.id must emit a Field edge in B")
	}
	if !fieldEdgeExists(h, "Organization", "B", "id") {
		t.Fatal("D5p: nested @external key field Organization.id must emit a Field edge in B")
	}
	if fieldEdgeExists(h, "Product", "B", "dimensions") {
		t.Fatal("D5p: @external non-key input Product.dimensions must still emit no Field edge in B")
	}
}

func TestBuildRenamedRootType_EmitsRootFieldDescent(t *testing.T) {
	// D5pp: the subgraph renames its root operation types (schema { query: AcmeQuery ... }), while the
	// node metadata lists the root fields under the COMPOSED name "Query"/"Mutation". The composite
	// output Widget is reachable ONLY through the root field `widget`/`makeWidget`, so the root-field
	// Descent must be emitted (resolving the output type under the subgraph's real root type name), or
	// (Widget,acme) is orphaned and every selection under it is unplannable.
	h, err := Build(RenamedRootConfig(), BuildConfig{Weights: DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation"}})
	if err != nil {
		t.Fatal(err)
	}
	if !descentExists(t, h, "Widget", "acme", "Query", "widget") {
		t.Fatal("D5pp: missing Descent (Query,acme).widget -> (Widget,acme) -- renamed query root dropped it")
	}
	if !descentExists(t, h, "Widget", "acme", "Mutation", "makeWidget") {
		t.Fatal("D5pp: missing Descent (Mutation,acme).makeWidget -> (Widget,acme) -- renamed mutation root dropped it")
	}
}

func TestBuildDefaultRootType_UnaffectedByRootAlias(t *testing.T) {
	// D5pp must be inert when the subgraph uses the DEFAULT root type names: EntityJumpConfig's
	// subgraph A declares `type Query { product: Product }` (no rename), so the root-field Descent
	// into (Product,A) must still be present.
	h, err := Build(EntityJumpConfig(), entityJumpBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !descentExists(t, h, "Product", "A", "Query", "product") {
		t.Fatal("default root type must still emit the root-field Descent (Query,A).product -> (Product,A)")
	}
}

func TestBuildSkipsMetaTypes(t *testing.T) {
	// Production UpstreamSchema() (federation.BuildFederationSchema) contains
	// `union _Entity = <all entities>`. The partial-union fixture carries it alongside the
	// real Action union: the builder must emit no (_Entity,s) node and no TypeMove for it.
	h, err := Build(PartialUnionConfig(), partialUnionBuildConfig())
	if err != nil {
		t.Fatal(err)
	}
	for id := NodeID(0); id < NodeID(h.NumNodes()); id++ {
		if n := h.Node(id); len(n.Type) > 0 && n.Type[0] == '_' {
			t.Fatalf("meta type must not appear in H: got node %+v", n)
		}
	}
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind != EdgeTypeMove {
			continue
		}
		for _, tail := range e.Tails {
			if n := h.Node(tail); len(n.Type) > 0 && n.Type[0] == '_' {
				t.Fatalf("TypeMove out of meta union must not exist: edge %+v from %+v", e, n)
			}
		}
	}
}

// --- helpers ---------------------------------------------------------------------------

func subgraphIDByName(t *testing.T, h *Hypergraph, name string) SubgraphID {
	t.Helper()
	for id := SubgraphID(1); id <= 64; id++ {
		if h.SubgraphName(id) == name {
			return id
		}
	}
	t.Fatalf("no subgraph named %q", name)
	return 0
}

func typeMoveMembers(t *testing.T, h *Hypergraph, typeName string, sid SubgraphID) []string {
	t.Helper()
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind != EdgeTypeMove {
			continue
		}
		for _, tail := range e.Tails {
			n := h.Node(tail)
			if n.Kind == NodeObject && n.Type == typeName && n.Subgraph == sid {
				return e.Members
			}
		}
	}
	t.Fatalf("no TypeMove edge for (%s, subgraph %d)", typeName, sid)
	return nil
}

func hasEntityJump(h *Hypergraph, typeName string) bool {
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind == EdgeEntityJump && h.Node(e.Head).Type == typeName {
			return true
		}
	}
	return false
}

func findEntityJump(t *testing.T, h *Hypergraph, typeName, subgraphName string) EdgeID {
	t.Helper()
	sid := subgraphIDByName(t, h, subgraphName)
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind != EdgeEntityJump {
			continue
		}
		head := h.Node(e.Head)
		if head.Type == typeName && head.Subgraph == sid {
			return id
		}
	}
	t.Fatalf("no EntityJump into (%s, %s)", typeName, subgraphName)
	return 0
}

func tailFieldLabels(h *Hypergraph, id EdgeID) []string {
	var out []string
	for _, tail := range h.Edge(id).Tails {
		n := h.Node(tail)
		if n.Kind == NodeField {
			out = append(out, n.Type+"."+n.Field)
		}
	}
	return out
}

func fieldEdgeExists(h *Hypergraph, typeName, subgraphName, field string) bool {
	var sid SubgraphID
	for id := SubgraphID(1); id <= 64; id++ {
		if h.SubgraphName(id) == subgraphName {
			sid = id
			break
		}
	}
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind != EdgeField {
			continue
		}
		n := h.Node(e.Head)
		if n.Kind == NodeField && n.Type == typeName && n.Subgraph == sid && n.Field == field {
			return true
		}
	}
	return false
}

// descentExists reports whether a Descent edge produces (headType, subgraphName) from the field node
// (tailType, subgraphName).tailField -- i.e. the root/parent field's composite output descent.
func descentExists(t *testing.T, h *Hypergraph, headType, subgraphName, tailType, tailField string) bool {
	t.Helper()
	sid := subgraphIDByName(t, h, subgraphName)
	for id := EdgeID(0); id < EdgeID(h.NumEdges()); id++ {
		e := h.Edge(id)
		if e.Kind != EdgeDescent {
			continue
		}
		head := h.Node(e.Head)
		if head.Kind != NodeObject || head.Type != headType || head.Subgraph != sid {
			continue
		}
		for _, tail := range e.Tails {
			tn := h.Node(tail)
			if tn.Kind == NodeField && tn.Type == tailType && tn.Field == tailField && tn.Subgraph == sid {
				return true
			}
		}
	}
	return false
}

func assertEqualStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("mismatch at %d: got %v want %v", i, got, want)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
