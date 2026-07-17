package external

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
)

// TestExternalCorpusSkipsWhenUnset is the CI-safety pin: with PLANNER_V2_EXTERNAL_CORPUS unset (the
// CI default), externalCorpusDir must report "not set" -- every other test/benchmark in this package
// gates on this exact signal.
func TestExternalCorpusSkipsWhenUnset(t *testing.T) {
	t.Setenv("PLANNER_V2_EXTERNAL_CORPUS", "")
	os.Unsetenv("PLANNER_V2_EXTERNAL_CORPUS")
	if dir, ok := externalCorpusDir(); ok {
		t.Fatalf("must report unset when env absent, got (%q, true)", dir)
	}
}

// TestExternalCorpusDifferential is the real harness entry point. It is a NO-OP everywhere except a
// developer's machine with PLANNER_V2_EXTERNAL_CORPUS pointed at a private, local corpus directory
// (see v2/docs/planner-v2/EXTERNAL_CORPUS.md) -- CI never sets the variable, so this always SKIPs in
// CI. Nothing here reads, logs, or embeds corpus content beyond the in-process run; the report is
// written inside the corpus directory, never the repo.
func TestExternalCorpusDifferential(t *testing.T) {
	dir, ok := externalCorpusDir()
	if !ok {
		t.Skip("PLANNER_V2_EXTERNAL_CORPUS unset; skipping external corpus (leak rule)")
	}

	graphs, err := LoadGraphs(dir)
	if err != nil {
		t.Fatalf("load external corpus: %v", err)
	}
	if len(graphs) == 0 {
		t.Fatal("PLANNER_V2_EXTERNAL_CORPUS set but no graphs discovered -- check the directory convention in EXTERNAL_CORPUS.md")
	}

	var all []CaseResult
	for _, g := range graphs {
		results := RunGraph(g)
		for _, r := range results {
			switch r.Status {
			case StatusPlanV2Error:
				// planv2 MUST plan every case -- never allow-listed, always a hard failure.
				t.Errorf("planv2 error: %s/%s: %s", r.Graph, r.Operation, r.Detail)
			case StatusUnexplained:
				// Divergence policy: silent divergence is prohibited. Either fix planv2 or register
				// the (schema, operation) pair in differential.KnownDivergences with an adjudication.
				t.Errorf("unexplained divergence: %s/%s: %s", r.Graph, r.Operation, r.Detail)
			}
		}
		all = append(all, results...)
	}

	report := NewReport(all)
	dst := reportDir(dir) // INSIDE the corpus directory -- never the repo (privacy rule)
	if err := WriteReport(dst, report); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("external corpus report written to %s (counts: %v)", dst, report.Counts)
}

// BenchmarkExternalCorpusPlanning benchmarks planv2 planning time per operation against the external
// corpus. Like the differential test, it is a NO-OP without PLANNER_V2_EXTERNAL_CORPUS set. The
// planner is constructed once per case (compile-time cost excluded); each b.N iteration re-parses
// and re-normalizes the operation (both planners mutate their AST inputs -- see runCase) so the
// reported time is parse+normalize+plan, not plan-time alone; that split is out of scope here.
func BenchmarkExternalCorpusPlanning(b *testing.B) {
	dir, ok := externalCorpusDir()
	if !ok {
		b.Skip("PLANNER_V2_EXTERNAL_CORPUS unset")
	}
	graphs, err := LoadGraphs(dir)
	if err != nil {
		b.Fatalf("load external corpus: %v", err)
	}
	for _, g := range graphs {
		for _, opPath := range g.OperationPaths {
			b.Run(g.Name+"/"+filepath.Base(opPath), func(b *testing.B) {
				benchmarkOneOperation(b, g, opPath)
			})
		}
	}
}

func benchmarkOneOperation(b *testing.B, g Graph, opPath string) {
	opBytes, err := os.ReadFile(opPath)
	if err != nil {
		b.Fatalf("read operation: %v", err)
	}
	opText := string(opBytes)

	auditCase := audit.Case{Subgraphs: g.Subgraphs, Definition: g.SupergraphSDL, Operation: opText}
	dataSources, _, err := audit.BuildDataSources(auditCase)
	if err != nil {
		b.Fatalf("build data sources: %v", err)
	}
	p, err := planv2.NewPlanner(plan.Configuration{DataSources: dataSources, DisableResolveFieldPositions: true})
	if err != nil {
		b.Fatalf("planv2.NewPlanner: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, def, report := parseAndNormalize(g.SupergraphSDL, opText)
		if report.HasErrors() {
			b.Fatalf("normalize: %s", report.Error())
		}
		if pl := p.Plan(op, def, "", report); pl == nil && report.HasErrors() {
			b.Fatalf("plan: %s", report.Error())
		}
	}
}

// TestLoadGraphsRefusesInRepoCorpus pins the RUNTIME privacy guard both ways:
//
//  1. A corpus directory inside a git repository -- simulated with a temp dir carrying a .git marker
//     and a corpus beneath it, exactly the layout the guard exists to refuse -- must be rejected by
//     LoadGraphs with an error naming the rule, before anything is read or written.
//  2. This repository's own committed fixture path (testdata/synthetic) is itself in-repo and must
//     be refused for the same reason -- pointing PLANNER_V2_EXTERNAL_CORPUS anywhere under this repo
//     can never produce a report file inside the tree.
//
// The guard's OUTSIDE-repo happy path is covered by TestSyntheticCorpusPipeline, which loads the
// same fixture from a plain temp directory successfully.
func TestLoadGraphsRefusesInRepoCorpus(t *testing.T) {
	// (1) fake repo layout: <tmp>/.git marker + <tmp>/corpus/...
	fakeRepo := t.TempDir()
	if err := os.Mkdir(filepath.Join(fakeRepo, ".git"), 0o755); err != nil {
		t.Fatalf("create .git marker: %v", err)
	}
	corpus := filepath.Join(fakeRepo, "corpus")
	if err := os.MkdirAll(corpus, 0o755); err != nil {
		t.Fatalf("create corpus dir: %v", err)
	}
	if _, err := LoadGraphs(corpus); err == nil {
		t.Fatal("LoadGraphs must refuse a corpus inside a git repository (fake repo layout)")
	} else if !strings.Contains(err.Error(), "INSIDE the git repository") {
		t.Errorf("refusal error must name the rule, got: %v", err)
	}

	// (2) this repository's own tree.
	if _, err := LoadGraphs("testdata/synthetic"); err == nil {
		t.Fatal("LoadGraphs must refuse a corpus path inside this repository")
	} else if !strings.Contains(err.Error(), "INSIDE the git repository") {
		t.Errorf("refusal error must name the rule, got: %v", err)
	}
}

// copySyntheticCorpus copies the committed testdata/synthetic fixture into a fresh temp directory
// (outside any git repository) so LoadGraphs' privacy guard admits it.
func copySyntheticCorpus(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir("testdata/synthetic", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("testdata/synthetic", path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy synthetic corpus: %v", err)
	}
	return dst
}

// TestSyntheticCorpusPipeline proves the loader/runner/report pipeline works end-to-end against the
// tiny two-subgraph toy corpus committed to testdata/synthetic -- a toy variation on the classic
// public federation demo shape (accounts/products: User/Product), containing no external data. It
// always runs (unlike the env-gated tests above): the pipeline smoke test the task requires
// independent of any real corpus being available. Because LoadGraphs refuses any corpus inside a git
// repository (the privacy guard), the committed fixture is first copied to a temp directory -- making
// this test double as the guard's OUTSIDE-repo happy path.
//
// The smoke operation is `me { id name reviews { ... } }` -- a single root plus one entity jump
// (User->Product). It deliberately AVOIDS placing the same object type at two response positions
// (the earlier `me { reviews { ... } } topProducts { ... }` shape put Product under both `reviews` and
// `topProducts`), which the M1.5 leaf-coverage oracle (audit assertion 6) now correctly reports as
// the sibling / path-conflation defect (planv2 drops `topProducts`). That defect is witnessed by the
// audit corpus and the `sibling-conflation/case-buyer-seller` fixture, not by this pipeline smoke
// test, whose job is the clean-PASS happy path. The oracle IS wired into this harness (RunGraph
// surfaces AuditStatus/AuditReason as a planv2-side check, not a comparison), so a conflating
// synthetic fixture would be caught here too.
func TestSyntheticCorpusPipeline(t *testing.T) {
	graphs, err := LoadGraphs(copySyntheticCorpus(t))
	if err != nil {
		t.Fatalf("load synthetic corpus: %v", err)
	}
	if len(graphs) != 1 {
		t.Fatalf("expected exactly 1 synthetic graph, got %d", len(graphs))
	}
	g := graphs[0]
	if g.Name != "toy" {
		t.Errorf("graph name = %q, want %q", g.Name, "toy")
	}
	if len(g.Subgraphs) != 2 {
		t.Errorf("expected 2 subgraphs, got %d", len(g.Subgraphs))
	}
	if len(g.OperationPaths) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(g.OperationPaths))
	}

	results := RunGraph(g)
	if len(results) != 1 {
		t.Fatalf("expected 1 case result, got %d", len(results))
	}
	r := results[0]
	t.Logf("%s %s/%s (audit=%s %s): %s", r.Status, r.Graph, r.Operation, r.AuditStatus, r.AuditReason, r.Detail)

	if r.Status == StatusPlanV2Error {
		t.Errorf("planv2 error on synthetic fixture: %s", r.Detail)
	}
	if r.Status == StatusUnexplained {
		t.Errorf("unexplained divergence on synthetic fixture: %s", r.Detail)
	}
	if r.AuditStatus != "PASS" {
		t.Errorf("audit plan-level assertions did not PASS on synthetic fixture: %s (%s)", r.AuditStatus, r.AuditReason)
	}

	report := NewReport(results)
	if report.Counts[r.Status] != 1 {
		t.Errorf("report counts = %v, want a single %s entry", report.Counts, r.Status)
	}

	// The report MUST be writable outside the repo (os.TempDir()-backed via t.TempDir()) -- this is
	// the same constraint the real external-corpus test observes when it calls reportDir(corpusDir).
	dst := t.TempDir()
	if err := WriteReport(dst, report); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "summary.json")); err != nil {
		t.Errorf("summary.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "detail.txt")); err != nil {
		t.Errorf("detail.txt not written: %v", err)
	}
}
