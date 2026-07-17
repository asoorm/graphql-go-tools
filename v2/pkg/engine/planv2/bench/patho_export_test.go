package bench

// patho_export_test.go is the pathological-shape benchmark's EXPORT leg (the patho analog of
// scale_export_test.go): it re-emits the interface-nesting supergraph (patho_gen_test.go) as
// self-describing Apollo-Federation-2 SDL + operation files + manifest.json, so external
// planners plan the IDENTICAL graph and operations the
// in-process planv2/v1 legs do.
//
// FED2 DIRECTIVE MAPPING (per subgraph s, re-derived from the same pathoOwner/pathoNeedsStubs math
// the generator uses):
//   - owned impl (d,j): @key(fields:"id"); `common`/`child` @shareable (resolved by all R owners);
//     the exclusive `only{d}_{j}` @shareable too when R>1 (all R owners resolve it).
//   - stub impl: @key(fields:"id", resolvable:false) -- may be referenced here, must be resolved by
//     an owner -- with the interface-mandated fields (`common`, `child`) declared @external, the
//     Fed2 idiom for satisfying an interface implementation without resolving the fields
//     (EXTERNAL_UNUSED exempts interface-satisfaction uses).
//   - interfaces carry no federation directives; all D are declared in every subgraph.
//
// Modes (mirroring TestExportScaleGraph):
//   - default (`go test`): exports a small config to t.TempDir and asserts every emitted SDL
//     re-parses and carries the expected directives -- race-safe correctness gate, no external tools.
//   - export (PATHO_EXPORT_DIR set): writes the graph for PATHO_D/PATHO_M/PATHO_S/PATHO_R (defaults
//     3/5/10/2) into that directory for the external-router harness to compose + serve:
//
//	PATHO_EXPORT_DIR=/tmp/patho/M5_D3_S10 PATHO_M=5 PATHO_D=3 PATHO_S=10 \
//	  go test ./pkg/engine/planv2/bench/ -run TestPathoExport -count=1

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// pathoFed2LinkHeader imports the directive subset the pathological shape uses.
const pathoFed2LinkHeader = `extend schema @link(url: "https://specs.apollo.dev/federation/v2.5", import: ["@key", "@external", "@shareable"])` + "\n\n"

// fed2PathoSubgraphSDL re-emits subgraph s of the pathological shape as self-describing Fed2 SDL.
func fed2PathoSubgraphSDL(s int, p pathoGenParams) string {
	var b strings.Builder
	b.WriteString(pathoFed2LinkHeader)

	ownsAnything := false
	for d := 0; d < p.Levels && !ownsAnything; d++ {
		ownsAnything = p.pathoOwnsAnyAtLevel(s, d)
	}
	switch {
	case s == 0:
		b.WriteString("type Query {\n  root: PIface0\n}\n\n")
	case !ownsAnything:
		fmt.Fprintf(&b, "type Query {\n  %s: Boolean\n}\n\n", pathoPadField(s))
	}

	for d := 0; d < p.Levels; d++ {
		fmt.Fprintf(&b, "interface %s {\n  common: String\n", pathoIfaceName(d))
		if pathoHasChild(d, p.Levels) {
			fmt.Fprintf(&b, "  child: %s\n", pathoIfaceName(d+1))
		}
		b.WriteString("}\n\n")
	}

	for d := 0; d < p.Levels; d++ {
		needStubs := p.pathoNeedsStubs(s, d)
		for j := 0; j < p.Impls; j++ {
			owner := p.pathoOwner(s, d, j)
			if !owner && !needStubs {
				continue
			}
			impl := pathoImplName(d, j)
			if owner {
				fmt.Fprintf(&b, "type %s implements %s @key(fields: \"id\") {\n  id: ID!\n  common: String @shareable\n", impl, pathoIfaceName(d))
				if pathoHasChild(d, p.Levels) {
					fmt.Fprintf(&b, "  child: %s @shareable\n", pathoIfaceName(d+1))
				}
				if p.Replicas > 1 {
					fmt.Fprintf(&b, "  %s: String @shareable\n", pathoOnlyField(d, j))
				} else {
					fmt.Fprintf(&b, "  %s: String\n", pathoOnlyField(d, j))
				}
			} else {
				fmt.Fprintf(&b, "type %s implements %s @key(fields: \"id\", resolvable: false) {\n  id: ID!\n  common: String @external\n", impl, pathoIfaceName(d))
				if pathoHasChild(d, p.Levels) {
					fmt.Fprintf(&b, "  child: %s @external\n", pathoIfaceName(d+1))
				}
			}
			b.WriteString("}\n\n")
		}
	}
	return b.String()
}

// writePathoGraph writes the per-subgraph Fed2 SDLs, the three operation files (walk.graphql,
// frag.graphql, leaffan.graphql) and manifest.json into dir. Returns the manifest path.
func writePathoGraph(tb testing.TB, dir string, p pathoGenParams) string {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", dir, err)
	}
	subs := make([]exportedSubgraph, p.Subgraphs)
	for s := 0; s < p.Subgraphs; s++ {
		name := fmt.Sprintf("sg%d", s)
		subs[s] = exportedSubgraph{
			Name:       name,
			RoutingURL: fmt.Sprintf("http://localhost:4001/%s", name),
			SchemaFile: name + ".graphql",
			sdl:        fed2PathoSubgraphSDL(s, p),
		}
		if err := os.WriteFile(filepath.Join(dir, subs[s].SchemaFile), []byte(subs[s].sdl), 0o644); err != nil {
			tb.Fatalf("write %s: %v", subs[s].SchemaFile, err)
		}
	}
	opsDir := filepath.Join(dir, "ops")
	if err := os.MkdirAll(opsDir, 0o755); err != nil {
		tb.Fatalf("mkdir ops: %v", err)
	}
	for _, op := range pathoOps(p) {
		if err := os.WriteFile(filepath.Join(opsDir, op.Name+".graphql"), []byte(op.Op+"\n"), 0o644); err != nil {
			tb.Fatalf("write op %s: %v", op.Name, err)
		}
	}
	manifest, err := json.MarshalIndent(subs, "", "  ")
	if err != nil {
		tb.Fatalf("marshal manifest: %v", err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		tb.Fatalf("write manifest: %v", err)
	}
	return manifestPath
}

// TestPathoExport is the export leg's correctness gate + on-demand artifact producer (see file doc).
func TestPathoExport(t *testing.T) {
	if dir := os.Getenv("PATHO_EXPORT_DIR"); dir != "" {
		p := pathoGenParams{
			Seed:      1,
			Levels:    envInt("PATHO_D", 3),
			Impls:     envInt("PATHO_M", 5),
			Subgraphs: envInt("PATHO_S", 10),
			Replicas:  envInt("PATHO_R", 2),
		}
		manifest := writePathoGraph(t, dir, p)
		var total int
		for s := 0; s < p.Subgraphs; s++ {
			total += len(fed2PathoSubgraphSDL(s, p))
		}
		t.Logf("exported D=%d M=%d S=%d R=%d -> %s (aggregate %.1fKB)",
			p.Levels, p.Impls, p.Subgraphs, p.Replicas, manifest, float64(total)/1e3)
		return
	}

	p := pathoGenParams{Seed: 1, Levels: 3, Impls: 4, Subgraphs: 6, Replicas: 2}
	dir := t.TempDir()
	writePathoGraph(t, dir, p)

	all := make([]string, p.Subgraphs)
	for s := 0; s < p.Subgraphs; s++ {
		sdl := fed2PathoSubgraphSDL(s, p)
		doc := unsafeparser.ParseGraphqlDocumentString(sdl)
		if doc.RootNodes == nil {
			t.Fatalf("sg%d: emitted SDL parsed to zero root nodes:\n%s", s, sdl)
		}
		all[s] = sdl
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{
		`@key(fields: "id", resolvable: false)`, // pure reference stubs
		`@key(fields: "id")`,                    // owned entities
		`common: String @external`,              // interface-satisfaction externals on stubs
		`child: PIface1 @external`,              // ...including the interface-returning field
		`common: String @shareable`,             // R-way overlapping resolution
		`implements PIface2`,                    // the deepest interface has members
		"type Query {\n  root: PIface0\n}",      // the root field
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("emitted SDL corpus is missing expected text %q", want)
		}
	}

	// Operation files must exist and match the generator byte-for-byte.
	for _, op := range pathoOps(p) {
		got, err := os.ReadFile(filepath.Join(dir, "ops", op.Name+".graphql"))
		if err != nil {
			t.Fatalf("read op %s: %v", op.Name, err)
		}
		if strings.TrimSpace(string(got)) != op.Op {
			t.Errorf("op file %s != generator output", op.Name)
		}
	}

	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var got []exportedSubgraph
	if err := json.Unmarshal(mb, &got); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(got) != p.Subgraphs {
		t.Errorf("manifest lists %d subgraphs, want %d", len(got), p.Subgraphs)
	}
}
