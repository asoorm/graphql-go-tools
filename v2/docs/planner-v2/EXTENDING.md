# Extending planner-v2: the development workflow

This document is the SDLC for adding a capability to planner-v2 -- a new
federation directive, a new operation type, a new optimization, or a change to
existing semantics. It is the process this codebase was built with, written
down so it survives the people who built it.

The one-sentence version: **specify before you code, prove or test before you
ship, and never let a claimed number outrun an artifact that reproduces it.**

## Why the process is spec-first

Planner-v1 accumulated behavior implementation-first: each new federation
feature was patched into the walker/visitor pipeline where it fit, and the
semantics of the planner became "whatever the code does." That is the property
this rebuild exists to remove. In planner-v2 the semantics live in
`FORMAL_SPEC.md`, the code implements the spec, and the test oracles check the
code against the spec. A capability that is not in the spec is not in the
planner -- even if the code happens to handle it.

The practical consequence: **the first file you edit is never a `.go` file.**

## The workflow, step by step

### 1. Specify the semantics (FORMAL_SPEC.md)

Write down what the capability means before deciding how to build it:

- Add a definition or amend an existing one. Amendments are named (D5p, D3''
  etc.) and never silently rewrite history -- the original stays visible with
  the amendment beside it, so reviewers can see what changed and why.
- State how the capability interacts with the four invariants (I1 soundness,
  I2 completeness-modulo-guards, I3 tree-cost optimality, I4 shape
  preservation). If it weakens one, say so explicitly in an Honest Scope note
  rather than hoping nobody notices.
- If it changes the search kernel or the cost model, check whether the
  theorems in `PROOFS.md` still hold. If a proof no longer covers the new
  behavior, rescope the theorem honestly -- a proof that quietly claims more
  than it shows is worse than no proof.
- New terms of art get a `GLOSSARY.md` entry. A CI test
  (`v2/internal/docscheck`) fails if FORMAL_SPEC, PROOFS, or RESEARCH bold a
  term the glossary doesn't define.

### 2. Establish authority (DIVERGENCES.md)

Decide whose behavior you are implementing. The authority hierarchy, in order:

1. The GraphQL specification
2. The Federation specification text
3. Apollo Router's observed behavior (the de-facto reference)
4. Our formal model
5. Audit-suite expectations (the vendored Guild corpus is a regression corpus,
   not an axiom -- it encodes one router's choices)

If the capability makes us diverge from Apollo, register a DV entry in
`DIVERGENCES.md`: what we do, what Apollo does, why ours is defensible, and
how a user would observe the difference. Divergence is allowed; undocumented
divergence is not.

### 3. Write the failing tests before the implementation

The project is TDD throughout, and the test layers already exist -- extend
them rather than inventing parallel ones:

- **Audit fixtures** (`planv2/audit/testdata/`): add cases in the corpus
  format for observable planning behavior. The runner applies seven
  assertions to every case (fetch-document validity, dependency order,
  response shape, root-entry honesty, leaf coverage, path correspondence,
  response-path oracle) -- a new case gets all seven for free.
- **Conformance generator** (`planv2/conformance`): the property-based
  conformance suite is a first-class verification surface, on par with the
  audit corpus. It generates cases (seeded, deterministic -- corpus committed
  as generator code, never as golden files) from the propositions of
  `FEDERATION_SEMANTICS.md`, runs the audit runner's seven assertions as its
  baseline, and adds proposition-specific oracles (representation contents,
  field-source assignment, member-fragment placement, determinism probes,
  twin-plan equalities, typed-error expectations). A new capability MUST be
  reflected here: reclassify its propositions in `coverage.go` (the honest
  GEN/EXEC/COMP/FREE table, pinned by test) and add or extend a scenario
  family targeting them; a capability whose propositions stay untargeted
  lands on the frozen untargeted-residual list, visibly. Self-test failures
  are triaged, never silenced: a genuine planner gap is registered in
  `conformance/triage.go` AND in `DIVERGENCES.md` with the generated case as
  witness (the expect-fail discipline -- a registered case that starts passing
  fails the suite until its entry is removed).
- **Brute-force oracle** (`planv2/search/`): if the change touches search or
  cost, extend the enumerated-instance oracle so optimality stays
  machine-checked instead of argued.
- **TLA+ model** (`docs/planner-v2/tla/`): if the change alters SETTLE's
  state machine, update the model and re-run the TLC configurations. The
  model is small on purpose -- it checks the algorithm, not the Go code.
- **Differential harness** (`planv2/differential/`): if v1 handles the
  capability today, the harness comparing plans against v1 is the cheapest
  regression net.

Run the new tests and watch them fail for the right reason before writing
implementation code.

### 4. Implement in the layer that owns the concern

The pipeline is `hypergraph -> obligation -> search -> lower`, and data flows one
way. Put the change where the concern lives:

| The change is about... | It belongs in... |
|---|---|
| What the composed schema makes reachable (types, keys, provides) | `hypergraph` (builder -- nodes, edges, edge kinds) |
| What a given operation must resolve (fields, requires-obligations) | `obligation` |
| Which routes are chosen and what they cost | `search` (cost model, SETTLE) |
| The shape of the fetch tree / wire representation | `lower` |
| Transport details (URLs, batching, input envelopes) | `lower` (transport lowering) |

The rule that keeps the architecture from decaying: **a downstream layer never
reaches back upstream.** If lowering needs information search didn't record,
the fix is to record it during search -- not to re-derive it during lowering.
Most of v1's brittleness traces to exactly this kind of backward reach.

Commit in small green stages. The branch history should never contain a red
tree; a reviewer bisecting a regression must be able to build at every commit.

### 5. Gate on the oracles

Before a capability is "done," all of these pass locally:

- Full audit run with `-race`. New failures must be **classified** -- either
  fixed, or registered in the Residual Register in `DIVERGENCES.md` with a
  class and a milestone. Zero unexplained failures is a hard gate, not a
  goal.
- Golden files byte-identical, or regenerated **deliberately** in their own
  commit with the diff reviewed. A golden that changed as a side effect is a
  bug until proven otherwise.
- Differential harness against v1 for any behavior v1 supports.
- If plans could change for real workloads: the external corpus sweep
  (`planv2/external`, env-gated with `PLANNER_V2_EXTERNAL_CORPUS`; the corpus
  itself lives outside this repository and must stay there).
- **The standing pre-merge CUSTOMER-SAFETY GATE:** the private customer-corpus
  sweep (env-gated as above) plus the D10 fallback-count check
  (`TestAudit_D10FallbackRegister`). The precedent is the conformance campaign's
  four consecutive byte-identical sweeps; a wave that moves customer plan-status
  counts must stop and classify the movement before landing.

### 6. Measure before you flip

If the change replaces an existing code path, run both paths side by side and
compare *before* making the new one the default. This project once attempted
a default-flip that measured as a net regression; the measurement turned a
would-be shipped defect into a root-cause fix. Benchmarks (`planv2/bench`)
run before/after for anything touching the hot path, and a capability that
regresses planning performance needs an explicit, recorded accept decision --
not a shrug.

### 7. Update the record

- `SCOREBOARD.md` numbers are copied from test artifacts, never hand-edited.
  If you can't reproduce a number with a command, the number doesn't go in.
- `ARCHITECTURE.md` conformance table gets a row mapping the new capability
  to its spec definition and its test evidence.
- Anything deferred goes in the Residual Register with enough context that a
  stranger can pick it up.

### 8. Independent adversarial review

Every substantial capability gets a review by someone (or some agent) whose
brief is to **falsify** the claim, not to confirm it -- reproduce the numbers,
attack the invariants, hunt for the case the tests don't cover. When a review
falsifies a claim, the response is an honest reset of the scoreboard, not a
defense of the old number. This happened once at scale here (an oracle gap
had inflated the audit score); the reset is documented in
`ADVERSARIAL_REVIEW.md` and the earned-back numbers are the ones you see now.

## Worked example: what "add @defer" would look like

1. FORMAL_SPEC: amend D-series with defer semantics -- which edges/walks a
   deferred selection contributes, how it partitions the fetch tree; note I4
   implications for response shape.
2. DIVERGENCES: record how Apollo Router fragments deferred payloads; decide
   whether to match it and register a DV entry if not.
3. Tests first: audit fixtures with `@defer` operations asserting the split
   fetch tree; differential cases skipped-vs-v1 (v1 support differs).
4. Implement: obligation layer marks deferred subtrees; lowering emits the
   incremental-delivery fetch nodes. Search is likely untouched -- if it
   isn't, the brute-force oracle grows defer instances.
5. Gates, measurement, scoreboard, adversarial review as above.

## Milestone-level process

Capabilities batch into milestones (M0 formal foundations, M1 core build,
M1.5 hardening). Day-to-day sequencing was tracked in process ledgers (a
task checklist and a per-session progress log) maintained outside the public
tree; the durable record lives here -- every capability's evidence trail is in
the committed docs (`FORMAL_SPEC.md`, `DIVERGENCES.md`, `SCOREBOARD.md`,
`ADVERSARIAL_REVIEW.md`) and the test suite. A milestone ends with a
whole-branch review whose verdict line is literal: "READY FOR OWNER REVIEW:
yes/no." Work between milestones follows the loop above per capability.

The registered next-milestone (M2) items at the time of writing:
operation-scoped SETTLE, the distributed-abstract-member class, distributed
`@requires`, and retirement of the legacy lowering escape hatch -- each is in
the Residual Register with its evidence trail.
