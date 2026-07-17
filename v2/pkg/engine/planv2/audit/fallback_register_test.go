package audit

// fallback_register_test.go is the committed drift instrument for the D10 typed-loud fall-back
// (FORMAL_SPEC D10 amendment -- typed-loud fall-back, two-tier semantics; DIVERGENCES.md D10
// register entry). A fallback event is a SEARCH-LEVEL observation -- post-flip, the
// obligation-driven lowering can emit a correct plan over a path-inconsistent walk -- so plan-level
// correctness stays governed by the runner's assertions 1-7, and this test holds the search-level
// line in both directions:
//
//   - TIER 2 (frozen set): the EXACT set of PASS cases with any search-level fallback equals the
//     three adjudicated benign witnesses below. A NEW passing case that fires the fallback fails
//     loud (it must be adjudicated: either its plan is genuinely correct and the set grows
//     deliberately, or the model gap is real and the case belongs in GAP). A frozen witness that
//     STOPS firing also fails, so an improvement is recorded deliberately by shrinking the set in
//     the same commit.
//   - PINNED COUNT: the corpus-wide number of DISTINCT fallback-using goals (the primary metric;
//     a goal that falls back at both sites -- root-pin and scoped-walk -- counts once) is pinned.
//     Event totals are logged informationally, not asserted.
//
// Retirement gate (FORMAL_SPEC D10 amendment): when this pinned count AND the customer-corpus
// fallback count are both zero, the fall-back is deleted in that same commit and unroutable goals
// fail loud -- at which point this file is deleted with it.

import (
	"fmt"
	"sort"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// pinnedFallbackGoals is the measured audit-corpus count of distinct fallback-using goals.
// History: 28 at the M2 D10 disposition landing (39 events across 18 cases -- 15 GAP, 3 PASS);
// 14 after the M2 class-D wave's D6pp dead-member exemption (21 events across 11 cases -- 8 GAP,
// 3 PASS): a dead member is no longer a goal at all, so its fallback-served route disappears --
// the designed shrinkage direction of the retirement gate. 12 after the M2 class-C wave's D7p
// interface-node jump heads (17 events across 9 cases -- 7 GAP, 2 PASS): the interface-level jump
// gives the two interface-object interface-field goals (`simple-interface-object/case-01`
// NodeWithName.name -- GAP->PASS -- and the frozen witness `interface-object-with-requires/case-01`)
// path-consistent routes, so their fallbacks disappear. Update it ONLY with a deliberate
// re-measurement in the same commit as the change that moves it, and mirror the new figure in
// the DIVERGENCES.md D10 register entry.
// 6 after the M2 class-C wave's D3pppp member expansion + D10 chain-layered consistent trace (11
// events across 6 cases -- 4 GAP, 2 PASS): the abstract-types provider-split family and the
// member-scoped leaf-coverage residual now route path-consistently, so their fallbacks disappear;
// what remains is the genuine foreign-root class A/B (non-resolvable-interface-object/case-02,
// requires-interface/case-02, requires-with-fragments/case-02,03) plus the two frozen witnesses.
// 3 after the M2 requires-chain wave's D7pp requires-scoped resolution (5 events across 3 cases --
// 1 GAP, 2 PASS): `requires-interface/case-02` and `requires-with-fragments/case-02,03` gained
// path-consistent routes (per-requires scoped jumps replace the all-or-nothing ride-along, so the
// requires field's jump exists and the D10 fall-back stops firing) -- all three flipped GAP->PASS.
// 2 after the M2 D10-narrow mini-wave's provable-non-resolvability narrowing (FORMAL_SPEC D10
// amendment): `non-resolvable-interface-object/case-02` -- the expected-errors case the root-pin
// fall-back rescued into a wrong plan -- now fails loud with ErrNoValidPlan (the guard proves every
// candidate sits behind @key(resolvable: false) with root-only entry), which the runner classifies
// PASS (errors expected). No fall-back fires for it, so its goal leaves the register; what remains
// is exactly the two frozen benign witnesses. Retirement stays gated on BOTH this pin and the
// customer-corpus fallback count reaching zero (the narrowing is conservative precisely so the
// measured customer reliance -- 72 ops / 547 events at 70e1e442 -- is untouched).
const pinnedFallbackGoals = 2

// frozenPassFallbackCases are the adjudicated benign witnesses: their SEARCH-level route
// falls back (foreign-root kappa walk), but their EMITTED plans are correct -- verified by assertions
// 1-7 and by inspection (fetch documents enter the requested roots / entity fetches). They are the
// living proof that a fallback event does not imply a plan defect. Shrink this set (same commit)
// when a model-gap close makes a witness path-consistent; grow it only with an explicit
// adjudication that the new case's plan is correct despite the fallback.
// History: `interface-object-with-requires/case-01` left the set at the M2 class-C wave's D7p --
// its NodeWithName.name route is now the path-consistent interface-node jump (no fallback fires).
var frozenPassFallbackCases = map[string]bool{
	"provides-on-interface/case-02":       true, // scoped-walk Cat.age via Query.book; plan serves age via a media-rooted entity fetch
	"union-interface-distributed/case-01": true, // root-pin+scoped-walk Node.id via Query.node; plan enters products correctly
}

func TestAudit_D10FallbackRegister(t *testing.T) {
	cases, err := loadCorpus("testdata")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("empty corpus")
	}

	totalGoals, totalEvents := 0, 0
	firingPass := map[string]bool{}
	var firingLines []string
	for _, c := range cases {
		out := Run(c)
		if len(out.RouteFallbacks) == 0 {
			continue
		}
		goals := map[obligation.GoalID]bool{} // goal ids are per-operation; dedupe within the case only
		for _, f := range out.RouteFallbacks {
			goals[f.Goal] = true
		}
		totalGoals += len(goals)
		totalEvents += len(out.RouteFallbacks)
		name := c.Suite + "/" + c.Name
		if out.Status == "PASS" {
			firingPass[name] = true
		}
		firingLines = append(firingLines,
			fmt.Sprintf("%-4s %s goals=%d events=%d", out.Status, name, len(goals), len(out.RouteFallbacks)))
	}
	sort.Strings(firingLines)
	for _, l := range firingLines {
		t.Log(l)
	}
	t.Logf("D10 fallback register: %d distinct goals (pinned=%d), %d events (informational), %d PASS cases firing (frozen=%d)",
		totalGoals, pinnedFallbackGoals, totalEvents, len(firingPass), len(frozenPassFallbackCases))

	// TIER 2: exact set equality with the frozen witnesses, loud in both directions.
	for name := range firingPass {
		if !frozenPassFallbackCases[name] {
			t.Errorf("NEW PASS case fires the D10 fallback: %s -- adjudicate it (correct plan -> grow the frozen set deliberately; real gap -> expect-fail the case), never let it pass silently", name)
		}
	}
	for name := range frozenPassFallbackCases {
		if !firingPass[name] {
			t.Errorf("frozen witness %s no longer fires the fallback on a PASS -- record the improvement by removing it from the frozen set in this same commit", name)
		}
	}

	// PINNED COUNT: distinct fallback-using goals across the corpus.
	if totalGoals != pinnedFallbackGoals {
		t.Errorf("audit-corpus distinct fallback-using goals drifted: pinned %d, measured %d -- re-measure deliberately, update the pin AND the DIVERGENCES.md D10 register entry in this same commit",
			pinnedFallbackGoals, totalGoals)
	}
}
