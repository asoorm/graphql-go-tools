package docscheck

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBranchAuthoredFilesAreASCII guards the M4 ASCII normalization sweep: every
// branch-authored file under the planner-v2 trees must stay plain ASCII. Typographic
// unicode (prime marks, em dashes, arrows, math glyphs) must use the ASCII notation
// mapped in the sweep (D5p/D7pp/D3pppp amendment names, -- for em dashes, -> <= x,
// "Section" for the section sign, kappa/pi spelled out, and so on).
//
// Scope (relative to the v2 module root):
//   - pkg/engine/planv2/...  (planner-v2 source, tests, bench, and our own fixtures)
//   - docs/planner-v2/...    (the planner-v2 document set, TLA+ models included)
//   - internal/docscheck/... (this package)
//
// Exempt: pkg/engine/planv2/audit/testdata/... is the vendored Guild audit corpus
// (LICENSE.the-guild; upstream-authored fixtures are not ours to rewrite) -- except
// its README.md, which is branch-authored attribution text and stays in scope.
//
// Justified exceptions go into ascii_allowlist.txt (one path per line, relative to
// the v2 module root, # comments allowed). The allowlist is meant to stay empty.
func TestBranchAuthoredFilesAreASCII(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	roots := []string{
		filepath.Join(moduleRoot, "pkg", "engine", "planv2"),
		filepath.Join(moduleRoot, "docs", "planner-v2"),
		filepath.Join(moduleRoot, "internal", "docscheck"),
	}
	vendoredCorpus := filepath.ToSlash(filepath.Join("pkg", "engine", "planv2", "audit", "testdata")) + "/"

	allowed := loadASCIIAllowlist(t)

	var candidates []string // absolute-ish paths, module-root relative kept alongside
	var rels []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(moduleRoot, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, vendoredCorpus) && filepath.Base(rel) != "README.md" {
				return nil // vendored Guild corpus fixtures -- exempt by policy
			}
			if allowed[rel] {
				return nil
			}
			candidates = append(candidates, path)
			rels = append(rels, rel)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	ignored := gitIgnoredSet(t, candidates)
	for i, path := range candidates {
		if ignored[path] {
			continue // gitignored local-only files (e.g. the external-corpus harness sweeps) never reach a PR
		}
		reportNonASCII(t, path, rels[i])
	}
}

// gitIgnoredSet returns the subset of paths that git ignores. Local-only files that
// .gitignore declares (the planner-v2 external-corpus sweeps, scratch notes) are not
// PR-bound content, so the ASCII guard must not fail on them. On CI checkouts only
// tracked files exist, so this is a local-developer nicety; if git is unavailable the
// guard simply checks everything.
func gitIgnoredSet(t *testing.T, paths []string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	if len(paths) == 0 {
		return out
	}
	cmd := exec.Command("git", append([]string{"check-ignore", "--"}, paths...)...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	// Exit status 1 means "no path is ignored"; treat any other failure as "no filtering".
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			return out
		}
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line != "" {
			out[line] = true
		}
	}
	return out
}

func loadASCIIAllowlist(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	b, err := os.ReadFile("ascii_allowlist.txt")
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("read ascii_allowlist.txt: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	return out
}

// reportNonASCII scans one file and errors with file:line:col plus the offending
// rune for the first hit on each affected line (capped so a pasted document does
// not flood the log).
func reportNonASCII(t *testing.T, path, rel string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", rel, err)
	}
	defer f.Close()

	const maxReports = 5
	reports := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := sc.Bytes()
		for col := 0; col < len(line); {
			if line[col] < utf8.RuneSelf {
				col++
				continue
			}
			r, size := utf8.DecodeRune(line[col:])
			if reports < maxReports {
				t.Errorf("%s:%d:%d: non-ASCII rune %q (U+%04X) -- normalize to ASCII or add the file to ascii_allowlist.txt with justification", rel, lineNo, col+1, r, r)
			}
			reports++
			col += size
			break // one report per line is enough
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", rel, err)
	}
	if reports > maxReports {
		t.Errorf("%s: %d more non-ASCII lines suppressed", rel, reports-maxReports)
	}
}
