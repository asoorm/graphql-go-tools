package conformance

// coverage.go -- the proposition-coverage table: every one of FEDERATION_SEMANTICS.md's 104
// propositions, classified by what kind of verification can decide it, and -- for the
// generator-targetable class -- which scenario families target it. The classification is honest
// by construction: a GEN proposition with NO targeting family is a REGISTERED residual (the
// frozen untargeted list below, shrink-only), never silently counted as covered.
//
// Classes:
//
//	GEN  -- decidable over the observable plan (FEDERATION_SEMANTICS.md Section 0.3 vocabulary): fetch
//	       documents, subgraph targets, dependency order, representations, response shape,
//	       typed planning errors. The generator's territory.
//	EXEC -- decidable only by executing the plan (subgraph servers / client delivery): runtime
//	       consumption contracts, delivered-payload equalities. The executed-truth harness's
//	       territory (SCOREBOARD executed-truth sections), not the generator's.
//	COMP -- a composition-level precondition or input contract: the proposition constrains what
//	       the planner receives, not what the plan looks like.
//	FREE -- a pure MAY-permission with no falsifiable obligation: conformance oracles must merely
//	       not forbid the licensed behaviors. (Stated per Section 0.2's honesty rule instead of
//	       inventing a vacuous test.)
type CoverageClass string

const (
	ClassGen  CoverageClass = "GEN"
	ClassExec CoverageClass = "EXEC"
	ClassComp CoverageClass = "COMP"
	ClassFree CoverageClass = "FREE"
)

// Coverage is one proposition's row.
type Coverage struct {
	ID    string
	Class CoverageClass
	// Families that generate cases asserting the proposition (GEN rows only; empty = registered
	// untargeted residual).
	Families []string
	Note     string
}

// CoverageTable returns all 104 rows, ordered by construct then number.
func CoverageTable() []Coverage {
	return []Coverage{
		// Section 1 FS-PLAN
		{ID: "FS-PLAN-1", Class: ClassGen, Families: []string{"*baseline*"}, Note: "audit assertion 2 runs on every generated query/mutation case; the subscription runner validates trigger + per-event documents"},
		{ID: "FS-PLAN-2", Class: ClassGen, Families: []string{"*baseline*", "key-chain", "requires-chain", "mutation"}, Note: "audit assertion 3 + OracleFetchBefore chains"},
		{ID: "FS-PLAN-3", Class: ClassGen, Families: []string{"*baseline*", "abstract-narrowing", "requires-basic", "inaccessible"}, Note: "audit assertion 4 (expected-shape producibility) + shape-lacks oracles for injected inputs"},
		{ID: "FS-PLAN-4", Class: ClassGen, Note: "UNTARGETED residual: response-key CollectFields order is plan-observable (resolve tree field order) but adjudicated at executed truth (DV-009); an order-aware tree oracle is the identified follow-up"},
		{ID: "FS-PLAN-5", Class: ClassGen, Families: []string{"*baseline*", "root-split"}, Note: "audit assertion 5 (root-entry honesty)"},
		{ID: "FS-PLAN-6", Class: ClassGen, Families: []string{"key-nonresolvable"}, Note: "ExpectPlanError cases: typed refusal, never a silent omission"},
		{ID: "FS-PLAN-7", Class: ClassFree, Note: "non-minimality is licensed; fetch-count oracles assert only what a family's own quality bar pins"},

		// Section 2 FS-KEY
		{ID: "FS-KEY-1", Class: ClassGen, Families: []string{"key-chain", "entity-cycle", "dual-role-subscription", "rootnodes-only"}},
		{ID: "FS-KEY-2", Class: ClassGen, Families: []string{"key-chain", "key-composite", "key-distributed"}},
		{ID: "FS-KEY-3", Class: ClassGen, Families: []string{"key-chain", "key-composite"}, Note: "key injection observed via representation + __typename injection; leaf-coverage baseline rejects response-shape leaks"},
		{ID: "FS-KEY-4", Class: ClassGen, Families: []string{"key-composite"}, Note: "AX-REP-1: nested representation paths asserted (mirrored, never flattened)"},
		{ID: "FS-KEY-5", Class: ClassGen, Families: []string{"key-multi", "key-chain"}},
		{ID: "FS-KEY-6", Class: ClassFree, Note: "key CHOICE is free (REJECTED-CONTINGENT disposition); key-multi asserts only choice-agnostic properties"},
		{ID: "FS-KEY-7", Class: ClassGen, Families: []string{"key-multi"}, Note: "determinism probe: datasource-order permutation must not change the plan"},
		{ID: "FS-KEY-8", Class: ClassGen, Families: []string{"key-nonresolvable"}},
		{ID: "FS-KEY-9", Class: ClassGen, Families: []string{"key-nonresolvable"}, Note: "expected-error case: provably-non-resolvable configuration"},
		{ID: "FS-KEY-10", Class: ClassGen, Families: []string{"key-distributed"}, Note: "earmarked regression family (distributed @key)"},
		{ID: "FS-KEY-11", Class: ClassGen, Families: []string{"key-chain"}, Note: "transitive mixed-key chains"},

		// Section 3 FS-REQ
		{ID: "FS-REQ-1", Class: ClassGen, Families: []string{"requires-basic", "requires-chain", "override", "interface-object"}},
		{ID: "FS-REQ-2", Class: ClassGen, Families: []string{"requires-basic", "requires-chain"}},
		{ID: "FS-REQ-3", Class: ClassGen, Families: []string{"requires-basic", "requires-relay"}, Note: "OracleRootDocLacks pins the local-descent bypass prohibition"},
		{ID: "FS-REQ-4", Class: ClassGen, Families: []string{"requires-args", "requires-conflict"}},
		{ID: "FS-REQ-5", Class: ClassGen, Families: []string{"requires-conflict"}, Note: "earmarked regression family (argument-conflict @requires)"},
		{ID: "FS-REQ-6", Class: ClassGen, Families: []string{"requires-chain"}},
		{ID: "FS-REQ-7", Class: ClassGen, Families: []string{"requires-distributed"}},
		{ID: "FS-REQ-8", Class: ClassGen, Families: []string{"requires-relay"}},
		{ID: "FS-REQ-9", Class: ClassGen, Families: []string{"requires-conditional"}, Note: "includes the AX-REQ-COND probe (conditioned coordinate resolvable nowhere must still plan)"},

		// Section 4 FS-PROV
		{ID: "FS-PROV-1", Class: ClassGen, Families: []string{"provides"}, Note: "fragment-free FieldSets asserted positively (single-fetch); fragment-crossing grants are MG-1"},
		{ID: "FS-PROV-2", Class: ClassGen, Families: []string{"provides"}},
		{ID: "FS-PROV-3", Class: ClassFree, Note: "using the provided route is never obligatory"},
		{ID: "FS-PROV-4", Class: ClassGen, Families: []string{"provides"}, Note: "correctness-only until MG-1 closes (owning-route coverage admitted; fetch count flagged, not failed)"},

		// Section 5 FS-EXT
		{ID: "FS-EXT-1", Class: ClassGen, Families: []string{"external", "provides"}},
		{ID: "FS-EXT-2", Class: ClassGen, Families: []string{"external"}, Note: "AX-EXT-KEY: extension subgraphs as jump sources"},
		{ID: "FS-EXT-3", Class: ClassGen, Families: []string{"requires-basic", "external"}, Note: "gathering targets the owner"},

		// Section 6 FS-OVR
		{ID: "FS-OVR-1", Class: ClassGen, Families: []string{"override"}},
		{ID: "FS-OVR-2", Class: ClassGen, Families: []string{"override"}, Note: "input-side usage of the losing subgraph (with-requires scenario); the retained-capability SET is composer-contingent (COMP-flavored input, FEDERATION_SEMANTICS_FORMAL Section 3.2)"},
		{ID: "FS-OVR-3", Class: ClassGen, Families: []string{"override"}, Note: "twin-equality: plan byte-equal to the directive-free SDL"},
		{ID: "FS-OVR-4", Class: ClassComp, Note: "progressive override: intent pinned, realization protocol unpublished; the fixture format carries no label-resolution input -- out of generator scope, honestly"},
		{ID: "FS-OVR-5", Class: ClassGen, Families: []string{"override"}},
		{ID: "FS-OVR-6", Class: ClassGen, Note: "UNTARGETED residual: override x abstract member-possibility calculus; witness override-type-interface in the audit corpus; a generated scenario is the identified follow-up"},

		// Section 7 FS-SHR
		{ID: "FS-SHR-1", Class: ClassGen, Families: []string{"shareable"}},
		{ID: "FS-SHR-2", Class: ClassGen, Families: []string{"shareable"}, Note: "merged split-root position; shape-identical merging enforced by baseline document validity + shape oracle"},
		{ID: "FS-SHR-3", Class: ClassGen, Families: []string{"shareable"}},
		{ID: "FS-SHR-4", Class: ClassFree, Note: "input-gathering source choice among sharing subgraphs is free"},

		// Section 8 FS-INACC
		{ID: "FS-INACC-1", Class: ClassGen, Families: []string{"inaccessible"}},
		{ID: "FS-INACC-2", Class: ClassGen, Families: []string{"inaccessible"}},
		{ID: "FS-INACC-3", Class: ClassComp, Note: "API-schema representability of inaccessible members is composition's problem (the section says so explicitly)"},

		// Section 9 FS-IFO
		{ID: "FS-IFO-1", Class: ClassGen, Families: []string{"interface-object"}},
		{ID: "FS-IFO-2", Class: ClassGen, Families: []string{"interface-object"}, Note: "AX-IFO-1: interface-typed entry fragments + representations"},
		{ID: "FS-IFO-3", Class: ClassGen, Families: []string{"interface-object"}},
		{ID: "FS-IFO-4", Class: ClassGen, Families: []string{"interface-object"}},
		{ID: "FS-IFO-5", Class: ClassGen, Families: []string{"interface-object"}},
		{ID: "FS-IFO-6", Class: ClassGen, Families: []string{"interface-object"}, Note: "plan half (member gates served by the member-knowing subgraph); the runtime __typename rewrite itself is executed-truth (C-disc residual)"},
		{ID: "FS-IFO-7", Class: ClassGen, Families: []string{"interface-object"}},

		// Section 10 FS-ABS
		{ID: "FS-ABS-1", Class: ClassGen, Families: []string{"abstract-narrowing", "abstract-explosion"}},
		{ID: "FS-ABS-2", Class: ClassGen, Families: []string{"abstract-typename"}},
		{ID: "FS-ABS-3", Class: ClassGen, Families: []string{"abstract-narrowing"}},
		{ID: "FS-ABS-4", Class: ClassGen, Families: []string{"abstract-narrowing"}, Note: "value-member narrowing + route-scoping (unreachable declarer must not narrow)"},
		{ID: "FS-ABS-5", Class: ClassGen, Families: []string{"abstract-narrowing"}},
		{ID: "FS-ABS-6", Class: ClassGen, Families: []string{"abstract-narrowing"}, Note: "dead member: ghost-exclusive member fetched nowhere, gate stays in shape"},
		{ID: "FS-ABS-7", Class: ClassGen, Families: []string{"abstract-narrowing"}},
		{ID: "FS-ABS-8", Class: ClassGen, Families: []string{"abstract-explosion"}},
		{ID: "FS-ABS-9", Class: ClassGen, Note: "UNTARGETED residual: abstract-typed fragment conditions under a union; witness union-interface-distributed/case-05"},
		{ID: "FS-ABS-10", Class: ClassFree, Note: "expression freedom (DV-008); oracles compare presence-unions, never surface syntax"},
		{ID: "FS-ABS-11", Class: ClassGen, Note: "UNTARGETED residual: same-response-key member variants with discriminating-ancestor gates; witness sibling-conflation family"},
		{ID: "FS-ABS-12", Class: ClassGen, Families: []string{"abstract-mismatch"}},

		// Section 11 FS-ENT
		{ID: "FS-ENT-1", Class: ClassGen, Families: []string{"*baseline*", "key-chain"}, Note: "typed entry fragments under _entities, one representation per instance (document-level half)"},
		{ID: "FS-ENT-2", Class: ClassExec, Note: "by-INDEX result consumption is a resolver-runtime contract; not decidable from the plan object"},
		{ID: "FS-ENT-3", Class: ClassExec, Note: "null-entry tolerance is runtime null-propagation behavior"},
		{ID: "FS-ENT-4", Class: ClassGen, Families: []string{"key-chain"}, Note: "list roots: batch entity fetch resolves every item (RequiresEntityBatchFetch observed at plan level)"},
		{ID: "FS-ENT-5", Class: ClassGen, Families: []string{"mutation", "subscription"}},
		{ID: "FS-ENT-6", Class: ClassGen, Families: []string{"key-chain"}},
		{ID: "FS-ENT-7", Class: ClassGen, Families: []string{"requires-basic", "requires-distributed"}, Note: "AX-REP-2: representations carry requires closures beyond the key"},
		{ID: "FS-ENT-8", Class: ClassGen, Families: []string{"requires-conflict"}},

		// Section 12 FS-ROOT
		{ID: "FS-ROOT-1", Class: ClassGen, Families: []string{"root-split", "dual-role-subscription", "rootnodes-only"}},
		{ID: "FS-ROOT-2", Class: ClassFree, Note: "concurrency of independent root fetches is licensed, not mandated"},
		{ID: "FS-ROOT-3", Class: ClassGen, Families: []string{"mutation", "dual-role-subscription", "rootnodes-only"}},
		{ID: "FS-ROOT-4", Class: ClassGen, Families: []string{"root-renamed"}, Note: "earmarked regression family (renamed query AND subscription roots)"},
		{ID: "FS-ROOT-5", Class: ClassGen, Families: []string{"root-split"}},
		{ID: "FS-ROOT-6", Class: ClassGen, Families: []string{"mutation"}, Note: "shareable mutation root single-execution pin (mutations_3 class): one root fetch per mutation root field, typed refusal when unpinnable"},

		// Section 13 FS-ARG
		{ID: "FS-ARG-1", Class: ClassGen, Families: []string{"arguments", "shareable"}},
		{ID: "FS-ARG-2", Class: ClassGen, Families: []string{"arguments", "shareable"}},
		{ID: "FS-ARG-3", Class: ClassGen, Families: []string{"requires-args"}},
		{ID: "FS-ARG-4", Class: ClassGen, Note: "UNTARGETED residual: same-position aliased argument variants; the FS-REQ-5 gathering-side aliasing (requires-conflict) covers the mechanism, not the client-alias form"},
		{ID: "FS-ARG-5", Class: ClassGen, Families: []string{"arguments"}},
		{ID: "FS-ARG-6", Class: ClassGen, Note: "UNTARGETED residual: argument-independence of routing needs PAIRED probes (same coordinate, different argument values, equal routing); single cases cannot falsify it"},

		// Section 14 FS-SUB
		{ID: "FS-SUB-1", Class: ClassGen, Families: []string{"subscription", "dual-role-subscription"}},
		{ID: "FS-SUB-2", Class: ClassGen, Families: []string{"subscription", "root-renamed", "dual-role-subscription"}},
		{ID: "FS-SUB-3", Class: ClassGen, Families: []string{"subscription"}},
		{ID: "FS-SUB-4", Class: ClassGen, Families: []string{"subscription", "root-renamed"}},
		{ID: "FS-SUB-5", Class: ClassGen, Families: []string{"subscription"}, Note: "structural: per-event fetches exist only in the response plan, ordered after the trigger by construction; asserted via the subscription baseline"},
		{ID: "FS-SUB-6", Class: ClassExec, Note: "per-event response shape is runtime delivery; the plan-level shape half rides FS-PLAN-3"},

		// Section 15 FS-DEF
		{ID: "FS-DEF-1", Class: ClassFree, Note: "advisory deferral (AX-DEF-ADV); the mutation-flattening case witnesses the license"},
		{ID: "FS-DEF-2", Class: ClassGen, Families: []string{"defer"}, Note: "scope partition: initial fetches never select deferred-only data and vice versa"},
		{ID: "FS-DEF-3", Class: ClassGen, Families: []string{"defer"}, Note: "plan half (descriptor path/label bookkeeping, AX-DEF-WIRE); payload delivery itself is executed-truth (defer-wave frame oracle)"},
		{ID: "FS-DEF-4", Class: ClassExec, Note: "union-equality of delivered payloads (executed-truth defer harness)"},
		{ID: "FS-DEF-5", Class: ClassFree, Note: "rounding to fetch boundaries is licensed (closure property, FEDERATION_SEMANTICS_FORMAL Section 2.15)"},
		{ID: "FS-DEF-6", Class: ClassGen, Families: []string{"defer"}, Note: "routing invariance vs the erased twin"},
		{ID: "FS-DEF-7", Class: ClassGen, Families: []string{"defer-erasure"}, Note: "subscription and mutation @defer plans equal their erased twins"},

		// Section 16 FS-EDFS (EDFS event-driven subscription/mutation roots). All OURS; the
		// EDFS-specific content is the entrance (COMP: an SDL-less event-source datasource is
		// accepted and typed from the composed schema) and the transport (EXEC: the pub/sub broker
		// binding is a runtime/executed-truth concern). The plan SHAPE below an EDFS trigger is
		// ordinary subscription planning, already GEN-covered by FS-SUB-3 (the "subscription"
		// family); a generating EDFS family is the registered follow-up (needs the real-router
		// event-source config shape -- see DIVERGENCES DV-011).
		{ID: "FS-EDFS-1", Class: ClassComp, Note: "event-source metadata acceptance: an SDL-less EDFS datasource is a first-class root entrance, not refused (a-class input contract, shared with any SDL-less kind)"},
		{ID: "FS-EDFS-2", Class: ClassComp, Note: "composed-schema payload typing (D5-EDFS): the composed schema is the authority for an event root's output type; the resulting plan shape below the root rides FS-SUB-3 (GEN)"},
		{ID: "FS-EDFS-3", Class: ClassExec, Note: "pub/sub subscription trigger transport (D11.12-EDFS): broker binding vs WebSocket/SSE; the trigger/response split is FS-SUB-1's, unchanged"},
		{ID: "FS-EDFS-4", Class: ClassExec, Note: "pub/sub publish/request roots (D11.12-EDFS): publish/request over the broker binding vs HTTP; single-execution FS-ROOT-6 preserved"},
		{ID: "FS-EDFS-5", Class: ClassComp, Note: "no silent drift: the composed-schema fallback fires only under positive EDFS evidence; no-evidence metadata drift stays fail-loud FS-PLAN-6 (TestPlanner_SubscriptionMetadataOnlyFailsLoud)"},
	}
}
