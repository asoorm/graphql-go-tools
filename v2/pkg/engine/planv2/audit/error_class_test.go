package audit

// Tests for the error-expectation reason-class pinning (anti-cheat must-fix 3): an error-expectation
// case must PASS only when the planner's rejection matches its pinned class -- an unrelated crash, a
// missing pin, or a plan where the pin demands a typed reject are all FAIL. The corpus run enforces
// the pins on the real fixtures; this test proves the classifier's discrimination by running real
// corpus cases under deliberately WRONG pins and watching them fail.

import (
	"strings"
	"testing"
)

func corpusCase(t *testing.T, suite, name string) Case {
	t.Helper()
	cases, err := loadCorpus("testdata")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	for _, c := range cases {
		if c.Suite == suite && c.Name == name {
			return c
		}
	}
	t.Fatalf("case %s/%s not in corpus", suite, name)
	return Case{}
}

func TestAudit_ErrorClassPinning(t *testing.T) {
	// non-resolvable-interface-object/case-06 rejects with search.ErrNoValidPlan("unreachable").
	unreachable := corpusCase(t, "non-resolvable-interface-object", "case-06")
	if unreachable.ErrorClass != errorClassNoValidPlanUnreachable {
		t.Fatalf("fixture pin drifted: want %q, got %q", errorClassNoValidPlanUnreachable, unreachable.ErrorClass)
	}
	// simple-inaccessible/case-03 plans successfully (runtime-error expectation).
	plans := corpusCase(t, "simple-inaccessible", "case-03")
	if plans.ErrorClass != errorClassPlanSuccess {
		t.Fatalf("fixture pin drifted: want %q, got %q", errorClassPlanSuccess, plans.ErrorClass)
	}

	run := func(c Case, pin string) Outcome {
		c.ErrorClass = pin
		c.ExpectFail = "" // adjudicate the raw outcome
		return Run(c)
	}

	t.Run("correct-class-passes", func(t *testing.T) {
		if out := run(unreachable, errorClassNoValidPlanUnreachable); out.Status != "PASS" {
			t.Errorf("correct pin must PASS, got %s: %s", out.Status, out.Reason)
		}
		if out := run(plans, errorClassPlanSuccess); out.Status != "PASS" {
			t.Errorf("plan-success pin on a planning case must PASS, got %s: %s", out.Status, out.Reason)
		}
	})

	t.Run("wrong-class-rejection-fails", func(t *testing.T) {
		// The masquerade the audit flagged: BEFORE the pinning, ANY plan failure scored PASS here.
		out := run(unreachable, errorClassNoValidPlanNonResolv)
		if out.Status != "FAIL" || !strings.Contains(out.Reason, "does not match") {
			t.Errorf("wrong-class rejection must FAIL with a mismatch reason, got %s: %s", out.Status, out.Reason)
		}
		out = run(unreachable, errorClassNormalize)
		if out.Status != "FAIL" {
			t.Errorf("plan-stage rejection under a normalize pin must FAIL, got %s: %s", out.Status, out.Reason)
		}
	})

	t.Run("unpinned-error-case-fails", func(t *testing.T) {
		out := run(unreachable, "")
		if out.Status != "FAIL" || !strings.Contains(out.Reason, "expected-error-class.txt") {
			t.Errorf("unpinned error-expectation case must FAIL demanding a pin, got %s: %s", out.Status, out.Reason)
		}
	})

	t.Run("rejection-under-plan-success-pin-fails", func(t *testing.T) {
		out := run(unreachable, errorClassPlanSuccess)
		if out.Status != "FAIL" {
			t.Errorf("a rejection where the pin demands a clean plan must FAIL, got %s: %s", out.Status, out.Reason)
		}
	})

	t.Run("plan-under-rejection-pin-fails", func(t *testing.T) {
		out := run(plans, errorClassNoValidPlanUnreachable)
		if out.Status != "FAIL" || !strings.Contains(out.Reason, "produced a plan") {
			t.Errorf("a successful plan where the pin demands a typed reject must FAIL, got %s: %s", out.Status, out.Reason)
		}
	})

	t.Run("every-error-expectation-case-is-pinned", func(t *testing.T) {
		cases, err := loadCorpus("testdata")
		if err != nil {
			t.Fatal(err)
		}
		var pinned []string
		for _, c := range cases {
			if expectsErrorsOnly(c.Expected) {
				if c.ErrorClass == "" {
					t.Errorf("%s/%s: error-expectation case without expected-error-class.txt", c.Suite, c.Name)
				}
				pinned = append(pinned, c.Suite+"/"+c.Name)
			} else if c.ErrorClass != "" && c.ErrorClass != errorClassPlanSuccess {
				t.Errorf("%s/%s: rejection-class pin on a non-error-only expectation", c.Suite, c.Name)
			}
		}
		// The corpus carries 8 error-only expectations (plus 4 partial-response errors:true cases
		// pinned plan-success); a drift in that census should be a conscious edit here.
		if len(pinned) != 8 {
			t.Errorf("error-only expectation census drifted: %d cases (%v), want 8 -- re-adjudicate the pins", len(pinned), pinned)
		}
	})
}
