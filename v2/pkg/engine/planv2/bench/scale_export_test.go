package bench

// scale_export_test.go is the M15 cross-planner-benchmark EXPORT leg: it re-emits the synthetic
// ring+sprinkle supergraph (scale_gen_test.go) as on-disk artifacts that external planners
// can consume -- so the identical graph the in-process planv2/v1 legs
// plan against is also the graph external planners plan against.
//
// It emits three things into an output directory:
//   - one Apollo-Federation-2 SDL file per subgraph (sg0.graphql ... sg{S-1}.graphql). Unlike
//     scale_gen_test.go's subgraph.sdl text (whose stub/helper types carry NO federation directives
//     -- the @key/@external/@requires/@provides live only in the hand-built DataSourceMetadata, which
//     is what planv2/v1 consume), these files are SELF-DESCRIBING Fed2 SDL: every key, external,
//     requires, provides and shareable directive is written inline so `@apollo/composition` (or
//     `rover supergraph compose`) can compose them into a supergraph the routers load. The shape is
//     re-derived from the SAME roleOf()/index math the generator uses, so the two stay in lockstep.
//   - one operation file per span (span1.graphql ... spanN.graphql), byte-identical to the strings the
//     in-process benchmarks feed spanOperation(), so every planner plans the exact same operations.
//   - manifest.json: [{name, routing_url, schema_file}] for the composition script + router configs.
//
// FED2 DIRECTIVE MAPPING (from the generator's topology, per subgraph i):
//   - Entity_i is OWNED here: @key(fields:"id") plus the sprinkled extra key (sku / id+region{code})
//     when subgraph (i+1) is a multi-key/composite helper; carries id, attr0..attr{F-1}, next,
//     profile, stats, meta and the sprinkle field (sku/region/weight).
//   - Entity_(i+1) is STUBBED here as a pure reference: @key(fields:"id", resolvable:false) { id }.
//     resolvable:false is the Fed2 spelling of scale_gen_test.go's "pure reference stub" (the router
//     may reference this entity by key here but must JUMP to its owner to resolve any real field) --
//     the exact pattern v1 cannot plan (scale_v1_test.go) and the reason this topology is interesting.
//   - when i%scaleProvidesEvery==0 (and F>0) the owner's `next` carries @provides(fields:"attr0"),
//     so the stub additionally declares attr0 as @external (Fed2 requires the provided field to be
//     external on the referencing subgraph).
//   - helper block for Entity_(i-1) when roleOf(i) is multi-key / composite / requires -- mirrors the
//     generator's bySku/compositeExtra/derived contributions with the matching key + @external/
//     @requires directives.
//   - Metadata and Region are cross-subgraph value types (every subgraph resolves Metadata for its
//     own entity; composite owner+helper both resolve Region) so their fields are @shareable.
//
// This test is COMMITTABLE and generic (no prospect/customer data, pure function of S/F). It runs in
// two modes:
//   - default (`go test`): exports a small S to t.TempDir and asserts every emitted SDL re-parses and
//     carries the expected federation directives -- a fast correctness gate, no external tools needed.
//   - export mode (SCALE_EXPORT_DIR set): writes S=SCALE_EXPORT_S (default 200), F=SCALE_EXPORT_F
//     (default 300) into that directory for the external-router benchmark harness to compose + serve.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// fed2LinkHeader is the Federation 2 schema link every emitted subgraph opens with -- it imports the
// directive set the ring+sprinkle shape uses so `@apollo/composition` recognises them.
const fed2LinkHeader = `extend schema @link(url: "https://specs.apollo.dev/federation/v2.5", import: ["@key", "@requires", "@provides", "@external", "@shareable"])` + "\n\n"

// exportedSubgraph is one subgraph's on-disk form: a routable name and its self-describing Fed2 SDL.
type exportedSubgraph struct {
	Name       string `json:"name"`
	RoutingURL string `json:"routing_url"`
	SchemaFile string `json:"schema_file"`
	sdl        string // written to SchemaFile, not serialised into the manifest
}

// fed2SubgraphSDL re-emits subgraph i of the scale ring as self-describing Apollo Federation 2 SDL.
// It re-derives the shape from the same roleOf()/(i+/-1 mod s) math generateScaleSupergraph uses, so a
// change to the topology there is a visible, compile-adjacent change here. s must be >= 3 (at s<=3 a
// subgraph's next-stub and prev-helper can target the same entity, which the ring shape only exhibits
// degenerately -- the export harness targets S in {50,100,200}).
func fed2SubgraphSDL(i, s, f int) string {
	nextIdx := (i + 1) % s
	prevIdx := (i - 1 + s) % s
	nextRole := roleOf(nextIdx) // extra key/field the (i+1) helper needs on Entity_i (owner side)
	helperRole := roleOf(i)     // what THIS subgraph contributes to Entity_(i-1)

	entityType := fmt.Sprintf("Entity%d", i)
	nextType := fmt.Sprintf("Entity%d", nextIdx)
	prevType := fmt.Sprintf("Entity%d", prevIdx)
	profileType := fmt.Sprintf("Profile%d", i)
	statsType := fmt.Sprintf("Stats%d", i)

	provides := f > 0 && i%scaleProvidesEvery == 0
	// If the PREVIOUS subgraph @provides attr0 on its `next` (which points at Entity_i), then attr0
	// is resolved both here (owner) and there (provider); Fed2 requires the owner's copy to be
	// @shareable for that @provides to compose.
	prevProvides := f > 0 && prevIdx%scaleProvidesEvery == 0
	hasRegion := nextRole == scaleRoleComposite || helperRole == scaleRoleComposite

	var b strings.Builder
	b.WriteString(fed2LinkHeader)

	if i == 0 {
		b.WriteString("type Query {\n  start: Entity0\n}\n\n")
	}

	// --- owned entity: Entity_i -----------------------------------------------------------------
	keyDirectives := []string{`@key(fields: "id")`}
	switch nextRole {
	case scaleRoleMultiKey:
		keyDirectives = append(keyDirectives, `@key(fields: "sku")`)
	case scaleRoleComposite:
		keyDirectives = append(keyDirectives, `@key(fields: "id region { code }")`)
	}
	fmt.Fprintf(&b, "type %s %s {\n  id: ID!\n", entityType, strings.Join(keyDirectives, " "))
	for k := 0; k < f; k++ {
		if k == 0 && prevProvides {
			b.WriteString("  attr0: String @shareable\n")
			continue
		}
		fmt.Fprintf(&b, "  attr%d: String\n", k)
	}
	if provides {
		fmt.Fprintf(&b, "  next: %s @provides(fields: \"attr0\")\n", nextType)
	} else {
		fmt.Fprintf(&b, "  next: %s\n", nextType)
	}
	fmt.Fprintf(&b, "  profile: %s\n  stats: %s\n  meta: Metadata\n", profileType, statsType)
	switch nextRole {
	case scaleRoleMultiKey:
		b.WriteString("  sku: String!\n")
	case scaleRoleComposite:
		b.WriteString("  region: Region\n")
	case scaleRoleRequires:
		b.WriteString("  weight: Float\n")
	}
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "type %s {\n  bio: String\n  tags: [String]\n}\n\n", profileType)
	fmt.Fprintf(&b, "type %s {\n  views: Int\n  rating: Float\n}\n\n", statsType)
	b.WriteString("type Metadata {\n  createdAt: String @shareable\n  updatedAt: String @shareable\n}\n\n")

	// --- pure reference stub for Entity_(i+1) ---------------------------------------------------
	if provides {
		fmt.Fprintf(&b, "type %s @key(fields: \"id\", resolvable: false) {\n  id: ID!\n  attr0: String @external\n}\n\n", nextType)
	} else {
		fmt.Fprintf(&b, "type %s @key(fields: \"id\", resolvable: false) {\n  id: ID!\n}\n\n", nextType)
	}

	// --- helper contribution to Entity_(i-1) ----------------------------------------------------
	switch helperRole {
	case scaleRoleMultiKey:
		fmt.Fprintf(&b, "type %s @key(fields: \"sku\") {\n  sku: String!\n  bySku%d: String\n}\n\n", prevType, prevIdx)
	case scaleRoleComposite:
		fmt.Fprintf(&b, "type %s @key(fields: \"id region { code }\") {\n  id: ID!\n  region: Region\n  compositeExtra%d: String\n}\n\n", prevType, prevIdx)
	case scaleRoleRequires:
		fmt.Fprintf(&b, "type %s @key(fields: \"id\") {\n  id: ID!\n  weight: Float @external\n  derived%d: Float @requires(fields: \"weight\")\n}\n\n", prevType, prevIdx)
	}

	if hasRegion {
		b.WriteString("type Region {\n  code: String! @shareable\n  name: String @shareable\n}\n")
	}

	return b.String()
}

// exportSubgraphs builds the Fed2 SDL for every subgraph in a size-(S,F) scale graph.
func exportSubgraphs(s, f int) []exportedSubgraph {
	out := make([]exportedSubgraph, s)
	for i := 0; i < s; i++ {
		name := fmt.Sprintf("sg%d", i)
		out[i] = exportedSubgraph{
			Name:       name,
			RoutingURL: fmt.Sprintf("http://localhost:4001/%s", name),
			SchemaFile: name + ".graphql",
			sdl:        fed2SubgraphSDL(i, s, f),
		}
	}
	return out
}

// writeScaleGraph writes the per-subgraph SDLs, the span operation files, and manifest.json into dir.
// spans are the operation sizes to emit (capped at s). Returns the manifest path.
func writeScaleGraph(tb testing.TB, dir string, s, f int, spans []int) string {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", dir, err)
	}
	subs := exportSubgraphs(s, f)
	for _, sg := range subs {
		if err := os.WriteFile(filepath.Join(dir, sg.SchemaFile), []byte(sg.sdl), 0o644); err != nil {
			tb.Fatalf("write %s: %v", sg.SchemaFile, err)
		}
	}
	opsDir := filepath.Join(dir, "ops")
	if err := os.MkdirAll(opsDir, 0o755); err != nil {
		tb.Fatalf("mkdir ops: %v", err)
	}
	for _, span := range spans {
		if span > s {
			continue
		}
		name := fmt.Sprintf("span%d.graphql", span)
		if err := os.WriteFile(filepath.Join(opsDir, name), []byte(spanOperation(span)+"\n"), 0o644); err != nil {
			tb.Fatalf("write %s: %v", name, err)
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

// TestExportScaleGraph is the export leg's correctness gate + the on-demand artifact producer.
//
// Default mode (no SCALE_EXPORT_DIR): exports a small ring to t.TempDir and asserts every emitted
// subgraph SDL re-parses and carries the federation directives the composition step needs (the
// resolvable:false reference stub, the sprinkled keys, a @requires, a @provides, @shareable value
// types). This needs no external tooling and is safe under `go test ./...` / -race.
//
// Export mode (SCALE_EXPORT_DIR=<path>): writes S=SCALE_EXPORT_S (default 200), F=SCALE_EXPORT_F
// (default 300) into <path> for the external-router harness. Example:
//
//	SCALE_EXPORT_DIR=/tmp/4way/graph SCALE_EXPORT_S=200 go test ./pkg/engine/planv2/bench/ \
//	  -run TestExportScaleGraph -count=1
func TestExportScaleGraph(t *testing.T) {
	spans := scaleSpans

	if dir := os.Getenv("SCALE_EXPORT_DIR"); dir != "" {
		s := envInt("SCALE_EXPORT_S", 200)
		f := envInt("SCALE_EXPORT_F", scaleFieldsPerEntity)
		manifest := writeScaleGraph(t, dir, s, f, spans)
		var total int
		for i := 0; i < s; i++ {
			total += len(fed2SubgraphSDL(i, s, f))
		}
		t.Logf("exported S=%d F=%d -> %s (%d subgraph SDLs, aggregate %.2fMB, spans=%v)",
			s, f, manifest, s, float64(total)/1e6, spans)
		return
	}

	const s, f = 50, 4
	dir := t.TempDir()
	writeScaleGraph(t, dir, s, f, spans)

	// Every emitted subgraph SDL must re-parse.
	for i := 0; i < s; i++ {
		sdl := fed2SubgraphSDL(i, s, f)
		doc := unsafeparser.ParseGraphqlDocumentString(sdl)
		if doc.RootNodes == nil {
			t.Fatalf("sg%d: emitted SDL parsed to zero root nodes:\n%s", i, sdl)
		}
	}

	// Directive-presence checks across the whole S sweep (the sprinkle roles are guaranteed present
	// at S=50 >= 4*scaleRoleCycle): the reference stub, each sprinkled key, requires, provides.
	all := make([]string, s)
	for i := 0; i < s; i++ {
		all[i] = fed2SubgraphSDL(i, s, f)
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{
		`@key(fields: "id", resolvable: false)`, // pure reference stub
		`@key(fields: "sku")`,                   // multi-key sprinkle
		`@key(fields: "id region { code }")`,    // composite key sprinkle
		`@requires(fields: "weight")`,           // requires sprinkle
		`@provides(fields: "attr0")`,            // provides sprinkle
		`@shareable`,                            // shared value types
		`@external`,                             // external key/requires fields
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("emitted S=%d SDL corpus is missing expected directive %q", s, want)
		}
	}

	// Operation files must exist and match spanOperation byte-for-byte.
	for _, span := range spans {
		if span > s {
			continue
		}
		got, err := os.ReadFile(filepath.Join(dir, "ops", fmt.Sprintf("span%d.graphql", span)))
		if err != nil {
			t.Fatalf("read span%d op: %v", span, err)
		}
		if strings.TrimSpace(string(got)) != spanOperation(span) {
			t.Errorf("span%d op file != spanOperation(%d)", span, span)
		}
	}

	// Manifest must enumerate all S subgraphs.
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var got []exportedSubgraph
	if err := json.Unmarshal(mb, &got); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(got) != s {
		t.Errorf("manifest lists %d subgraphs, want %d", len(got), s)
	}
}

// envInt reads an int env var, falling back to def when unset/blank/invalid.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
