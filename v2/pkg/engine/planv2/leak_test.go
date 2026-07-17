// This file guards against GOROUTINE leaks (go.uber.org/goleak), not data leaks. The customer-data
// privacy guard lives in planv2/external/loader.go: the corpus loader refuses any corpus directory
// that resolves inside a git repository (pinned by TestLoadGraphsRefusesInRepoCorpus).
package planv2

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces the leak rule: the planv2 facade is pure synchronous planning and must spawn no
// goroutines, so any leftover goroutine after the suite is a regression.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
