package hypergraph

import (
	"slices"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
)

// D7p -- entity-interface interface-node jump heads (M2 class-C wave). A key on an entity interface
// jumps to the interface node ITSELF in addition to each concrete implementer: a source position that
// only knows the interface (an @interfaceObject subgraph, which cannot name concrete types) must be
// able to move an instance into the interface-declaring subgraph and resolve the interface's own
// fields there (the Fed 2.3 entity-interface `_entities` contract). Spec: FORMAL_SPEC D7p.
func TestJumpHeadTypes_EntityInterfaceIncludesInterfaceNode(t *testing.T) {
	s2 := sg{fed: plan.FederationMetaData{
		EntityInterfaces: []plan.EntityInterfaceConfiguration{
			{InterfaceTypeName: "NodeWithName", ConcreteTypeNames: []string{"User"}},
		},
	}}
	heads := jumpHeadTypes(s2, "NodeWithName")
	if !slices.Contains(heads, "NodeWithName") {
		t.Fatalf("D7p: entity-interface key must head the interface node itself; got %v", heads)
	}
	if !slices.Contains(heads, "User") {
		t.Fatalf("D7p: concrete implementer heads must be preserved; got %v", heads)
	}
}

// The @interfaceObject variant is unchanged: a key on a concrete member of an interface-object
// config heads the interface node only (member edges reach the members from there), and a plain
// entity heads itself.
func TestJumpHeadTypes_InterfaceObjectAndPlainEntityUnchanged(t *testing.T) {
	s2 := sg{fed: plan.FederationMetaData{
		InterfaceObjects: []plan.EntityInterfaceConfiguration{
			{InterfaceTypeName: "Account", ConcreteTypeNames: []string{"Admin", "Regular"}},
		},
	}}
	if heads := jumpHeadTypes(s2, "Admin"); !slices.Equal(heads, []string{"Account"}) {
		t.Fatalf("@interfaceObject concrete-member key must head the interface node only; got %v", heads)
	}
	if heads := jumpHeadTypes(s2, "Order"); !slices.Equal(heads, []string{"Order"}) {
		t.Fatalf("plain entity key must head itself; got %v", heads)
	}
}
