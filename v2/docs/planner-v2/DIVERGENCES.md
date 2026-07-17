# Divergence Policy & Register

## Why this document exists

Planner-v2's correctness bar cannot be "passes a vendor's test suite." The Guild's
federation-gateway-audit is authored by the same organization that builds Hive Router --
it is best understood as Hive Router's regression suite, not as a neutral specification
of Apollo Federation semantics. It remains a *valuable corpus* (broad, executable,
adversarial), but where its expectations encode Hive-specific planner choices rather
than spec-mandated behavior, following it blindly would make us a Hive clone, not a
correct federation planner.

Apollo is the de facto reference implementation of Federation (they define the spec,
the composition semantics, and the directive contracts). Where behavior is ambiguous,
Apollo's documented semantics and the `apollo-federation` reference implementation's
behavior carry more authority than any third-party suite. And where *we* believe both
are wrong, our formal model gives us something neither vendor has: a stated definition
of correctness (I1-I4 over the D1-D11 model) from which behavior is *derived* -- the
long-term ambition is that this document plus FORMAL_SPEC.md becomes reference-grade.

## Authority hierarchy

When expected behavior is contested, planner-v2 adjudicates in this order:

1. **GraphQL specification** (response shape, field merging, null propagation).
2. **Apollo Federation specification & composition semantics** (directive contracts:
   @key/@requires/@provides/@shareable/@external/@interfaceObject).
3. **Apollo reference implementation behavior** (`apollo-federation` planner + router),
   *except where it contradicts 1-2 or its own documented spec* (Apollo has shipped
   documented planner defects; being the reference does not mean being correct).
4. **Our formal model** (FORMAL_SPEC.md D1-D11, invariants I1-I4) -- which must itself
   be justified upward against 1-3; when the model forces a behavior, the derivation
   is the justification.
5. **The Guild federation-gateway-audit expectations** -- treated as a regression
   corpus. Deviations from it are acceptable *if and only if* justified by 1-4 and
   registered below.

A "199/199 audit pass" is therefore a *target*, not an axiom: if adjudication shows an
audit case encodes Hive-specific behavior that contradicts 1-3, we document the
divergence here, encode OUR expected behavior as the test, and accept < 199 with a
register entry. Silent divergence -- from Apollo, from the audit, or from v1 -- is the
only prohibited outcome.

## Divergence register

Every knowing behavioral divergence between planner-v2 and (a) Apollo's implementation,
(b) the Guild audit's expectations, or (c) the v1 planner gets an entry:

| ID | Scenario | planner-v2 behavior | Diverges from | Their behavior | Adjudication (authority level + reasoning) | Status |
|----|----------|--------------------|---------------|----------------|--------------------------------------------|--------|
| DV-001 | *(template)* | | | | | |
| DV-005 | `keys-mashup` audit suite | PASSES (per audit expectation) | Apollo Router/Gateway | Fails suite | Level-3 exception: genuine Apollo planner bug (apollographql/federation#2695 class), corroborated by multiple independent implementations passing (the cross-vendor corroboration record is maintained outside this repository) -- audit expectation upheld at levels 1-2 | **VERIFIED at Task 11 (plan level)**: planv2 plans the case, every fetch document validates against its subgraph schema, and the response shape matches (`planv2/audit/testdata/keys-mashup`). Router-level (executed) verification lands with the M1.5 harness |
| DV-006 | `requires-with-argument` audit suite | PASSES the argument-in-requires case (per audit expectation) | Apollo Router/Gateway | Fails | Level-3 exception: genuine Apollo bug (apollographql/federation#2996 class); same corroboration as DV-005 | **VERIFIED (plan level) for the argument-in-`@requires` case.** The requires-args wave made the D7 tokenizer argument-aware (`parseSelection` skips a field's `(...)` group whole) and carries the literal requires argument values on `Edge.Requires` (outside the A-3 tuple), re-rendered at lowering into both the source document and the `Requires` fragment. `case-01` (`shippingEstimate` @requires `price(currency: "USD") weight`; `isExpensiveCategory` @requires `category { averagePrice(currency: "USD") }`) now PASSES all 7 assertions -- every fetch document validates against its subgraph and the argument literal is present (`planv2/audit/testdata/requires-with-argument/case-01`; lower-package regression test `TestNewPathRequiresWithArgument`). Cases 02-05 remain SKIP for a SEPARATE, non-argument reason: a distributed `@requires` (`author` @requires `comments(limit: 3) { authorId }` where `comments` is local to subgraph d but `authorId` is @external, owned by c) that no single source subgraph can supply -- a multi-jump-requires model gap (per-case `skip.txt`). Router-level (executed) verification lands with the M1.5 harness |
| DV-007 | `requires-with-argument-conflict` audit suite | PASSES the conflict-split case (per audit expectation) | Apollo Router/Gateway | Fails suite | Level-3 exception: genuine Apollo bug; same corroboration. Note v1 planner also historically failed here (fixed in #1566) | **VERIFIED (plan level).** The same-coordinate CONFLICT split is now lowered: `shippingEstimate` @requires `price(currency: "USD")` and `shippingEstimateEUR` @requires `price(currency: "EUR")` select `Product.price` with different arguments, which one entity representation cannot carry. Mirroring v1 (`FederationFieldConfigurations.HasArgumentConflictWith`), lowering PARTITIONS the requiring fields across separate `_entities` fetches (`lower/obligation_driven.go splitArgConflictGroups`, greedy first-fit). Four components: (a) per-field requires association (`Edge.RequiresBy`, outside the A-3 identity tuple); (b) the conflict group-split; (c) source-document aliasing -- the shared source subgraph (b) selects `price(currency: "USD")` and `_planv2req_price_1: price(currency: "EUR")` so both coexist in one valid document; (d) representation value-path indirection (`repNode.readAs`) -- the EUR group's representation presents the field as `price` while reading the aliased response key. `case-01` PASSES all 7 assertions (`planv2/audit/testdata/requires-with-argument-conflict/case-01`; lower-package regression `TestNewPathRequiresArgumentConflict`). MERGE's `argsConflict` is the enforcing HasArgumentConflictWith predicate for the legacy node-keyed path. The A-3 edge-identity decision (FORMAL_SPEC D5) stays settled (hypergraph argument-blind; a D11 representation concern). Router-level (executed) verification lands with the M1.5 harness |

| DV-008 | Abstract-position member-gate EXPRESSION difference (customer-corpus M15 field-drop wave) | planv2 emits an abstract (union/interface) leaf ONCE -- ungated at the interface level, or member-gated without a redundant ungated twin | v1 planner | v1 repeats the leaf: an ungated `__typename`/interface field PLUS a redundant `... on Member { ... }` copy at the same response key, OR a full per-member expansion where planv2 stays ungated | Level-1 (GraphQL response shape): the client JSON is **byte-identical** -- an ungated selection already writes that response key for every concrete type, so the member-gated duplicate is redundant. planv2's dedup is the cleaner, equally-correct plan; forcing it to reproduce v1's duplicate would pessimize it with no client-visible benefit | **RESOLVED (oracle) for the subsumption direction; RESIDUAL registered for the partition direction.** The differential response-shape oracle keyed leaves by `respKey+gate` and over-reported the redundant copy. `CompareResponseShapes` is now coverage-aware for leaves (`leafGateCovered`, harness.go): an ungated or member-gate-union selection of the same shape at a response key subsumes a gated duplicate of that key. This collapsed **392** of 557 customer divergences to MATCH (sweep 6,767->7,159 MATCH). The remaining **165** are ungated-vs-per-member-**partition** expression differences (one plan selects the field ungated at the interface level, the other expands it per concrete member); a per-case diagnostic over all 7,920 operations confirmed **zero** total field-drops (`otherside=absent`=0) -- nothing vanishes from any response. Deciding response-equivalence for the partition direction requires the supergraph's member set at each position (to prove a per-member gate set equals "all members"), which a plan-only oracle does not have; a schema-aware coverage expansion is the identified follow-up (owner: M1.5/M2 harness). This class is DISTINCT from the member-scoped leaf-coverage audit GAPs (item 7 / EVAL_VS_AUDIT Section 2c), which are a fetch-document-coverage concern (assertion 6), not a response-shape one |

| DV-009 | Response FIELD ORDER under interface-fragments-on-abstract (M2 class-D wave, executed-truth) | planv2 renders response keys in GraphQL-spec CollectFields order: a field's response position is its FIRST occurrence in selection-set traversal (`... on Comment { upc } ... on Question { body }` => a Question renders `upc` before `body`; `... on Store { ... on Sale { product { name } } } ... on Sale { product { upc } }` => `product` renders `{name, upc}`) | v1 planner | v1's abstract-selection rewriter regroups interface fragments into per-member fragments, merging into any EXISTING member fragment -- so the member's explicitly-selected fields precede the interface-fragment-derived ones (Question renders `body` before `upc`; `product` renders `{upc, name}`) | Level-1 (GraphQL spec Section ExecuteSelectionSet/CollectFields): the spec orders a grouped field set by first occurrence in traversal order -- planv2's order IS the spec order; v1's is a rewriter artifact. VALUES are byte-identical (verified: the only diffs in the three executed-truth scenarios are key order) | **FINAL -- ADJUDICATED BY OWNER (2026-07-16).** planv2 emits the GraphQL-spec CollectFields response-key order; v1's order is a rewriter artifact, and planv2 does not reproduce it. Witnesses: `TestFederationIntegrationTest/{Complex_nesting, More_complex_nesting, More_complex_nesting_typename_variant}` -- all three execute with correct, complete data through planv2 (previously 422s, the class-D defect) and return byte-identical DATA to the v1-generated goldens, differing ONLY in JSON object key order, which carries no semantics. The harness's byte-exact comparison therefore reports them failed; executed truth is reported as **24/27 byte-exact + 3 adjudicated-order-divergent -- functionally 27/27** (not 27/27 byte-exact). This is a permanent, intended divergence: matching v1's bytes would mean reproducing its rewriter artifact against the spec order |
| DV-011 | EDFS (`@edfs__*`) event-source root entrances -- the metadata-only-subscription-root residual, rescoped by the M4.2 EDFS wave | planv2 treats an EDFS event source (a Kafka/NATS-backed root field with no HTTP upstream SDL) as a FIRST-CLASS ROOT ENTRANCE: the composed schema is the authority for the event root's output type (`FORMAL_SPEC.md` `D5-EDFS`), and the trigger transport is the pub/sub broker binding (`D11.12-EDFS`). The composed-schema payload-typing fallback fires ONLY under positive EDFS evidence; a metadata-declared root field with no EDFS evidence and no SDL-resolvable output type STAYS fail-loud `ErrNoValidPlan` (`FS-EDFS-5`) | the pre-M4.2 planner, which failed loud on ALL metadata-only subscription roots (the `SubscriptionMetadataOnlyFailsLoud` residual), and the adapter, which SKIPPED all non-GRAPHQL datasource kinds | v1 plans EDFS roots via the router's pubsub datasource (out of this repo); the pre-M4.2 planv2 refused/failed-loud on the whole class regardless of EDFS evidence | Level-4 (our model): an event stream carries no SDL, so the composed schema is the only defensible authority for the payload type (`FS-PLAN-1`); rescuing ONLY the EDFS-evidenced case (not blind schema drift) keeps the no-silent-degrade guarantee (`FS-PLAN-6`) | **SPEC LANDED; IMPLEMENTATION PARTIAL (M4.2).** The FS-EDFS propositions (1-5), `D5-EDFS`, and `D11.12-EDFS` are landed and gated (docscheck + conformance coverage green). The drift fail-loud case is UNCHANGED and still pinned (`TestPlanner_SubscriptionMetadataOnlyFailsLoud`). NEEDS THE COORDINATOR'S REAL-ROUTER CHECK: the exact `customEvents` execution-config shape and the pub/sub trigger wire envelope are router-side (not in this repo) -- the adapter PUBSUB decoding and the `D11.12-EDFS` transport encoding are reconstructed from the nodev1 proto and MUST be validated against the live router before the class is marked RESOLVED. A generating EDFS conformance family is the registered follow-up (blocked on the same real-router config shape) |
| DV-010 | M4.1 typed-loud sweep OVER-REFUSED non-semantic optimization flags (`MinifySubgraphOperations`, `EnableOperationNamePropagation`) -- caught by the clean per-commit real-audit adjudication | planv2 ACCEPTS these configs and plans, emitting an unminified / unnamed but semantically identical subgraph document (results byte-identical; the optimization/observability nicety is simply not applied) | v1 planner (which minifies / propagates the operation name) and the M4.1 refuse.go that refused them | v1 emits a minified subgraph document and propagates the client operation name; the M4.1 sweep refused both with a typed sentinel, forcing the router seam to fall back to v1 | Level-1 (GraphQL response shape): responses are byte-identical whether or not the subgraph query string is minified or its operation is named -- neither is a result. The M4.1 sweep classified these as MISSING-LOUD (refuse) but they are NON-SEMANTIC (accept). CRITICAL SEVERITY: `MinifySubgraphOperations` is `envDefault:"true"` in the Cosmo router (`cosmo/router/pkg/config/config.go`), ON in EVERY default deployment, so the refusal made `NewPlanner` reject EVERY real config and disabled planv2 for 100% of operations. The refuse philosophy now distinguishes SEMANTIC divergence (response changes -> MUST refuse) from NON-SEMANTIC (response identical -> MUST NOT refuse) | **RESOLVED (M4.1.1).** Both refusals removed from `refuse.go`; the two sentinels retired (`ErrMinifySubgraphOperationsNotSupported`, `ErrOperationNamePropagationNotSupported` -- no external `errors.Is` users). The flags moved to refuse.go's package-doc "Deliberate NON-refusals" list with the results-identical justification. Proven results-identical by the clean per-commit real Guild audit: at `b7b8e1f0` planv2 PLANNED these suites WITH `MinifySubgraphOperations` set and scored 188; at `e1a86364` (where refuse.go was added) planv2 refused everything and the audit collapsed to the stock-v1 result (185/199, all ops v1-fallback). Red-first guard `TestNewPlanner_AcceptsRealRouterDefaults` (a config mirroring router defaults MUST be accepted) failed on the M4.1 refuse.go and passes after; the SEMANTIC-refusal precision tests (Types-rename, SubscriptionFilterCondition, ComputeCosts, etc.) stay green. Actually minifying / propagating remains an M4.6 quality follow-up. **(Coordinator annotation, M4.4/M4.5: this cell records the M4.1.1 state as-authored per the no-history-rewrite rule; two of the examples cited have SINCE been IMPLEMENTED and no longer refuse -- ComputeCosts landed M4.5 (DV-012), SubscriptionFilterCondition landed M4.4 -- so the still-refused semantic set at current HEAD is Types-rename, CustomResolveMap, FetchReasons/ValidateRequiredExternalFields, FieldConfiguration.Path, UnescapeResponseJson, ArgumentRendering, DirectiveRenames.)** |
| DV-012 | IBM cost (`ComputeCosts`) per-field datasource ATTRIBUTION under a shared federation entity (M4.5 cost wave) | planv2 attributes each field's `@cost` weight to the ONE route the search chose to resolve it, and its `CostCalculator.EstimateCost`/`ActualCost` price that single route | v1 planner's `CostCalculator` | v1's cost tree records EVERY planner that touched a field (`CostVisitor.getFieldDataSourceHashes` returns `fieldPlanners[fieldRef]`, a list) and SUMS each datasource's cost config for that field; for a shared entity resolvable on N subgraphs this charges the default object weight N times, while planv2 charges it once | Level-4 (our model), Level-1-neutral: the IBM cost spec prices the fields the client requested; charging a single client-selected `me: User` once per resolving subgraph is a v1 sum-over-planners artifact, not a spec requirement. planv2's single-route cost is the SAME non-empty subset already adopted for `FieldInfo.Source` (M4.1) -- an entity's weight comes from the subgraph that resolves it. For the common case (a field's weight defined on its resolving subgraph, no duplicate default-object double-count) the estimate is IDENTICAL to v1 | **LANDED (M4.5).** Cost is POST-PLAN (attached via `plan.SetCostCalculator`, never steering plan selection -- the search kernel and its I3 tree-cost are untouched), reusing v1's exact cost machinery via `plan.BuildCostCalculator`; only the per-field datasource attribution is planv2's (route the search chose). Single-subgraph parity is byte-identical to v1 (`TestCost_Witness_SingleSubgraph`, `TestCost_DifferentialParity`: field/type/list-size weights). The shared-entity subset is a PINNED WITNESS: `TestCost_DifferentialParity_Federated` asserts planv2 = 8 (single route) and v1 = 9 (sum-over-planners; the extra +1 is the shared `User` default object weight), and that planv2 never exceeds v1. Router cost-limit enforcement (`ValidateSliceArguments` + `EstimateCost`/`ActualCost`) now sees a working calculator instead of nil |
| DV-013 | gRPC/ConnectRPC datasource transport emission (M4.3) -- the live RPC transport is not part of `plan.Configuration` | planv2 builds a per-fetch executable `grpc_datasource.DataSource` from the config's mapping/compiler/upstream-SDL/federation-configs (reusing `grpc_datasource.NewDataSource` unchanged), but sources the live RPC transport (gRPC client connection / Connect transport) from `planv2.Config.GRPCTransports`, an embedder-supplied input keyed by subgraph name -- NOT from `plan.Configuration` | v1 planner, which obtains the RPC transport from the datasource `Factory` (`NewFactoryGRPC(grpcClient)`) inside `ConfigureFetch` | v1's `Factory` holds the `grpcdatasource.RPCTransport`, and `Planner.ConfigureFetch` reads it (`p.rpcTransport`) to build the `DataSource`; the transport travels with the factory, not the config | Level-4 (our model), forced by an interface boundary: the `Factory` is stored in an unexported field of `plan.dataSourceConfiguration` and is NOT reachable through the `plan.DataSource` interface planv2 is handed -- the identical limitation already documented for the HTTP `http.Client` and the subscription client (`planv2/transport.go` package doc). planv2 therefore reconstructs the executable `DataSource` from the config surface it CAN read and takes the one unreachable runtime dependency (the transport) as an explicit `NewPlanner` input. When no transport is supplied the fetch is still executable machinery (RPC plan compiled) and `Load` fails typed ("requires an rpc transport"), never a silent shape-only stub | **LANDED (M4.3).** Pure lowering/transport (class (b), PARITY.md Section 1): the search kernel is untouched. `buildTransportTable` attaches a `lower.GRPCFetchFactory` per gRPC subgraph; the former `ErrGRPCDatasourceNotSupported` refusal is removed. `@connect__fieldResolver` (field-level RPC) rides the same surface for free (`GRPCMapping.ResolveRPCs` passed through, resolved inside `grpc_datasource.Load`). EXECUTED-TRUTH in-repo: `TestNewPlanner_GRPCFetchExecutesRealOutput` drives the emitted fetch's DataSource through the `grpc_datasource` bufconn harness and asserts the mock service's real data; `TestNewPlanner_GRPCFetchNoTransportLoadsError` pins the typed no-transport degradation. NEEDS THE COORDINATOR'S REAL-ROUTER CHECK: how the router supplies per-subgraph `RPCTransport`s to the planv2 facade (the external adapter that bypasses `NewPlanner`), and execution against a live upstream gRPC/Connect server, are router-side and MUST be validated before the class is marked fully RESOLVED |

*(DV-002-DV-004 reserved; not used -- the anticipated D6 partial-union divergence did not
materialize, see below.)*

Entries are added by the task that discovers the divergence (differential harness,
audit corpus, or external corpus runs) and adjudicated before the owning milestone
closes. An entry with an unresolved adjudication blocks the milestone.

## Contested-territory adjudications (research: `research-notes/apollo-vs-audit.md`)

- **Partial-union narrowing (D6) -- CLOSED, no divergence.** Apollo Router passes the
  entire partial-union family (`partial-union`, `partial-union-complex`,
  `union-intersection`, `provides-on-union`) with the same null-exclusive-members
  semantics the audit expects and planner-v2's D6 derives. External gateways disagree
  on parts of this family (see the authority hierarchy above; the cross-vendor
  observation record is maintained outside this repository) -- evidence the suite's
  expectation here is not vendor-preferential. D6's level-1/2 justification stands,
  corroborated at level 3.
- **Reference-divergent audit cases -- adjudicated, none contested.** Every audit suite
  where the reference implementation's published result diverges from the audit's
  expectation adjudicates as a genuine reference-implementation planner defect
  (level-3 exception), not a vendor-flavored expectation: see DV-005-DV-007. Zero
  (b)-type cases found. The audit's expectations are therefore usable as test oracles
  for all 199 cases, with the register recording that passing DV-005-DV-007 means
  knowingly diverging from the reference implementation's observed behavior.
- **v1 calibration:** Cosmo Router scores 91.96% (9 failing suites); its
  partial-union-family failures are genuine regressions the v1 planner exhibits
  (strip-fragments->null class), further motivating planner-v2's derived semantics.


## M1 Residual Register (consolidated carry-forward to M1.5 / M2)

The single findable list of everything M1 knowingly defers. Each item names its owning artifact.

> **M1.5 wave-1b THE FLIP (landed).** The obligation-driven per-position lowering is now the shipping
> default (`lower.Lower`; legacy node-keyed path behind `LowerConfig{LegacyNodeKeyedGrouping}` for one
> release cycle). The **sibling / path-conflation class is RESOLVED** for the general case (see item 7);
> the audit headline rose **65/135 -> 98/135** (37 GAPs) and all 70 expect-fail markers were reconciled
> (33 stale removed, 37 rewritten with post-flip reasons). The residuals below are updated to the
> post-flip reality.

**Correctness residuals (flip audit GAPs/SKIPs to PASSes when closed):**
1. **Field-argument lowering** -- LARGELY CLOSED (M1.5 wave 2 + requires-args wave). Client field
   arguments, forwarded variables (ContextVariable Input segments), `@include`/`@skip` (resolved by
   normalization), mutations, and fetch-document JSON escaping landed in wave 2; the requires-args wave
   added argument-in-`@requires` RENDERING (D7 tokenizer argument-aware; literal requires values on
   `Edge.Requires`; re-rendered at lowering into the source document and `Requires` fragment) -- DV-006
   `case-01` VERIFIED at plan level. The same-coordinate `@requires` argument-CONFLICT split (DV-007) is
   now ALSO VERIFIED at plan level: lowering partitions the conflicting requiring fields into separate
   `_entities` fetches (v1 `HasArgumentConflictWith`), with per-field requires association
   (`Edge.RequiresBy`), source-document aliasing, and a representation value-path indirection
   (`repNode.readAs`). REMAINING (one distinct gap): DISTRIBUTED `@requires` whose selection spans two
   subgraphs (`requires-with-argument` cases 02-05) -- a multi-jump-requires model gap, NOT an argument
   concern. A-3 settled in FORMAL_SPEC D5 (hypergraph argument-blind). Owner: M1.5 follow-up.
2. **Foreign-root / missing-jump model gaps** (classes A-D per `SCOREBOARD.md`): missing root-level /
   base-field entity jumps and @external extension-key non-resolution (class A1), owning-subgraph
   provider-split -- @requires/@provides and @interfaceObject member-flattening not routed (A2),
   distributed abstract-member expansion emitting a member fragment against a subgraph that does not
   declare it (class D -- **CLOSED, M2 class-D wave; see below**). Closing the rest makes the D10
   fall-back dead code and restores the strict path-consistency reading. Owner: M1.5/M2.
   - **CLASS D LANDED (M2 class-D wave, `D6pp` + `D11.7` + `D11.8`).** The distributed
     abstract-member-expansion class is closed: member narrowing is judged POSITIONALLY (`D6pp`
     position-possible member sets -- dead members exempt regardless of entity-ness, the abstract-C
     possibility fix un-narrows interface refinements, distributed members stay cover requirements),
     lowering keys grouping/attribution by MEMBER-QUALIFIED position keys (`D11.7` -- member leaves
     re-root into the declaring subgraph's fetch with `... on Member` wrappers materialized and keys
     injected inside the member scope), and same-key member response variants stay sibling fields
     with postprocess owning the depth-correct fold (`D11.8` -- the lowering-side merge silently
     dropped member fields at runtime). Audit headline **152/181 -> 161/184** (29 -> 23 GAPs; all
     7 class-D gaps PASS: `union-interface-distributed/02,05,08`, `union-intersection/04,08,11,12`;
     `union-intersection/case-09` (class B) and `override-type-interface`'s dead-member shapes ride
     along; `abstract-types/case-17,18` surfaced SKIP->GAP as newly-plannable class-C instances).
     Executed truth **20/27 -> 24/27**: `Abstract_object{,_nested,_nested_reverse}` and
     `Union_response_type_with_interface_fragments` now execute correctly; the 3 remaining are the
     DV-009 order-only divergence (values byte-identical). D10 fallback register 28 -> 14 distinct
     goals. RESIDUAL (owner: M2 follow-up): the `D10` member-scoped kappa mask is SPECIFIED but its
     realization was measured and deliberately deferred -- it changes no audit outcome, but exposes
     one pre-existing silent path-inconsistent @provides-scope route on a PASS witness
     (`circular-reference-interface/case-02`, plans byte-identical) which would GROW the frozen
     register set (forbidden this wave), and splits `union-intersection/case-07` into a
     correct-both-ways second fetch. Until it lands, a DISTRIBUTED member served by a sneak walk
     into a subgraph where it is impossible remains a theoretical hole no corpus/executed case
     exercises (member leaves on inconsistent walks inherit the parent position's group, which
     `D6pp` guarantees valid for every non-distributed member).
   - **CLASS A1 LANDED (search-reachability wave, `D5p` key-tail float).** The `@external`
     extension-key non-resolution subclass is closed: an `@external` field that is a `@key` field is
     now locally producible (it rides in the entity representation -- `FORMAL_SPEC` D5p, `PROOFS` L6p),
     so the `EntityJump` out of the extension subgraph fires instead of falling to the D10 foreign-root
     mask. Audit headline **126->136 in-scope, 51->42 GAPs**: 9 GAP->PASS flips (`fed1`/`fed2`
     `-external-extends`/`-external-extension` cases 01/03, `mysterious-external/case-01`) plus 1
     SKIP->PASS (`fed1-external-extends-resolvable/case-01`, previously unplannable). No regressions
     (full planv2 suite `-race` green; only the expected "now PASSES" reconciliations). REMAINING in
     this class: A2 provider-split and class-D distributed-member expansion, plus the base-field
     entity-jump (A2/B) and interface-object routing (C) constructions -- still GAP, still fall-back.
   - **CLASS E LANDED (search-reachability wave, `D5pp` renamed-root-type descent) -- CUSTOMER-DOMINANT.**
     A federation subgraph that renames its root operation types (`schema { query: AcmeQuery ... }`) still
     has its root fields listed under the composed name (`Query`) in the router node metadata; the
     builder resolved a root field's composite output type by looking that name up in the subgraph's own
     SDL, found nothing, and silently dropped the root field's `Descent`, orphaning every type reachable
     only through a root field (the whole selection under a root-returned type becomes unplannable).
     `FORMAL_SPEC` D5pp / `PROOFS` L6pp resolve the output type under the subgraph's real root type name
     (a pure `Descent`-edge addition; monotone-additive; kernel/settle untouched). This was the
     DOMINANT customer-corpus unplannable class -- NOT the audit's foreign-root classes A-D (the audit
     subgraphs use standard root names, so the audit scoreboard is unchanged). Private external-corpus
     sweep: **`ErrNoValidPlan` 542 -> 166 (-376, 69%)**, MATCH +372, zero new response-shape drops
     (field-dropped count unchanged; the small extra-field delta is the pre-existing benign
     dedup/member-gate class, DV-008, now surfacing on newly-plannable operations).
   - **INTERFACE-REFINEMENT LANDED (IR+D6 wave, `D3ppp` member fallback) -- the residual real-blocker
     class.** A field goal under `... on U` where U is a mixin interface never returned directly
     ((U,s) orphan in H) was a hard `ErrNoValidPlan` even though the concrete member the parent
     instance is IS reachable and declares the field -- the ~13 remaining v1-OK customer blockers
     after D5pp. `FORMAL_SPEC` D3ppp augments cand(g) with the members' reachable field nodes, gated on
     primary-cand-all-unreachable + refinement context + U-non-entity + non-exempt (fires only on
     goals that currently fail; kernel/settle/cost untouched). Jointly, lowering's refinement gate now
     expands an ABSTRACT member fragment's OnTypeNames to concrete implementers (the resolver matches
     gates byte-exactly against the runtime `__typename`, so an interface-named gate never matched and
     silently nulled -- a genuine wrong-data class this wave closed). Private sweep: `ErrNoValidPlan`
     167->156, MATCH 7531->7541, **field-dropped 89->83 (net safer than baseline)**: 8 pre-existing
     field-drops -> MATCH, 7 -> benign extra-field, ~10 D3ppp-planned ops report field-dropped ONLY via
     the oracle's exact-(key+gate) identity for OBJECT fields -- v1 emits per-member gated copies with
     identical subtrees, planv2 one ungated copy; same client JSON (the DV-008 partition direction,
     object-field variant; the object-field subsumption extension is the identified oracle follow-up).
   - **D6p ROUTE-SCOPING LANDED (IR+D6 wave) -- ADVERSARIAL_REVIEW demand-3b CLOSED.** P(g) for the D6
     member-narrowing intersection is now route-scoped (optimistic H-reachability filter on the
     parent field's resolution nodes), not schema-scoped: an unreachable @shareable producer no
     longer folds its narrower member set into Intersect and nulls members the only real route resolves
     (`partial-union/case-02` GAP->PASS). `D3pp` additionally promotes an all-narrowed composite to a
     typename-terminal coverage goal, gated on the narrowing being reachable-exempt (a genuine D6
     null, never the interface-unreachable class) -- `partial-union-complex/case-03` GAP->PASS.
   - **TYPED-LOUD FALLBACK LANDED (M2 D10 disposition) -- the fall-back no longer fires silently.**
     Every firing is a typed `search.Result.RouteFallbacks` record (goal coordinate, branch --
     root-pin vs scoped-walk, serving node/subgraph, route taken), threaded through
     `planv2.PlanWithDiagnostics` as Warn-level `D10_ROUTE_FALLBACK` diagnostics by default
     (FORMAL_SPEC `D10` amendment -- typed-loud fall-back). Measured at landing: **28 distinct
     fallback-using goals / 39 events across 18 audit cases** -- 15 expect-fail GAP cases (34 events)
     and **3 PASS cases (5 events)**: `interface-object-with-requires/case-01`,
     `provides-on-interface/case-02`, `union-interface-distributed/case-01`. The three PASS
     witnesses FALSIFY the pre-measurement premise that a fallback-using goal always surfaces as a
     plan defect: post-flip, lowering places selections from O(Q), so a path-inconsistent kappa walk
     does not necessarily reach the wire -- all three emitted plans are correct (assertions 1-7
     hold; inspected fetch documents enter the requested roots / entity fetches; e.g.
     `union-interface-distributed/case-01` falls back through `Query.node` at search level yet
     emits `query { products { __typename ... on Node { id } } }`). The audit gate is therefore
     TWO-TIER (`audit/fallback_register_test.go`): plan-level correctness stays governed by
     assertions 1-7, the exact set of PASS cases with any search-level fallback is FROZEN at those
     three (a new firing fails loud; a witness dropping out must shrink the frozen set in the same
     commit), and the distinct-goal count is PINNED -- 28 at landing, **re-measured 14 after the M2
     class-D wave's D6pp dead-member exemption** (21 events across 11 cases -- 8 GAP, 3 PASS; a dead
     member is no longer a goal, so its fallback-served route disappears: the designed shrinkage
     direction; the three frozen PASS witnesses are unchanged), and **re-measured 12 after the M2
     class-C wave's D7p interface-node jump heads** (17 events across 9 cases -- 7 GAP, 2 PASS: the
     interface-level entity jump gives the interface-object interface-field goals path-consistent
     routes, so `simple-interface-object/case-01` flips GAP->PASS with no fallback and the frozen
     witness `interface-object-with-requires/case-01` stops firing -- removed from the frozen set
     in the same commit), and **re-measured 6 after the same wave's D3pppp member expansion + D10
     chain-layered consistent trace** (11 events across 6 cases -- 4 GAP, 2 PASS): the
     abstract-types provider-split family and the member-scoped leaf-coverage residual
     (`partial-union-complex/case-05`) now route path-consistently. What remains is the genuine
     foreign-root class A/B -- `non-resolvable-interface-object/case-02` (an honest-reject case the
     root-pin fall-back rescues into a wrong plan; closing it is a fall-back-retirement decision,
     not a missing jump), `requires-interface/case-02`, `requires-with-fragments/case-02,03` --
     plus the two frozen witnesses. Events stay informational.
     **Re-measured 3 after the M2 requires-chain wave (D7pp requires-scoped resolution)** -- 5 events
     across 3 cases (1 GAP, 2 PASS): `requires-interface/case-02` and
     `requires-with-fragments/case-02,03` flipped GAP->PASS (the per-requires scoped jump replaces
     the all-or-nothing ride-along, so the requires field's route exists and is path-consistent;
     their expect-fail markers were removed in the same commit). The register's remaining users are
     `non-resolvable-interface-object/case-02` -- the fall-back RETIREMENT policy case -- and the two
     frozen benign witnesses. The retirement decision is now gated on exactly one GAP case plus the
     unmeasured customer-corpus figure.
     **Re-measured 2 after the M2 D10-narrow mini-wave (provable-non-resolvability narrowing,
     FORMAL_SPEC D10 amendment)** -- 3 events across 2 cases (2 PASS, 0 GAP): the fall-back is now
     NARROWED, not retired. A goal whose every candidate is PROVABLY non-resolvable -- every @key
     declaration heading the candidate's type in its subgraph carries `resolvable: false` (recorded
     as builder metadata, `Hypergraph.OnlyNonResolvableKeys`), and the candidate is structurally
     reachable only through its own subgraph's operation roots -- is no longer rescued: both firing
     sites (root-pin `search/search.go`, scoped-walk `search/scope.go`) share the guard
     (`search/nonresolvable.go`) and fail loud with `ErrNoValidPlan` instead.
     `non-resolvable-interface-object/case-02` (an expected-errors case: `field` lives only on b's
     `Node @key(id, resolvable: false) @interfaceObject`, so the reference gateway cannot fetch it
     either) flipped GAP -> PASS by erroring honestly; its goal leaves the register, whose remaining
     users are EXACTLY the two frozen benign witnesses. The narrowing is CONSERVATIVE by
     construction -- suppression requires schema-level proof over ALL candidates; a resolvable key,
     a keyless type (the class-A/B missing-jump premise), absent metadata, or any non-root
     structural entry (parent descent, TypeMove) keeps the fall-back byte-identical.
     **CUSTOMER-CORPUS RELIANCE (measured 2026-07-16; retirement-gate input).** The private
     customer corpus RELIES on the fall-back -- the reason this wave NARROWS instead of retiring:
     79 ops / 608 events at the post-class-C measurement point, 72 ops / 547 events at `70e1e442`
     (post-requires-chain + fix); byStatus at the latter: MATCH=30 (v1-identical plans),
     UNEXPLAINED=39, v1-error=3. The narrowing predicate's conservatism is what protects those 72
     ops (their shapes are the resolvable/missing-jump class the guard never touches); the owner
     re-verifies against the customer corpus after each register move.
     Follow-ups (owner: M2): per-event WIRE-ATTRIBUTION (`ReachedWire`) was assessed and
     skipped as not-cheap -- lowering would have to report per-edge document provenance back onto
     the search output, a cross-layer contract change; register it here instead of guessing.
     RETIREMENT GATE (restated per the amendment; unchanged by the narrowing): delete the fall-back
     and fail loud when the audit-corpus distinct-goal count AND the customer-corpus fallback count
     are both zero -- currently 2 (frozen witnesses) and 53 ops / 455 events respectively.
     CUSTOMER-CORPUS EVIDENCE TRAIL (2026-07-16, coordinator-run private sweeps; corpus reached
     only via `PLANNER_V2_EXTERNAL_CORPUS`): four consecutive sweeps across the night's waves --
     post-class-C, post-requires+type-cycle-fix (`70e1e442`), post-D10-narrow (`ac2e6ab0`), and
     post-D7ppp full-marks (`1daf64e1`) -- returned byte-identical plan-status counts (MATCH 7549,
     UNEXPLAINED 164, planv2-error 199, v1-error 8): zero customer-visible change while the audit
     went 152->202. Fallback reliance fell monotonically as model gaps closed: 79 ops/608 events ->
     72/547 -> 53/455 (final; byStatus MATCH=11, UNEXPLAINED=39, v1-error=3) -- the designed
     retirement trajectory, observed in production shapes.
3. **@interfaceObject concrete refinement -- CLOSED (M2 class-C wave).** The former 9 audit SKIPs
   plan and PASS: D7p interface-node jump heads (interface-level entity jumps out of an
   @interfaceObject position), D3io member-flattening candidates (a member-refined field served by
   the interface-object subgraph), D11.9 flattened placement (the member fragment never prints
   against a subgraph that lacks the member type), and D3pppp member expansion (a bare interface field
   locally unresolvable at its position fans out per position-possible member -- the abstract-types
   provider-split family, 14 GAPs -> PASS, via the D10 chain-layered consistent trace for the nested
   positions). TWO REGISTERED RESIDUALS remain, both runtime-level (plan-level oracles hold):
   - **C-disc (interface-object runtime discriminator/representation rewrite).** A member-gated
     selection flattened onto an interface-object resolves its gate against the runtime
     `__typename`, which the interface-object subgraph reports as the INTERFACE name; and a
     concrete-head jump departing an interface-typed source builds a representation whose
     `__typename` the source cannot supply concretely. v1 owns this with its runtime
     entity-interface representation/typename rewrite, which planv2's lowering did not then have --
     the member-knowing discriminator jump (fetch `__typename` from an interface-declaring
     subgraph) was not yet synthesized as a goal.
     **CLOSED (M3 executed-encoding wave).** Two mechanisms: the interface-object
     representation gate/`StaticString` rewrite + concrete re-discrimination (`2bc2e15d`), and
     discriminator-jump SYNTHESIS for positions where no fetched subgraph knows the members
     (`db6e9887` -- `lower/obligation_driven.go`, head = the interface node itself,
     non-@interfaceObject, key tails source-local, deterministic smallest-EdgeID pick).
     Executed-truth evidence: all 16 real-Guild-audit C-disc failures pass (149->188 wave,
     m3-encoding-wave-report Section class-1), reproduced in-repo by the exec-sync harness
     (`differential/exec_sync_cases_test.go`), verified live in the M3 final review.
     Honest scope note (M3 final review M-2): the synthesis' redundancy suppression -- an existing
     jump at the same entryPath into any `entityFragmentNeedsTypename` subgraph suppresses
     synthesis without checking that jump covers ALL instances at the position
     (`obligation_driven.go` `already`) -- is safe under composition validity (entity-interface
     definers know all members; `findDiscriminatorJump` targets only non-@interfaceObject
     interface-node heads), but it is the mechanism's weakest point; a composition-invalid config
     could in principle leave an instance undiscriminated.
   - **requires-bypass on same-subgraph interface-object roots -- CLOSED (M2 requires-chain wave,
     D7pp requires-scoped resolution + D11.10 requires-input pipeline).** A requires-bearing field
     now resolves ONLY from its requires-scope node, whose sole in-edges are jumps carrying that
     field's inputs (`interface-object-with-requires/case-05` plans the b->a->b relay; the
     locally-descended input-less route no longer exists in the model). The bypass class is gone
     model-wide -- `requires-circular/case-02`, previously a silent bypass PASS, now plans the full
     a->b->(a)->b->a chain.
   **Chained @requires across subgraphs -- CLOSED (same wave).** `requires-requires` (5 SKIPs -> 5
   PASS: nested requires settle by AND-relaxation over the scoped-jump chain, exactly the L6
   static-conditions argument), `requires-with-argument/02-05` (4 SKIPs -> 4 PASS: distributed
   requires tails assign each coordinate to a resolving subgraph; D11.10 materializes the gather
   pipeline -- `feed->comments(limit:3)->authorId@c->author` with the argument-conflict ALIASED gather
   position when the client selects the same field under different arguments), and
   `requires-circular/case-01` (1 SKIP -> PASS: v1's relay with the `feed.@.author` gather hop; the
   assertion-7 oracle gained the requires-mechanism position rule -- a gather fetch may attach along
   a consuming fetch's Requires-fragment path, and nothing else).
   REGISTERED RESIDUALS of the wave (all executed-truth level; plan-level oracles hold):
   - **requires-fragment representation value**: a fragment-conditioned @requires coordinate
     (`data { foo ... on Bar { bar } }`) prints in the gather document and the Requires fragment,
     and gates NO static tail (conditional input, completeness-favoring), but the representation
     VALUE builder (repNode.object, name-keyed) omits the fragment branches -- the enclosing
     composite is read as a whole object instead. `requires-with-fragments/04-06` and
     `requires-interface/case-03` (previously bypass-served) plan the relay with the unconditional
     coordinates gathered. Owner: M2 follow-up.
   - **scoped/unscoped twin co-location**: a requires-scoped jump and a plain jump into the same
     subgraph at one position are separate fetches where v1 may merge them into one -- valid, one
     extra fetch, never wrong data (FORMAL_SPEC D11.10). Owner: M2 follow-up (quality).
   - **DISTRIBUTED @key -- CLOSED (M2 distributed-key wave, D7ppp + D11.11).** The ONE remaining audit
     SKIP (`complex-entity-call/case-01`) is un-skipped and PASSES -- the corpus is COMPLETE
     (202/202, 0 GAP, 0 SKIP). The D7pp per-assignment tail machinery generalized to key coordinates
     (explicit path tails, path-coherent assignment, target excluded, gated to keys with NO full
     foreign supplier -- zero drift on single-source keys, verified: every other case byte-identical,
     D10 register unchanged at 2). The reverted prototype's two lowering defects closed exactly per
     the walk-threading prescription: (a) cross-goal producedByAll ambiguity -- the chain-layered
     consistent trace (Cover.Spines) gained the TAIL-REACHABILITY RETRY tier (FORMAL_SPEC D10
     chain-layered amendment), so distributed-key goals repair into recorded chain-consistent spines
     and lowering reads positions off the spine, never the ambiguous union; (b) branch-parent
     position bookkeeping -- `groupForObject` resolves a producing jump's parent at the position the
     KEY structure denotes (`keyAnchorPath` over `Edge.KeySelection`), reusing the goal-attributed
     group at the deeper position instead of minting a mis-positioned twin. NEW REGISTERED RESIDUAL
     (executed-truth level, same class as the requires-fragment representation value): a LIST-VALUED
     key path's representation VALUE (`products { id pid }` under `[Product!]!`) is built by the
     name-keyed object builder without list nesting -- the Key fragment, gather placement, and every
     plan-level oracle are exact. Owner: M2 follow-up together with the requires-fragment value.
4. **Edge.Conditions path-prefix tightening** -- the D7 applicability filter is a global-presence,
   completeness-favoring approximation (documented in `search/search.go` HONEST SCOPE). Owner: M1.5.

**Oracle/harness residuals (tighten what the tests can see):**
5. **Assertion-5 root-entry granularity** -- name-level comparison; unexploitable in today's corpus
   (composition forbids same-named-but-distinct roots) but noted in `audit/runner.go`. Owner: M2.
6. **Differential oracle blind spots** -- `resolve.Array` item-shape recursion beyond list depth and
   same-key-same-gate field dedup (noted in `differential/harness.go`). Owner: M1.5 with list fixtures.
7. **Same-root sibling granularity** of D10 path-consistency (FORMAL_SPEC Honest Scope 2) -- **RESOLVED
   (M1.5 wave-1b THE FLIP)** for the general case by the obligation-driven per-position lowering: sibling,
   repeated, and self-referential positions each get their own per-position fetch (both `sibling-conflation`
   witnesses PASS). RESIDUAL (14 of the 37 GAPs): the **member-scoped** subset -- a distributed position whose
   different concrete members resolve in different root groups, which `posGroup` cannot yet own by a
   member-qualified position key (13x leaf-coverage forward + 1x path-correspondence backward). Owner: M1.5/M2.
   **IR+D6 wave re-characterization + partial close:** the marker prose above was wrong for most of the
   member-scoped cases (see the gaps-wave report): the real causes were D3p typename-terminal goals
   (10 closed earlier), D6 over-narrowing (`partial-union/case-02` closed by D6p route-scoping;
   `partial-union-complex/case-03` closed by D3pp exempt-terminal promotion), and member-fragment
   liveness (`simple-interface-object/case-11` closed by the non-live-Refine discriminator ride).
   What remained of this item -- the genuine distributed abstract-member expansion class
   (`union-interface-distributed/02,05,08`, `union-intersection/04,08,11,12`) -- is **CLOSED by the
   M2 class-D wave** (`D6pp` + `D11.7` + `D11.8`; posGroup's member-qualified key is exactly the
   `D11.7` posKey): all 7 cases PASS. See item 2's CLASS D LANDED entry for figures and the one
   deferred realization (the D10 member-scoped kappa mask).

**Milestone-deferred by design:** router-level end-to-end audit harness (M1.5);
non-GraphQL datasources (M3); `(+)=max` cost family and cross-branch optimizing MERGE
(M2, NP-hard boundary per L1/L2); Lean machine-checked kernel (M4). Subscriptions LANDED (M3
subscriptions wave, `D11.12` trigger/response split): planned natively through the facade with
differential trigger+shape parity vs v1 and the live-websocket executed-truth scenario passing
through a planv2 plan. Subscription FILTERS (`@openfed__subscriptionFilter`) LANDED (M4.4): subscription
lowering (`lower/subfilter.go`) builds the `resolve.SubscriptionFilter` from the root field's
`SubscriptionFilterCondition` EXACTLY as v1's `pathBuilderVisitor.buildSubscriptionFilterCondition`
does (And/Or/Not/In recursion + `{{ args.path }}` -> ContextVariable resolution) and sets it on
`GraphQLSubscription.Filter` (v1 `visitor.go configureSubscription`), with malformed templates failing
lowering loudly rather than silently dropping the filter; the `ErrSubscriptionFilterNotSupported`
refusal is removed. This is SECURITY-GRADE parity -- a dropped filter would silently DELIVER events the
condition says to skip. Evidence: executed-truth `differential/exec_subfilter_test.go` (drives
`SkipEvent`: a must-skip event IS skipped, a must-pass event IS delivered, matching v1 per event),
structural v1-parity in the differential subscription oracle (`canonFilter`), and the real-config guard
`TestNewPlanner_AcceptsSubscriptionFilter` (a `SubscriptionFilterCondition` config is ACCEPTED and the
plan carries a non-nil `Filter`). Still deferred within subscription scope: pubsub/non-GraphQL trigger
datasources (the non-GraphQL datasource item above).
Two executable-transport residuals are documented in the facade (`planv2/transport.go` package doc):
per-START subscription clients (no cross-subscription connection multiplexing; every client
goroutine is scoped to its subscription's context) and `http.DefaultClient` for fetches -- both
follow-ups of the same "reach the factory's configured clients" item, neither a semantics change.

**`@defer` LANDED (M3 defer wave, `D11.13` scope-variant partitioning):** query operations carrying
`@defer` plan natively -- the obligation tree carries the defer scope (query ops only, FS-DEF-7),
fetch groups partition into scope variants (root re-walk / entity re-entry anchoring, `@key` in the
parent scope), and the plan is emitted in v1's `DeferResponsePlan`/`DeferDescriptor` encoding so
postprocess and the resolve incremental machinery run it unchanged. Evidence: differential
`deferCases` (descriptors + increment partition + `DeferField`-stamped shape, MATCH under every
ordering) and the executed-truth harness (`exec_defer_test.go`: streamed frames JSON-equal to
v1-planned execution through the untouched postprocess+resolve pipeline on root-rewalk, nested,
all-deferred-placeholder, entity-jump). **Registered defer residuals:** (a) deferx`@requires` -- a
deferred field whose serving jump carries `@requires` keeps the `D11.10` pipeline's scope-0
placement, so its requires-INPUT fetches ride the initial set (an FS-DEF-2 tension for input-only
fetches; v1 places them in the requesting field's own scope); (b) deferxdistributed-key (`D11.11`)
and `D11.7` member-fragment re-materialization inside deferred scopes follow the base machinery
untested by the defer corpus; (c) the v1 defer ENGINE test suite (73 subtests) is keyed on v1's
exact upstream request bytes -- under the PLANV2=1 seam every case fails at the mock with "received
unexpected body" (the adjudicated document-print divergence), so executed-truth parity is
established by the semantic-subgraph frame oracle instead, and mocked-suite parity is out of reach
by harness construction, not plan behavior; (d) ~~planv2 fetches omit `SelectResponseErrorsPath`
(v1 sets `["errors"]` on every fetch)~~ -- **CLOSED (M3 executed-encoding wave, `67768b1a`)**:
every planv2 fetch (both lowering paths, sync and defer) now carries
`SelectResponseErrorsPath: ["errors"]` with v1 `DefaultPostProcessingConfiguration` parity;
subgraph-error propagation is executed-truth verified (the real Guild audit's errors-path
failures -- `enum-intersection_1`, `union-interface-distributed_7/_8`,
`non-resolvable-interface-object_3` -- pass; the class's other two original members,
`corrupted-supergraph-node-id_0/_5`, were re-diagnosed as route-choice cases and stay in the
real-audit residual register below; injected-field-error cases in the in-repo exec-sync harness
pin the mechanism; the M3 final review re-verified live that every probe plan carries the errors
path).

**Subscriptions wave, customer-sweep follow-ups (all landed):** (1) the fallback-perturbation
defect -- subscription roots participating in query-operation reachability/routing (D10 usage
53 ops/455 events -> 104/5,683 measured) -- is closed by D11.12 root scoping (kind-scoped
reachability + base-mask exclusion; with/without-Subscription byte-identity pinned by
regression); (2) the exec-config adapter now maps `customGraphql.subscription` (v1-error class
closed; planv2 triggers on exec-config corpora are no longer hollow); (3) the renamed
subscription root class ("obligation 0 unreachable") is closed by the D5pp reverse-direction rule.
RESIDUAL of (3): a subscription root field declared in metadata whose owner SDL carries NO
subscription type (schema drift, or a pubsub/EDFS-owned trigger field whose datasource the
GraphQL-only harness skips) has no in-config evidence to resolve the payload type -- it stays a
typed fail-loud `ErrNoValidPlan`, pinned by `TestPlanner_SubscriptionMetadataOnlyFailsLoud`;
closing it needs either composed-schema-informed graph building or non-GraphQL trigger
datasources (both registered above as M3 scope). **Re-sweep correction:** subscription-root
recognition is by declared operation-root IDENTITY per subgraph (SDL schema-block bindings;
entity/child evidence under default naming) -- never by the bare name `Subscription`, which
billing/commerce schemas use for an ordinary keyed entity (the re-sweep's 49-op ErrNoValidPlan
regression, fixed with red-first twin-rename regressions). **Re-sweep rounds 2-3 (final: the DUAL-ROLE model):** ground truth from the failing config showed
the production shape is a composed type that is SIMULTANEOUSLY the subscription operation root and
a data object -- one subgraph's SDL declares `schema { ... subscription: Subscription }` AND returns
the same type from payload fields, while another subgraph contributes a genuine realtime root
field; composition merges both into one type named `Subscription`. Rounds 1-2 tried to CLASSIFY
the type (identity evidence, then SDL data-usage evidence) -- either/or classification can never
represent that shape, and round 2's registered "pathological reverse" residual was the actual
production case. The landed model stops classifying: a type's field edges are ALWAYS emitted in
their data role (object-tailed, every operation kind -- exactly pre-wave routing, which is also
v1's semantics: rootNodes membership just means "these fields resolve on this type here"); the
subscription root is a pure ANCHOR whose entry edges are ADDITIVE and kind-masked (D5pp reverse
direction, D11.12 root scoping). Both rounds' "undecidable" residuals DISSOLVE -- nothing is
undecidable when nothing needs deciding: an unreferenced data type routes as data regardless
(inert if unproduced), and the self-payload/merged shape is precisely what the dual role models
(pinned by `TestPlanner_MergedDualRoleSubscriptionType`, red-first on the verified shape). The one
remaining residual in this family was orthogonal: a subscription root field whose owner SDL carries
NO type declaration at all (schema drift / pubsub-owned trigger fields) had no output-type source
and stayed a typed fail-loud `ErrNoValidPlan` (`TestPlanner_SubscriptionMetadataOnlyFailsLoud`).
**RESCOPED by the M4.2 EDFS wave (DV-011).** The pubsub/EDFS-owned direction is now a modeled
capability: an EDFS event source is a first-class root entrance whose payload type comes from the
composed schema (`FORMAL_SPEC.md` `D5-EDFS`, `FEDERATION_SEMANTICS.md` `FS-EDFS`). Crucially the
rescue is EVIDENCE-GATED (`FS-EDFS-5`): only a root field with positive EDFS event-source evidence
is typed from the composed schema; blind schema drift (no EDFS evidence) STAYS fail-loud, so
`TestPlanner_SubscriptionMetadataOnlyFailsLoud` is unchanged and still pins the drift case. The
transport/config-shape half needs the coordinator's real-router validation (DV-011).

**Conformance-generator findings (M3 Stage-2 wave -- `planv2/conformance`, the property-based
conformance suite over FEDERATION_SEMANTICS.md's 98 propositions).** The generated suite's job is
to FIND gaps, and its first run did; per the wave's honest-counting rule these are REGISTERED with
the generated case as witness, not fixed in the discovering wave. Each is carried in the suite's
own triage register (`conformance/triage.go`, class `planv2-gap`) -- a registered case that starts
passing fails the suite until its entry is removed, the audit expect-fail discipline.

1. **D6 narrowing is schema-scoped, not position-scoped** (witness
   `FS-ABS-4/abstract-narrowing/route-scoped-ghost`): a subgraph whose ONLY route to the parent
   type is an unrequested root field at a DIFFERENT position (`ghost.ghostEntry: Wrapper`) still
   counts toward the abstract-member intersection; the requested position over-narrows to
   `{ __typename }` and a member both real parent-capable subgraphs resolve (`Common.c`) is
   silently nulled -- the exact silent-degrade class FS-PLAN-6/L7 exists to forbid.
   **CLOSED (M3, two-wave landing) FOR THE POSITION-SCOPING DIRECTION.** The encoding wave's
   `positionSubgraphs` (`obligation/narrow.go`) scopes the capable set per POSITION: the ancestor
   field chain root->parent is walked as per-level subgraph sets, transport admitted
   condition-blind via entity-jump heads, an empty level falling back to that level's
   field-global D6p set (never louder than before). The reachability-gaps wave added the model
   form (`FORMAL_SPEC` **D6ppp** -- position-scoping of P) and pinned FS-ABS-4's route-scoping
   wording as per-position. This witness (resolvable:false ghost key, unrequested ghost root)
   passes and its triage entry is retired; `partial-union-complex/case-04` (executed truth) is
   the distributed-member direction of the same fix. **The condition-blind TRANSPORT direction of
   the same class remained OPEN as finding 4 below (M3 final review I-1; CLOSED by the M4
   blockers wave -- see finding 4's closure note):** a ghost admitted via a
   resolvable:true key unobtainable at the requested position still joins the intersection and
   still over-narrows; "no longer joins the member intersection" holds only for ghosts with no
   admitting jump head, not in general. Honest scope: `D3pppp`'s expansion trigger still uses D6p
   route-scoping for its P (the conservative direction there); unifying it onto D6ppp is a
   registered follow-up, stated in the D6ppp text.
2. **@interfaceObject contribution unreachable at concrete-typed positions** (witness
   `FS-IFO-1/interface-object/contributed-field-concrete`): a contributed field selected on
   `Query.usersConcrete: [User]` (concrete position) is `ErrNoValidPlan` -- `D3io`
   member-flattening candidates fire only at ABSTRACT positions, so the interface-keyed jump into
   the interface-object subgraph is never modelled for a concrete parent. The interface-typed twin
   plans correctly. Composition adds the field to every implementer, so the concrete-position
   selection is legal client GraphQL (FS-IFO-1 makes no abstractness distinction).
   **CLOSED (reachability-gaps wave).** `D3io`'s concrete-position clause (FORMAL_SPEC amendment):
   the flattening fires for a bare field goal whose owner is a composed concrete object type, not
   only under `... on C` -- the gate change is one predicate in
   `obligation/tree.go expandInterfaceObjectFlattening` (a goal under a refinement onto a
   DIFFERENT type stays excluded). No builder change was needed: the deriver already mirrors v1's
   config convention (concrete-implementer Keys + propagated field metadata for @interfaceObject
   subgraphs), so the concrete-source jump `(User,definer).id -> (Node,ifo)` existed and the search
   routes it path-consistently; the emitted plan is v1's exact shape
   (`usersConcrete { __typename id }` then `_entities { ... on Node { username } }`). The audit
   leaf-coverage oracle gained the matching UPCAST credit -- a concrete client coordinate satisfied
   by the interface coordinate, the same @interfaceObject wire mechanism its backward check
   already named. Witness passes; triage entry retired.
3. **AX-REQ-COND probe: conditioned requires coordinate rendered where unresolvable** (witness
   `FS-REQ-9/requires-conditional/probe-unresolvable`): with the fragment-conditioned `@requires`
   coordinate resolvable NOWHERE, planv2 correctly still plans (the conditional-input reading
   holds -- the jump is not gated on the conditioned branch, the FEDERATION_SEMANTICS_FORMAL Section 3.1
   probe's first question answered), but the gathering document renders `... on Book { title }`
   against the owner subgraph, which does not resolve `title` -- an INVALID fetch document
   (FS-PLAN-1).
   **CLOSED (reachability-gaps wave) -- MG-2 closed.** `D7pp(4)` now states the AX-REQ-COND clause
   (conditioned coordinates contribute no static AND-tail) PLUS the rendering-validity rule the
   probe forced: a conditioned branch renders only where its coordinates are resolvable --
   resolvable NOWHERE renders NOWHERE. Realized at edge emission (`hypergraph/builder.go
   filterConditionalRequires`, unit-pinned): `Edge.Requires` carries the pruned selection (the
   tokenizer now carries raw argument groups so a rebuilt selection loses no literals); an
   unconditional composite left childless renders `{ __typename }`, keeping the document valid
   and the enclosing object riding the representation. AX-REQ-COND's Section 1.2 statement and FS-REQ-9's
   wording carry the same caveat; "conditional input" is a GLOSSARY term. Honest scope (stated in
   D7pp(4)): pruning is realized for the resolvable-NOWHERE case -- a branch resolvable somewhere
   renders as before, valid on every corpus shape. The pre-existing executed-truth residual (the
   representation VALUE builder omits fragment branches) is unchanged and stays registered.
4. **D6ppp condition-blind jump transport admits position-unobtainable ghosts -- OPEN, REGISTERED
   (M3 final review finding I-1; witness
   `FS-ABS-4/abstract-narrowing/unobtainable-key-ghost`, triage class `planv2-gap`).
   SILENT-WRONG-DATA severity; value-type members only.** The residual direction of finding 1:
   `positionSubgraphs` (`obligation/narrow.go`) admits a subgraph at a chain level whenever ANY
   `EntityJump` heads the enclosing type in it (`jumpHeadSubs` -- condition-blind: jump
   conditions, requires-tails, and the key fields' PRODUCIBILITY at the position are all
   ignored). A ghost subgraph whose key is `resolvable: true` but UNOBTAINABLE at the requested
   position -- no position-capable subgraph produces the key's fields, so no real route can ever
   enter the ghost there (the reviewer's witness: `Wrapper` keyed `id` in the requested-root
   owner A, keyed `gid` only in a subgraph rooted at a DIFFERENT position and in the rootless
   ghost, whose member set is smaller) -- is admitted into the capable set and its member set
   joins the verdict-2 intersection: a real union member (`... on OnlyB { b }`) is silently
   narrowed to a response-only null with ZERO fallbacks while the only genuinely
   position-capable subgraph resolves it (FS-PLAN-6/L7, the class FS-ABS-4's route-scoping
   exists to forbid). Blast radius: confined to abstract VALUE-type members (the entity gate at
   the narrowing verdicts protects entity members); the search itself never routes through the
   ghost -- only the narrowing verdict is corrupted; pre-existing direction (field-global D6p had
   the same admission; `0f4c8244`/`d126074f` narrowed but did not eliminate it). The id-keyed
   (genuinely position-capable) ghost twin narrows CORRECTLY per FS-ABS-4, so the witness
   discriminates the defect from prescribed narrowing. Corpus/conformance/differential stayed
   green pre-witness because no family generated this shape (the `route-scoped-ghost` scenario
   uses `resolvable: false`, which never enters `jumpHeadSubs`). **Queued fix (its own wave --
   kernel-adjacent, NOT a doc-fix):** condition-aware jump transport -- position-scoped
   key-OBTAINABILITY for transport admission (the walk already computes per-level capable sets;
   a jump's key tails' producibility at the position is checkable). Until it lands, `D6ppp`'s
   over-approximation is honest-scoped in FORMAL_SPEC (it can ADD false origins, which is
   narrowing-unsafe for verdict 2) and the `narrow.go` comments carry the same caveat.
   **CLOSED (M4 blockers wave) -- the queued fix landed as `FORMAL_SPEC` `D6pppp`
   (condition-aware transport, key-obtainability admission):** `positionSubgraphs`
   (`obligation/narrow.go`) now admits a subgraph at a chain level only via a jump whose KEY
   tails are obtainable at the position -- every key-tail subgraph already holds the instance
   there, judged as a per-level LEAST FIXPOINT over the previous level's capable set, so
   multi-hop relays stay admitted while position-unobtainable ghosts never enter. The witness
   flips to PASS and its triage entry is removed (register discipline); THREE adversarial
   variants are pinned alongside it, all measured RED under the old condition-blind admission
   before the fix landed: `abstract-narrowing/double-ghost` (the CHAINED shape -- the narrowing
   ghost is globally reachable through an unrequested root's key relay yet position-unobtainable;
   the subtler class the M3 reviewer warned about), `partial-composite-key-ghost` (composite key
   `id org` with only `id` obtainable at the position -- partial obtainability must not admit),
   and two leniency pins that pass in BOTH directions: `two-hop-narrower` (a genuinely
   position-capable two-hop subgraph must STILL narrow -- the fixpoint must not under-admit) and
   `ghost-cycle` (the rootless mutual-admission pair, verified to be caught UPSTREAM by D6p
   optimistic reachability's own fixpoint -- pinned as a regression guard for that property).
   FS-ABS-4's narrowing clause now states the obtainability rule normatively; D6ppp's Honest
   scope 2 is rescoped to CLOSED. Honest scope (stated in D6pppp): obtainability is judged at
   SUBGRAPH granularity -- strictly tighter than condition-blind, still an over-approximation in
   the lenient (never-narrow) direction.

**Boundary observation -- ADJUDICATED (reachability-gaps wave):** the generated
`abstract-narrowing` family exposed that planv2 treats the SAME shape differently on either side
of the FS-ABS-4 (intersection narrowing) / FS-ABS-7 (distributed-member expansion) boundary: with
all-value members, a member declared by only one parent-capable subgraph is NARROWED (fetched
nowhere -- the Section 10 worked-example semantics); with an entity member in the set, the equivalent
exclusive value member is EXPANDED (`... on OnlyB` fetched via its declaring subgraph's
re-entry). The adjudication PINS this as the deliberate reading (FS-ABS-4's new boundary clause):
FS-ABS-4's premise is *no entity member anywhere in the position's member population* -- no
identity to reconcile, so one static (intersection) member set must hold for every parent origin;
the moment ANY position-possible member carries a `@key`, instances at the position carry
reconcilable identity, cross-subgraph re-entry is meaningful, and FS-ABS-7 governs
subset-possible members (value members included). The two propositions partition on the
entity-presence test -- the audit corpus is calibrated to exactly this boundary (partial-union
narrows; union-intersection/union-interface-distributed expand). The entity-member conformance
scenario now ASSERTS the FS-ABS-7 direction (`... on OnlyB` fetched via subb, absent from suba);
FS-ABS-4's route-scoping wording is pinned per-position (finding 1's D6ppp).

**Fixture-convention note:** the audit metadata deriver now normalizes RENAMED root operation
types to their composed canonical names (`deriver.go` `canonicalRoot`), matching the composition
convention planv2's builder documents (`sg.rootAlias`: node metadata lists root fields under the
composed name; the SDL keeps the rename). Corpus impact: zero (no transcribed fixture renames
roots). The pre-existing D5pp-reverse behavior -- metadata listing root fields under the subgraph's
OWN renamed name -- is handled for subscription roots only; whether renamed-name metadata for
QUERY roots occurs in real exec-configs (and needs the same reverse rule) is an open question
recorded here, not a registered gap.

**Diagnostic-artifact adjudication -- the "7-op [v1-OK] orphan value-type class" (reachability-gaps
wave).** The 2026-07-16 unplannable-shape classification reported 7 customer operations planv2
could not plan but v1 could (5x `FIELD->ORPHAN:valuetype-unreachable-everywhere`, 2x
`FIELD->TYPEMOVE:ORPHAN:valuetype-unreachable-everywhere`) -- registered as the last real-blocker
class ahead of the strict-superset milestone. ADJUDICATED AS A DIAGNOSTIC ARTIFACT, not a planner
gap: the classifier (`external/reachability_classify_test.go`) replayed the pipeline with
`RootType {query, mutation}` while the shipping facade registers the subscription root too -- a
SUBSCRIPTION operation then fails its own root goal and its whole payload subtree walks up to the
un-rooted `Subscription` object node, producing exactly those two orphan shapes. Evidence: the
flanking real-pipeline sweeps (20:09 and 21:56 UTC that day) both report `ErrNoValidPlan` **156**
while the classifier (20:40, same 7,920 ops) reports **163 = 156 + 7**, and the classifier's
`[v1-FAILS]` split equals the sweep's count *exactly* -- every `[v1-OK]` classifier NVP is an
operation the real pipeline plans. Both artifact shapes are synthesized corpus-free and pinned in
`external/classify_parity_test.go` (the `{query,mutation}`-only config reproduces the exact shape
strings; the classifier's now-shared `classifyBuildConfig` -- the facade's config verbatim -- and
the facade itself plan both operations). CONSEQUENCE, pending the coordinator's private-corpus
re-verification: the real pipeline's remaining `ErrNoValidPlan` set contains no v1-plannable
operation -- planv2 plans a strict superset of v1 on the corpus. Process lesson (pinned in the
test): a diagnostic that replays the pipeline must replay the pipeline's CONFIG; a
diagnostic-only divergence manufactures phantom gap classes.

**Real Guild audit -- executed-truth residual register (M3 executed-encoding wave;
149->188 of 199, stock-v1 baseline 185).** The wave's full record is the SCOREBOARD
executed-encoding section; the 11 remaining failures split 6 + 5. The SIX shared stock-stack
failures (`complex-entity-call_0`, `non-resolvable-interface-object_1`,
`provides-on-interface_0/_1`, `provides-on-union_0/_1`) fail the gate-off v1 baseline
byte-identically -- composition/validation issues outside the planner, not registered as planv2
gaps. The FIVE planv2-attributable failures are REGISTERED here with their diagnoses:

1. **`mutations_3` -- side-effecting mutation DOUBLE-EXECUTED on a shared-root split
   (silent-wrong-effect severity; the branch's most correctness-significant open defect).**
   `Mutation.addCategory` is `@shareable` in subgraphs a and b; the search splits the selection
   subtree across BOTH roots, so the mutation executes twice (live capture: b's copy succeeds,
   a's copy fails with "already added" -- with non-idempotent resolvers both could succeed and
   double-apply). v1 pins each mutation root field to ONE planner/subgraph. The fix belongs in
   the SEARCH/obligation layer -- a joint per-root-field subgraph pin so every obligation under
   one mutation root field settles in a single subgraph -- and was out of the encoding wave's
   "search untouched" scope. Owner: queued search-layer wave; until it lands, planv2 must not be
   preferred for schemas with shareable mutation root fields.
   **CLOSED (M4 blockers wave) -- the queued search-layer pin landed (`FORMAL_SPEC` `D10`
   amendment "mutation-root subgraph pin"; `FEDERATION_SEMANTICS` FS-ROOT-6;
   `search/mutationpin.go`):** on a mutation operation, a root field with root entrances in
   several subgraphs is pinned to ONE subgraph by extending the BASE mask (every table -- plain,
   root-pinned, scoped, chain-layered -- inherits it, so no fall-back tier can re-admit a second
   entrance); the joint choice is the viable subgraph minimizing the sum of the field's goals'
   settle values (ties by subgraph name -- content-based), oracle-checked by brute-force
   enumeration over single-entrance instances (`search/mutationpin_test.go`); when NO single
   subgraph anchors the whole selection, planning fails LOUD (`ErrNoValidPlan`, mutation-specific
   reason) rather than emit a double-executing split -- single-execution dominates completeness
   for side effects. Executed-truth red-first witness:
   `differential/exec_sync_cases_test.go` `TestExecSync_ShareableMutationRootSingleExecution`
   (execution counters through shared state; pre-fix planv2 executed the shareable root TWICE
   with byte-correct merged data -- the silent shape -- v1 once; post-fix exactly once with the
   cross-subgraph half riding the entity jump). Conformance pins: `mutation/shareable-root-pin`
   (one root fetch + one entity fetch, wb never sees the root field) and
   `mutation/shareable-root-unpinnable` (typed refusal). Queries deliberately keep the split
   (FS-ROOT-1; `TestMutationRootPin_QueryTwinStillSplits`). The "planv2 must not be preferred
   for schemas with shareable mutation root fields" restriction is LIFTED.
2. **`override-type-interface_0` -- `@override` on an interface-typed position.** The query
   selects `createdAt` on interface `Post` in a; the override moved `ImagePost.createdAt` to b,
   but a's INTERFACE field node still resolves it (returns the "NEVER" sentinel). Needs
   member-aware interface-field modelling under `@override` (builder-level D3 work). Owner: with
   the same queued builder/search wave.
3. **`corrupted-supergraph-node-id_0/_2/_5` (3 tests) -- route-choice parity on a
   deliberately-corrupted fixture.** The suite corrupts its node-id mapping such that only v1's
   habitual route observes the corruption; planv2's alternative route is VALID against the
   supergraph but reads the corrupted copy (`_2`: `chat.id` = "never") or resolves cleanly where
   the audit expects the corrupted route's error (`_0`/`_5`). Not an encoding or semantics
   defect -- a route-tie difference the fixture happens to reward; would only close via
   v1-route-parity tie-breaking. Owner: recorded, no fix queued (adjudication candidate).

`mysterious-external` flaked once during the wave's first full run (correct answers on immediate
re-query; both definitive runs pass) -- harness/environment flakiness, not registered.

**M4.0 re-measurement of this register (full real-audit run at `277a1f59`; SCOREBOARD M4.0
section carries the numbers): 185/199.** Residual entry 1 (`mutations_3`) is CLOSED by the
mutation-root subgraph pin (its closure note above; `mutations` suite 4/4). The SIX shared
stock-stack failures still fail. The run also measured, bisected per-commit via router rebuilds
at `b7b8e1f0` / `e1a86364` / `277a1f59`, that the INTERLEAVED M4.1 commit `e1a86364`
(FieldInfo/RootFields emission) REGRESSED 8 formerly-passing tests
(`child-type-mismatch_0/_1/_2`, `partial-union-complex_0..3`, `parent-entity-call-complex_0`)
and FLIPPED the 4 remaining registered failures to pass (`override-type-interface_0` -- entry 2
above -- and `corrupted-supergraph-node-id_0/_2/_5` -- entry 3). OWNER: the M4.1 wave -- the
regression needs a fix or its own registered diagnosis, and the entry-2/entry-3 flips need
own-eyes verification before those entries are retired; entries 2-3 are left standing until
then. This wave's commits are exonerated by the bisection (the 8 failures are byte-identical at
`e1a86364` and `277a1f59`; `mutations` moves 3/4 -> 4/4 across this wave's commits only).

**Hygiene gate before any public push (MANDATORY -- squash-merge or `git filter-repo`; never a plain
merge/push):** the branch's full git HISTORY must not be published as-is. The scope is broader than
any single blob -- history carries, removed at HEAD but present in earlier commits: a private-corpus
keyword (pre-scrub commit `3361ba85`); a ~3.4MB compiled demo binary; filtered CI logs; local
browser-session logs (an internal research trail -- scanned and verified free of credentials and
customer identifiers, but not for publication); and internalized third-party planner-analysis
research notes. A plain merge or push would publish all of it. The gate is the squash (or a
filter-repo rewrite of the whole branch), and it must not be downgraded to scrubbing "just one
keyword" -- that is exactly the misreading this entry exists to prevent.
