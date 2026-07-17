# V1 feature-parity register

This document is the systematic inventory of every v1 planner/engine capability with
planner-v2's status -- the M4 campaign work-list, and at handover the single answer to
"what does the new planner NOT do yet?". It was compiled from CODE, not memory: every v1
surface (the `plan` package public API, the datasource packages, the resolver features a
plan encodes, the exec-config adapter's mapped-vs-skipped fields, and the router seam's
call surface) was enumerated and each capability's planv2 status verified against the
planv2 sources and the committed test evidence. Compiled on branch
`feat/planner-v2-hypergraph` (post-M3; audit corpus 202/202 plan-level, real Guild audit
188/199 executed vs 185 stock-v1).

Statuses:

- **PARITY** -- verified equivalent behavior; the citation names the test/wave.
- **PARTIAL** -- works for a stated subset; the missing part is stated.
- **MISSING** -- planv2 silently ignores or omits the capability. These are flagged
  loudest: a silent ignore is the one outcome the project's no-silent-degrade bar
  forbids, and each MISSING row is an M4 work item (or an explicit typed rejection).
- **MISSING-LOUD** -- planv2 refuses with a typed error today; safe to adopt around.
- **N-A** -- v1-only by design; the justification is stated.

Each gap also carries the pluggability class (Section 0), a corpus-relevance note where
measurable, and an estimated wave size (S/M/L).

---

## 0. The pluggability contract (owner framing)

The datasource architecture is pluggable: **the planner core is not tied to any
datasource kind.** This is an architectural fact of the code, not an aspiration, and the
M4 waves should exploit it:

- The planner core (hypergraph -> obligation -> search) consumes **capability metadata
  only**: `hypergraph.Build` reads `plan.DataSource` through `plan.NodesAccess`
  (RootNodes/ChildNodes), `FederationConfiguration()` (keys/requires/provides/
  entity-interfaces), and `UpstreamSchema()` -- nothing kind-specific
  (`planv2/hypergraph/builder.go`, `Build`). What a subgraph CAN resolve is the only
  planner-level question; HOW it is fetched never enters the model.
- Datasource kinds are a **lowering/transport concern**. The facade's
  `buildTransportTable` (`planv2/transport.go`) is, by its own package doc, "the one
  place planv2 reads datasource-specific config"; lowering emits executable fetches for
  any subgraph with a transport entry and shape-only fetches otherwise.
- Evidence the seam holds: subscriptions (D11.12) and `@defer` (D11.13) both landed as
  lowering-layer encodings with the obligation/search layers untouched (the waves'
  stated scope; see DIVERGENCES.md "Subscriptions LANDED" / "@defer LANDED"). The
  operation kind and the delivery mode changed; the routing model did not.

Consequently every datasource gap below is classified by WHICH side it needs:

- **(a) capability-metadata acceptance** -- the kind's metadata must build a correct
  graph. Mostly free by construction (metadata is kind-agnostic); the only cost is
  where a kind's metadata is *thinner* than GraphQL's (no upstream SDL).
- **(b) lowering/transport encoding emission** -- a per-kind transport-table entry and
  fetch/trigger encoding at lowering. The gRPC, static, and introspection gaps are
  entirely this class.
- **(c) genuine capability-model extension** -- the kind changes what is *plannable*.
  The one known (c) case: EDFS/pubsub-owned subscription root fields whose owner has no
  SDL declaring the payload type -- today a typed `ErrNoValidPlan`
  (`TestPlanner_SubscriptionMetadataOnlyFailsLoud`); closing it needs composed-schema-
  informed graph building (registered in DIVERGENCES.md).

A wave that is pure (b) cannot regress routing; a wave with (c) content needs the full
spec-first process (EXTENDING.md).

---

## 1. Datasource kinds

v2 ships five datasource packages (`v2/pkg/engine/datasource/`): `graphql_datasource`,
`grpc_datasource`, `staticdatasource`, `introspection_datasource`, and `httpclient`
(the last is the shared HTTP client library the others use, not a datasource kind --
N-A). Pub/sub (EDFS: Kafka/NATS) datasources do **not** live in this repository; they
live in the Cosmo router and plug into the engine via `plan.PlannerFactory`/
`plan.DataSourcePlanner` plus `resolve.SubscriptionDataSource` (the pubsub-specific hook
surface is `resolve.HookablePubsubDatasource`, `v2/pkg/engine/resolve/datasource.go`).

| Capability | Status | Class | Notes |
|---|---|---|---|
| GraphQL fetches (URL/method/header, entity `_entities` fetches single + batch) | PARTIAL | (b) | Transport rebuilt from the datasource's exported config via `graphql_datasource.NewSource` (`planv2/transport.go`); single and batch entity fetches emitted (`RequiresEntityFetch`/`RequiresEntityBatchFetch`, `lower/obligation_driven.go`; audit 202/202). The missing part: fetches run on `http.DefaultClient` -- the factory's configured `http.Client` (timeouts, mTLS, custom transport) is unreachable through `plan.DataSource` (transport.go limitation 1). Size S once the client seam exists. |
| GraphQL subscriptions (WS `graphql-ws`/`graphql-transport-ws`/auto, SSE, SSE-POST, forwarded client headers + regexes, startup hooks) | PARTIAL | (b) | Landed (D11.12 trigger/response split; M3 subscriptions wave, live-websocket executed truth). Wire options mapped 1:1 from `SubscriptionConfiguration` (`planv2/transport.go`). Missing parts: per-START subscription client -- no cross-subscription connection multiplexing (transport.go limitation 2). Trigger `Filter` (`@openfed__subscriptionFilter`) is now populated (M4.4 LANDED; Section 7). Size S (client seam). |
| GraphQL upstream-schema handling (per-subgraph SDL, renamed root types) | PARITY | -- | Builder resolves against each subgraph's own SDL incl. renamed roots (D5pp both directions; class-E wave, customer-dominant: `ErrNoValidPlan` 542->166 on the private sweep). |
| GraphQL `CustomScalarTypeFields` (fields of custom scalar type resolved as opaque scalars) | MISSING | (b) | `graphql_datasource/configuration.go:21`; planv2 never reads it -- a custom scalar returning object-shaped JSON would be walked as a composite. M4.1 adjudication: CANNOT be refused typed today -- the field is unexported on `graphql_datasource.Configuration` with no accessor, unreachable through the public config surface planv2 is handed; the refusal (or the implementation) needs the same one-v1-touch accessor seam as the `http.Client` (M4.6). Not mapped by the exec-config adapter; low measured relevance. Size S. |
| GraphQL upstream-operation minification (`MinifySubgraphOperations`) | PARTIAL | (b) | v1 `plan/planner.go:186` + `plan/minifier.go`. planv2 prints unminified but semantically identical documents -- results-identical; the size optimization is not applied. NON-SEMANTIC, so ACCEPTED (not refused): this flag is `envDefault:"true"` in the Cosmo router, ON in every default deployment, so the M4.1 refusal disabled planv2 for 100% of real operations; corrected M4.1.1 (refuse.go's semantic-vs-non-semantic split). Proven results-identical at b7b8e1f0 (planv2 planned WITH the flag set, scored 188). Size S-M to actually minify (M4.6). |
| gRPC datasource -- incl. the ConnectRPC transport | PARITY | (b) | M4.3 LANDED (was MISSING-LOUD). gRPC is configured *through* `graphql_datasource.Configuration.GRPC` (`grpcdatasource.GRPCConfiguration`, `graphql_datasource/configuration.go:23`); metadata acceptance (a) was already free (upstream SDL present, graph builds). The (b) gap is closed: `buildTransportTable` (`planv2/transport.go`) now gives a gRPC-configured datasource a transport entry whose `lower.GRPCFetchFactory` builds a per-fetch executable `grpc_datasource.DataSource` from that fetch's rendered operation, reusing `grpc_datasource.NewDataSource` unchanged (the same constructor v1's `ConfigureFetch` calls -- proto compiler, execution plans, `resolve.DataSource` impl, ConnectRPC transport in `transport_connect.go`). A read-only `Configuration.GRPCConfiguration()` accessor exposes the per-subgraph mapping/compiler/disabled to the facade. `@connect__fieldResolver` (field-level RPC) rides the same surface for free: the mapping's `ResolveRPCs` are passed through verbatim and resolved inside `grpc_datasource.Load` (dependency-graph decomposition), needing no separate planv2 emission. The former `ErrGRPCDatasourceNotSupported` refusal is removed. Executed-truth: `TestNewPlanner_GRPCFetchExecutesRealOutput` plans a gRPC query, pulls the fetch's DataSource, and drives it through the `grpc_datasource` harness against a real bufconn server, asserting the mock's data. HONEST SCOPE: the live RPC transport (gRPC client connection) is a Factory-level runtime dependency unreachable through `plan.Configuration` -- the same limitation as the HTTP `http.Client` -- so the embedder supplies it via `planv2.Config.GRPCTransports`; with none supplied the fetch is still built executable (RPC plan compiled) and `Load` errors typed ("requires an rpc transport"), never a silent shape-only stub (`TestNewPlanner_GRPCFetchNoTransportLoadsError`). A live upstream gRPC server / the coordinator real-router check is out of in-repo scope. No subscriptions in the gRPC kind. |
| Static datasource | MISSING-LOUD | (a)+(b) | `staticdatasource.Factory.UpstreamSchema` returns `(nil,false)`; `hypergraph.Build` fails typed: "datasource %q has no upstream schema" (`builder.go:91-94`), so `planv2.NewPlanner` refuses the whole config. (a): build must tolerate SDL-less metadata-only subgraphs; (b): trivial static fetch encoding. Size S. |
| Introspection datasource | MISSING-LOUD | (a)+(b) | Same typed refusal (factory returns no upstream schema). The router seam filters SDL-less datasources out before planv2 and lets introspection queries fall back to v1 (`internal-notes/router-seam/cosmo-router-planv2-seam.patch`). Size S-M (introspection fetch encoding reuses `introspection_datasource`'s resolve source). |
| Pub/sub -- EDFS Kafka/NATS event configuration | MISSING-LOUD | (a)+(b)+(c) | Not in this repo (router-side). Adapter evidence: `PUBSUB` is an observed `kind` in real exec-configs and the adapter skips it (`external/execconfig.go BuildDataSources` "Non-GraphQL datasources (PUBSUB, etc.) are skipped"). Reclassified MISSING-LOUD at the M4.1 audit: BOTH entry paths already refuse typed -- a pubsub datasource exposes no upstream schema, so `hypergraph.Build` fails with the same typed no-upstream-schema error as static/introspection (`builder.go:91-94`), and the metadata-only trigger case is typed `ErrNoValidPlan` (pinned by `TestPlanner_SubscriptionMetadataOnlyFailsLoud`). Needs: (a) metadata-only subgraph acceptance (shared with static), (b) trigger transport encoding against `resolve.SubscriptionDataSource`/`HookablePubsubDatasource`, and (c) the one genuine model extension -- a trigger root field with no SDL-declared payload type (DIVERGENCES.md subscription residual). Size L (the (c) slice is the L; (a)+(b) are M). |
| `httpclient` package | N-A | -- | Shared HTTP client library, not a planner-facing datasource kind; planv2 already rides it via the reused graphql sources. |

## 2. `plan.Configuration` surface

Every field of `plan.Configuration` (`v2/pkg/engine/plan/configuration.go`), with what
planv2's facade does. planv2 consumes a field, ignores it silently (flagged), or the
field is v1-internal. M4.1 landed the typed-rejection path: `planv2/refuse.go`
(`refuseUnsupportedConfig`, called first in `NewPlannerWithConfig`) refuses every
config-carried capability planv2 would silently drop, each with an `errors.Is`-matchable
sentinel; the deliberate non-refusals (inverted-default flags, ObjectFieldSource, and the
NON-SEMANTIC `MinifySubgraphOperations`/`EnableOperationNamePropagation`) are adjudicated in
refuse.go's package doc and the rows below. M4.1.1 correction: the sweep refuses only SEMANTIC
divergences (honoring the config would change the response); a NON-SEMANTIC surface (results
byte-identical; only an optimization or observability nicety is skipped) is accepted, never
refused -- refusing `MinifySubgraphOperations` (a router `envDefault:"true"`) had disabled planv2
for 100% of real operations.

| Field | Status | Notes |
|---|---|---|
| `DataSources` | PARITY | Consumed at `NewPlanner` (`planv2.go:85`); GraphQL-kind executable, others per Section 1. |
| `DefaultFlushIntervalMillis` | PARITY | `planv2.go:101` -> subscription plans' `FlushInterval` (`Plan` sets it on the D11.12 path). |
| `Logger` | N-A | The facade is deliberately logging-free; non-fatal findings are typed values on `PlanWithDiagnostics` instead (planv2.go `Diagnostic`). Nothing to wire. |
| `MaxDataSourceCollectorsConcurrency` | N-A | Tunes v1's node-suggestion collectors; planv2's search has its own budget (`search.Config` PreflightCap/StateCap) and no collector stage. |
| `Fields` (`FieldConfigurations`) | PARTIAL | See Section 3 -- was the loudest silent-ignore cluster; after M4.1 every row there is PARITY, MISSING-LOUD, or an adjudicated corpus-compat flag (ObjectFieldSource). |
| `Types` (`TypeConfigurations` -- `RenameTo`) | MISSING-LOUD | Type renaming for stitched/contracted schemas; zero planv2 hits. M4.1: typed refusal (`ErrTypeRenamesNotSupported`). Not mapped by the exec-config adapter -> corpus-safe; relevant only to non-federation embedding. Size M if ever needed. |
| `EntityInterfaceNames` | N-A | Redundant input: planv2 derives entity-interface handling from per-datasource `FederationMetaData.EntityInterfaces`/`InterfaceObjects` (builder.go:144-148); the entity-interface audit suites pass (M2 class-C wave). |
| `DisableResolveFieldPositions` | MISSING (adjudicated) | planv2 never emits field `Position` info at all -- it behaves as if the flag were always `true`. Zero test impact (harnesses set `true`), but production error messages lose field positions v1 provides. M4.1 adjudication: an INVERTED-DEFAULT flag -- the zero value (false) REQUESTS positions, so a typed refusal on it would refuse every default config and break every harness and the corpus sweep; and stamping positions needs the obligation layer to capture operation AST positions, which is the M4.0 agent's territory this wave. Registered; size S once the obligation capture lands. |
| `EnableOperationNamePropagation` | PARTIAL | v1 propagates the client operation name into subgraph documents; planv2 prints unnamed documents. Observability/tracing only -- the operation NAME is not a result, so responses are byte-identical; the propagation is not applied. NON-SEMANTIC, so ACCEPTED (not refused): default-off, but refusing was wrong-in-kind. Corrected M4.1.1 (refuse.go's semantic-vs-non-semantic split). Size S to actually propagate (M4.6). |
| `CustomResolveMap` (`resolve.CustomResolve`) | MISSING-LOUD | Custom scalar resolvers never attached. M4.1: typed refusal (`ErrCustomResolveMapNotSupported`). Size S-M to implement. |
| `Debug` (`DebugConfiguration`) | N-A | Every flag prints v1-pipeline internals (node suggestions, planning paths, visitor traces) that have no planv2 counterpart. The intent (planner observability) is served by `PlanWithDiagnostics` + the audit/differential tooling. `PrintQueryPlans` intent covered -- see Section 6 query-plan printing. |
| `MinifySubgraphOperations` | PARTIAL | Section 1 row; NON-SEMANTIC, ACCEPTED (results-identical; optimization not applied). Corrected M4.1.1 -- was `envDefault:"true"` in the router, so the M4.1 refusal disabled planv2 wholesale. |
| `DisableIncludeInfo` | PARITY | M4.1 Info wave: planv2 generates per-field `resolve.FieldInfo` and `FetchInfo.RootFields` by default and honors the disable (suppresses both). One strictly-additive divergence, documented in `lower/info.go`: planv2 keeps its minimal FetchInfo record (DataSourceID/Name/OperationType/QueryPlan) even under the disable, because the query-plan printer dereferences `Fetch.Info` unconditionally (v1 emits nil there). Pinned by `TestPlanner_FieldInfoDisabled`. |
| `DisableIncludeFieldDependencies` / `DisableCalculateFieldDependencies` | MISSING (adjudicated) | `FetchInfo.CoordinateDependencies` never built (Section 6). M4.1 adjudication: INVERTED-DEFAULT flags -- the zero value requests the capability, so a typed refusal would refuse every default config (every harness, the whole corpus). Registered; recommended to land with the fetch-reasons machinery in M4.4 (shared dependency structures). |
| `BuildFetchReasons` / `ValidateRequiredExternalFields` | MISSING-LOUD | `FetchInfo.FetchReasons`/`PropagatedFetchReasons` never built; the requires-validation mode that depends on them therefore cannot be enabled. M4.1: typed refusal (`ErrFetchReasonsNotSupported`) -- both are opt-in flags, corpus-safe. Router feature (fetch-reason propagation via the `fetch_reason` extension). Size M (Section 12 sizing), M4.4 recommended. |
| `ComputeCosts` / `StaticCostDefaultListSize` / `IgnoreImplementingTypeWeights` (+ per-DS `CostConfig`) | PARITY (attribution subset, DV-012) | M4.5: planv2 honors `ComputeCosts` by attaching a `plan.CostCalculator` to every emitted plan (all kinds), built by the `planv2/cost` package via `plan.BuildCostCalculator` -- v1's EXACT cost tree + weight/list-size/multiplier math + `EstimateCost`/`ActualCost`/`ValidateSliceArguments`, reused byte-for-byte. Cost is POST-PLAN (a plan adjunct; it does NOT steer plan selection, so the search kernel and its I3 tree-cost oracle are untouched -- no oracle extension needed). The tuning knobs (`StaticCostDefaultListSize`, `IgnoreImplementingTypeWeights`) and per-DS `CostConfig` flow through `plan.NewCostCalculator` unchanged. ONE documented divergence (DV-012): per-field datasource attribution is the single route the search chose (like `FieldInfo.Source`), so a shared entity's default object weight is charged once, not once per resolving subgraph -- identical to v1 for single-subgraph fields, a bounded subset for co-planned entities. Guards: `TestCost_Witness_SingleSubgraph`, `TestCost_DifferentialParity`, `TestCost_DifferentialParity_Federated` (differential), `TestNewPlanner_AcceptsComputeCosts` (facade nil-calculator guard). |
| `RelaxSubgraphOperationFieldSelectionMergingNullability` | N-A (with caveat) | The flag relaxes v1's upstream-document VALIDATION inside datasource factories; planv2 runs no upstream validation at plan time, so there is nothing to relax. Caveat recorded: v1's strict mode can reject a plan planv2 would emit; planv2's documents are validated by the audit oracle per fetch, not per config flag. |

## 3. `plan.FieldConfiguration` surface

v1's per-field table (`plan/configuration.go:136`). The exec-config adapter proves
which parts real router configs carry: it decodes `argumentsConfiguration` (both
`FIELD_ARGUMENT` and `OBJECT_FIELD` source types appear in the protobuf contract) and
deliberately skips path/authorization/subscription-filter "not needed for plan-shape
comparison" (`external/execconfig.go FieldConfigJSON` doc) -- i.e. those features ARE in
the production config surface, unmeasured only because the harness didn't need them.

| Field | Status | Notes |
|---|---|---|
| `Arguments` -- `FieldArgumentSource` | PARITY | planv2 renders arguments from the normalized operation itself (M1.5 wave 2: printed arguments, `query($v:T)` headers, ContextVariable Input segments -- the v1 graphql_datasource contract); `requires-with-argument`(+`-conflict`) suites pass. It never reads `config.Fields` -- argument routing is operation-derived, which for FIELD_ARGUMENT is equivalent. |
| `Arguments` -- `ObjectFieldSource` | MISSING (adjudicated, CORPUS-COMPAT FLAG) | Argument sourced from a parent object field (REST-mapping style). No planv2 consumption of `ArgumentConfiguration.SourcePath` for object sources. M4.1 adjudication: DELIBERATELY NOT refused -- OBJECT_FIELD appears in the router execution-config contract and the exec-config adapter decodes it (`external/execconfig.go argumentSourceType`), so a constructor refusal could reject real production supergraphs wholesale; whether any of the private-corpus graphs actually carry it is unmeasured (the corpus is coordinator-run). FLAGGED TO COORDINATOR for a compat decision; until then this is the one remaining config surface that is silently ignored, pinned by `TestNewPlanner_AcceptsSupportedConfig`. Size M (M4.4). |
| `Arguments` -- `RenderConfig` (CSV / GraphQL / JSON rendering), `RenameTypeTo` | MISSING-LOUD | Renderer variants never consulted. M4.1: typed refusal (`ErrArgumentRenderingNotSupported`) on any non-default `RenderConfig` or `RenameTypeTo`. Niche (REST-flavored); adapter does not map them -> corpus-safe. Size S to implement (M4.4). |
| `Path` (response-path mapping) | MISSING-LOUD | planv2 always emits default mapping. M4.1: typed refusal (`ErrFieldPathMappingNotSupported`). Federation configs rarely carry it (adapter skips); stitching/REST configs do. Size M to implement (M4.4). |
| `UnescapeResponseJson` | MISSING-LOUD | Escaped-JSON-string field unwrapping never set on planv2 fields. M4.1: typed refusal (`ErrUnescapeResponseJSONNotSupported`). Size S to implement (M4.4). |
| `HasAuthorizationRule` | PARITY | **The security row -- closed by the M4.1 Info wave.** planv2 stamps the flag onto per-field `FieldInfo` and `FetchInfo.RootFields`; postprocess derives the same `AuthorizationCoordinates` as v1 (differential oracle `TestFieldInfoParity`; executed-truth deny test `TestExecutionEngine_FieldAuthorization` under `PLANV2=1`). |
| `SubscriptionFilterCondition` | PARITY | See Section 7. M4.4 LANDED (was MISSING-LOUD): built into `resolve.SubscriptionFilter` in subscription lowering (`lower/subfilter.go`, v1-exact), set on `GraphQLSubscription.Filter`. `ErrSubscriptionFilterNotSupported` removed. Executed-truth SkipEvent parity in `differential/exec_subfilter_test.go`. |
| `DisableDefaultMapping` | N-A | v1 marks it "has no effect as of now, remove?" (configuration.go:140). |

## 4. `DataSourceMetadata` / `FederationMetaData`

| Capability | Status | Notes |
|---|---|---|
| `RootNodes`/`ChildNodes` incl. `ExternalFieldNames` | PARITY | Builder consumes both, external key fields ride representations (D5p key-tail float; `builder.go:181-196`). |
| `Keys` incl. `DisableEntityResolver` (`resolvable: false`) | PARITY | `builder.go:132-134,695-704`; drives the D10 provable-non-resolvability narrowing (M2 mini-wave; `non-resolvable-interface-object` suite passes). |
| Key `Conditions` (implicit-key conditions) | PARTIAL | Carried on edges (`hypergraph/graph.go:51-54`) and unioned across discoveries; the D7 applicability filter is a documented global-presence, completeness-favoring approximation (`search/search.go` HONEST SCOPE; M1 residual 4). |
| `Requires` (incl. arguments, conflicts, chains, distributed tails) | PARITY | D7pp requires-scoped resolution + D11.10 pipeline (M2 requires-chain wave); DV-006/DV-007 verified. Executed-truth residual: fragment-conditioned representation VALUES (DIVERGENCES registered). |
| `Provides` | PARITY | `builder.go:1154`; provides suites pass (class-C wave). |
| `EntityInterfaces` / `InterfaceObjects` | PARITY | M2 class-C + M3 C-disc closure (discriminator-jump synthesis, `db6e9887`); real-audit interface-object failures pass. |
| `Directives` (`DirectiveConfiguration` renames) | MISSING-LOUD | Directive renaming never consulted. M4.1: typed refusal (`ErrDirectiveRenamesNotSupported`) when any datasource carries a non-empty table; not adapter-mapped -> corpus-safe. Size S to implement. |
| `CostConfig` | PARITY (attribution subset, DV-012) | Section 2 cost row: consumed by the M4.5 `CostCalculator` via `plan.NewCostCalculator`. |
| `FetchReasonFields` / `RequireFetchReasons` | MISSING | Section 2 fetch-reasons row. |
| `DataSourcePlanningBehavior` (MergeAliasedRootNodes, AllowPlanningTypeName, AlwaysFlattenFragments) | N-A | Behavior flags for v1's visitor/datasource-planner pipeline, which planv2 never runs; the equivalent decisions are lowering-internal. Kept honest per kind when new kinds land (Section 1). |

## 5. Planner API, plan kinds, `plan.Opts`

| Capability | Status | Notes |
|---|---|---|
| Drop-in signature `NewPlanner(config)` + `Plan(op, def, name, report, opts...)` | PARITY | Byte-compatible seam; the router patch swaps planners with zero call-site change (`internal-notes/router-seam`). Concurrent `Plan` on one `Planner` safe, matching v1 (planv2.go doc). |
| `SynchronousResponsePlan` | PARITY | Differential + audit + executed-truth waves. |
| `SubscriptionResponsePlan` | PARITY (features PARTIAL, Section 7) | D11.12; trigger+shape differential parity, live-ws executed truth. |
| `DeferResponsePlan` (incremental delivery) | PARITY (residuals registered) | D11.13; postprocess/resolve run planv2 defer plans unchanged (`exec_defer_test.go`). Registered residuals: deferx@requires scope-0 inputs; deferxdistributed-key/member-fragments untested; v1's 73-subtest mocked defer suite unusable by harness construction (DIVERGENCES "@defer LANDED"). |
| `@stream` | N-A | v1 has no @stream support either (no planner/resolver hits). |
| `plan.Opts` -- `IncludeQueryPlanInResponse` (the only v1 opt) | PARTIAL | planv2 attaches `QueryPlan` to every fetch unconditionally (`obligation_driven.go`), so the data the opt gates is always present (superset); the toggle itself is accepted-and-ignored. Cost: query-plan strings built even when nobody asked. Size S (M4.6 quality item). |
| Unknown future `Opts` | N-A (adjudicated M4.1) | `plan.Opts` is `func(*_opts)` -- an opaque closure over the v1 planner's UNEXPORTED options struct. planv2 cannot construct an `_opts` value, so it can neither invoke nor inspect a passed option; a "refuse unknown opts" check is unimplementable outside the v1 package. The only option constructible through the public API is `IncludeQueryPlanInResponse` (row above), whose effect planv2 satisfies as a superset -- so acceptance is sound by construction, documented in refuse.go and `Plan`'s godoc. Revisit only if the v1 package ever grows a second `Opts` constructor. |
| `Plan.GetCostCalculator`/`SetCostCalculator` | PARITY | M4.5: when `ComputeCosts` is set the facade attaches a working `CostCalculator` (Section 2 cost row); otherwise nil, matching v1's plan-with-cost-disabled. |

## 6. Resolver features the plan encodes

| Capability | Status | Notes |
|---|---|---|
| `resolve.FetchInfo` | PARTIAL | planv2 sets `DataSourceID`, `DataSourceName`, `OperationType`, `QueryPlan` (both lowering paths -- the DEFECT-1 fix made the query-plan printer nil-safe by construction) and, since M4.1, `RootFields` (`[]GraphCoordinate` incl. `HasAuthorizationRule`; `lower/info.go rootFieldCoords` -- the fetch's top-level coordinates on its entry type plus top-level member-fragment fields, v1's fieldIsChildNode boundary). Still MISSING: `CoordinateDependencies` (adjudicated inverted-default row, Section 2) and `FetchReasons`/`PropagatedFetchReasons` (MISSING-LOUD, Section 2). |
| `resolve.FieldInfo` per field | PARTIAL (was the highest-impact gap; M4.1) | Emitted on every response field by the obligation-driven path (`lower/info.go buildFieldInfos`): Name, ExactParentTypeName, ParentTypeNames (owner + interface implementers, sorted), NamedType, `Source.IDs`/`Names`, HasAuthorizationRule. Differential oracle: `TestFieldInfoParity` (attribute equality vs v1; Source as non-empty SUBSET -- planv2 attributes the ONE route the search chose, v1 lists every planner that touched the field). Stated non-parity remainder: `Source` single-route subset; `IndirectInterfaceNames` not populated (no consumer in this repo); `FetchID` zero (v1's visitor never sets it either -- postprocess reads the zero value). Suppressed under `DisableIncludeInfo` (v1 parity). |
| Authorization -- `@authenticated`/`@requiresScopes` propagation | PARITY | **Closed by the M4.1 Info wave (was the loudest row in this register).** postprocess `collect_authorization_coordinates.go` now derives the same `AuthorizationCoordinates` from planv2 plans as from v1 plans (`TestFieldInfoParity` exact-coordinate oracle; facade test `TestPlanner_AuthorizationCoordinates`), and the executed-truth deny test (`execution/engine TestExecutionEngine_FieldAuthorization`, run under `PLANV2=1`) proves an unauthorized request is REJECTED with `UNAUTHORIZED_FIELD_OR_TYPE`, not silently resolved. |
| Field `Position` (error-message positions) | MISSING (adjudicated) | Section 2 `DisableResolveFieldPositions` row. |
| `SelectResponseErrorsPath` (subgraph error propagation) | PARITY | Closed M3 executed-encoding wave (`67768b1a`); v1 `DefaultPostProcessingConfiguration` parity, executed-truth verified. |
| Postprocess compatibility (dedup, parallel nodes, defer tree, input templates, fetch ordering) | PARITY | planv2 output runs the untouched v1 postprocess+resolve pipeline (executed-truth harnesses; D11.8 explicitly delegates the member-variant fold to postprocess). |
| Single flight | N-A | Resolver/runtime concern (`DataSourceLoadTrace.SingleFlightUsed`); v2 has no planner-set single-flight switch. |
| Caching hints | N-A | No v1 planner-level caching-hint surface exists in v2. |
| `GraphQLResponseInfo.OperationType` | PARITY | Set on all three lowering paths (`lower.go:306`, `obligation_driven.go:306`, `subscription.go:122`). |

## 7. Subscription features

| Capability | Status | Notes |
|---|---|---|
| Trigger/response split, flush interval, WS/SSE/SSE-POST, subprotocol negotiation (auto/ws/transport-ws), forwarded client headers (+regexes), startup hooks (`SubscriptionOnStartFn`) | PARITY | D11.12 + `planv2/transport.go`; exec-config adapter maps the `customGraphql.subscription` block (protocol + subprotocol enums) -- the v1-error class on exec-config corpora is closed. Trigger-input BYTES differ from v1 (key order/document print -- adjudicated; resolver reads the envelope key-wise). |
| Subscription filters (`@openfed__subscriptionFilter` -> `SubscriptionFilterCondition` -> `resolve.SubscriptionFilter.SkipEvent`) | PARITY | M4.4 LANDED (was MISSING). `GraphQLSubscription.Filter` is now built in subscription lowering (`lower/subfilter.go`) mirroring v1's consumption chain (field config -> `buildSubscriptionFilterCondition` -> `GraphQLSubscription.Filter`) EXACTLY, argument templates included. Executed-truth SkipEvent parity (`differential/exec_subfilter_test.go`); structural v1-parity in the differential subscription oracle (`canonFilter`). |
| Shared engine-lifecycle subscription client (connection multiplexing) | PARTIAL | Per-START client scoped to the subscription's context (goleak-driven design; transport.go limitation 2). Correctness unaffected; upstream connection sharing lost. Size S once the client seam exists. |
| EDFS event configuration (Kafka/NATS triggers) | MISSING | Section 1 pub/sub row -- the (c) case. |
| Dual-role subscription types, renamed subscription roots | PARITY | D11.12 root scoping + D5pp reverse (pinned: `TestPlanner_MergedDualRoleSubscriptionType`, twin-rename regressions). |

## 8. Introspection / debugging surface

| Capability | Status | Notes |
|---|---|---|
| Query-plan printing (router dev-mode; `resolve.FetchTreeQueryPlanNode.PrettyPrint`) | PARITY | Every planv2 fetch carries `QueryPlan` + non-nil `Info` (printer dereferences Info unconditionally -- the encoding wave fixed the nil-panic class), and since M4.1 the `RootFields` block prints populated (Section 6 FetchInfo row). |
| ART (execution tracing; `TestFederationIntegrationTestWithArt`) | PARTIAL (diagnosed M4.1) | Still fails under `PLANV2=1`; confirmed pre-existing by a HEAD-worktree baseline run. M4.1 re-diagnosis: with the Info wave landed the ART trace RENDERS COMPLETELY (no nil-Info interaction; the executed data is correct) -- the remaining diff is the GOLDEN FIXTURE, which encodes v1's exact plan bytes: fetch-tree nesting (planv2 emits an explicit Parallel node where v1's golden has a bare Single), document print style (planv2's spaced print vs v1's compact print), planv2's complete member expansion (`... on Store` kept where v1 drops it), and JSON key order. A harness-construction mismatch (plan-byte golden), not an emission or planning gap; closing it is an adjudicate-or-regenerate-fixture decision, size S. |
| Diagnostics channel (`PlanWithDiagnostics`, typed D10 route-fallback warnings) | PARITY+ | planv2-only addition; v1 has no equivalent non-fatal channel. |
| `DebugConfiguration` prints | N-A | Section 2 Debug row. |

## 9. Known behavioral divergences and open defects (cross-reference)

Not feature gaps, but M4 planning inputs; the authoritative register is DIVERGENCES.md.

- **`mutations_3` -- shareable mutation root fields split across subgraphs => mutation
  double-execution.** Silent-wrong-EFFECT severity; the branch's most
  correctness-significant open defect. Search/obligation-layer fix (per-root-field
  subgraph pin). Until closed, planv2 must not front schemas with shareable mutation
  root fields.
- **D6ppp condition-blind jump transport admits position-unobtainable ghosts** (M3 final
  review I-1) -- silent-wrong-data, abstract VALUE-type members only; fix queued as its
  own kernel-adjacent wave (condition-aware transport admission).
- **`override-type-interface_0`** -- `@override` on interface-typed position (builder
  D3 work, queued with the search wave).
- **`corrupted-supergraph-node-id_0/_2/_5`** -- route-choice difference on a corrupted
  fixture; adjudication candidate, no fix queued.
- **DV-009** -- response key ORDER is spec CollectFields order, permanently diverging
  from v1's rewriter artifact (owner-adjudicated FINAL).
- Executed-truth representation-value residuals: fragment-conditioned `@requires`
  values; list-valued key paths (name-keyed object builder).
- D10 route fallback: register at 2 frozen benign witnesses; customer-corpus reliance
  53 ops/455 events -- retirement gated on both reaching zero.

## 10. Parity scorecard

Counting UNIQUE capabilities across Sections 1-8 -- a capability appearing in more than one
table (e.g. cost in Section 2/Section 4/Section 5, authorization in Section 3/Section 6, minification in Section 1/Section 2) is counted
once at its primary row; Section 9's divergences/defects are tracked separately in
DIVERGENCES.md. 59 capabilities total. Post-M4.1 (the Info wave + the typed-loud sweep):

| Status | Count | Delta vs pre-M4.1 |
|---|---|---|
| PARITY | 21 | +2 (authorization propagation; query-plan printing) |
| PARTIAL | 7 | FieldInfo entered (Source-subset remainder), query-plan printing left |
| MISSING (silent) | 4 | -16 |
| MISSING-LOUD (typed refusal) | 15 | +13 |
| N-A (justified) | 12 | +1 (plan.Opts, adjudicated unimplementable) |

The 4 remaining silent-MISSING rows are each individually adjudicated in their tables:
`CustomScalarTypeFields` (refusal unreachable through the public config surface; M4.6 accessor
seam), `DisableResolveFieldPositions`/field positions and
`DisableIncludeFieldDependencies`/CoordinateDependencies (inverted-default flags -- a typed
refusal on the zero value would refuse every default config), and `ObjectFieldSource`
(deliberate corpus-compat flag to the coordinator -- refusing could reject real production
supergraphs). Authorization propagation, pre-M4.1 the register's loudest row, is closed;
the shareable-mutation double-execution defect (Section 9) remains M4.0's blocker.

## 11. Recommended M4 campaign plan

Business-weighted order (Cosmo adoption path: EDFS first among datasource kinds, then
gRPC/ConnectRPC), with correctness blockers and adoption-safety pulled ahead of
everything because silent gaps poison any soak that runs concurrently with them.

| Wave | Content | Class | Size | Rationale / dependencies |
|---|---|---|---|---|
| M4.0 -- correctness blockers | mutations_3 root-field pinning; D6ppp condition-aware transport admission; override-type-interface | model (search/builder) | M | Both silent-wrong classes; every later wave's soak data is untrustworthy while they're open. Independent of all datasource work. |
| M4.1 -- adoption safety: Info + typed-loud config | Emit `FieldInfo` + `FetchInfo.RootFields` (auth flags, telemetry Source.IDs), field positions, honor `DisableIncludeInfo`/`DisableResolveFieldPositions`; typed refusal for every config/Opts surface still ignored (auth rules present => reject until wave lands; unknown Opts => reject); re-diagnose ART on top | (b) | M-L | Converts every silent ignore in Sections 2-6 into PARITY or MISSING-LOUD -- the precondition for honest production trials. No routing-model content. |
| M4.2 -- EDFS / pub-sub | Metadata-only (SDL-less) subgraph acceptance in `hypergraph.Build`; pubsub trigger transport entries (`resolve.SubscriptionDataSource`/`HookablePubsubDatasource`); the (c) extension: composed-schema-informed payload typing for metadata-only trigger fields | (a)+(b)+(c) | L | First datasource wave by business weight (Cosmo adoption blocker; PUBSUB kinds observed in real exec-configs). The (c) slice follows EXTENDING.md spec-first. (a) is shared with M4.3's static/introspection. **SPEC LANDED (M4.2):** the (c) capability model is defined -- `FS-EDFS-1..5` (FEDERATION_SEMANTICS Section 16), `D5-EDFS` (event-source root entrance, composed-schema payload typing, additive to E / kernel-untouched), `D11.12-EDFS` (pub/sub trigger transport); the metadata-only residual is rescoped evidence-gated (DV-011, drift stays fail-loud). IMPLEMENTATION/adapter-PUBSUB-decode + transport wire encoding are reconstructed from the nodev1 proto and need the coordinator's real-router validation before RESOLVED. |
| M4.3 -- gRPC + ConnectRPC, static, introspection | Transport-table entries + fetch encoding for `Configuration.GRPC` datasources (reuse `grpc_datasource` compiler/DataSource, both transports); static + introspection fetch encoding riding M4.2's (a) | (b) | M | Pure lowering/transport; zero kernel risk. After EDFS per business weight; after M4.2 for the shared SDL-less acceptance. Closes the "shape-only fetch" class entirely. |
| M4.4 -- subscription filters + field-config remainder | `SubscriptionFilterCondition` -> trigger `Filter` (**LANDED**); `ObjectFieldSource`/`RenderConfig`/`RenameTypeTo` arguments; `Path` mapping; `UnescapeResponseJson`; `CustomScalarTypeFields` | (b) | M | Field-configuration table consumption -- one shared seam (planv2 reading `config.Fields`), several small emitters behind it. **Subscription-filter slice LANDED:** `lower/subfilter.go` builds `resolve.SubscriptionFilter` (v1-exact) and sets it on `GraphQLSubscription.Filter`; refusal removed; executed-truth SkipEvent parity. The remaining field-config emitters (Path/UnescapeResponseJson/render configs) stay MISSING-LOUD. |
| M4.5 -- cost | `ComputeCosts`/`CostCalculator`/`CostConfig`/list-size defaults/implementing-type weights | (b) (plan-adjunct) | M-L | **LANDED.** v1's calculator reused byte-for-byte via `plan.BuildCostCalculator`; the `planv2/cost` package supplies the single-route datasource attribution and the facade attaches the calculator to every plan. Cost is post-plan (search kernel untouched, no oracle extension). Attribution subset documented as DV-012. Guards: differential cost parity + facade nil-calculator guard. |
| M4.6 -- transport/quality parity | Configured `http.Client` + shared engine-lifecycle subscription client (the one factory-seam item behind both transport.go limitations); `MinifySubgraphOperations`; `EnableOperationNamePropagation`; unconditional-QueryPlan toggle | (b) | S-M | Perf/quality; no semantics. The client seam requires the one agreed v1 `plan` package touch (exposing the factory clients) -- schedule when that edit is negotiable. |
| M4.7 -- residual polish | Deferxrequires scoping; representation-value builders (fragment-conditioned requires, list-valued keys); scoped/unscoped twin merge; D10 retirement check | model (small) | S-M | Executed-truth residuals from DIVERGENCES; batched last as none blocks adoption and each is individually pinned. |

Dependency spine: M4.0 -> M4.1 -> {M4.2 -> M4.3} || {M4.4, M4.5} -> M4.6 -> M4.7. The two
datasource waves are the only ones touching what planv2 accepts; everything else is
emission-side and cannot change a routing decision -- the pluggability contract (Section 0) is
what makes this campaign parallelizable.

## 12. Cosmo federation-directive coverage register (M4.1)

Owner directive: the FULL Cosmo directive index
(cosmo-docs.wundergraph.com/federation/federation-directives-index) must be supported. This
register adjudicates all 27 directives FROM CODE: where (and whether) this engine consumes each
one, what its compiled form is (Cosmo's composition compiles most directives into the router
execution config, which this engine sees as `plan.Configuration` / `plan.DataSourceConfiguration`
fields -- the directive string itself often never appears here), planv2's status, and the owning
M4 wave. Adjudication classes:

- **planner-relevant** -- the v1 planner (or a datasource's plan-building) consumes it, directly
  or via its compiled config form; planv2 must match it or refuse typed.
- **composition-only** -- resolved entirely by Cosmo composition; this engine sees only the
  result (node lists, merged SDL). Nothing for a planner to do.
- **runtime-only** -- consumed by validation/introspection/resolver, not by planning.

| Directive | Compiled form / consumption in this repo | Class | planv2 status | Wave |
|---|---|---|---|---|
| `@deprecated` | Introspection output only (`pkg/introspection/converter.go`, `IsDeprecated`/`DeprecationReason`) | runtime-only (introspection) | N-A for planning; introspection datasource itself is Section 1's MISSING-LOUD row | M4.3 |
| `@oneOf` | Operation-validation rule (`pkg/astvalidation/operation_rule_values.go` `objectValueViolatesOneOf`) -- read live from the schema | runtime-only (validation) | N-A -- validation runs upstream of planning, shared by both planners | -- |
| `@semanticNonNull` | ZERO trace anywhere in `v2/` (not even parsing) | composition-only | N-A -- nothing in this engine consumes it (v1 included) | -- |
| `@specifiedBy` | Introspection output only (`pkg/introspection/generator.go` `SpecifiedByURL`) | runtime-only (introspection) | N-A for planning | M4.3 |
| `@authenticated` | `plan.FieldConfiguration.HasAuthorizationRule` -> planner stamps `FieldInfo`/`RootFields` auth flags -> postprocess `collect_authorization_coordinates.go` -> resolver BatchAuthorizer | planner-relevant | PARITY (M4.1 Info wave: FieldInfo + RootFields emitted; differential coordinate oracle + executed-truth deny test) | M4.1 (landed) |
| `@composeDirective` | ZERO trace in `v2/` | composition-only | N-A | -- |
| `@connect__fieldResolver` | Read live from subgraph SDL by the gRPC datasource's execution-plan builder (`grpc_datasource/execution_plan.go` `fieldResolverDirectiveName`, resolver context fields) | planner-relevant (gRPC kind only) | Rides the gRPC transport-table wave -- the `grpc_datasource` package is reused as-is, so coverage arrives with the (b)-side encoding | M4.3 |
| `@edfs__kafkaPublish` | Router-repo pubsub datasource; engine seam is `resolve.SubscriptionDataSource`/`HookablePubsubDatasource`; `PUBSUB` kinds observed in real exec-configs (adapter skips them) | composition + router runtime | MISSING (Section 1 pub/sub row; the (c) model-extension case) | M4.2 |
| `@edfs__kafkaSubscribe` | same as above | composition + router runtime | MISSING (Section 1) | M4.2 |
| `@edfs__natsPublish` | same as above | composition + router runtime | MISSING (Section 1) | M4.2 |
| `@edfs__natsRequest` | same as above | composition + router runtime | MISSING (Section 1) | M4.2 |
| `@edfs__natsSubscribe` | same as above | composition + router runtime | MISSING (Section 1) | M4.2 |
| `@external` | `plan.TypeField.ExternalFieldNames` -> `HasExternalRootNode`/`HasExternalChildNode` node selection | planner-relevant | PARITY (Section 4 RootNodes/ChildNodes row; D5p key-tail float) | done (M2) |
| `@extends` | Normalized away by `pkg/astnormalization/extends_directive.go` before planning | composition-only (schema normalization) | N-A | -- |
| `@inaccessible` | Read live from the composed schema: enum-value skip (v1 `visitor.go:791`), inaccessible-type possible-types filtering; operation/variable validation | planner-relevant (minor) + validation | PARITY -- lower emits `resolve.Enum.InaccessibleValues` and filters inaccessible types from `PossibleTypes` (`lower.go` `enumLeafValues`/`typeInaccessible`) | done (M1.5) |
| `@interfaceObject` | `FederationMetaData.InterfaceObjects`/`EntityInterfaces` | planner-relevant | PARITY (Section 4 row; M2 class-C + M3 C-disc closure) | done (M3) |
| `@key` | `FederationMetaData.Keys` (SelectionSet, DisableEntityResolver, Conditions) | planner-relevant (core) | PARITY (Section 4 rows; Conditions PARTIAL -- D7 approximation, M1 residual 4) | done |
| `@link` | No consumption (audit boilerplate only) | composition-only | N-A | -- |
| `@openfed__configureDescription` | ZERO trace in `v2/` | composition-only | N-A | -- |
| `@openfed__requireFetchReasons` | `plan.TypeField.FetchReasonFields` -> `DataSourceMetadata.RequireFetchReasons()` lookup + `Configuration.BuildFetchReasons`/`ValidateRequiredExternalFields`; v1 planner derives `FetchInfo.FetchReasons`/`PropagatedFetchReasons` (`visitor.go` `buildFetchReasons`/`getPropagatedReasons`); resolver propagates `fetch_reason` extension (`loader.go`) and validates nullable requires via `tainted_objects.go` | planner-relevant | MISSING-LOUD (M4.1: `ErrFetchReasonsNotSupported` at NewPlanner when `BuildFetchReasons`/`ValidateRequiredExternalFields` set). See sizing note below | M4.4 (recommended -- previously UNOWNED, see below) |
| `@openfed__subscriptionFilter` | `plan.FieldConfiguration.SubscriptionFilterCondition` -> v1 `path_builder_visitor.go` `buildSubscriptionFilterCondition` -> `resolve.SubscriptionFilter.SkipEvent` | planner-relevant + runtime | PARITY (M4.4 LANDED, was MISSING-LOUD). Subscription lowering (`lower/subfilter.go`) mirrors v1's `buildSubscriptionFilterCondition`/`buildSubscriptionFieldFilter` EXACTLY (same And/Or/Not/In recursion, same `{{ args.path }}` -> ContextVariable resolution) and sets the built `resolve.SubscriptionFilter` on `GraphQLSubscription.Filter` (v1 `visitor.go configureSubscription`). Malformed templates fail lowering loudly (no silent drop). Executed-truth: `differential/exec_subfilter_test.go` drives `SkipEvent` -- an event the filter must skip IS skipped, one that passes IS delivered, matching v1 per event. The `ErrSubscriptionFilterNotSupported` refusal is removed. | M4.4 (landed) |
| `@override` | Composition rewrites node OWNERSHIP; no dedicated config field -- the planner sees only the resulting RootNodes/ChildNodes | composition-only | N-A by construction, EXCEPT the `override-type-interface_0` builder defect (Section 9) -- an M4.0 correctness item, not a directive gap | M4.0 (defect) |
| `@provides` | `FederationMetaData.Provides` | planner-relevant | PARITY (Section 4 row) | done (M2) |
| `@requires` | `FederationMetaData.Requires` (arguments, conflicts, chains, distributed tails) | planner-relevant (core) | PARITY (Section 4 row; executed-truth representation-value residuals registered) | done (M2/M3) |
| `@requiresScopes` | Same compiled form as `@authenticated`: `HasAuthorizationRule` (scope SETS live in the router's authorizer, not in this engine) | planner-relevant | PARITY (M4.1 Info wave -- indistinguishable from `@authenticated` at this layer) | M4.1 (landed) |
| `@shareable` | Composition only: the field simply appears in several datasources' node lists | composition-only | N-A by construction, EXCEPT the `mutations_3` shareable-mutation-root double-execution defect (Section 9) -- an M4.0 correctness item | M4.0 (defect) |
| `@tag` | Testdata/`@link` boilerplate only; contract concern | composition-only | N-A | -- |

Tally: 11 planner-relevant (`@authenticated`, `@connect__fieldResolver`, `@external`,
`@inaccessible` (minor), `@interfaceObject`, `@key`, `@openfed__requireFetchReasons`,
`@openfed__subscriptionFilter`, `@provides`, `@requires`, `@requiresScopes`); 5 EDFS directives
are router-side datasource work (M4.2); the remaining 11 are composition-, validation-, or
introspection-only with nothing for a planner to consume. Of the 11 planner-relevant: 10 are
PARITY (`subscriptionFilter` landed M4.4), 1 is MISSING-LOUD (`requireFetchReasons`).

### `@openfed__requireFetchReasons` sizing (previously unscoped)

Nobody had scoped this item; adjudicated from code this wave. v1's machinery is three-layered:

1. Config plumbing (~15 lines, exists): `TypeField.FetchReasonFields` indexed into the
   `RequireFetchReasons()` coordinate lookup at `InitNodesIndex`.
2. Planner derivation (~200 dense lines, the real cost): `visitor.go buildFetchReasons` (~90
   lines: per-fetch `FetchReason{TypeName, FieldName, BySubgraphs, ByUser, IsKey, IsRequires,
   Nullable}` from the planner's field-dependency maps) + `getPropagatedReasons` (~90 lines:
   filter to the `RequireFetchReasons()` coordinates with interface/implementing-type fan-out
   both directions). Depends on per-fetch coordinate-dependency structures planv2 does not
   build yet -- the natural sibling of the `CoordinateDependencies` gap (Section 6), so the two
   should land together.
3. Runtime consumption (reusable AS-IS if planv2 populates the same `FetchInfo` fields):
   `loader.go` marshals `PropagatedFetchReasons` into the subgraph request's
   `extensions.fetch_reasons` (~15 lines); `tainted_objects.go` (179 lines + 355-line test)
   reads `FetchReasons` where `IsRequires && Nullable` to skip entities whose requires inputs
   came back null-with-error (`ValidateRequiredExternalFields`).

Size: M (planner-side derivation against planv2's group/obligation structures; runtime rides
free). Recommendation: attach to M4.4 (the config.Fields consumption wave) alongside
`CoordinateDependencies`; until then the M4.1 typed refusal (`ErrFetchReasonsNotSupported`)
keeps the surface loud.
