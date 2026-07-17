// This file guards against GOROUTINE leaks (go.uber.org/goleak), not data leaks. The customer-data
// privacy guard lives in planv2/external/loader.go: the corpus loader refuses any corpus directory
// that resolves inside a git repository (pinned by TestLoadGraphsRefusesInRepoCorpus).
package differential

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces the leak rule for the differential harness. Both planners plan synchronously and
// the zero-value graphql_datasource factory starts no clients, so no goroutine may outlive the suite.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
