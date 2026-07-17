package lower

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// renderPlanCanonical renders a plan's fetch side into a deterministic multi-line string for golden
// pinning: one sorted line per fetch (subgraph, response path, deps, document).
func renderPlanCanonical(p *plan.SynchronousResponsePlan) string {
	var lines []string
	for _, f := range p.Response.RawFetches {
		sf := f.Fetch.(*resolve.SingleFetch)
		lines = append(lines, fmt.Sprintf("[%s] path=%q deps=%v doc=%s",
			string(sf.DataSourceIdentifier), f.ResponsePath, sf.DependsOnFetchIDs, extractDoc(sf)))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func extractDoc(sf *resolve.SingleFetch) string {
	if sf.QueryPlan != nil {
		return sf.QueryPlan.Query
	}
	return sf.Input
}

// TestOldPathEntityJumpUnchanged pins the LEGACY node-keyed escape hatch's Section 7.2 output verbatim. Post
// THE FLIP the shipping default is the obligation-driven path; the escape hatch
// (LowerConfig{LegacyNodeKeyedGrouping: true}) is retained one release cycle and this golden is the
// regression guard that it stays byte-identical to the pre-flip shipping output.
func TestOldPathEntityJumpUnchanged(t *testing.T) {
	h, o, res, op, def := planEntityJump(t)
	p, err := lowerWithConfig(h, o, res, op, def, LowerConfig{LegacyNodeKeyedGrouping: true}, nil, InfoConfig{}) // escape hatch
	if err != nil {
		t.Fatal(err)
	}
	const want = `[A] path="" deps=[] doc=query { product { id organization { id } dimensions { length width height } } }
[B] path="product" deps=[0] doc=query($representations: [_Any!]!) { _entities(representations: $representations) { ... on Product { shippingEstimate } } }`
	if got := renderPlanCanonical(p); got != want {
		t.Fatalf("shipping path Section 7.2 output drifted:\n got:\n%s\nwant:\n%s", got, want)
	}
}
