package conformance

// export_test.go -- the external-drivability leg (the bench scale_export_test.go pattern): the
// generated corpus re-emits as on-disk per-case artifacts an EXTERNAL planner harness can
// consume -- per-subgraph Fed2 SDLs, the composed client schema, the operation, and a case
// manifest. The external-planner RUNNER stays out of this tree (internal notes); only the
// export is committed.
//
// Modes:
//   - default (`go test`): exports one seed's corpus to t.TempDir and asserts every emitted SDL
//     re-parses and the manifest is complete -- a fast correctness gate, no external tools.
//   - export (CONFORMANCE_EXPORT_DIR=<path>): writes the full default-seed corpus (or
//     CONFORMANCE_SEEDS) into <path>/<case-id-slug>/ for external legs.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// exportManifest is the per-case machine-readable descriptor.
type exportManifest struct {
	ID              string   `json:"id"`
	Family          string   `json:"family"`
	Propositions    []string `json:"propositions"`
	Seed            uint64   `json:"seed"`
	Operation       string   `json:"operation_file"`
	Supergraph      string   `json:"supergraph_file"`
	Subgraphs       []string `json:"subgraph_files"`
	ExpectPlanError bool     `json:"expect_plan_error"`
	Subscription    bool     `json:"subscription"`
	Defer           bool     `json:"defer"`
}

// slugify turns a case ID into a filesystem-safe directory name.
func slugify(id string) string {
	return strings.NewReplacer("/", "__", " ", "-").Replace(id)
}

func exportCase(t testing.TB, dir string, c GeneratedCase) {
	t.Helper()
	caseDir := filepath.Join(dir, slugify(c.ID))
	if err := os.MkdirAll(filepath.Join(caseDir, "subgraphs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(caseDir, rel), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	m := exportManifest{
		ID: c.ID, Family: c.Family, Propositions: c.Propositions, Seed: c.Seed,
		Operation: "operation.graphql", Supergraph: "supergraph.graphql",
		ExpectPlanError: c.ExpectPlanError, Subscription: c.Subscription, Defer: c.Defer,
	}
	write("operation.graphql", c.Case.Operation+"\n")
	write("supergraph.graphql", c.Case.Definition)
	for _, sg := range c.Case.Subgraphs {
		rel := filepath.Join("subgraphs", sg.Name+".graphql")
		m.Subgraphs = append(m.Subgraphs, rel)
		write(rel, sg.SDL)
	}
	if len(c.Case.Expected) > 0 {
		write("expected.json", string(c.Case.Expected))
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	write("case.json", string(mb)+"\n")
}

// TestExportConformanceCorpus is the export leg's correctness gate + on-demand artifact producer.
func TestExportConformanceCorpus(t *testing.T) {
	if dir := os.Getenv("CONFORMANCE_EXPORT_DIR"); dir != "" {
		cases := GenerateAll(seedsUnderTest(t))
		for _, c := range cases {
			exportCase(t, dir, c)
		}
		t.Logf("exported %d conformance cases to %s", len(cases), dir)
		return
	}

	dir := t.TempDir()
	cases := GenerateAll([]uint64{1})
	for _, c := range cases {
		exportCase(t, dir, c)
	}

	// Every emitted SDL must re-parse; every case dir must carry a complete manifest.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read export dir: %v", err)
	}
	if len(entries) != len(cases) {
		t.Fatalf("exported %d case dirs, want %d", len(entries), len(cases))
	}
	for _, e := range entries {
		caseDir := filepath.Join(dir, e.Name())
		mb, err := os.ReadFile(filepath.Join(caseDir, "case.json"))
		if err != nil {
			t.Fatalf("%s: missing case.json: %v", e.Name(), err)
		}
		var m exportManifest
		if err := json.Unmarshal(mb, &m); err != nil {
			t.Fatalf("%s: bad manifest: %v", e.Name(), err)
		}
		if m.ID == "" || len(m.Subgraphs) == 0 || len(m.Propositions) == 0 {
			t.Errorf("%s: incomplete manifest: %+v", e.Name(), m)
		}
		for _, rel := range append([]string{m.Supergraph, m.Operation}, m.Subgraphs...) {
			b, err := os.ReadFile(filepath.Join(caseDir, rel))
			if err != nil {
				t.Fatalf("%s: missing %s: %v", e.Name(), rel, err)
			}
			if strings.HasSuffix(rel, ".graphql") && rel != m.Operation {
				doc := unsafeparser.ParseGraphqlDocumentString(string(b))
				if len(doc.RootNodes) == 0 {
					t.Errorf("%s: %s parsed to zero root nodes", e.Name(), rel)
				}
			}
		}
	}
}
