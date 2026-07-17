package obligation_test

// External test package (imports audit for corpus fixtures; audit -> planv2 -> obligation, so the
// internal package cannot import it -- same pattern as lower's external corpus tests).

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// D3pppp -- abstract-position member expansion. `products { id reviews { id } }`
// (abstract-types/case-07): `reviews` is an interface field locally unresolvable at the `products`
// position (no capable subgraph declares Product.reviews), whose position-possible members Book and
// Magazine declare it in the reviews subgraph. The obligation tree must be rewritten to the
// member-exploded form: the `reviews` subtree replaced by one Refine per member, each holding a
// retyped copy -- while the locally-resolvable `id` stays un-expanded.
func TestExpand_AbstractPositionMemberExpansion(t *testing.T) {
	m, err := audit.LoadCaseForTest("abstract-types", "case-07")
	if err != nil {
		t.Fatal(err)
	}
	h, err := hypergraph.Build(m.DataSources, hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := obligation.Build(m.Operation, m.Definition, "", h)
	if err != nil {
		t.Fatal(err)
	}
	obs := o.Obligations()

	type refineKey struct{ u, c string }
	refines := map[refineKey]obligation.ObID{}
	var reviewsOwners []string
	idCount := 0
	for _, ob := range obs {
		switch {
		case ob.Kind == obligation.Refine:
			refines[refineKey{ob.Type, ob.Concrete}] = ob.ID
		case ob.Kind == obligation.Field && ob.Field == "reviews":
			reviewsOwners = append(reviewsOwners, ob.Type)
		case ob.Kind == obligation.Field && ob.Field == "id" && ob.Type == "Product":
			idCount++
		}
		// Structural invariant the goal resolution relies on: parents precede children.
		if ob.Parent != obligation.NoParent && ob.Parent >= ob.ID {
			t.Fatalf("obligation %d has parent %d >= its own id (rewrite broke creation order)", ob.ID, ob.Parent)
		}
	}
	if _, ok := refines[refineKey{"Product", "Book"}]; !ok {
		t.Fatalf("expected a Product|>Book refinement from member expansion; obligations: %+v", obs)
	}
	if _, ok := refines[refineKey{"Product", "Magazine"}]; !ok {
		t.Fatalf("expected a Product|>Magazine refinement from member expansion; obligations: %+v", obs)
	}
	if len(reviewsOwners) != 2 || reviewsOwners[0] == reviewsOwners[1] {
		t.Fatalf("reviews must be cloned once per member with retyped owners, got %v", reviewsOwners)
	}
	for _, owner := range reviewsOwners {
		if owner != "Book" && owner != "Magazine" {
			t.Fatalf("expanded reviews copy has owner %q, want Book/Magazine", owner)
		}
	}
	// The locally-resolvable interface field `id` is NOT expanded (gate 2): one un-cloned obligation.
	if idCount != 1 {
		t.Fatalf("Product.id must stay un-expanded (locally resolvable at the position), got %d copies", idCount)
	}
}
