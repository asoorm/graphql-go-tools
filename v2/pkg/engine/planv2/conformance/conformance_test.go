package conformance

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// seedsUnderTest returns the base seed set: DefaultSeeds, extended via the CONFORMANCE_SEEDS
// knob (comma-separated uint64s or a single count N meaning seeds 1..N) for deeper sweeps.
func seedsUnderTest(t testing.TB) []uint64 {
	v := os.Getenv("CONFORMANCE_SEEDS")
	if v == "" {
		return DefaultSeeds
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 && !strings.Contains(v, ",") {
		seeds := make([]uint64, n)
		for i := range seeds {
			seeds[i] = uint64(i + 1)
		}
		return seeds
	}
	var seeds []uint64
	for _, part := range strings.Split(v, ",") {
		n, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64)
		if err != nil {
			t.Fatalf("CONFORMANCE_SEEDS: bad seed %q: %v", part, err)
		}
		seeds = append(seeds, n)
	}
	return seeds
}

// TestConformance_Suite runs the full generated suite against planv2. Outcomes reconcile against
// the triage register exactly like the audit corpus's expect-fail discipline: unregistered
// failures FAIL; registered cases must still fail (a registered case that passes demands the
// entry's removal). The headline logs the honest split.
func TestConformance_Suite(t *testing.T) {
	cases := GenerateAll(seedsUnderTest(t))
	if len(cases) == 0 {
		t.Fatal("generator produced no cases")
	}

	var pass, gap, fail int
	perFamily := map[string]*[3]int{} // pass, gap, fail
	for _, c := range cases {
		out := Run(c)
		agg := perFamily[c.Family]
		if agg == nil {
			agg = &[3]int{}
			perFamily[c.Family] = agg
		}
		reg, registered := TriageFor(c)
		switch {
		case out.Status == "PASS" && !registered:
			pass++
			agg[0]++
		case out.Status == "PASS" && registered:
			fail++
			agg[2]++
			t.Errorf("REGISTERED case now PASSES -- remove its triage entry (%s: %s)", c.ID, reg.Reason)
		case registered:
			gap++
			agg[1]++
			t.Logf("GAP  [%s] %s -- %s (registered: %s)", reg.Class, c.ID, out.Reason, reg.Reason)
		default:
			fail++
			agg[2]++
			t.Errorf("FAIL %s -- %s", c.ID, out.Reason)
		}
	}

	// Stale register entries (scenarios the generator no longer emits) are drift -- fail loudly.
	emitted := map[string]bool{}
	for _, c := range cases {
		emitted[scenarioKey(c.ID, c.Seed)] = true
	}
	for _, key := range TriageKeys() {
		if !emitted[key] {
			t.Errorf("triage register entry %q matches no generated scenario (stale key?)", key)
		}
	}

	fams := make([]string, 0, len(perFamily))
	for f := range perFamily {
		fams = append(fams, f)
	}
	sort.Strings(fams)
	var b strings.Builder
	for _, f := range fams {
		a := perFamily[f]
		fmt.Fprintf(&b, "  %-24s pass=%d gap=%d fail=%d\n", f, a[0], a[1], a[2])
	}
	t.Logf("CONFORMANCE HEADLINE: %d cases -- %d pass, %d registered gaps, %d fail\n%s",
		len(cases), pass, gap, fail, b.String())
}

// TestConformance_GeneratorDeterminism pins the generator's own contract: two independent
// expansions of the same seed set are byte-identical (IDs, SDLs, operations, expectations) --
// no time, no global randomness, no ordering sensitivity.
func TestConformance_GeneratorDeterminism(t *testing.T) {
	render := func() string {
		var b strings.Builder
		for _, c := range GenerateAll(DefaultSeeds) {
			b.WriteString(c.ID + "\n" + c.Case.Definition + "\n" + c.Case.Operation + "\n" + string(c.Case.Expected) + "\n")
			for _, sg := range c.Case.Subgraphs {
				b.WriteString(sg.Name + "\n" + sg.SDL + "\n")
			}
		}
		return b.String()
	}
	if render() != render() {
		t.Fatal("generator is nondeterministic: two expansions of the same seeds differ")
	}
}

// TestConformance_CaseBudget keeps the default-seed corpus inside the CI budget's case-count
// envelope: big enough to mean something, small enough to stay well under the runtime gate.
// Move the bounds DELIBERATELY when families grow.
func TestConformance_CaseBudget(t *testing.T) {
	n := len(GenerateAll(DefaultSeeds))
	const min, max = 80, 600
	if n < min || n > max {
		t.Fatalf("default-seed corpus has %d cases, outside the deliberate envelope [%d,%d]", n, min, max)
	}
	t.Logf("default-seed corpus: %d generated cases", n)
}
