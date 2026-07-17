# Adversarial Review -- Formal Core (owner-commissioned)

Conducted 2026-07-15 against HEAD of the M1 branch, with empirical probes (constructed
schemas run through the real planner kernel and pipeline). This is the permanent record;
the probes' findings drive the first M1.5 wave. Verdicts per load-bearing decision:

## Headline: three all-oracles-green wrong-data plans constructed; one is a shipped PASS

**The class: a response-shape leaf whose covering walk does not factor through its own
obligation path** -- equivalently, missing/misplaced fetch selections *below* the fetch-document
top level. All four oracles are structurally blind to it: audit assertions 1-4 are
shape/validity-only; assertion 5 checks top-level fetch fields only; the differential oracle
compares response trees (identical -- I4 holds on the wrong plans!); the brute-force oracle
checks per-goal tree-pi at the search layer (both goals ARE optimal); TLA models SETTLE only.

- **Counterexample A (sibling entity conflation):** `order { buyer { rating } seller { rating } }`
  (same `User` entity at two sibling positions -- buyer/seller, from/to, author/assignee: arguably
  the most common federation shape in existence). planv2 emits `query { order { buyer { id } } }` --
  `seller` never fetched anywhere -- plus one misplaced entity fetch. **v1 plans this correctly.**
  Mechanism (pinned by kernel probe): obligation tree correct; D3 collapses both goals onto the
  single path-blind D4 node `(User,b).rating`; `back[(User,a)]` holds ONE derivation chosen by the
  C.4 lexicographic tie-break; D10's cover requirement ("some valid walk reaches cand(g)") is
  technically satisfied. **I1-I4 all hold on the wrong plan.** The defect is in the D4/D10
  definitions, not the implementation.
- **Counterexample B (shipped):** `union-intersection/case-04` -- a counted PASS -- emits
  un-requested sibling fields (`aMedia`/`bMedia`) and never selects requested ones (`media`,
  `song`). Same mechanism one level below assertion 5's top-level check. Cases 08/11/12 likely
  same. Part of BENCHMARKS' celebrated tree-vs-folded gap (case-11, 19,044) is therefore
  **wrong sharing**, not savings.

This upgrades FORMAL_SPEC Honest Scope 2 ("same-root sibling granularity", registered M2-low-risk)
to a live wrong-data defect, and falsifies the 111/133 headline as stated (annotated in
SCOREBOARD.md pending re-audit).

## Decision verdicts

1. **Tree-cost optimality (I3/C.2): sound-but-rescope.** The honesty of not claiming folded
   optimality stands. BUT: on shareable-field schemas candidates tie constantly, and the C.4
   lexicographic tie-break then decides plan QUALITY -- constructed strangler-monolith case yields
   k+1 fetches vs 2 depending only on subgraph NAMES (unbounded regret; Section 6.4 co-location
   structurally cannot fix it -- it never fires on any fixture). The Section 3 gap distribution measures
   folding savings, not regret; no oracle measures regret. Cheap fixes exist: prefer
   already-covered subgraphs at equal pi (one line), and/or greedy set-cover rescoring of tied
   candidate sets. "NP-hard so we don't try" is a false dichotomy for the tie-break.
2. **D10 preference + fallback: wrong as shipped.** The fallback is a silent per-request
   correctness degrade -- precisely what the project's own no-silent-degrade principle forbids --
   and on the sibling class planv2 is worse than v1 while the scoreboard reads PASS. Posture must
   become typed-loud (config-gated refusal or per-plan diagnostic), and the root-entry
   granularity recursed below the top level.
3. **D6 local-resolution: sound-but-rescope.** The intersection rule is right; its quantifier is
   wrong. P is schema-scoped ("subgraphs able to resolve the parent field") but must be
   ROUTE-scoped ("subgraphs that can originate parent instances on this operation's routes") --
   constructed migration-shape case where D6 silently nulls resolvable member data that v1 and
   Apollo both fetch.

## Assumption most likely violated in production: A-6 (D11 conformance)

D1 declares `Directives` renaming and `RemappedPaths` lowering concerns; M1 lowering does not
implement them. Real Cosmo configs using per-datasource renames will emit misdirected fetch
documents while every theorem still holds -- and assertion 5's name-level comparison silently
weakens the moment renaming lands. Secondary: nothing validates weight-config sanity (w_f=0 or
negative weights void the proofs' assumptions with no guard).

## Ranked demands (drive M1.5 wave 1)

1. Fix sibling-path conflation (path-scoped object nodes or per-goal walk-tree consistency) and
   RE-AUDIT the 111 passes.
2. Add assertion 6: leaf-coverage / path-correspondence (every expected leaf selected by some
   fetch along the client's path, modulo aliases) + recurse assertion 5 below top level.
3. Flip the D10 fallback to typed-loud (config-gated); route-scope D6's P.
4. (Quality) folded-aware C.4 tie-break; stop citing the gap distribution as safety evidence
   until a regret oracle exists; guard weight-config sanity.

## M1.5 wave 1b THE FLIP, FINAL -- resolution status (the flip landed)

- **Demand 1 -- RESOLVED.** The obligation-driven per-position lowering **IS the shipping default.**
  `lower.Lower` (the zero-value `LowerConfig`) routes through it; the legacy node-keyed path survives one
  release cycle behind `lower.LowerConfig{LegacyNodeKeyedGrouping}` as an emergency escape hatch (pinned
  byte-identical by `TestOldPathEntityJumpUnchanged`, switch-OFF-scoped) and is scheduled for removal. The
  sibling / path-conflation wrong-data class -- Counterexample A (`order { buyer { rating } seller { rating } }`)
  and Counterexample B (`union-intersection/case-04` family) -- is closed for the general case: both
  `sibling-conflation` witnesses PASS (markers removed), and the celebrated priority families
  (union-intersection, partial-union*, provides-on-interface) that were false-PASSes now pass honestly. The
  re-audit landed: SCOREBOARD headline **65/135 -> 98/135** (37 GAPs), zero invalid/foreign plans on the
  unmarked set (`TestNewPathCorpusMeasure`: PASS=98 FAIL=0 INVALID=0 PANIC=0). Commit trail: `b6f0f2b4`
  (multi-jump attribution), `0c57fcb4` (interface-object key typing), `0483d31f` (distributed-root
  re-entry), then the flip + all-70-marker reconciliation (33 stale removed, 37 rewritten with honest
  post-flip reasons). Implementation finding vs the earlier *"root blocker: walkSpine is node-keyed"*: the
  attribution WAS the blocker and it was fixed lowering-side -- `walkSpine` now follows the jump's @key tail
  and records deep root-group positions from $O(Q)$; no search-side per-parent mask was needed (kernel
  `settle` and search goldens untouched). The residual 37 GAPs are genuine model/feature gaps (member-scoped
  leaf-coverage; foreign-root/missing-jump routing), no longer the whole-class collapse. From the flip
  session's full analysis (a process report maintained outside the public tree), the load-bearing detail:
  the 37 rewritten GAPs classify as A1 foreign-root / missing-root-jump x12, A2 missing entity-jump /
  provider-split x7, B distributed abstract-member expansion x4, C member-scoped leaf-coverage x13, and
  D path-correspondence backward x1 (every case named in its `expect-fail.txt`); assertion 7 examined 58
  entity fetches across 39 corpus cases with the headline holding at zero honest drops;
  `TestNewPathCorpusMeasure` read PASS=98 FAIL=0 INVALID=0 PANIC=0 on the raw pre-marker buckets; and the
  differential harness on the new default read MATCH=8 EXPECTED_DIVERGENCE=1 UNEXPLAINED=0 with every
  fetch count <= v1.
- **Demand 2 -- RESOLVED (assertion 6) + EXTENDED (assertion 7).** Assertion 6 (leaf-coverage /
  path-correspondence) landed in wave 1 and remains wired into `audit.Run()`. Its wave-1 caveat -- "does not
  yet catch the residual SHARED-INSTANCE subset (one entity fetch serving two positions of the same
  coordinate), which needs response-path data" -- is now addressed by **assertion 7 (response-path oracle):**
  every entity fetch's `ResponsePath`/`FetchPath` must match the obligation position it serves. See the
  wave-1b-final report for its wiring and re-audit result.

## M1.5 wave 1b THE FLIP -- resolution status (delta on the parallel path)

- **Demand 1 -- FLIP ATTEMPTED, PROVEN A NET REGRESSION, NOT LANDED; FIX STILL PENDING.** The default was
  NOT flipped: routing the whole audit corpus through the obligation-driven path (measured directly, with
  the empty-selection pruning fix already applied) **regresses ~39 cases to gain 17** -- 17 invalid-document
  FAILs + 22 empty-selection PANICs across `@requires`, `@provides`, interface-objects, external
  extensions, abstract unions, and general D11.4 aliasing, versus 17 conflation shapes it genuinely fixes.
  Shipping a planner that emits invalid/empty documents for those families is not viable, so the shipping
  node-keyed path remains the default (byte-identical; Section 7.2 golden intact) and the switch is retained as
  the parallel/escape path. **Root blocker (structural):** `walkSpine` still attributes fetch groups via a
  **node-keyed** map, so it collapses on repeated/recursive response positions -- the very ambiguity the
  redesign exists to remove, reproduced one level up. This was *proven*, not merely suspected: the combined
  self-reference-with-jump witness (wave-1b concern 4, previously *"believed correct"*) is now VALIDATED as
  a DEFECT (`lower.TestNewPathSelfRefWithJumpAtDeepPosition`). Pre-flip hardening landed green: the
  empty-selection pruning fix (`965a9ab7`), the combined witness, and the requested `audit.LoadCaseForTest`
  helper + `TestNewPathConflationCorpus` corpus gate (`e96a4318`). The 48 conflation markers are DELIBERATELY
  UNCHANGED -- they describe the SHIPPING path's live defects, which this session did not alter; the SCOREBOARD
  headline stays 65/135. The next session's single blocker is making `walkSpine` O(Q)-driven. From the
  session's full measured breakdown (a process report maintained outside the public tree): flipping the
  whole corpus read PASS=47 GAP=32 FAIL=34 PANIC=22 against the shipping path's PASS=65 GAP=70 FAIL=0
  PANIC=0; the breakage enumerates six classes (empty root/jump fetch groups, `@requires` field drops,
  key selection on a union without a member fragment, representation keys injected at the operation root,
  unwired general D11.4 aliasing, interface-object co-resolution), all rooted in the same node-keyed
  `walkSpine` attribution; the empty-selection pruning fix had already reduced flipped-path panics 28 -> 22
  before measurement.

## M1.5 wave 1b parallel-path -- resolution status (delta on wave 1b)

- **Demand 1 -- FIX IMPLEMENTED BEHIND SWITCH; FLIP + RE-AUDIT PENDING.** The obligation-driven
  lowering the wave-1b design specified is now implemented as a **parallel path** behind an internal
  lowering switch (`lower.LowerConfig{ObligationDrivenGrouping}`, default OFF) -- the honest way to break
  the grouping-rewrite-plus-re-audit atomicity that three prior sessions could not land green in one
  pass. The **shipping default is unchanged and byte-identical** (audit markers UNTOUCHED, differential
  harness unperturbed; pinned by a Section 7.2 old-path golden), so nothing is gamed. The **new path is
  D11-conformant**: it walks $O(Q)$ for structure (bounded by query depth -- self-reference terminates by
  construction) and consults $\kappa = $ `Cover.Walks` for group boundaries + keys, keying fetch groups
  by *(obligation response position, subgraph, jump)* so one reused `EntityJump` yields per-position
  entity fetches with the correct `ResponsePath`/`FetchPath`. It is validated by its OWN witness tests
  (switch ON in-test): `buyer/seller` (both ids at root + two entity fetches at `order.buyer` /
  `order.seller`), self-referential `friends` (full nesting preserved, no shortest-route collapse,
  terminates), and the Section 7.1/Section 7.2 fixtures. The deliberately-deferred remainder is a small later session
  that **flips the default and reconciles the 48 conflation markers** (re-audit); aliasing (D11.4) stays
  bound to the shipping path until then. The session (its full report maintained outside the public tree)
  landed as three each-green commits -- `9f6ce2a1` (switch + `lower/obligation_driven.go` + the Section 7.2
  old-path byte-identical guard `TestOldPathEntityJumpUnchanged`), `cea51198` (new-path witness tests),
  `a4204fa4` (docs) -- with all 9 planv2 packages `-race` green and the audit headline UNCHANGED at 65/135,
  markers untouched.

## M1.5 wave 1b -- resolution status (delta on wave 1)

- **Demand 1 -- FIX SPECIFIED + SECOND WITNESS ADDED; CODE STILL PENDING.** Wave 1b landed the
  spec-first fix design the fix must implement: FORMAL_SPEC D10 reframes the cover as a **function**
  $\kappa$ from goals to per-parent walk-tree-consistent scoped walks (not an edge set), with the
  precise per-parent `siblingEdges(g)` mask formula and the W2/C.3 note that the priced object stays
  the edge SET $\bigcup_g\kappa(g)$; a new D11 amendment specifies **obligation-driven** lowering
  (walk $O(Q)$, place each fetch at the ResponsePath of the obligation it serves, one entity fetch per
  response position); PROOFS T3.2 extends the masked-graph argument to the per-parent mask (still a
  sub-hypergraph, so exactness/determinism/P1 transfer verbatim) and T4 records that A-6 is currently
  violated by this class and strengthens *toward by-construction* under obligation-driven lowering. A
  second canonical witness `sibling-conflation/case-self-referential-friends` was added (the
  self-referential / nesting-collapse subset). An empirical correction was recorded: the structural
  walk does NOT infinite-loop on `friends { friends { ... } }` -- it terminates on the finite edge set but
  collapses the nesting to the shortest route (measured). The search-side per-parent mask and the
  O(Q)-driven lowering remain **not implemented** -- the change is a coordinated rewrite of the cover
  data structure + the lowering module that cannot land without the full re-audit, so it is staged
  rather than rushed. Headline now 65/135 (70 GAPs) after the +1 witness.

## M1.5 wave 1 -- resolution status

- **Demand 2 -- RESOLVED.** Assertion 6 (leaf-coverage / path-correspondence) is implemented
  (`pkg/engine/planv2/audit/leaf_coverage.go`) and wired into `audit.Run()` AND the differential /
  external harness (`external/runner.go` surfaces it as a planv2-side check, not a comparison). It
  checks both directions at the (Type.field) coordinate level, recursing below the fetch top level:
  FORWARD -- every unconditionally-requested field must be selected by some fetch document (catches a
  DROPPED sibling); BACKWARD -- every selected field must be a request or a `@key`/`@requires`/`__typename`
  mechanism (catches an UN-REQUESTED sibling). Sound (no false FAIL verified against the corpus, incl.
  interface/concrete-type key splits); it does not yet catch the residual SHARED-INSTANCE subset (one
  entity fetch serving two positions of the same coordinate), which needs response-path data.
- **Demand 1 -- RE-AUDIT DONE; FIX PENDING.** The re-audit (`SCOREBOARD.md`, regenerated) flips the
  headline from **111/133 to 65/134** (22->69 GAPs): **46 corpus cases** plus the ported buyer/seller
  witness (`testdata/sibling-conflation/case-buyer-seller`) are false-PASSes exposed by assertion 6 --
  an order of magnitude wider than this review's ~4-case estimate, reaching the celebrated priority
  families (union-intersection, partial-union*, provides-on-interface) and self-referential shapes.
  They are registered as honest `GAP`s. The FIX itself is **not** shipped: the spec is amended
  (FORMAL_SPEC Honest Scope 2) to require per-**parent** walk-tree consistency (generalizing per-root
  masking), and the wave-1 finding is that the search-side mask is necessary but **not sufficient** --
  the edge-SET cover + structural-walk lowering cannot represent a repeated/recursive response
  position, so the fix also requires O(Q)-driven lowering (fetch `ResponsePath` assignment +
  per-position entity-fetch instantiation, both currently absent). This is a larger change than a
  single wave; tracked in the M1.5 wave 1 report.
- **Demands 3 (typed-loud D10 fallback config; D6 route-scoping) and 4 -- NOT STARTED** in wave 1
  (deferred behind the correctness core).

## M1.5 IR+D6 wave -- demand-3b (D6 route-scoping) RESOLVED

- **Demand 3b -- RESOLVED (`D6p`, FORMAL_SPEC).** P(g) for the member-narrowing intersection is now
  ROUTE-scoped: a subgraph enters P only if its copy of the parent field-resolution node is reachable
  in H (optimistic, condition-blind hyperpath reachability from the roots -- an over-approximation of
  the per-operation route, so the filter only removes subgraphs with NO producing path at all; it can
  only reduce spurious exemptions, never introduce a wrong null). The review's migration-shape
  counterexample class is pinned red-first by `obligation.TestClassifyNarrowing_RouteScopedP`
  (`hypergraph/testdata.PartialUnionUnreachableParentConfig`: `Outer = X|Y` where the second producer
  is a rootless, keyless @shareable copy) and by `partial-union/case-02` flipping GAP->PASS
  (146->147 in-scope at the landing commit). The per-operation refinement of the quantifier ("this
  operation's routes", not just "some route in H") remains intentionally conservative in the safe
  direction -- a per-op route filter could only narrow MORE, and narrowing is the dangerous direction.
- **Demand 3a (typed-loud D10 fallback config) -- still open**; fallback usage re-measured at 28 goals
  on the audit corpus (unchanged by this wave: the D3ppp member fallback covers previously-unplannable
  goals, which were never fallback users).
