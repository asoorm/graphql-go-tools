package external

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
)

// externalCorpusDir is the ONLY gateway to external schemas (leak rule): a local directory named by
// PLANNER_V2_EXTERNAL_CORPUS. Unset -> (_, false) and every caller skips. CI never sets this
// variable, so every test in this package is a no-op in CI by construction.
func externalCorpusDir() (string, bool) {
	dir, ok := os.LookupEnv("PLANNER_V2_EXTERNAL_CORPUS")
	return dir, ok && dir != ""
}

// Graph is one discovered graph from the external corpus: its federation subgraphs, its
// client-facing composed supergraph SDL, and the paths of the operations to run against it. File
// paths only are retained -- file CONTENTS are read lazily at run time and never copied into any
// committed artifact.
type Graph struct {
	Name           string
	Subgraphs      []audit.Subgraph // reuses audit.Subgraph: {Name, SDL}
	SupergraphSDL  string
	OperationPaths []string
}

// LoadGraphs discovers every graph under dir per the corpus directory convention (documented in
// v2/docs/planner-v2/EXTERNAL_CORPUS.md):
//
//	<dir>/<graph>/subgraphs/<name>.graphql   one federation subgraph SDL per file
//	<dir>/<graph>/supergraph.graphql         client-facing composed schema
//	<dir>/<graph>/operations/*.graphql       one or more operations to run against this graph
//
// This mirrors the audit package's own testdata/<suite>/{subgraphs,supergraph.graphql} convention
// (pkg/engine/planv2/audit/loader.go) so the same subgraph-derivation code path (audit.BuildDataSources)
// applies unchanged. A subdirectory missing supergraph.graphql or with zero discovered operations is
// skipped rather than erroring -- a partially-populated corpus directory (e.g. scratch notes, a
// non-graph subdirectory) should not abort the whole run.
//
// PRIVACY GUARD (enforced in code, not just docs): dir must resolve OUTSIDE any git repository --
// see ensureOutsideAnyRepo. LoadGraphs refuses such a directory before reading anything, so a run
// pointed at an in-repo path can never write a report (which would carry per-case graph/operation
// NAMES -- exactly where a corpus-identifying string would live) one `git add` away from a commit.
func LoadGraphs(dir string) ([]Graph, error) {
	dir, err := ensureOutsideAnyRepo(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read corpus dir %q: %w", dir, err)
	}
	var graphs []Graph
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		graphDir := filepath.Join(dir, e.Name())
		supergraphPath := filepath.Join(graphDir, "supergraph.graphql")
		supergraphSDL, err := os.ReadFile(supergraphPath)
		if err != nil {
			continue // not a graph directory (e.g. no supergraph.graphql) -- skip, don't fail the run
		}
		ops, err := discoverOperations(filepath.Join(graphDir, "operations"))
		if err != nil {
			return nil, fmt.Errorf("graph %s: %w", e.Name(), err)
		}
		if len(ops) == 0 {
			continue // no operations to run for this graph
		}
		subgraphs, err := loadSubgraphSDLs(filepath.Join(graphDir, "subgraphs"))
		if err != nil {
			return nil, fmt.Errorf("graph %s: %w", e.Name(), err)
		}
		graphs = append(graphs, Graph{
			Name:           e.Name(),
			Subgraphs:      subgraphs,
			SupergraphSDL:  string(supergraphSDL),
			OperationPaths: ops,
		})
	}
	sort.Slice(graphs, func(i, j int) bool { return graphs[i].Name < graphs[j].Name })
	return graphs, nil
}

// ensureOutsideAnyRepo is the RUNTIME half of the privacy rule (the docs are the other half): it
// resolves dir (filepath.Abs + filepath.EvalSymlinks, so a symlink into a repo cannot dodge the
// check) and rejects it if it lies inside ANY git repository -- found by walking dir's resolved
// ancestor chain for a .git entry (findRepoRoot). Because THIS repository has a .git root, a corpus
// path anywhere under graphql-go-tools is necessarily rejected too; the broader any-repo rule also
// keeps the run report (written INSIDE the corpus directory, see reportDir) out of reach of any
// `git add` whatsoever. On violation it returns a clear error naming the rule; callers (LoadGraphs,
// and through it every test) fail loudly without reading or writing anything.
func ensureOutsideAnyRepo(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve corpus dir %q: %w", dir, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve corpus dir %q: %w", dir, err)
	}
	if root, inRepo := findRepoRoot(resolved); inRepo {
		return "", fmt.Errorf(
			"external corpus dir %q resolves to %q, INSIDE the git repository rooted at %q -- refusing to load: "+
				"the privacy rule requires the external corpus (and the run report written inside it) to live "+
				"outside any repository, so nothing derived from it can ever be committed; "+
				"see v2/docs/planner-v2/EXTERNAL_CORPUS.md",
			dir, resolved, root)
	}
	return resolved, nil
}

// findRepoRoot walks up from start looking for a .git entry (directory OR file -- worktrees and
// submodules use a .git file), returning the containing directory and true if found.
func findRepoRoot(start string) (string, bool) {
	dir := start
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// discoverOperations globs every *.graphql file under dir, sorted for a deterministic run order.
func discoverOperations(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.graphql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// loadSubgraphSDLs reads every *.graphql file directly under dir into an audit.Subgraph, named by
// its file basename (without extension) -- same convention as audit.loadSubgraphs.
func loadSubgraphSDLs(dir string) ([]audit.Subgraph, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read subgraphs dir %q: %w", dir, err)
	}
	var out []audit.Subgraph
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".graphql") {
			continue
		}
		sdl, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, audit.Subgraph{
			Name: strings.TrimSuffix(e.Name(), ".graphql"),
			SDL:  string(sdl),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
