package search

import (
	"fmt"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
)

// The three ways search can fail. The search never quietly returns a worse plan; it returns one of
// these. They are kept distinct so a caller can tell a resource limit (the two caps) apart from a
// genuine "the schema can't answer this query" diagnosis (ErrNoValidPlan). Spec: FORMAL_SPEC Section 6.3.

// ErrSearchStateCap means the search settled more nodes than StateCap allows and was stopped. The
// search's size bound should keep this from ever happening on a well-formed graph, so hitting it
// points to a bug or a bad metric, not a plan that is merely large.
type ErrSearchStateCap struct{ States, Cap int64 }

func (e *ErrSearchStateCap) Error() string {
	return fmt.Sprintf("planv2: search state cap %d exceeded (%d)", e.Cap, e.States)
}

// ErrPlanTooLarge means the up-front size estimate exceeded PreflightCap, so the query was rejected
// before the search allocated anything.
type ErrPlanTooLarge struct{ Est, Cap int64 }

func (e *ErrPlanTooLarge) Error() string {
	return fmt.Sprintf("planv2: preflight estimate %d exceeds cap %d", e.Est, e.Cap)
}

// ErrNoValidPlan means a required field simply can't be reached: every candidate node for the named
// obligation is unreachable (for example, a requirement cycle that never resolves). This is a real
// "the schema can't answer this query" error, not one of the two resource limits above.
type ErrNoValidPlan struct {
	Obligation obligation.GoalID
	Reason     string
}

func (e *ErrNoValidPlan) Error() string {
	return fmt.Sprintf("planv2: no valid plan: obligation %d unreachable (%s)", e.Obligation, e.Reason)
}
