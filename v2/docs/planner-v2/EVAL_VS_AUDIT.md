# Eval vs the federation audit -- what we have, what's missing

> **M1.5 requires-args CONFLICT-SPLIT update (DV-007 -- landed).** The live headline is
> **152/181 in-scope passed (29 known gaps), 21 skipped** (`SCOREBOARD.md`, regenerated). The requires-args
> wave made the D7 selection tokenizer argument-aware (`parseSelection` skips a field's `(...)` group
> whole) and carries the literal `@requires` argument values on `Edge.Requires` (outside the A-3 identity
> tuple), re-rendered at lowering into the source document and the `Requires` fragment. **DV-006 is
> VERIFIED at plan level** via `requires-with-argument/case-01` (`shippingEstimate` @requires
> `price(currency: "USD") weight`; `isExpensiveCategory` @requires `category { averagePrice(currency:
> "USD") }`) -- a registered reference-divergence result (see `DIVERGENCES.md`). **DV-007 is now ALSO
> VERIFIED at plan level.** The same-coordinate CONFLICT (`shippingEstimate` price@USD vs
> `shippingEstimateEUR` price@EUR) is split across separate `_entities` fetches (v1
> `HasArgumentConflictWith`) via `splitArgConflictGroups`: per-field requires association
> (`Edge.RequiresBy`), source-document aliasing (`_planv2req_price_1: price(currency: "EUR")`), and a
> representation value-path indirection (`repNode.readAs`). `requires-with-argument-conflict/case-01`
> PASSES all 7 assertions. `requires-with-argument` cases 02-05 stay SKIP for a SEPARATE reason -- a
> distributed `@requires` spanning two subgraphs (multi-jump-requires model gap), not an argument
> concern. The prior wave-2
> headline was 126/177; the per-suite table in Section 1 predates these waves -- the live authority is
> `SCOREBOARD.md` and the M1.5 requires-args report.

Branch `feat/planner-v2-hypergraph`. This document puts planner-v2's own scoreboard next to what
The Guild's `graphql-federation-gateway-audit` measures and says, plainly, where we stand: what we
have, what's missing, and how the two measurements differ in what they actually check. Cross-vendor
comparisons against other gateways' published audit results are maintained outside this repository.
No suite is hidden; every non-pass below is a filed, expect-fail-marked
finding (`pkg/engine/planv2/audit/testdata/**/expect-fail.txt` or `skip.txt`), not a silent gap.

Sources: `SCOREBOARD.md` (re-run live for this document -- see below), `research-notes/apollo-vs-audit.md`,
`DIVERGENCES.md`, `ADVERSARIAL_REVIEW.md`, and the audit fixture `expect-fail.txt`/`skip.txt` reasons
under `pkg/engine/planv2/audit/testdata/`.

## 0. Measurement honesty, up front

**These two numbers are not the same measurement, and putting them in one table risks implying
they are. Read this section before the table.**

- **planv2's 152/181 is a plan-level score.** It never executes a subgraph and never checks a
  scalar value. It checks: the operation plans; every emitted fetch document is valid GraphQL
  against its subgraph's schema; fetch dependencies form a legal order; the plan's response shape
  can produce the expected response's keys/nesting/list-ness; every ROOT fetch enters only fields
  the client asked for (assertion 5); every requested field is selected by some fetch along its
  obligation path and no fetch selects an un-requested field (assertion 6); every entity fetch's
  response path matches the position it serves (assertion 7).
- **The published gateway-audit numbers are router-level, executed scores** -- real subgraph
  servers, real HTTP fetches, real scalar-value equality against the expected response. That is a
  stronger check in one specific way ours is not: **ours never proves a plan returns the correct
  data at runtime.** (Per-vendor published results and cross-comparisons are maintained outside
  this repository.)
- **In another specific way, ours is stronger than any published router score:** assertions 5, 6,
  and 7 (root-entry honesty, leaf-coverage/path-correspondence, response-path correctness) are
  *our own* oracles, added after an adversarial review found planv2 could construct a plan that
  every other oracle called correct and that was still wrong -- a class of bug a router-level
  pass/fail on scalar values does not by itself localize or even guarantee it would catch (a plan
  that fetches the right leaves at the wrong response path, or drops a sibling silently, can still
  round-trip a coincidentally-matching JSON tree in a small fixture). No published audit score
  applies an equivalent per-position obligation check.
- **The correct summary is: plan-level rigor without execution, vs. execution without our
  specific structural rigor.** Neither subsumes the other. The router-level harness that would
  make both checks simultaneously is explicitly out of scope for M1 and is called out as the M1.5
  deliverable in every source document referenced here -- it does not exist yet.

| | planv2 (this branch) |
|---|---|
| Score | **152/181 in-scope** (29 known gaps), 21 skipped, plan-level |
| What's checked | plan shape + 3 planv2-original structural oracles (assertions 5-7); no scalar values |
| Who reports it | us, re-run live for this document |
| Suites failed | see Section 2 |

Cross-vendor comparisons against the published per-router audit results (which are executed,
router-level scores) are maintained outside this repository.

The 152/181 was re-verified live for this document:

```
cd v2 && go test ./pkg/engine/planv2/audit/ -run TestAudit_Corpus -count=1 -v
--- PASS: TestAudit_Corpus (0.16s)
AUDIT PLAN-LEVEL HEADLINE: in-scope 152/181 passed (29 known gaps), 21 skipped, 202 total transcribed
```

Zero `FAIL` -- everything that isn't a `PASS` is a registered `GAP` (known defect) or `SKIP`
(feature not yet built), matching `SCOREBOARD.md` exactly.

**Corpus note:** the Guild's audit ships 46 suites / 199 cases. Our fixture set carries 48 rows /
202 cases: the extra suites are `sibling-conflation` (2 cases), an in-house regression witness added
during the adversarial review to pin the buyer/seller and self-referential-friends counterexamples
permanently, and `interface-refinement` (1 case), an in-house witness added in the M1.5 IR+D6 wave --
neither is part of the Guild's published corpus. They are included in every table below and
flagged where they appear.

## 1. Per-suite table

> **Historical snapshot (98/135 epoch).** This per-suite breakdown predates the argument-lowering,
> requires-args, and IR+D6 waves (see the banner at the top); the live per-suite authority is
> `SCOREBOARD.md`. It is retained because the reason-bucket annotations document the pre-wave gap
> classification the Section 2 narrative builds on.

Legend for planv2's non-pass reason (root-cause buckets, defined fully in Section 2):
`ARG` = M1 field-argument/`@include`/`@skip` lowering gap (SKIP, out of scope).
`JUMP-A` = foreign-root: missing `@external` extension-key or base-field entity jump (GAP).
`JUMP-A2` = missing entity-jump / provider-split (`@requires`/`@provides`/`@interfaceObject`) (GAP).
`JUMP-B` = distributed abstract-member expansion (GAP).
`LEAF` = member-scoped leaf-coverage (GAP).
`CHAINED-REQ` = chained `@requires` across subgraphs, D7 scopes to jump-source only (SKIP).
`IFACE-OBJ` = `@interfaceObject` concrete-refinement / other single-feature gap (SKIP).

Per-vendor published-audit results and the per-suite cross-comparison previously tabulated
alongside this table are maintained outside this repository; the table below carries planv2's
own results only.

| Suite | Cases | planv2 P/G/S | planv2 reason |
|---|---|---|---|
| `abstract-types` | 18 | 3/3/12 | GAP: JUMP-A2x2, JUMP-Ax1 * SKIP: ARGx10, IFACE-OBJx2 |
| `child-type-mismatch` | 4 | 4/0/0 | full pass |
| `circular-reference-interface` | 2 | 1/1/0 | GAP: LEAFx1 |
| `complex-entity-call` | 1 | 0/0/1 | SKIP: IFACE-OBJx1 |
| `corrupted-supergraph-node-id` | 10 | 0/0/10 | SKIP: ARGx10 (`node(id:)`) |
| `enum-intersection` | 5 | 3/0/2 | SKIP: ARGx2 |
| `fed1-external-extends` | 4 | 2/1/1 | GAP: JUMP-Ax1 * SKIP: ARGx1 |
| `fed1-external-extends-resolvable` | 1 | 0/0/1 | SKIP: IFACE-OBJx1 |
| `fed1-external-extension` | 4 | 2/1/1 | GAP: JUMP-Ax1 * SKIP: ARGx1 |
| `fed2-external-extends` | 4 | 2/1/1 | GAP: JUMP-Ax1 * SKIP: ARGx1 |
| `fed2-external-extension` | 4 | 2/1/1 | GAP: JUMP-Ax1 * SKIP: ARGx1 |
| `include-skip` | 4 | 0/0/4 | SKIP: ARG (`@include`/`@skip`)x4 |
| `input-object-intersection` | 3 | 0/0/3 | SKIP: ARGx3 |
| `interface-object-indirect-extension` | 1 | 1/0/0 | full pass |
| `interface-object-with-requires` | 7 | 4/0/3 | SKIP: IFACE-OBJx3 |
| `keys-mashup` | 1 | 1/0/0 | full pass -- **DV-005** |
| `mutations` | 4 | 0/0/4 | SKIP: ARGx4 |
| `mysterious-external` | 2 | 1/1/0 | GAP: JUMP-Ax1 |
| `nested-provides` | 2 | 2/0/0 | full pass |
| `node` | 1 | 1/0/0 | full pass |
| `non-resolvable-interface-object` | 7 | 6/1/0 | GAP: JUMP-Ax1 (case-02) |
| `null-keys` | 1 | 1/0/0 | full pass |
| `override-type-interface` | 4 | 3/0/1 | SKIP: IFACE-OBJx1 |
| `override-with-requires` | 4 | 4/0/0 | full pass |
| `parent-entity-call` | 1 | 1/0/0 | full pass |
| `parent-entity-call-complex` | 1 | 0/0/1 | SKIP: ARGx1 |
| `partial-union` | 2 | 1/1/0 | GAP: LEAFx1 |
| `partial-union-complex` | 5 | 3/2/0 | GAP: LEAFx1, JUMP-A2x1 |
| `provides-on-interface` | 2 | 2/0/0 | full pass |
| `provides-on-union` | 2 | 2/0/0 | full pass |
| `requires-circular` | 2 | 1/0/1 | SKIP: CHAINED-REQx1 |
| `requires-interface` | 5 | 4/1/0 | GAP: JUMP-Ax1 |
| `requires-requires` | 5 | 0/0/5 | SKIP: CHAINED-REQx5 |
| `requires-with-argument` | 5 | 1/0/4 | case-01 PASS (**DV-006 VERIFIED**, plan level); 02-05 SKIP: distributed-@requires (separate model gap) |
| `requires-with-argument-conflict` | 1 | 1/0/0 | case-01 PASS (**DV-007 VERIFIED**, plan level): conflict split into separate `_entities` fetches (v1 HasArgumentConflictWith) |
| `requires-with-fragments` | 6 | 0/6/0 | GAP: LEAFx4, JUMP-Ax2 |
| `shared-root` | 2 | 2/0/0 | full pass |
| `sibling-conflation`(*) | 2 | 2/0/0 | full pass -- the flip's own witness |
| `simple-entity-call` | 1 | 1/0/0 | full pass |
| `simple-inaccessible` | 4 | 2/1/1 | GAP: JUMP-A2x1 * SKIP: ARGx1 |
| `simple-interface-object` | 13 | 7/2/4 | GAP: JUMP-A2x1, LEAFx1 * SKIP: IFACE-OBJx4 |
| `simple-override` | 2 | 2/0/0 | full pass |
| `simple-requires-provides` | 12 | 10/2/0 | GAP: JUMP-A2x2 |
| `typename` | 6 | 2/4/0 | GAP: LEAFx4 |
| `unavailable-override` | 2 | 2/0/0 | full pass |
| `union-interface-distributed` | 10 | 4/3/3 | GAP: JUMP-Bx1, JUMP-A2x1, LEAFx1 * SKIP: ARGx3 |
| `union-intersection` | 12 | 7/5/0 | GAP: JUMP-Bx3, JUMP-Ax2 |
| **Total** | **201** | **98/37/66** | |

(*) `sibling-conflation` is the in-house witness suite, not part of the Guild's 46; see the corpus
note in Section 0.

### Highlights

- **The three registered reference-divergence results all VERIFY at plan level: `keys-mashup`
  (DV-005), `requires-with-argument`/case-01 (DV-006), AND `requires-with-argument-conflict`/case-01
  (DV-007).** Each is an adjudicated level-3-exception divergence from the reference
  implementation's observed behavior -- see `DIVERGENCES.md` for the adjudications and upstream
  issue citations; planv2 plans each per the audit expectation. DV-006 renders the
  argument-bearing `@requires` (`price(currency: "USD")`) into both the source document and the
  `Requires` fragment. DV-007 -- now moved from intent to a verified plan-level PASS -- SPLITS the
  same-coordinate conflict (`shippingEstimate` price@USD vs
  `shippingEstimateEUR` price@EUR) across separate `_entities` fetches (v1 `HasArgumentConflictWith`),
  aliasing the two price selections in the shared source document and reading each representation's value
  from its aliased key.
- **planv2 closes the v1 planner's "strip fragments -> null" failure family: `provides-on-union`
  and `provides-on-interface` (both 2/2 clean), and `partial-union-complex` (3/5 genuine passes).**
  This is the family DIVERGENCES.md's D6 was built to avoid; the v1 planner exhibits exactly the
  bug D6 prevents.
- **`union-intersection` is the honest low point of this snapshot: 7/12**, with 5 registered GAPs
  (3 distributed abstract-member expansion, 2 foreign-root); `non-resolvable-interface-object`
  lands at 6/7 (case-02, class JUMP-A). (Both suites have since been closed -- see `SCOREBOARD.md`
  for the live numbers.)

## 2. What's missing, by root cause

Four buckets account for all 103 non-passes (37 GAP + 66 SKIP). Each entry below traces which
`DIVERGENCES.md`/`ADVERSARIAL_REVIEW.md` items track it, verified by grepping every
`expect-fail.txt`/`skip.txt` in `pkg/engine/planv2/audit/testdata/` rather than estimated.

### (a) Field-argument lowering -- LANDED in M1.5 wave 2 (was 48 SKIPs, the largest bucket)

**Status update.** This bucket is closed for the CLIENT-argument subset. The obligation tree now carries
each field's arguments and referenced variables (D3), and the fetch-document printer renders them,
declares the per-document variable set in the operation header, forwards values as ContextVariable Input
segments (the v1 contract), emits the `mutation` root keyword for mutation operations, and JSON-escapes
the document at Input assembly. `@include`/`@skip` need no lowering -- normalization resolves literal /
defaulted conditions before O(Q). The suites that flipped to PASS or entered the bar: `mutations`,
`corrupted-supergraph-node-id` (the concrete-field cases), `include-skip`, `enum-intersection`,
`input-object-intersection`, `parent-entity-call-complex`, and the argument cases of `simple-inaccessible`
/ `union-interface-distributed` / `abstract-types` (those with no additional routing gap). The ONLY
argument sub-family still SKIP is `@requires`-with-argument (`requires-with-argument` 5, DV-006;
`requires-with-argument-conflict` 1, DV-007): the D7 hypergraph parses requires selections field-name-only
and does not yet carry an argument-bearing requires into the representation nor split a same-coordinate
argument conflict (the A-3 oracle) -- a kernel-adjacent change deferred to a follow-up.

The historical description of the gap (retained for context):
D7 selection parsing and the fetch-document printer do not carry field arguments or
`@include`/`@skip` conditions. This is **not** merely "unplanned" -- SCOREBOARD.md is explicit that
if planv2 attempted these cases anyway, the printer would emit a fetch document with the argument
silently dropped: invalid when the argument is required, silently wrong when optional. That's the
same defect class (an emitted fetch that doesn't match what the client asked) as the wrong-route
bug the M1.5 flip closed, so these are held out of the plan-level bar rather than false-passed.

Suites: `corrupted-supergraph-node-id` (10, `node(id:)`), `mutations` (4), `include-skip` (4,
`@include`/`@skip`), `requires-with-argument` (5, DV-006), `input-object-intersection` (3),
`union-interface-distributed` (3), `abstract-types` (10), `enum-intersection` (2),
`requires-with-argument-conflict` (1, DV-007), `simple-inaccessible` (1),
`parent-entity-call-complex` (1), `fed1-external-extends` (1), `fed1-external-extension` (1),
`fed2-external-extends` (1), `fed2-external-extension` (1).

Tracked by: `DIVERGENCES.md` M1 Residual Register item 1; DV-006, DV-007 (both explicitly deferred
pending this). Fix path: implement argument lowering in D7/`lower` (owner: M1.5) -- this alone
would flip up to 48 SKIPs toward PASS (some will additionally need the routing fixes below).

### (b) Foreign-root / missing-jump routing -- 23 GAPs

The obligation-driven per-position lowering (the M1.5 "flip") is the shipping default and resolves
the requested response *positions* correctly, but the underlying model still lacks three edge
kinds Apollo and Hive have -- `research-notes/d10-path-routing.md` reverse-engineers exactly which
edges both routers' query graphs carry that ours doesn't:

- **JUMP-A (12 GAPs)** -- `@external` extension-key non-resolution / missing entity jump to the
  base-field subgraph. D7 pins key-field tails to the jump's source subgraph; Apollo's
  `handle_key` resolves key `conditions` by a recursive reachability search that can gather
  `@external` key fields from *any* subgraph on the path. Suites: `abstract-types` (1),
  `non-resolvable-interface-object` (1), `fed1-external-extends` (1), `fed1-external-extension`
  (1), `fed2-external-extends` (1), `fed2-external-extension` (1), `mysterious-external` (1),
  `requires-interface` (1), `requires-with-fragments` (2), `union-intersection` (2).
- **JUMP-A2 (7 GAPs)** -- owning-subgraph provider-split: `@requires`/`@provides` provider routing
  and `@interfaceObject` member-flattening not routed to their own `_entities` jump. Suites:
  `abstract-types` (2), `partial-union-complex` (1), `simple-inaccessible` (1),
  `simple-interface-object` (1), `simple-requires-provides` (2), `union-interface-distributed` (1).
- **JUMP-B (4 GAPs)** -- distributed abstract-member expansion: a member fragment (`... on Movie`)
  is emitted against a subgraph that doesn't declare that member; needs to be expanded only where
  the member is modelled, or co-resolved via a jump. Suites: `union-intersection` (3),
  `union-interface-distributed` (1).

Tracked by: `DIVERGENCES.md` M1 Residual Register item 2 (classes Ax12, A2x7, Bx4 -- matches
exactly); full mechanism and Apollo/Hive source citations in `research-notes/d10-path-routing.md`.
Fix path: `d10-path-routing.md`'s recommendation (d)-primary -- float D7 key-field tails off the
source subgraph, complete the entity-jump digraph, model interface-object jumps as D7 variants and
allow a `TypeMove` to compose after an `EntityJump`. The note estimates this closes 17 of 22 (its
count, pre-latest-reaudit) at "highest value, lowest risk," dead-coding the D10 fall-back entirely
on the remaining cases. Owner: M1.5/M2.

### (c) Member-scoped leaf-coverage -- 14 GAPs

The flip closed the *general* sibling/path-conflation class (Counterexample A, `order { buyer {
rating } seller { rating } }`, and the shipped false-PASS Counterexample B both now PASS). What
remains is narrower: a distributed response position whose different concrete members resolve in
different root fetch groups, which `posGroup` cannot yet own by a member-qualified key -- so a leaf
under one member either never gets selected (forward, 13 cases) or a fetch over-selects a sibling
member's leaf it shouldn't carry (backward, 1 case, `union-interface-distributed/case-07`).

Suites: `typename` (4), `requires-with-fragments` (4), `partial-union` (1),
`partial-union-complex` (1), `circular-reference-interface` (1), `simple-interface-object` (1),
`union-interface-distributed` (1, backward), `union-interface-distributed` case is the 14th
counted separately above.

Tracked by: `ADVERSARIAL_REVIEW.md` M1.5 wave-1 demand 2 (assertion 6, "does not yet catch the
residual SHARED-INSTANCE subset") and `DIVERGENCES.md` M1 Residual Register item 7 ("RESIDUAL (14
of the 37 GAPs)... 13x leaf-coverage forward + 1x path-correspondence backward"). Fix path: give
`posGroup` a member-qualified position key so Refine-member goals sharing one `segPath` are no
longer collapsed. Owner: M1.5/M2.

### (d) Everything else -- 18 SKIPs

Two sub-groups, both genuine unbuilt features rather than defects:

- **Chained `@requires` across subgraphs -- 6 SKIPs** (`requires-requires` 5, `requires-circular`
  1). D7 currently scopes a `@requires` clause's requirements to the jump-source subgraph only; a
  `@requires` chain spanning more than one hop isn't modelled yet.
- **`@interfaceObject` concrete-refinement / single-feature gaps -- 12 SKIPs**
  (`interface-object-with-requires` 3, `simple-interface-object` 4, `abstract-types` 2,
  `override-type-interface` 1, `fed1-external-extends-resolvable` 1, `complex-entity-call` 1).

Tracked by: `DIVERGENCES.md` M1 Residual Register item 3. Owner: M1.5.

**Reconciliation:** (a) 48 + (b) 23 + (c) 14 + (d) 18 = 103 = all 37 GAPs + all 66 SKIPs. No
non-pass case is unaccounted for.

## 3. Trust differential -- what our scoring measures that the published tables don't

- **The 7 plan-level assertions are ours, not the audit's.** Assertions 1-4 (plans, valid fetch
  documents, valid dependency order, response shape) are what any plan-level check would need.
  Assertions 5-7 (root-entry honesty, leaf-coverage/path-correspondence, response-path
  correctness) exist *because* an owner-commissioned adversarial review constructed a plan that
  every other oracle -- audit assertions 1-4, the differential harness, the brute-force search
  oracle, even the TLA+ model -- called correct while it silently dropped a requested field and
  fetched a foreign one. No published gateway audit applies an equivalent structural check; a
  router-level pass/fail on final JSON output can, in principle, hide the same class of bug behind
  a small fixture that happens to round-trip correctly.
- **The audit-authority adjudication found zero vendor-flavored cases in the expectations we test
  against.** `research-notes/apollo-vs-audit.md` checked every suite where the reference
  implementation's published result diverges from the audit's expectation and adjudicated each
  (`keys-mashup`, `requires-with-argument`, `requires-with-argument-conflict`) as a genuine,
  independently-corroborated reference-implementation defect -- not audit-author house style -- and
  confirmed the partial-union family's expected behavior (D6) against the reference
  implementation's own observed behavior, not only the audit's expectation. That means when this
  document says planv2 matches the audit's expectation, it isn't crediting a partisan test:
  multiple independent implementations agree with the expectation being upheld (the cross-vendor
  corroboration record is maintained outside this repository; see `DIVERGENCES.md` DV-005-DV-007
  for the adjudications).
- **Our gaps are findings, not hidden failures.** Every one of the 37 GAPs carries an
  `expect-fail.txt` with a precise, mechanism-level explanation (which class, why, what closes it)
  and the corpus test (`corpus_test.go`) *fails* if a GAP-marked case starts passing without the
  marker being removed -- the register can't silently drift stale in either direction. No published
  gateway audit result comes with an equivalent per-case, mechanism-cited paper trail; you get a
  glyph (PASS/FAIL), not a root cause.

## 4. Roadmap

| Remaining work | Cases unlocked | Milestone |
|---|---|---|
| Field-argument / `@include`/`@skip` lowering in D7 selection parsing + fetch-document printer | up to 48 SKIPs (some also need routing fixes below); unblocks DV-006/DV-007 verification | M1.5 |
| Float D7 key-field/`@requires` tails off the source subgraph + complete the entity-jump digraph into base-field owners (`d10-path-routing.md` d.1+d.2) | 17 of the 23 foreign-root/missing-jump GAPs (JUMP-A, most of JUMP-A2) | M1.5 |
| Model interface-object jumps as D7 variants + allow `TypeMove` to compose after `EntityJump` (`d10-path-routing.md` d.3) | remaining ~5-6 foreign-root GAPs (JUMP-A2 tail, JUMP-B) | M1.5/M2 |
| Member-qualified `posGroup` key (Refine-member goals no longer collapsed on shared `segPath`) | re-characterized (gaps + IR+D6 waves): of the 14, 10 were D3p typename-terminal (closed), 2 D6 over-narrowing (closed by D6p route-scoping / D3pp exempt-terminal promotion), 1 member-fragment liveness (closed); true residual = distributed abstract-member expansion (`union-interface-distributed/02,05,08`, `union-intersection/04,08,11,12`) | M2 |
| Chained `@requires` across subgraphs (D7 requirement scoping beyond the jump-source subgraph) | 6 SKIPs (`requires-requires`, `requires-circular`) | M1.5 |
| `@interfaceObject` concrete-type refinement + remaining single-feature gaps | 12 SKIPs | M1.5 |
| Router-level end-to-end harness (real subgraph servers, executed values) | closes the trust gap in Section 0 -- the check no plan-level table can make. **Landed + transport-lowered:** planv2 plans now EXECUTE against real subgraphs (DEFECT 2 resolved); executed 0 -> 18/27 on `TestFederationIntegrationTest`. Remaining executed gaps = one lowering class (single-vs-BATCH entity fetch under array positions), not transport -- see SCOREBOARD / m15-transport-report | M1.5 (harness + transport), BATCH class + Cosmo forward-port ongoing |
| `(+) = max` cost family, cross-branch optimizing MERGE (NP-hard boundary, off by default) | plan-quality regret cases (ADVERSARIAL_REVIEW decision 1), not corpus pass count | M2 |
| Subscriptions, `@defer` (both LANDED -- M3 subscriptions/defer waves, `D11.12`/`D11.13`; evidence lives in the differential + executed-truth harnesses, the corpus carries no such operations), non-GraphQL datasources | out of the audit corpus entirely | M3 |
| Lean machine-checked kernel | proof tooling, not corpus pass count | M4 |

When this roadmap was first written the headline was 98/135, and the arithmetic here projected
roughly 98 + up to 40 (argument cases minus routing-dependent overlap) + 17 ~ 155 in-scope passes
once (a) and (b) landed. That projection has since been borne out: argument lowering (a) landed in
M1.5 wave 2, the requires-args and IR+D6 waves followed, and the live headline re-verified above is
now **152/181 in-scope** -- in line with the ~155 estimate, including its caveat that newly-plannable
SKIP cases would surface some new GAPs (they did; the way assertion 6 did to the old 111/133
headline). The only number this document treats as load-bearing is the one re-verified live above:
**152/181, zero FAIL, 29 named GAPs, 21 named SKIPs.**


## Customer-corpus eval (private external corpus; counts only, no schema content)

Swept via the execution-config adapter (real production router configs + most-requested
operations; 7,920 operations, 221 graph versions, 97 graphs; both planners; report written
outside the repo per the privacy policy).

| bucket | initial sweep | after leaf-kind parity fix | after field-drop oracle fix (M15) |
|---|---|---|---|
| MATCH | 4,389 (55.4%) | 6,767 (85.4%) | **7,159 (90.4%)** |
| UNEXPLAINED | 2,935 | 557 (544 field-drops + 13 extra-field) | 165 (89 field-drops + 76 extra-field) |
| planv2-error | 587 (542 ErrNoValidPlan across 35 graphs; 37 subscriptions; 8 args) | unchanged | unchanged |
| v1-error | 9 | 9 | 9 |

The dominant divergence class (leaf scalar kind: enum/__typename node kinds) was a systematic
lowering-parity gap, fixed in one commit and verified by re-sweep.

The M15 field-drop root-cause wave then re-classified the 557 residual divergences. They were **not**
planner field-drops: every one is a member-gate EXPRESSION difference at an abstract (union/interface)
position where the client response is byte-identical. v1 redundantly repeats a member-gated leaf
(`... on Member { __typename }` / an interface field) on top of an ungated selection at the same
response key; planv2 dedups it to one selection. The differential response-shape oracle keyed leaves by
`respKey + gate` and reported the redundant copy as a divergence. Making the oracle coverage-aware for
leaves (an ungated / member-gate-union selection subsumes a same-shape gated duplicate at the same
response key) collapsed **392** cases to MATCH. A per-case diagnostic over all 7,920 operations found
**zero** response keys that planv2 drops entirely (`otherside=absent` count = 0): the residual 165 are
ungated-vs-per-member-partition expression differences whose response-equivalence is undecidable by a
plan-only oracle without the supergraph's member sets (registered -- DIVERGENCES.md DV-008). The
member-scoped leaf-coverage audit class (14 C-class GAPs) is a distinct fetch-document-coverage oracle
(assertion 6) and is unaffected by this wave; the audit headline is unchanged (126/177). Search
reachability for the 542 unplannable operations (foreign-root/missing-jump wave) is likewise unchanged;
62 of 97 graphs have zero unplannable operations.

## Executed-truth addendum (M1.5 router-level harness)

The differential sweep above is a PLAN-level oracle (v1-vs-planv2 response *shape*). The M1.5
router-level harness closed the executed-truth loop by running planv2 plans through the real
`execution/engine` against the real `execution/federationtesting` subgraphs. It surfaced two
runtime-contract defects that no plan-level oracle can see:

1. **`GraphQLResponse.Info` nil** (FIXED, commit `10c3b38a`) -- resolver nil-panic; not part of the
   compared response shape, so MATCH-classified operations still panicked at execution.
2. **No executable transport on planv2 fetches** (OPEN) -- `lower` sets no `FetchConfiguration.DataSource`
   and no `url`/`method` in the fetch `Input`; plans are shape-correct but non-executable. This is the
   headline executed-truth finding and the reason executed correct-responses is 0/22 despite plan-level
   152/181. Single systemic root cause, not per-case bugs.

Implication for this eval doc: the 7,159 MATCH classifications remain valid as *plan-shape* parity,
but "MATCH" does not imply "executes correctly" until DEFECT 2 is fixed. See SCOREBOARD.md
"Executed-truth results" for the live status. The harness session's full report (a process report
maintained outside the public tree) established the rest of the record: the harness is an env-gated
`PLANV2=1` seam in `execution/engine`'s `getCachedPlan` (deliberately uncommitted -- it is the test
vehicle, not a planv2 fix), which planned 22/24 `TestFederationIntegrationTest` scenarios (1
subscription + 1 "obligation unreachable" case fell back to v1); DEFECT 2's root cause is that
`lower` sets `DataSourceIdentifier` but never `FetchConfiguration.DataSource`, and emits fetch
`Input` without the `url`/`method` wire fields `Source.Load` reads -- a single systemic gap, not 22
bugs; and a direct Cosmo router integration was attempted first but walled on version skew (Cosmo
`main` pins engine v2.12.1 APIs absent from this branch's 2.11.0 base, with no 2.11.x-pinned Cosmo
revision to `replace` against).
