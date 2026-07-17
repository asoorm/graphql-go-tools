package conformance

// opscoped_equality_test.go -- the conformance-generator dual-mode gate for the operation-scoped
// search (FORMAL_SPEC Section 6.5): every generated case runs under both modes and must produce (a) the
// same suite classification (so the registered gaps stay registered in BOTH modes and the
// pass/gap split is mode-identical), and (b) a byte-identical canonical plan -- or the identical
// plan error -- mode-vs-mode. Complements the audit corpus gate (audit/opscoped_equality_test.go)
// with the generated scenario families.

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
)

func TestConformance_OpScopedModeEquality(t *testing.T) {
	scoped := planv2.Config{OperationScopedSearch: true}
	cases := GenerateAll(seedsUnderTest(t))
	if len(cases) == 0 {
		t.Fatal("generator produced no cases")
	}
	var compared, errEqual int
	for _, c := range cases {
		outU := Run(c)
		outS := RunWithPlannerConfig(c, scoped)
		if outU.Status != outS.Status || outU.Reason != outS.Reason {
			t.Errorf("%s: outcome differs between modes:\n unscoped: %s (%s)\n scoped:   %s (%s)",
				c.ID, outU.Status, outU.Reason, outS.Status, outS.Reason)
			continue
		}
		canonU, errU := planCanonCfg(c.Case, c.Defer, planv2.Config{})
		canonS, errS := planCanonCfg(c.Case, c.Defer, scoped)
		if (errU == nil) != (errS == nil) {
			t.Errorf("%s: plan error divergence between modes: unscoped=%v scoped=%v", c.ID, errU, errS)
			continue
		}
		if errU != nil {
			if errU.Error() != errS.Error() {
				t.Errorf("%s: different plan errors between modes:\n unscoped: %v\n scoped:   %v", c.ID, errU, errS)
				continue
			}
			errEqual++
			continue
		}
		if canonU != canonS {
			t.Errorf("%s: canonical plans differ between modes:\n--- unscoped ---\n%s\n--- scoped ---\n%s",
				c.ID, canonU, canonS)
			continue
		}
		compared++
	}
	t.Logf("op-scoped mode equality: %d generated cases byte-identical, %d identical typed errors, %d total",
		compared, errEqual, len(cases))
	if compared == 0 {
		t.Fatal("no generated case produced comparable plans -- the gate is vacuous")
	}
}
