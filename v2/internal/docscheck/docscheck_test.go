package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// boldRe matches a bolded glossary term. The character class must not admit `*`:
// with `--` (the ASCII em dash) in prose, a `*` in the class would let a match
// span from one **term** across intervening text into the next **term**.
var boldRe = regexp.MustCompile(`\*\*([a-zA-Z][a-zA-Z0-9 \-/]{2,40})\*\*`)

func glossaryHeadings(t *testing.T, dir string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "GLOSSARY.md"))
	if err != nil {
		t.Fatalf("read glossary: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if h, ok := strings.CutPrefix(line, "### "); ok {
			out[normalize(h)] = true
		}
	}
	return out
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, "s")
}

func TestGlossaryCoversMarkedTerms(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "planner-v2")
	headings := glossaryHeadings(t, dir)
	for _, doc := range []string{"FORMAL_SPEC.md", "PROOFS.md", "RESEARCH.md"} {
		b, err := os.ReadFile(filepath.Join(dir, doc))
		if err != nil {
			continue // doc not written yet; later tasks make this meaningful
		}
		for _, m := range boldRe.FindAllStringSubmatch(string(b), -1) {
			if !headings[normalize(m[1])] {
				t.Errorf("%s: bolded term %q has no GLOSSARY.md entry", doc, m[1])
			}
		}
	}
	if len(headings) == 0 {
		t.Fatal("GLOSSARY.md missing or has no ### entries")
	}
}
