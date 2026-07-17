package conformance

import (
	"fmt"
	"sort"
	"strings"
)

// triage.go -- the self-test triage register: every generated case whose outcome is not PASS is
// classified here, by ID, with an honest class. The register is the conformance analogue of the
// audit corpus's expect-fail markers, with the same discipline:
//
//   - a registered case that starts PASSING fails the suite until its entry is removed (the
//     register tracks reality, not history);
//   - an UNREGISTERED failure fails the suite immediately (no silent drift);
//   - class "planv2-gap" entries are REPORTED FINDINGS -- each one is registered in
//     DIVERGENCES.md's residual register with the generated case as witness, and is NOT fixed in
//     the generator wave that discovered it (honest counting: the suite's job is to find gaps,
//     not to hide them).
//
// Classes:
//
//	planv2-gap        a genuine planner defect/limitation the case witnesses;
//	oracle-strictness a defensible planner behavior the oracle is too strict for (the oracle or
//	                  the proposition wording owns the follow-up, not the planner);
//	fixture-limit     the audit-format fixture/deriver cannot express the shape faithfully
//	                  (the case is a harness limitation, not planner evidence).
//
// Generator BUGS are never registered: a generator defect is fixed, not recorded.
type Triage struct {
	Class  string
	Reason string
}

// triageRegister maps a case's SCENARIO key -- the ID with its "-s<seed>" suffix stripped
// ("<FS-ID>/<family>/<scenario>") -- to its triage, so a registered gap stays classified under
// every seed of a deeper sweep (the gap is a property of the scenario shape, not of one seed's
// name variation).
var triageRegister = map[string]Triage{
	// FS-ABS-4/abstract-narrowing/route-scoped-ghost: CLOSED (M3 encoding wave) FOR THE
	// POSITION-SCOPING DIRECTION -- D6 narrowing's capable set is POSITION-scoped
	// (obligation/narrow.go positionSubgraphs: the chain of field obligations root->parent), so a
	// ghost whose only route is an unrequested root at a different position and whose key is
	// resolvable:false no longer joins the member intersection (that witness passes).
	// FS-ABS-4/abstract-narrowing/unobtainable-key-ghost (M3 final review I-1, the transport
	// residual): CLOSED (M4 blockers wave) -- transport admission is now condition-AWARE (D6pppp,
	// obligation/narrow.go positionSubgraphs): a subgraph joins a level's capable set via an entity
	// jump only when the jump's KEY tails are obtainable at the position (every key-tail subgraph
	// already holds the instance there, judged as a per-level least fixpoint so multi-hop relays
	// stay admitted), so a resolvable:true-keyed ghost whose key no position-capable subgraph
	// produces no longer joins the verdict-2 intersection. The witness and its adversarial
	// variants (double-ghost mutual-admission cycle, partially-obtainable composite key, and the
	// two-hop legitimate narrower that pins the fixpoint's lenient direction) pass and its entry
	// is removed per the register discipline.
	// FS-IFO-1/interface-object/contributed-field-concrete: CLOSED (reachability-gaps wave) -- the
	// D3io concrete-position clause (obligation/tree.go expandInterfaceObjectFlattening) fires for a
	// bare field goal at a concrete-typed position too (not only under `... on C`); the plan enters
	// the ifo subgraph via the deriver's concrete-implementer key (v1 config convention) and selects
	// the field flattened on the interface. Audit leaf-coverage gained the matching UPCAST credit
	// (a concrete request satisfied by the interface coordinate -- the @interfaceObject wire form).
	// FS-REQ-9/requires-conditional/probe-unresolvable: CLOSED (reachability-gaps wave, MG-2
	// closure) -- D7pp(4) now states the AX-REQ-COND rendering clause and the builder realizes it
	// (hypergraph filterConditionalRequires): a fragment-conditioned requires coordinate resolvable
	// NOWHERE is pruned from Edge.Requires, so no gathering document renders it (an unconditional
	// composite left childless renders `{ __typename }`); the jump still fires, the conditioned
	// value is absent, and every emitted document is valid.
	//
	// All three Stage-2 discoveries and the I-1 transport residual are closed. The discipline
	// stands -- the next registered failure goes here, class-tagged, with its DIVERGENCES entry.
}

// scenarioKey strips the "-s<seed>" suffix from a case ID.
func scenarioKey(id string, seed uint64) string {
	suffix := fmt.Sprintf("-s%d", seed)
	return strings.TrimSuffix(id, suffix)
}

// TriageFor returns the register entry for a generated case, if any.
func TriageFor(c GeneratedCase) (Triage, bool) {
	tr, ok := triageRegister[scenarioKey(c.ID, c.Seed)]
	return tr, ok
}

// TriageKeys returns the registered scenario keys (for stale-entry detection).
func TriageKeys() []string {
	out := make([]string, 0, len(triageRegister))
	for k := range triageRegister {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
