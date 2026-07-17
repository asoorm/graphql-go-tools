package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// loadCorpus reads every transcribed suite under dir. Layout (per suite, subgraphs shared across the
// suite's cases -- mirroring the audit's own per-suite subgraph set):
//
//	testdata/<suite>/
//	  subgraphs/<name>.graphql   one federation subgraph SDL per file
//	  supergraph.graphql         client-facing composed schema (the Plan definition)
//	  skip.txt                   optional: whole-suite SKIP reason (a feature planv2 can't yet plan)
//	  <case>/operation.graphql   one operation per case directory
//	  <case>/expected.json       the audit's expected response for that case
//	  <case>/skip.txt            optional: per-case SKIP reason
//	  <case>/expect-fail.txt     optional: KNOWN planv2 defect (case runs; FAIL reports as GAP)
//	  <case>/expected-error-class.txt
//	                             REQUIRED on error-expectation cases (`errors: true` with data
//	                             null/absent; also allowed, as `plan-success`, on partial-response
//	                             errors:true cases): pins the plan-level rejection reason class --
//	                             first line only; see the errorClass* vocabulary in runner.go
func loadCorpus(dir string) ([]Case, error) {
	suiteEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var cases []Case
	for _, se := range suiteEntries {
		if !se.IsDir() {
			continue
		}
		suiteDir := filepath.Join(dir, se.Name())
		suiteCases, err := loadSuite(se.Name(), suiteDir)
		if err != nil {
			return nil, fmt.Errorf("suite %s: %w", se.Name(), err)
		}
		cases = append(cases, suiteCases...)
	}
	sort.Slice(cases, func(i, j int) bool {
		if cases[i].Suite != cases[j].Suite {
			return cases[i].Suite < cases[j].Suite
		}
		return cases[i].Name < cases[j].Name
	})
	return cases, nil
}

func loadSuite(suite, dir string) ([]Case, error) {
	subgraphs, err := loadSubgraphs(filepath.Join(dir, "subgraphs"))
	if err != nil {
		return nil, err
	}
	definition, err := readFile(filepath.Join(dir, "supergraph.graphql"))
	if err != nil {
		return nil, err
	}
	suiteSkip := optionalFile(filepath.Join(dir, "skip.txt"))

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var cases []Case
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "subgraphs" {
			continue
		}
		caseDir := filepath.Join(dir, e.Name())
		op, err := readFile(filepath.Join(caseDir, "operation.graphql"))
		if err != nil {
			return nil, err
		}
		var expected json.RawMessage
		if raw := optionalFile(filepath.Join(caseDir, "expected.json")); raw != "" {
			expected = json.RawMessage(raw)
		}
		skip := suiteSkip
		if s := optionalFile(filepath.Join(caseDir, "skip.txt")); s != "" {
			skip = s
		}
		cases = append(cases, Case{
			Name:       e.Name(),
			Suite:      suite,
			Subgraphs:  subgraphs,
			Definition: definition,
			Operation:  op,
			Expected:   expected,
			Skip:       skip,
			ExpectFail: optionalFile(filepath.Join(caseDir, "expect-fail.txt")),
			ErrorClass: firstLine(optionalFile(filepath.Join(caseDir, "expected-error-class.txt"))),
		})
	}
	return cases, nil
}

func loadSubgraphs(dir string) ([]Subgraph, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Subgraph
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".graphql") {
			continue
		}
		sdl, err := readFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Subgraph{
			Name: strings.TrimSuffix(e.Name(), ".graphql"),
			SDL:  sdl,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func optionalFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// firstLine returns s's first line, trimmed -- pin files carry the machine-read token on line one
// and free commentary below.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
