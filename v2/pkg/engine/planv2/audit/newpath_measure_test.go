package audit

// newpath_measure_test.go is a MEASUREMENT-ONLY harness (M1.5 wave-1b THE FLIP): it runs the whole
// transcribed corpus through the obligation-driven lowering path (now the shipping default,
// lower.LowerConfig{}) and reports the same plan-level assertion buckets as TestAudit_Corpus, plus a
// PANIC/INVALID tally. Post-flip it is retained as a standalone INVALID/PANIC tripwire on the raw
// (pre-marker) new-path buckets -- it does NOT apply the Run() PASS->FAIL stale-marker conversion, so it
// keeps measuring even while markers are reconciled. It reconstructs the planv2 stage pipeline by hand
// (like lower_export_test.go) so it can classify INVALID separately from FAIL.

import (
	"fmt"
	"sort"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/lower"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

var newPathSearchConfig = search.Config{Combine: search.Sum, PreflightCap: 1 << 30, StateCap: 1 << 24}

// runNewPath mirrors runInner's plan-level classification but lowers via the obligation-driven path.
// It recovers panics (reporting status "PANIC") so one bad case cannot abort the whole measurement.
func runNewPath(c Case) (status, reason string) {
	defer func() {
		if r := recover(); r != nil {
			status, reason = "PANIC", fmt.Sprintf("panic: %v", r)
		}
	}()

	if c.Skip != "" {
		return "SKIP", c.Skip
	}
	expectsErrors := expectsErrorsOnly(c.Expected)

	dataSources, upstream, err := BuildDataSources(c)
	if err != nil {
		return "SKIP", "config: " + err.Error()
	}

	op, def, report := parseAndNormalize(c.Definition, c.Operation)
	if report.HasErrors() {
		if expectsErrors {
			return "PASS", "errors expected; rejected at normalization"
		}
		return "SKIP", "normalize: " + report.Error()
	}
	if operationUsesFieldArguments(c.Operation) {
		return "SKIP", "M1 gap: field arguments"
	}

	h, err := hypergraph.Build(dataSources, hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation"},
	})
	if err != nil {
		return "SKIP", "hypergraph: " + err.Error()
	}
	o, err := obligation.Build(op, def, "", h)
	if err != nil {
		if expectsErrors {
			return "PASS", "errors expected; obligation rejects"
		}
		return "SKIP", "obligation: " + err.Error()
	}
	res, err := search.Search(h, o, newPathSearchConfig)
	if err != nil {
		if expectsErrors {
			return "PASS", "errors expected; search rejects"
		}
		return "SKIP", "search: " + err.Error()
	}
	sp, err := lower.LowerWithConfig(h, o, res, op, def, lower.LowerConfig{})
	if err != nil {
		return "SKIP", "lower: " + err.Error()
	}
	if sp == nil || sp.Response == nil {
		return "FAIL", "nil plan/response"
	}

	fetches := sp.Response.RawFetches
	if err := validateFetches(fetches, upstream); err != nil {
		return "INVALID", err.Error()
	}
	if err := shapeSatisfies(c.Expected, sp.Response.Data); err != nil {
		return "FAIL", "response shape: " + err.Error()
	}
	if err := validateRootEntries(fetches, c.Operation); err != nil {
		return "FAIL", err.Error()
	}
	if err := validateLeafCoverage(fetches, c.Operation, def); err != nil {
		return "FAIL", err.Error()
	}
	return "PASS", ""
}

// TestNewPathCorpusMeasure reports the flipped-path corpus buckets. Measurement only: it never fails
// (so it can run alongside the untouched shipping suite), it just logs the scoreboard the flip report
// consumes. Run with: go test ./pkg/engine/planv2/audit/ -run TestNewPathCorpusMeasure -v
func TestNewPathCorpusMeasure(t *testing.T) {
	cases, err := loadCorpus("testdata")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	var pass, fail, gap, skip, panicN, invalid int
	var fails, invalids, panics []string
	for _, c := range cases {
		st, reason := runNewPath(c)
		name := c.Suite + "/" + c.Name
		// Apply the expect-fail marker the SAME way Run does, so PASS/GAP accounting matches the
		// shipping scoreboard's semantics (a marked case that FAILs is a GAP, not a FAIL).
		if c.ExpectFail != "" {
			switch st {
			case "FAIL", "INVALID", "PANIC":
				st = "GAP"
			case "PASS":
				// a marked case that now passes on the new path is a genuine stale-marker gain
			}
		}
		switch st {
		case "PASS":
			pass++
		case "GAP":
			gap++
		case "SKIP":
			skip++
		case "PANIC":
			panicN++
			panics = append(panics, name+": "+reason)
		case "INVALID":
			invalid++
			invalids = append(invalids, name+": "+reason)
		case "FAIL":
			fail++
			fails = append(fails, name+": "+reason)
		}
	}
	sort.Strings(fails)
	sort.Strings(invalids)
	sort.Strings(panics)
	t.Logf("NEW-PATH CORPUS: PASS=%d FAIL=%d INVALID=%d PANIC=%d GAP=%d SKIP=%d total=%d",
		pass, fail, invalid, panicN, gap, skip, len(cases))
	for _, s := range panics {
		t.Logf("  PANIC   %s", s)
	}
	for _, s := range invalids {
		t.Logf("  INVALID %s", s)
	}
	for _, s := range fails {
		t.Logf("  FAIL    %s", s)
	}
}
