package cost

// cost_test.go unit-pins responsePath -- the assembly that must reproduce v1's CostVisitor fieldPath
// key ("Query.hero.name") exactly, so the single-route datasource attribution lines up with the tree
// v1's walker builds. The end-to-end parity (EstimateCost == v1) lives in the differential package;
// here we pin the pure path logic against hand-built obligations, including the refinement-skip rule.

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

func TestResponsePath(t *testing.T) {
	// hero(Field) -> ... on Hero (Refine, no response key) -> name(Field)
	obs := []obligation.Obligation{
		{ID: 0, Kind: obligation.Field, Parent: obligation.NoParent, Type: "Query", Field: "hero", RespKey: "hero"},
		{ID: 1, Kind: obligation.Refine, Parent: 0, Type: "Hero", Concrete: "Hero", RespKey: ""},
		{ID: 2, Kind: obligation.Field, Parent: 1, Type: "Hero", Field: "name", RespKey: "name"},
		// An aliased sibling under the root field, to pin that the response KEY (not the field name)
		// is used: `rank: level`.
		{ID: 3, Kind: obligation.Field, Parent: 0, Type: "Hero", Field: "level", RespKey: "rank"},
	}
	byID := make(map[obligation.ObID]obligation.Obligation, len(obs))
	for _, ob := range obs {
		byID[ob.ID] = ob
	}

	cases := []struct {
		id   obligation.ObID
		want string
	}{
		{0, "Query.hero"},
		{2, "Query.hero.name"}, // the Refine ancestor contributes no segment
		{3, "Query.hero.rank"}, // response key wins over the schema field name
	}
	for _, tc := range cases {
		if got := responsePath(byID, byID[tc.id], "Query"); got != tc.want {
			t.Fatalf("responsePath(ob %d) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
