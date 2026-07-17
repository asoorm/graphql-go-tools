package lower

import (
	"strings"
	"testing"

	hgtestdata "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph/testdata"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// TestNewPathSelfNestedEntityCycleTerminates is the SELF-NESTED ENTITY regression (M2
// requires-chain wave addendum; customer-corpus stack overflow in groupForObject): the entity V
// re-appears nested inside itself through the type cycle V -> M -> P -> E.node -> V, and V carries a
// @requires field on a second subgraph. The deep re-entry collapses two response positions onto ONE
// (V,a) graph node; the deep goal's chain-layered-repaired walk therefore visits (V,a) twice and
// the head-keyed producedBy union records the DEEP descent as (V,a)'s producer -- a CYCLIC map
// (V,a)->(E,a)->(P,a)->(M,a)->(V,a). The D11.10 requires-input placement's producer-chain walk
// (groupForObject) recursed over that map ungrounded and overflowed the stack.
//
// The fix grounds the walk in the FINITE query structure: position segments are consumed only
// strictly below the consuming jump's entry position (the base), and anything at-or-above the entry
// resolves to the source parent (representation inputs live at the entry position by definition).
// A per-resolution visited set is a defense-in-depth invariant that FAILS LOUD (plan error), never
// loops or silently truncates.
func TestNewPathSelfNestedEntityCycleTerminates(t *testing.T) {
	h := buildH(t, hgtestdata.TypeCycleConfig())
	const schema = `
schema { query: Query }
type Query { v(id: String!): V }
type V { id: String! w: String m: M t: T score: Int }
type M { p: P }
type P { edges: [E] }
type E { node: V }
type T { s: String }
`
	// score FIRST (its walk records the root descent early), the deep self-nested chain after (its
	// repaired walk overwrites (V,a)'s producer with the deep descent -- the cycle trigger).
	const op = `query($id: String!) { v(id: $id) { score m { p { edges { node { id t { s } } } } } } }`
	o, opDoc, defDoc := buildTree(t, h, schema, op)
	res := runSearch(t, h, o)

	p, err := Lower(h, o, res, opDoc, defDoc)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}

	// The plan must gather score's @requires input (t { s }) in the SOURCE subgraph's document at
	// the entity position, and the scoped fetch must carry the Requires fragment -- the source-local
	// gather the grounded resolution lands on.
	var rootDoc string
	var requiresFrag string
	for _, f := range p.Response.RawFetches {
		sf := f.Fetch.(*resolve.SingleFetch)
		if !sf.RequiresEntityFetch && !sf.RequiresEntityBatchFetch {
			rootDoc = sf.QueryPlan.Query
			continue
		}
		if sf.QueryPlan != nil {
			for _, rep := range sf.QueryPlan.DependsOnFields {
				if rep.Kind == resolve.RepresentationKindRequires {
					requiresFrag = rep.Fragment
				}
			}
		}
	}
	if !strings.Contains(rootDoc, "t { s }") {
		t.Fatalf("root document must gather score's requires input t { s }; got %q", rootDoc)
	}
	if !strings.Contains(requiresFrag, "t { s }") {
		t.Fatalf("scoped fetch must carry the Requires fragment t { s }; got %q", requiresFrag)
	}
}
