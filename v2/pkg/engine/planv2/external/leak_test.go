// This file guards against GOROUTINE leaks (go.uber.org/goleak), not data leaks. The customer-data
// privacy guard lives in loader.go in this package: the corpus loader refuses any corpus directory
// that resolves inside a git repository (pinned by TestLoadGraphsRefusesInRepoCorpus).
package external

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces the leak rule for the external-corpus harness. Both planners plan synchronously
// and the zero-value graphql_datasource factory starts no clients, so no goroutine may outlive the
// suite -- same convention as the differential and audit packages this harness reuses.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
