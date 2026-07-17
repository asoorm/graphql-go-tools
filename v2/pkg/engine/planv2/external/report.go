package external

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Report aggregates every CaseResult from a corpus run: counts by Status (the five buckets in
// runner.go) plus the full per-case detail. This is the local scoreboard -- see WriteReport for
// where it may be written (never the repo; see the privacy rules in doc.go and
// v2/docs/planner-v2/EXTERNAL_CORPUS.md).
type Report struct {
	Generated time.Time
	Counts    map[string]int
	Cases     []CaseResult
}

// NewReport aggregates results into a Report.
func NewReport(results []CaseResult) Report {
	r := Report{Generated: time.Now().UTC(), Counts: map[string]int{}, Cases: results}
	for _, c := range results {
		r.Counts[c.Status]++
	}
	return r
}

// WriteReport writes the local scoreboard to dir: summary.json (generation time + counts by
// Status) and detail.txt (one line per case, including its AuditStatus/AuditReason).
//
// dir MUST be a path INSIDE the external corpus directory or under os.TempDir() -- NEVER a path
// inside this repository (the privacy rule in doc.go). This function has no notion of "the repo" and
// does not itself enforce that; callers (this package's tests) are responsible for passing a
// compliant dir, which is exactly what they do: reportDir(corpusDir) below and t.TempDir() in tests.
func WriteReport(dir string, r Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report dir %q: %w", dir, err)
	}

	summary, err := json.MarshalIndent(struct {
		Generated time.Time      `json:"generated"`
		Counts    map[string]int `json:"counts"`
		Total     int            `json:"total"`
	}{r.Generated, r.Counts, len(r.Cases)}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), summary, 0o644); err != nil {
		return fmt.Errorf("write summary.json: %w", err)
	}

	cases := append([]CaseResult(nil), r.Cases...)
	sort.Slice(cases, func(i, j int) bool {
		if cases[i].Graph != cases[j].Graph {
			return cases[i].Graph < cases[j].Graph
		}
		return cases[i].Operation < cases[j].Operation
	})
	var detail strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&detail, "%-20s %s/%s\taudit=%s (%s)\t%s\n",
			c.Status, c.Graph, c.Operation, c.AuditStatus, c.AuditReason, c.Detail)
	}
	if err := os.WriteFile(filepath.Join(dir, "detail.txt"), []byte(detail.String()), 0o644); err != nil {
		return fmt.Errorf("write detail.txt: %w", err)
	}
	return nil
}

// reportDir picks the local scoreboard destination for a run against corpusDir: a hidden
// subdirectory INSIDE the corpus directory, timestamped so repeat runs don't clobber each other.
// Always inside the corpus dir (never the repo) per the privacy rule.
func reportDir(corpusDir string) string {
	return filepath.Join(corpusDir, ".external-corpus-report", time.Now().UTC().Format("20060102T150405Z"))
}
