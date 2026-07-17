package audit

import (
	"strings"
	"testing"
)

// TestPreserveAppendix pins the -update writer's contract: regenerating the scoreboard must NOT
// clobber the hand-appended executed-truth appendix (everything from the first "---" separator).
// A prior agent lost that appendix to a full-file overwrite; this is the regression net.
func TestPreserveAppendix(t *testing.T) {
	board := "# Audit corpus -- planner-v2 plan-level scoreboard\n\n| Suite |\n|---|\n| `x` |\n"
	appendix := "\n---\n\n## Executed-truth results (M1.5 router-level harness)\n\nhand-written record\n"

	t.Run("appendix preserved across regeneration", func(t *testing.T) {
		existing := "OLD BOARD CONTENT\n" + appendix
		out := preserveAppendix(board, existing)
		if !strings.HasPrefix(out, "# Audit corpus") {
			t.Fatalf("generated board missing from output:\n%s", out)
		}
		if strings.Contains(out, "OLD BOARD CONTENT") {
			t.Fatalf("stale generated content survived:\n%s", out)
		}
		if !strings.Contains(out, "hand-written record") {
			t.Fatalf("executed-truth appendix was clobbered:\n%s", out)
		}
		// Idempotent: regenerating again over the combined file keeps exactly one appendix.
		again := preserveAppendix(board, out)
		if again != out {
			t.Fatalf("preserveAppendix not idempotent:\n--- first\n%s\n--- second\n%s", out, again)
		}
	})

	t.Run("no marker yields the board unchanged", func(t *testing.T) {
		if got := preserveAppendix(board, "just an old board, no appendix"); got != board {
			t.Fatalf("expected plain board, got:\n%s", got)
		}
		if got := preserveAppendix(board, ""); got != board {
			t.Fatalf("expected plain board for empty existing file, got:\n%s", got)
		}
	})
}
