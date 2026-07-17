# Federation Query-Planning Semantics -- a Normative Per-Construct Reference

**Status:** normative, timeless. This document states, construct by construct, what a *correct
federation query plan* is. The GraphQL specification defines requests and responses; the Apollo
Federation specification defines directives and composition; **no published document defines what a
correct query plan is** -- every router re-derives planning behavior from the reference
implementation's observable output. This document is a candidate for that missing reference: for
each federation construct it states the planning obligations as numbered, falsifiable propositions,
and -- crucially -- classifies each behavior by *where its authority actually comes from*, so an
implementer knows which behaviors are mandatory, which are conventions, and which are contested.

**Scope.** Query *planning* only: the mapping from (supergraph configuration, client operation) to
a plan -- a set of subgraph fetches with documents, input representations, dependency order, and a
response shape. Composition (whether a set of subgraphs composes) is covered only as
*preconditions*; runtime execution (loaders, batching internals, error propagation at resolve time)
is covered only where the execution contract constrains what a plan must look like. Nothing here
describes any particular implementation's status: propositions are stated over *observable plan
properties*, independent of who implements them or when.

**Relationship to the planner-v2 model.** Where a proposition is derived from the formal model, the
derivation cites `FORMAL_SPEC.md` (definitions `D1`-`D11`, invariants `I1`-`I4`) and `PROOFS.md`
(T1-T5). Contested behaviors cite the adjudications in `DIVERGENCES.md`. Behavioral evidence cites
the audit corpus (`v2/pkg/engine/planv2/audit/testdata`, extracted from The Guild's MIT-licensed
`graphql-federation-gateway-audit`, used as an adjudicated regression corpus -- see
`DIVERGENCES.md`). Terminology follows `GLOSSARY.md`. This document is not scanned by the
`docscheck` bold-term test; bold is used for RFC-2119 keywords and emphasis only.

**Formal dispositions of the FOLKLORE class.** Each of the 23 FOLKLORE propositions is
individually dispositioned -- derived from the formal model, adopted as a named axiom, or
registered as a contingent divergence -- in `FEDERATION_SEMANTICS_FORMAL.md` (the Stage-1.5
formalization). After that pass, no proposition in this document rests on unformalized folklore;
the updated census is stated there (Section 5) and restated in Section 17 below.

---

## 0.1 Authority hierarchy

Restated from `DIVERGENCES.md` (the repository's standing adjudication policy). When expected
behavior is contested, adjudicate in this order:

1. **GraphQL specification** (October 2021 edition): response shape, field collection and merging,
   null propagation, validation, subscription semantics.
2. **Apollo Federation specification and composition semantics**: the directive contracts
   (`@key`/`@requires`/`@provides`/`@external`/`@override`/`@shareable`/`@inaccessible`/
   `@interfaceObject`) and the subgraph specification (`_entities`, `_Any`, `_Entity`,
   `_service`).
3. **Apollo reference implementation behavior** (`apollo-federation` planner and router), *except
   where it contradicts 1-2 or its own documented spec* -- the reference has shipped documented
   planner defects, and being the reference does not mean being correct.
4. **A stated formal model** (here: `FORMAL_SPEC.md` `D1`-`D11`, `I1`-`I4`), which must itself be
   justified upward against 1-3.
5. **Third-party test-suite expectations**, treated as regression corpora, never as specification.

## 0.2 The three-way authority classification

Every behavior in this document carries exactly one of three classifications:

- **SPEC-PINNED** -- a document at authority level 1 or 2 states the behavior. The classification
  quotes or cites the exact text. Be warned how *little* of federation planning this covers: the
  Federation directive reference and subgraph specification pin the directive signatures, the
  `_entities` request/response contract, and a handful of prose sentences about intent -- they say
  nothing about how plans are constructed.
- **FOLKLORE** -- behavior no document mandates, established by the reference implementation's
  observable output and reproduced by independent implementations. Each FOLKLORE call states what
  observation grounds it (a corpus suite, a cited reference-implementation source location, or
  cross-vendor corroboration). Where a convention is believed but not grounded, this document says
  **unverified convention** rather than asserting.
- **OURS** -- behavior this document *defines*, derived from the formal model or from the GraphQL
  specification upward. Each OURS call cites its derivation. OURS propositions are offered as the
  proposed reference semantics; a reader who rejects the derivation knows exactly what they are
  rejecting.

Draft-stage GraphQL RFCs (notably incremental delivery / `@defer`) are unratified and are treated
at FOLKLORE authority even when their text is quoted -- see Section 15.

## 0.3 Observable plan vocabulary

Propositions are phrased over these observables and nothing else (no planner internals):

- **fetch** -- one subgraph request in the plan: a target subgraph, an executable GraphQL
  *fetch document*, an operation kind (query/mutation/subscription), an attachment position in the
  response (*response path*), and for entity fetches an *input representation* template.
- **entity fetch** -- a fetch whose document's top-level field is `_entities(representations: ...)`.
- **root fetch** -- a fetch whose document enters through a root operation type's fields.
- **dependency order** -- the partial order over fetches induced by "fetch *v* consumes values
  produced by fetch *u*".
- **representation** -- the per-instance input object sent in `_entities(representations:)`:
  `__typename` plus data fields.
- **response shape** -- the tree of response keys (with aliases, nesting, list-ness, and type
  conditions) the plan commits to produce.
- **planning error** -- a typed refusal to produce a plan.

A proposition is *checkable* when a test can decide it from these observables: which subgraph a
field is fetched from, what a fetch document says, whether it validates against the target
subgraph's schema, what order fetches run in, what a representation contains, what the response
shape is.

## 0.4 Proposition IDs and conformance claims

Propositions are numbered `FS-<CONSTRUCT>-<n>` (e.g. `FS-KEY-3`, `FS-REQ-7`) and never renumbered:
retired propositions are marked withdrawn, new ones take fresh numbers. The complete index is Section 17.

**Conformance claims.** A plan-level conformance harness decides each proposition through five
observable oracles: (1) every fetch document validates against its target subgraph schema; (2)
root fetches enter only client-requested root fields; (3) the fetch dependency order is acyclic and
respects data dependencies; (4) every requested, resolvable leaf is covered by exactly the
subgraph(s) the propositions permit, at the response position the client selected; (5) the response
shape can produce the operation's expected response. The audit corpus supplies behavioral evidence:
where a proposition names a corpus suite as *witness*, that suite exercises the behavior and its
expectation has been adjudicated under Section 0.1 (adjudicated deviations are registered in
`DIVERGENCES.md` -- e.g. DV-005/006/007, where the corpus expectation was upheld at levels 1-2
against observed reference-implementation defects, and DV-008/009, where response-equivalence and
GraphQL-spec field order were adjudicated). This document deliberately carries no pass counts or
scoreboard figures: those are point-in-time measurements and live in the repository's scoreboard
artifacts; the propositions are the timeless part.

---

## 1. Cross-cutting plan obligations (`FS-PLAN`)

Obligations every plan carries regardless of which directives are in play. Everything later builds
on these.

### Authority classification

- Document validity (FS-PLAN-1): **OURS**, derived from GraphQL spec Section 5 (Validation): a service
  only executes requests known to be valid, so a fetch whose document fails validation against its
  target subgraph cannot contribute data to any response -- an invalid document in a plan is a
  planning defect, not a runtime unlucky case.
- Response shape and response-key order (FS-PLAN-3/4): **SPEC-PINNED** -- GraphQL spec Section 6.3
  (*CollectFields*: grouped fields are ordered by first occurrence in selection-set traversal) and
  Section 7.1 (the response map preserves that grouped-field order). See `DIVERGENCES.md` DV-009 for an
  adjudication that upheld the spec order against an implementation's rewriter artifact.
- No-foreign-root (FS-PLAN-5): **OURS**, `FORMAL_SPEC.md` `D10` path-consistency. No document
  forbids a router from fetching root fields the client never requested; the derivation is that an
  unrequested root field can carry required arguments (making the fetch invalid) or resolver side
  effects, and on argument-free schemas silently substitutes data from the wrong data path.
- Loud failure (FS-PLAN-6): **OURS**, `FORMAL_SPEC.md` I2 and the L7 no-silent-degrade principle.

### Planning obligations

- **FS-PLAN-1** (MUST) -- Every fetch document in a plan MUST be a valid executable GraphQL document
  against the target subgraph's schema including its federation additions (`_entities`, `_Any`,
  `_service`). *Checkable:* run the GraphQL validator per fetch.
- **FS-PLAN-2** (MUST) -- The fetch dependency order MUST be acyclic, and if fetch *v* consumes a
  value produced by fetch *u* (a representation field, a `@requires` input), then *u* MUST precede
  *v*. Fetches with no data dependency MAY run concurrently.
- **FS-PLAN-3** (MUST) -- The plan's response shape MUST equal the client selection tree exactly:
  same response keys, same aliases, same nesting, same list-ness, type conditions intact
  (`FORMAL_SPEC.md` I4). Selections the plan cannot or must not fetch (e.g. Section 9's narrowed members)
  appear in the shape and render as nulls/absent-per-type-condition -- never as a *missing key*.
- **FS-PLAN-4** (MUST) -- Response keys MUST render in GraphQL *CollectFields* order: a field's
  response position is its first occurrence in selection-set traversal order, including occurrences
  inside inline fragments (GraphQL spec Section 6.3.2, Section 7.1; DV-009).
- **FS-PLAN-5** (MUST NOT) -- A root fetch MUST NOT select a root operation field the client
  operation does not select. (An entity fetch's `_entities` field is exempt: it is the designated
  entry point, Section 11.)
- **FS-PLAN-6** (MUST) -- If no plan satisfying these obligations exists for an operation, planning
  MUST fail with an error naming the unsatisfiable selection; it MUST NOT emit a plan that silently
  omits a resolvable requested field. (The error conditions per construct are below; the
  provably-non-resolvable class is Section 2's FS-KEY-9.)
- **FS-PLAN-7** (MAY) -- A plan MAY be non-minimal (more fetches than necessary, redundant but valid
  selections). Fetch-count and latency minimality are quality properties, not correctness
  properties; no document pins them. Cost-optimal planning is a stated *model* property
  (`FORMAL_SPEC.md` I3, C) -- OURS -- and deliberately not a conformance requirement on other
  implementations.

### Error conditions

Planning MUST error (FS-PLAN-6) exactly when some requested selection has no covering route under
the obligations of this document. Resource-limit refusals (operation too large) are permitted as
*typed* errors distinct from "no valid plan" (`FORMAL_SPEC.md` Section 6.3).

### Worked example

Operation `{ a b }` where `Query.a` lives only in subgraph A and `Query.b` only in B. Obligated
shape: two root fetches -- `query { a }` -> A, `query { b }` -> B -- with no dependency between them
(FS-PLAN-2 allows parallel), response shape `{ a, b }` in client order (FS-PLAN-3/4). A plan that
sent `query { a b }` to A would violate FS-PLAN-1 (invalid document: B's field is not in A's
schema); a plan that resolved `b` by also selecting A's root field `c` would violate FS-PLAN-5.

---

## 2. `@key` (`FS-KEY`)

`directive @key(fields: FieldSet!, resolvable: Boolean = true) repeatable on OBJECT | INTERFACE`

Covers: single-field keys, composite keys (multiple and nested coordinates), multiple keys per
type, `resolvable: false`, and distributed keys (no single subgraph supplies every coordinate).

### Authority classification

- The directive signature and intent are **SPEC-PINNED**: "Key fields are a set of fields that a
  subgraph can use to uniquely identify any instance of the entity" (Apollo Federation directives
  reference). `resolvable: false` "indicates to the router that this subgraph doesn't define a
  reference resolver for this entity" (ibid.).
- The `_entities` transport an entity fetch uses is **SPEC-PINNED** by the subgraph specification
  (Section 11 quotes it). That a router *must* use `_entities` to resolve an entity across subgraphs is,
  strictly, a derivation: the subgraph specification provides no other entry point for it, so any
  plan resolving entity fields cross-subgraph without it has no valid document (FS-PLAN-1).
- Which key to use when several qualify: **FOLKLORE** -- no document states a selection rule; the
  reference implementation selects among reachable keys by its internal cost heuristics. This
  document leaves the choice free (FS-KEY-6) and states determinism as an OURS quality obligation
  (FS-KEY-7).
- Transitive key routing (entity hops across three or more subgraphs, including hops via
  *different* keys): **FOLKLORE** in mechanism, **OURS** in obligation -- completeness (I2) forces a
  plan whenever a chain of valid hops exists. Witness: `keys-mashup`, `parent-entity-call`,
  `parent-entity-call-complex`. Note the reference implementation's published behavior diverges on
  parts of this family (tracked upstream as apollographql/federation#2695-class defects); the
  corpus expectation was upheld at levels 1-2 -- `DIVERGENCES.md` DV-005.
- Distributed keys (FS-KEY-10): **OURS** -- no document contemplates a composite key no single
  foreign subgraph can supply; `FORMAL_SPEC.md` `D7ppp` defines the gathering semantics. Witness:
  `complex-entity-call`.

### Composition preconditions

The type is declared with `@key` in every subgraph that hosts entity data for it (or uses it as an
entity reference); each `fields` argument is a valid FieldSet over the type's fields in that
subgraph; key fields are resolvable in the declaring subgraph (locally or as `@external` carried
fields, Section 5). Composition validates FieldSet syntax and field existence -- planning may assume both.

### Planning obligations

- **FS-KEY-1** (MUST) -- If a plan resolves any field of entity type `T` in subgraph `s2` for
  instances that originate in a different subgraph `s1`, it MUST do so through an entity fetch to
  `s2` (document rooted at `_entities`), never by re-entering `s2`'s root fields for those
  instances.
- **FS-KEY-2** (MUST) -- The entity fetch's representation MUST contain `__typename` and every field
  of the FieldSet of *one* `@key` declared on `T` in the *target* subgraph `s2` (subgraph spec: a
  representation "must include a `__typename` string field" and "must contain all fields included
  in the fieldset of a `@key` directive").
- **FS-KEY-3** (MUST) -- Every representation field MUST be produced by fetches that precede the
  entity fetch, selected at the *same response position* as the instances being resolved -- injected
  into the upstream fetch's document if the client did not request them. (Derived: FS-KEY-2 +
  FS-PLAN-2; the injected fields do not appear in the response shape, FS-PLAN-3.)
- **FS-KEY-4** (MUST) -- For a composite or nested key (`@key(fields: "id organization { id }")`),
  the upstream selection and the representation MUST mirror the FieldSet's nesting: the
  representation carries the nested object with its selected sub-fields. *(The representation shape
  for nested keys is FOLKLORE -- the subgraph specification does not address nested keys in
  representations; the mirrored-object shape is the reference implementation's observable wire
  format, reproduced cross-vendor. Witness: `complex-entity-call`, `FORMAL_SPEC.md` Section 7.2.)*
- **FS-KEY-5** (MUST) -- The key used for an entity fetch MUST be one whose every coordinate is
  producible on the source side of the jump (locally resolvable, `@external`-carried per FS-EXT-2,
  or gathered per FS-KEY-10).
- **FS-KEY-6** (MAY) -- When several keys qualify under FS-KEY-5, the planner MAY use any of them;
  no document constrains the choice. Repeatable `@key` with distinct FieldSets exists precisely to
  offer alternatives.
- **FS-KEY-7** (SHOULD, OURS) -- Plan output SHOULD be a deterministic function of (supergraph,
  operation): semantically equal inputs produce identical plans (`FORMAL_SPEC.md` C.4). Falsifiable
  by permutation testing; stated as SHOULD because no external document requires it.
- **FS-KEY-8** (MUST NOT) -- A plan MUST NOT contain an entity fetch into `s2` for type `T` when
  every `@key` on `T` in `s2` is declared `resolvable: false` (SPEC-PINNED: the subgraph declares
  no reference resolver; the subgraph specification excludes such types from `_Entity`, so the
  fetch document would also violate FS-PLAN-1).
- **FS-KEY-9** (MUST) -- If a requested field's every candidate subgraph is behind
  `resolvable: false` keys and is reachable only through its own subgraph's root fields none of
  which the client requested, planning MUST error (FS-PLAN-6): the configuration is *provably
  non-resolvable* (`FORMAL_SPEC.md` `D10` provable-non-resolvability narrowing). Witness:
  `non-resolvable-interface-object/case-02` (an expected-errors corpus case: the reference gateway
  cannot fetch the field either).
- **FS-KEY-10** (MUST) -- A key whose coordinates no single foreign subgraph supplies (a
  *distributed key*) MUST be satisfiable by gathering each coordinate from a subgraph that resolves
  it, with the gathering fetches ordered before the entity fetch and the representation assembled
  across them; coordinate assignment must be path-coherent (a coordinate nested under a composite
  path is read in that path's subgraph when it resolves it). OURS (`FORMAL_SPEC.md` `D7ppp`,
  `D11.11`); witness: `complex-entity-call/case-01`. No coordinate may be assigned to the target
  subgraph itself (its production would depend on the jump it feeds -- a cycle, FS-PLAN-2).
- **FS-KEY-11** (MUST) -- Entity hops MAY chain across subgraphs (`s1->s2->s3`), including via
  *different* keys per hop, and MUST chain when the only route to a requested field is transitive:
  each hop independently satisfies FS-KEY-1..5, and hop *i*'s representation fields are produced by
  hops < *i*. Witness: `keys-mashup`, `parent-entity-call-complex`.

### Error conditions

`ErrNoValidPlan`-class: FS-KEY-9 (provably non-resolvable); a distributed key whose some coordinate
is resolvable *only* in the target subgraph (FS-KEY-10's cycle exclusion); a requested entity field
whose type has no key at all in any subgraph that could supply the parent instances (no jump
exists -- the honest refusal, not a rescue through unrequested roots, FS-PLAN-5).

### Worked example

Subgraph A: `type Product @key(fields: "id") { id: ID! name: String }`, `Query.topProducts:
[Product]`. Subgraph B: `type Product @key(fields: "id") { id: ID! reviews: [String] }`.
Operation `{ topProducts { name reviews } }`. Obligated shape: fetch-1 -> A
`query { topProducts { name __typename id } }` (key + `__typename` injected, FS-KEY-3 / FS-ENT-6);
fetch-2 -> B, entity fetch `query ($representations: [_Any!]!) { _entities(representations:
$representations) { ... on Product { reviews } } }` with representations
`{ __typename: "Product", id: ... }` (FS-KEY-2), depending on fetch-1 (FS-PLAN-2), attached at
response position `topProducts` (batched over the list, FS-ENT-4). Response shape
`{ topProducts { name reviews } }`.

---

## 3. `@requires` (`FS-REQ`)

`directive @requires(fields: FieldSet!) on FIELD_DEFINITION`

Covers: plain requires, argument-bearing FieldSets, same-coordinate argument conflicts, nested
(chained) requires, distributed requires, circular-looking relays, and genuine cycles.

### Authority classification

- The *ordering* obligation is **SPEC-PINNED** in prose: `@requires` "indicates that the resolver
  for a particular entity field depends on the values of other entity fields that are resolved by
  other subgraphs", and tells "the router that it needs to fetch the values of those externally
  defined fields first, even if the original client query didn't request them" (Apollo Federation
  directives reference). That is the entire pinned content: *fetch the values first*.
- The *transport* -- required values ride in the entity fetch's representation alongside the key
  fields -- is **FOLKLORE**: the subgraph specification defines representations as `__typename` plus
  key fields and never mentions `@requires` data. The extended representation is the reference
  implementation's observable wire contract, reproduced by all known routers, and the only
  transport the `_entities` contract admits. Witness: every `requires-*` corpus suite;
  `FORMAL_SPEC.md` Section 7.2.
- Argument-bearing FieldSets (FS-REQ-4) and same-coordinate conflicts (FS-REQ-5): **OURS** --
  composition accepts arguments in `@requires` FieldSets, but no document states the planning
  semantics, and the reference implementation's published behavior diverges (tracked upstream as
  apollographql/federation#2996-class defects). The semantics here were adjudicated at levels 1-2:
  `DIVERGENCES.md` DV-006/DV-007. Witness: `requires-with-argument`,
  `requires-with-argument-conflict`.
- Nested and distributed requires (FS-REQ-6/7) and the bypass prohibition (FS-REQ-3): **OURS**
  (`FORMAL_SPEC.md` `D7pp`, `D11.10`), grounded in corpus witnesses `requires-requires`,
  `requires-with-argument/case-02-05`, `requires-circular`, `interface-object-with-requires`.

### Composition preconditions

`@requires` appears on a field of an entity type; every FieldSet coordinate exists on the type (or
its nested composites) and is `@external` in the declaring subgraph or resolvable elsewhere;
argument literals in the FieldSet type-check against the target field's arguments.

### Planning obligations

- **FS-REQ-1** (MUST) -- A fetch that resolves field `f` carrying `@requires(R)` in subgraph `s2`
  MUST be preceded by fetches that produce every coordinate of `R` at the same response position,
  and the entity fetch resolving `f` MUST carry the produced values of `R` in its representation
  alongside the FS-KEY-2 key fields.
- **FS-REQ-2** (MUST NOT) -- Required fields fetched only to satisfy `R` MUST NOT appear in the
  response shape (FS-PLAN-3): they are plan-internal inputs.
- **FS-REQ-3** (MUST NOT) -- A plan MUST NOT resolve a `@requires`-bearing field through any fetch
  whose inputs do not include `R`'s values -- including when the parent instances already live in
  `s2` (the local-descent bypass): the correct shape is a relay that exits `s2` (or a sibling
  gathering fetch), gathers `R`, and re-enters `s2` with the gathered representation. Silent
  input-less resolution is wrong data, not a valid plan. OURS (`FORMAL_SPEC.md` `D7pp`); witness:
  `interface-object-with-requires/case-05`, `requires-circular/case-02`.
- **FS-REQ-4** (MUST) -- An argument-bearing coordinate (`@requires(fields: "price(currency:
  \"USD\")")`) MUST be rendered with its literal arguments in the gathering fetch's document, and
  the produced value rides the representation under the coordinate's field name. Witness:
  `requires-with-argument/case-01`; DV-006.
- **FS-REQ-5** (MUST) -- When two fields of one type carry `@requires` FieldSets that bind the same
  coordinate to *different* argument values (`price(currency: "USD")` vs `price(currency: "EUR")`),
  one representation cannot carry both bindings: the plan MUST keep the bindings distinct -- e.g.
  partition the requiring fields across separate entity fetches and alias the conflicting selections
  in the shared gathering document so both coexist validly (GraphQL spec Section 5.3.2 field-merging makes
  the unaliased document invalid, so FS-PLAN-1 already forbids the naive form). Witness:
  `requires-with-argument-conflict`; DV-007.
- **FS-REQ-6** (MUST) -- A required coordinate that is itself `@requires`-dependent (nested
  requires) MUST be planned as a chain: the inner requirement's gathering fetches precede the fetch
  producing the coordinate, which precedes the outer entity fetch. No fixed nesting depth may be
  assumed. Witness: `requires-requires` (chains up to `price -> isExpensive -> isExpensiveCategory`).
- **FS-REQ-7** (MUST) -- A requires FieldSet whose coordinates no single source subgraph resolves
  (distributed requires) MUST be satisfied by gathering each coordinate from a subgraph that
  resolves it, exactly as FS-KEY-10 gathers distributed key coordinates. Witness:
  `requires-with-argument/case-02-05` (`author` requires `comments(limit: 3) { authorId }` where
  `comments` is local and `authorId` foreign).
- **FS-REQ-8** (MUST) -- A relay that re-enters the *same* subgraph (`s2 -> s1 -> s2`: root instances
  in `s2`, requirement gathered in `s1`, requiring field resolved back in `s2` via `_entities`) is
  a valid and, where it is the only input-complete route, obligatory plan shape. Witness:
  `requires-circular/case-01`, `interface-object-with-requires/case-05`.
- **FS-REQ-9** (MAY) -- Fragment-conditioned coordinates in a requires FieldSet
  (`data { foo ... on Bar { bar } }`) are accepted by composition; the gathering document MUST
  render the fragment *where its coordinates are resolvable* -- FS-PLAN-1 dominates the rendering
  obligation, so a conditioned coordinate resolvable NOWHERE renders NOWHERE (the probe shape: the
  jump still fires, the conditioned value is absent, and an unconditional composite left childless
  renders `{ __typename }` so the document stays valid). Whether the conditioned branch is a hard
  input (its absence blocks the jump) or a conditional one is **unverified convention** -- no
  document states it, and observable reference behavior treats it permissively. This document
  takes the completeness-favoring reading: conditioned coordinates gate nothing statically
  (AX-REQ-COND; model form `D7pp(4)`, MG-2 closed). Witness: `requires-with-fragments`,
  `requires-interface/case-03`; probe witness
  `FS-REQ-9/requires-conditional/probe-unresolvable`.

### Error conditions

A genuinely cyclic requirement -- every gathering order for `R` depends on the value it feeds, with
no acyclic relay -- has no valid dependency order (FS-PLAN-2) and MUST be a planning error. Note the
corpus's "circular" witnesses are *relays* (acyclic once unrolled, FS-REQ-8) and MUST plan; the
error fires only for true cycles.

### Worked example

Subgraph A: `Product @key(id) { id dimensions { l w h } }`, root `Query.product`. Subgraph B:
`Product @key(id) { shippingEstimate @requires(fields: "dimensions { l w h }") }`. Operation
`{ product { shippingEstimate } }`. Obligated shape: fetch-1 -> A `query { product { __typename id
dimensions { l w h } } }` (key + requirement injected, FS-KEY-3/FS-REQ-1); fetch-2 -> B, entity
fetch selecting `shippingEstimate` with representations `{ __typename: "Product", id: ...,
dimensions: { l: ..., w: ..., h: ... } }` (FS-REQ-1 transport), after fetch-1. Response shape
`{ product { shippingEstimate } }` -- `dimensions` absent (FS-REQ-2).

---

## 4. `@provides` (`FS-PROV`)

`directive @provides(fields: FieldSet!) on FIELD_DEFINITION`

### Authority classification

- The capability and its *path-scoping* are **SPEC-PINNED** in prose: `@provides` "specifies a set
  of entity fields that a subgraph can resolve, but only at a particular schema path" (Apollo
  Federation directives reference). The scoping clause is the load-bearing pinned content.
- Everything about *when a planner uses* the provided route is **FOLKLORE**/quality: the reference
  prose frames it as an optimization ("reducing the total number of subgraphs that your router
  needs to communicate with"); no document obliges its use.
- Provided selections over abstract-typed fields (FS-PROV-4): **FOLKLORE**, grounded in corpus
  witnesses `provides-on-interface`, `provides-on-union`, `nested-provides`.

### Composition preconditions

`@provides(fields: sigma)` appears on a field whose (possibly list-wrapped) return type hosts `sigma`; each
coordinate of `sigma` is `@external` in the declaring subgraph (it is resolvable there *only* via this
path) and resolvable in some owning subgraph.

### Planning obligations

- **FS-PROV-1** (MAY) -- When the plan reaches instances of `U` *through* the providing field
  (`s`'s `T.f @provides(sigma)`), it MAY resolve any of `sigma` inline in `s`'s same fetch, with no entity
  fetch.
- **FS-PROV-2** (MUST NOT) -- A plan MUST NOT select a provided-only field in `s` for `U` instances
  reached by any *other* path (a different field, a root entry, an entity fetch into `s`): the
  grant is per-path (SPEC-PINNED scoping clause). Checkable: a fetch document selecting `sigma`
  members outside the providing field's sub-selection is a defect even when it happens to validate.
- **FS-PROV-3** (MAY) -- Using the provided route is never obligatory: the plan MAY still take the
  entity-fetch route to `sigma`'s owning subgraph. (Quality: under any cost model that charges fetches,
  the provided route dominates; this document does not mandate cost policy, FS-PLAN-7.)
- **FS-PROV-4** (MUST) -- `@provides` whose FieldSet crosses an abstract-typed field (provides on
  interface/union members, nested provides) grants exactly the coordinates the FieldSet names,
  under the type conditions it names, still path-scoped per FS-PROV-2. Witness:
  `provides-on-interface`, `provides-on-union`, `nested-provides`.

### Error conditions

None specific: `@provides` only *adds* routes (`FORMAL_SPEC.md` `D8` -- monotone extension,
`PROOFS.md` L6). A configuration where a field is resolvable *only* via a provides grant on a path
the operation does not traverse falls back to the owning subgraph's route; if none exists either,
the generic FS-PLAN-6 error applies.

### Worked example

Subgraph A: `Query.feed: [Post]`, `Post @key(id) { id author: User @provides(fields: "name") }`,
`User @key(id) { id name @external }`. Subgraph B: `User @key(id) { id name }`. Operation
`{ feed { author { name } } }`: single fetch to A selecting
`feed { author { name } }` is a correct (and minimal) plan -- `name` rides the provides grant
(FS-PROV-1). Operation `{ user(id: 1) { name } }` against a hypothetical `Query.user` in A MUST NOT
resolve `name` in A (FS-PROV-2): obligated shape is A for `user { __typename id }` then an entity
fetch to B for `name`.

---

## 5. `@external` (`FS-EXT`)

`directive @external on FIELD_DEFINITION | OBJECT`

### Authority classification

- The base rule is **SPEC-PINNED** in prose: `@external` "indicates that this subgraph usually
  can't resolve a particular object field, but it still needs to define that field for other
  purposes" (Apollo Federation directives reference) -- the field is declared for `@key`/
  `@requires`/`@provides` reference, not owned.
- The key-field exception (FS-EXT-2) is **FOLKLORE** with a precise grounding: the reference
  implementation's query-graph construction keeps `@external` *key* fields resolvable in the
  declaring subgraph so that key-based edges out of it can fire (`apollo-federation`
  `build_query_graph.rs`, the key-condition node handling around lines 573-583 and
  `handle_key`'s recursive condition semantics) -- the entity's own resolver returns its key, so the
  key value travels with the instance even though the field is externally owned. Witness:
  `fed1-external-extends`, `fed1-external-extension`, `fed2-external-extends`,
  `fed2-external-extension`, `mysterious-external`, `fed1-external-extends-resolvable`. Model form:
  `FORMAL_SPEC.md` `D5p`.
- Federation-1 `extends` interactions: **FOLKLORE**, same witnesses (the `fed1-*` suites encode the
  legacy `extend type` + `@external` idiom the composed supergraph still admits).

### Composition preconditions

Every `@external` field's type/signature matches the owning subgraph's definition; each `@external`
field is actually referenced by a `@key`, `@requires`, or `@provides` in the declaring subgraph
(composition hygiene -- some composers warn rather than reject; planning must tolerate unreferenced
externals existing).

### Planning obligations

- **FS-EXT-1** (MUST NOT) -- A plan MUST NOT source a field's *response value* from a subgraph where
  it is `@external` and not covered by a `@provides` grant on the traversed path (FS-PROV-1) or the
  key-field carry (FS-EXT-2). The declaring subgraph does not own the field.
- **FS-EXT-2** (MAY) -- An `@external` field that appears (at any nesting depth) in some `@key`
  FieldSet of its type *in the same subgraph* MAY be treated as producible in that subgraph for
  representation-building purposes: when the subgraph supplies the entity instances, the key value
  rides with them, and entity fetches out of that subgraph MAY select it. (FOLKLORE, grounding
  above.) Consequently a subgraph holding only an entity "extension" (keys `@external`, plus its
  own fields) is a valid *source* of entity jumps.
- **FS-EXT-3** (MAY) -- An `@external` field MAY be selected in a fetch document purely as a
  `@requires`/key *input* being gathered from its owning subgraph (FS-REQ-1, FS-KEY-3); the
  gathering fetch targets the owner, not the `@external` declarer.

### Error conditions

None specific: `@external` removes candidate sources rather than adding failure modes. A field
whose *only* declarations anywhere are `@external` (no owner) cannot be planned -- FS-PLAN-6 -- but
composition normally rejects that configuration first.

### Worked example

Subgraph A (owner): `Product @key(id) { id name }`, root `Query.products`. Subgraph B (extension):
`Product @key(id) { id @external reviews: [String] }`. Operation `{ products { reviews } }`.
Obligated shape: fetch-1 -> A `{ products { __typename id } }`; fetch-2 -> B entity fetch for
`reviews` keyed on `id`. B's `id @external` is what makes B *addressable* (its key), and -- reverse
direction -- had the operation started at a B root returning `Product`, FS-EXT-2 lets B's instances
carry `id` so a jump *to A* for `name` can fire without A->B pre-fetches.

---

## 6. `@override` (`FS-OVR`)

`directive @override(from: String!, label: String) on FIELD_DEFINITION` (the `label` argument
exists from Federation 2.7).

### Authority classification

- The base semantics are **SPEC-PINNED**: `@override` "indicates that an object field is now
  resolved by this subgraph instead of another subgraph where it's also defined" (Apollo Federation
  directives reference). "Instead of" is an exclusion, not a preference -- the pinned content is
  strong here, unusually.
- Progressive override: the `label: "percent(x)"` form is **SPEC-PINNED** as to intent -- it
  specifies "the percentage of traffic for the field that's resolved by this subgraph. The
  remaining percentage is resolved by the other subgraph" (ibid.) -- but the *planning realization*
  (two plan variants? a per-request coin flip keyed where? plan-cache semantics?) is pinned
  nowhere; the reference implementation resolves labels at plan time per request. FS-OVR-4 states
  the observable obligation and leaves realization free. Non-`percent` label resolution (custom
  feature-flag coprocessors) is an **unverified convention** -- vendor-specific, not treated here.
- Override interacting with `@requires`/abstract types (FS-OVR-5/6): **FOLKLORE**, grounded in the
  corpus witnesses `override-with-requires`, `override-type-interface`.
- Unavailable override (FS-OVR-3): **FOLKLORE** -- no document states what happens when `from`
  names a subgraph absent from the supergraph; composed behavior (the override is inert and the
  declaring subgraph simply owns the field it declares) is grounded in the corpus witness
  `unavailable-override` and the composed supergraphs it ships.

### Composition preconditions

At most one subgraph overrides a given field; `from` names a subgraph (existing or not); the
overriding subgraph resolves the field. Composition removes the overridden field from the losing
subgraph's *resolvable* capabilities (it may remain as an input, e.g. key/requires source) -- in
supergraph-SDL terms the field's ownership moves. Planning consumes the post-composition
capabilities; hence most `@override` semantics are *precondition*, not plan-time logic.

### Planning obligations

- **FS-OVR-1** (MUST) -- A field `T.f @override(from: "A")` declared in subgraph `B` (no label)
  MUST be fetched from `B` in every plan that resolves it; `A` MUST NOT appear as its source.
  Witness: `simple-override` (both `feed` -- a shared root -- and `createdAt` resolve via `B`).
- **FS-OVR-2** (MAY) -- The overridden field in the losing subgraph MAY still be *used* by the plan
  as a non-output capability where composition retains it (e.g. as a key coordinate the losing
  subgraph still carries) -- override moves response-value ownership, not instance identity.
- **FS-OVR-3** (MUST) -- When `from` names a subgraph that does not exist in the supergraph, the
  override is inert: the declaring subgraph owns the field, and -- the other subgraph's identical
  field declaration being unaffected -- normal `@shareable`/ownership rules decide sources. Witness:
  `unavailable-override`.
- **FS-OVR-4** (MUST) -- Under progressive override (`label: "percent(x)"`), *both* subgraphs remain
  valid sources at the supergraph level, and each individual *request* MUST be planned with exactly
  one of them as the field's source (a single response never mixes the two for one position);
  the traffic split across requests approximates the labelled percentage. A conforming planner
  MUST therefore be able to produce both variants. (Pinned intent; realization free.)
- **FS-OVR-5** (MUST) -- An overriding field that carries `@requires` obeys FS-REQ-1..8 with the
  *overriding* subgraph as the requiring side: the plan gathers the requirement (possibly from the
  overridden subgraph, which still owns those inputs) and resolves the field in the overriding
  subgraph. Witness: `override-with-requires`.
- **FS-OVR-6** (MUST) -- Override of fields on/under abstract types changes the per-position member
  possibility calculus of Section 10 exactly as any ownership move does; members made position-impossible
  by the override render per FS-ABS-6 (dead member). Witness: `override-type-interface` (expected
  response `[{}, {}]` -- the overridden member never occurs at the position).

### Error conditions

None specific at plan time (composition owns the conflict cases). A label whose resolution
infrastructure is unavailable at plan time MUST degrade to a *valid* single-source plan, never to a
mixed or absent field -- this last clause is OURS (derived from FS-PLAN-3/6).

### Worked example

Subgraphs A and B both declare `Post @key(id) { createdAt }`, root `feed: [Post]` shareable in
both; B's `createdAt` carries `@override(from: "a")`. Operation `{ feed { createdAt } }`. Obligated
shape: a single fetch to B `{ feed { createdAt } }` -- B can supply both the root and the overridden
field; a plan fetching `createdAt` from A violates FS-OVR-1 regardless of which subgraph served
`feed`. With `label: "percent(50)"`, half the *requests* plan `createdAt` via B, half via A
(each per-request plan single-source, FS-OVR-4).

---

## 7. `@shareable` (`FS-SHR`)

`directive @shareable repeatable on FIELD_DEFINITION | OBJECT`

### Authority classification

- **SPEC-PINNED** as a *composition* permission: `@shareable` "indicates that an object type's
  field is allowed to be resolved by multiple subgraphs (by default in Federation 2, object fields
  can be resolved by only one subgraph)" (Apollo Federation directives reference). Note what is
  pinned: *allowed to be resolved by multiple subgraphs*. Nothing constrains which one a plan picks.
- Value-identity across subgraphs -- the assumption that every sharing subgraph resolves the field
  to the *same* value -- is the directive's documented intent (Apollo docs warn resolvers must be
  identical) but is unenforceable and unobservable at plan level; plans may rely on it. FOLKLORE.
- Split root re-entry with argument duplication (FS-SHR-3): **OURS** derivation (document validity)
  + **FOLKLORE** grounding (witness `shared-root`).

### Composition preconditions

Every subgraph resolving the field marks it `@shareable` (or `@external`); the field's shape agrees
across declarations. Planning may assume any sharing subgraph is a legitimate source.

### Planning obligations

- **FS-SHR-1** (MAY) -- For each response position of a shareable field, the plan MAY source it from
  any one subgraph that resolves it. The choice is per-position: two positions of the same field
  MAY use different sources.
- **FS-SHR-2** (MUST) -- Each response position MUST have a single effective source shape: if a
  plan lets several fetches write one position (e.g. a shared parent object materialized in two
  subgraph fetches), the selections MUST be shape-identical per GraphQL field-merging (Section 5.3.2), so
  the merged response is well-defined.
- **FS-SHR-3** (MUST) -- When a shareable *root* field's children split across subgraphs, the plan
  MAY re-enter the root field in each subgraph's own root fetch (no entity key is needed at the
  root); every such document MUST repeat the root field's full argument list (FS-ARG-1 -- omitting a
  required argument is invalid, FS-PLAN-1). Witness: `shared-root` (`product` re-entered in
  `name`/`price`/`category` subgraphs, one root fetch each).
- **FS-SHR-4** (MAY) -- A shareable field consumed only as an input (key/requires coordinate) MAY be
  gathered from whichever sharing subgraph the gathering fetch already targets.

### Error conditions

None specific: sharing only widens the candidate set.

### Worked example

Three subgraphs each declare `Query.product: Product @shareable` and their own child of `Product`
(`name`/`price`/`category`). Operation `{ product { name { brand } price { amount } category
{ name } } }`. Obligated shape: three parallel root fetches -- each `{ product { <its child...> } }` --
merged at the shared `product` position (FS-SHR-3, FS-SHR-2). A plan entering `product` in one
subgraph and jumping is impossible here (no `@key` on `Product`); the split-root shape is the only
correct one, which is exactly what makes this suite a discriminating witness.

---

## 8. `@inaccessible` (`FS-INACC`)

`directive @inaccessible on FIELD_DEFINITION | OBJECT | INTERFACE | UNION | ARGUMENT_DEFINITION |
SCALAR | ENUM | ENUM_VALUE | INPUT_OBJECT | INPUT_FIELD_DEFINITION`

### Authority classification

- **SPEC-PINNED**, and unusually completely: an `@inaccessible` element "should be omitted from the
  router's API schema, even if that definition is also present in other subgraphs", while it "is
  not omitted from the supergraph schema, so the router still knows it exists (but clients can't
  include it in operations)" (Apollo Federation directives reference). Both halves matter to
  planning: clients cannot select it; plans still can.

### Composition preconditions

Composition removes inaccessible elements from the client API schema and validates the removal
leaves a coherent API (e.g. no inaccessible type referenced by an accessible field). Operation
validation against the API schema happens before planning -- a client selection of an inaccessible
element never reaches the planner.

### Planning obligations

- **FS-INACC-1** (MUST NOT) -- No inaccessible field/type may appear in the plan's *response shape*
  (clients cannot have selected it; FS-PLAN-3 then forbids inventing it).
- **FS-INACC-2** (MUST) -- Inaccessibility MUST NOT prevent plan-internal use: an inaccessible field
  that is a key coordinate or `@requires` input MUST still be selected in fetch documents and
  representations exactly per FS-KEY-3/FS-REQ-1 (the subgraph schemas retain it; fetch documents
  validate against subgraph schemas, not the API schema). Witness: `simple-inaccessible`.
- **FS-INACC-3** (MUST) -- Abstract-type member calculus (Section 10) runs over the *subgraph/supergraph*
  member sets, but inaccessible members can never be client-selected as type conditions; a position
  whose runtime instance is an inaccessible member renders only the accessible selections
  (typically `__typename` of the nearest accessible supertype is not affected -- the instance's
  concrete `__typename` is what execution reports, and API-schema design must ensure that is
  representable; this edge is composition's problem, not the planner's).

### Error conditions

None at plan time (validation owns client misuse).

### Worked example

Subgraph A: `Product @key(fields: "sku") { sku: String! @inaccessible name: String }`, root
`products`. Subgraph B: `Product @key(fields: "sku") { sku: String! @inaccessible reviews:
[String] }`. Operation `{ products { name reviews } }` (clients cannot select `sku`). Obligated
shape: fetch-1 -> A `{ products { name __typename sku } }` -- `sku` injected though inaccessible
(FS-INACC-2) -- then the entity fetch to B keyed on `sku`. Response shape has no `sku` (FS-INACC-1).

---

## 9. `@interfaceObject` and entity interfaces (`FS-IFO`)

`directive @interfaceObject on OBJECT`

An *entity interface* is an interface carrying `@key` in its defining subgraph; `@interfaceObject`
lets another subgraph model that interface as a plain object type and contribute fields to all its
implementers.

### Authority classification

- The composition semantics are **SPEC-PINNED**: `@interfaceObject` "indicates that an object
  definition serves as an abstraction of another subgraph's entity interface", enabling a subgraph
  "to automatically contribute fields to all entities that implement a particular entity
  interface"; in composition "the fields of every `@interfaceObject` are added both to their
  corresponding `interface` definition and to all entity types that implement that interface"
  (Apollo Federation directives reference).
- Every *wire-level planning* consequence is **FOLKLORE** or **OURS**. In particular: that an
  entity interface is itself an `_entities` target in its defining subgraph (representations typed
  on the interface, FS-IFO-2) is the Federation >=2.3 entity-interface contract as observed in the
  reference implementation -- no subgraph-spec text covers interfaces in `_Entity`. Grounding:
  corpus suites `simple-interface-object`, `interface-object-with-requires`,
  `interface-object-indirect-extension`, `non-resolvable-interface-object`; model form
  `FORMAL_SPEC.md` `D7p`, `D3io`, `D11.9`.
- The `__typename` rewrite at the interface-object boundary (FS-IFO-3) is **FOLKLORE**: the
  interface-object subgraph does not know concrete implementers, so representations sent to it are
  typed with the *interface* name, and the concrete `__typename` visible to clients must come from
  a member-knowing subgraph. Grounded in reference-implementation behavior (its runtime
  representation/typename rewrite for entity interfaces); the *discriminator obligation* FS-IFO-6
  is OURS.

### Composition preconditions

The interface is an entity (`@key` on the interface) in its defining subgraph; the
`@interfaceObject` type carries the same `@key`; the interface's implementer set is known to
composition (the supergraph knows the members; the interface-object subgraph does not).

### Planning obligations

- **FS-IFO-1** (MUST) -- A field contributed by an `@interfaceObject` subgraph is, per composition,
  a field of *every* implementer: a plan resolving it for instances of implementer `C` MUST fetch
  it from the interface-object subgraph via an entity fetch keyed by the interface's key
  (there is no `C` to type against there -- see FS-IFO-2/4).
- **FS-IFO-2** (MUST) -- Entity fetches into an interface-object or entity-interface subgraph MUST
  type their entry fragment and representations on the *interface* name (`... on NodeWithName`,
  `__typename: "NodeWithName"`): the target subgraph's schema has no implementer types, so a
  member-typed document or representation is invalid/unresolvable there.
- **FS-IFO-3** (MUST NOT) -- Fetch documents against an interface-object subgraph MUST NOT contain
  inline fragments on concrete implementers (FS-PLAN-1 forces this -- the types do not exist there);
  a client's member-gated selection (`... on User { username }`) served by that subgraph is
  *flattened* to an interface-level selection in the fetch document, while the response shape keeps
  the member gate (FS-PLAN-3). Witness: `simple-interface-object`.
- **FS-IFO-4** (MUST) -- The reverse hop -- from interface-typed instances supplied by an
  interface-object subgraph to a member-knowing subgraph -- is an entity fetch typed on the
  interface in the *defining* subgraph (whose `_entities` resolves the interface entity and
  returns concretely-typed instances). Witness: `simple-interface-object` (`anotherUsers { age }`
  where `age` lives on the concrete side).
- **FS-IFO-5** (MUST) -- `@requires` on an interface-object field obeys FS-REQ-\*, with the
  representation typed per FS-IFO-2 and the requirement gathered from member-knowing subgraphs
  (relay shapes included). Witness: `interface-object-with-requires`.
- **FS-IFO-6** (MUST) -- A response position whose data is served *only* by an interface-object
  subgraph but whose selections are member-gated (client fragments on implementers, or a client
  `__typename` selection) MUST obtain the concrete `__typename` from a subgraph that knows the
  implementers (the defining subgraph, via the interface-key entity fetch), never report the
  interface name as the instance's `__typename`. OURS (derived from GraphQL Section 4.4: `__typename`
  "returns the name of the object type currently being resolved" -- an interface name is not an
  object type of the instance).
- **FS-IFO-7** (MUST) -- Indirect extension: a subgraph that extends an implementer of an entity
  interface participates in the ordinary Section 2 key routing for that implementer; interface-object
  contribution and concrete extension compose in one plan. Witness:
  `interface-object-indirect-extension`.

### Error conditions

The interface-object variant of FS-KEY-9: an interface-object subgraph whose only key is
`resolvable: false` and whose contributed field is requested for instances originating elsewhere is
provably non-resolvable -- planning MUST error, matching the reference gateway's inability to fetch
it. Witness: `non-resolvable-interface-object/case-02`.

### Worked example

Subgraph A: `interface NodeWithName @key(fields: "id") { id name }`, `User implements NodeWithName
@key(fields: "id") { id name age }`, root `users: [User]`. Subgraph B: `type NodeWithName
@interfaceObject @key(fields: "id") { id username }`. Operation `{ users { username } }`. Obligated
shape: fetch-1 -> A `{ users { __typename id } }`; fetch-2 -> B entity fetch
`... on NodeWithName { username }` with representations `{ __typename: "NodeWithName", id: ... }`
(FS-IFO-2) -- even though the client's position is `[User]`. The response shape places `username`
under `users` items unconditionally (composition added it to `User`).

---

## 10. Abstract types: interfaces, unions, `__typename` (`FS-ABS`)

Cross-subgraph federation makes abstract types the hardest planning territory: each subgraph
declares its *own* member/implementer set for a composed abstract type, and positions typed by an
abstract type may be supplied by several subgraphs with different sets.

### Authority classification

- Fragment/`__typename` *evaluation* semantics are **SPEC-PINNED** (GraphQL Section 6.3.2 CollectFields:
  type conditions filter by the runtime object type; Section 4.4 `__typename`). Everything about *which
  subgraph may claim which member at which position* is not in any document.
- Member narrowing for value-type members (FS-ABS-4) -- the "intersection nulls" semantics -- is
  **FOLKLORE** with cross-vendor corroboration: the reference implementation observably produces
  the null-exclusive-member behavior on the partial-union family, multiple independent
  implementations agree, and the corpus encodes it (`partial-union`, `partial-union-complex`,
  `union-intersection`, `provides-on-union`; adjudication record: `DIVERGENCES.md`
  "Partial-union narrowing -- CLOSED"). The planner-v2 model *derives* it (`FORMAL_SPEC.md` `D6`
  and its Section 7.1 discussion) from a stated standing assumption -- the derivation is OURS; the behavior
  itself is corroborated folklore. Honesty note: an alternative (committing every parent instance
  to one subgraph via its entity key) is *sound* but non-canonical and cost-dominated; a document
  claiming the intersection is the *only* correct behavior would overclaim.
- Position-scoped possibility -- dead members, distributed members, per-position member sets
  (FS-ABS-5..7) -- is **OURS** (`FORMAL_SPEC.md` `D6p`, `D6pp`, `D3pppp`), grounded in corpus witnesses
  (`union-interface-distributed`, `union-intersection`, `abstract-types`, `child-type-mismatch`,
  `interface-refinement`, `circular-reference-interface`). The reference implementation's *type
  explosion* (expanding an interface position into per-member fragments) is FOLKLORE grounding for
  the same class.
- Typename-only selections requiring a fetch (FS-ABS-2): **OURS**, derived from GraphQL Section 4.4 --
  producing `__typename` requires resolving the position. Witness: `typename` suite.

### Composition preconditions

The composed schema fixes each abstract type's total member/implementer set; each subgraph's SDL
fixes its per-subgraph subset; positions (fields returning the abstract type) may have
*per-subgraph output types* (one subgraph declares `Viewer.media: Book`, another
`Viewer.media: Media`) -- composition admits this; planning must consume it per position
(witness: `child-type-mismatch`).

### Planning obligations

- **FS-ABS-1** (MUST) -- Inline fragments in *fetch documents* MUST name only types the target
  subgraph declares, and -- stronger -- a member fragment at a position MUST only be sent to a
  subgraph for which that member is *possible at that position* (it declares the member for the
  position's abstract type, or supplies instances that can be that member). A document that
  validates but names a member the position cannot produce there fetches nothing and masks the
  member's real route. Witness: `union-interface-distributed`.
- **FS-ABS-2** (MUST) -- A selection consisting only of `__typename` (or only of type conditions
  with no resolvable field) still obligates the plan to *fetch the position*: some fetch MUST
  resolve the enclosing field so execution can name the runtime type. Witness: `typename`.
- **FS-ABS-3** (MUST) -- After any subgraph boundary at or under an abstract position, the parent
  fetch MUST select `__typename` for the instances (representations need it per FS-KEY-2, and
  member-gated response entries need it for CollectFields evaluation).
- **FS-ABS-4** (MUST) -- *Value-type member narrowing.* When an abstract position's members are
  value types (no `@key` anywhere -- no identity to reconcile instances across subgraphs) and the
  position's parent field is resolvable by several subgraphs, a member not declared by *every*
  parent-capable subgraph (the intersection) MUST NOT be fetched: its selections stay in the
  response shape and render as type-condition non-matches (nulls/absence). Only subgraphs that can
  actually supply parent instances on a route *to that position* count toward the intersection
  (route-scoping is judged **per position**, not schema-wide: a subgraph that merely declares the
  field but has no producing path does not narrow -- witness `partial-union/case-02` -- and neither
  does one whose only route to the parent type enters through an unrequested root field at a
  *different* position -- witness the conformance case
  `FS-ABS-4/abstract-narrowing/route-scoped-ghost`; model form `D6ppp`). Entity-key TRANSPORT into
  a subgraph counts toward position-capability only when the key is *obtainable at that position*:
  a subgraph enterable solely by a key (even `resolvable: true`) whose fields no position-capable
  subgraph can produce there does NOT narrow -- neither directly, nor through a chain of such
  jumps, nor via a composite key only PARTIALLY obtainable at the position (witnesses: the
  conformance cases `FS-ABS-4/abstract-narrowing/unobtainable-key-ghost`, `double-ghost`,
  `partial-composite-key-ghost`; a subgraph genuinely enterable through a multi-hop key relay from
  the position's real origins DOES count and narrows -- witness `two-hop-narrower`; model form
  `D6pppp`). *Boundary with FS-ABS-7
  (adjudicated):* this proposition's premise is that **no member of the abstract type at the
  position is an entity** -- with no identity anywhere in the member population, one static member
  set must hold for every parent origin, and the intersection is that set. The moment *any*
  position-possible member carries a `@key`, instances at the position carry reconcilable
  identity, cross-subgraph re-entry at the position is meaningful, and a value member possible via
  some but not all parent-capable subgraphs is governed by FS-ABS-7 (fetched, through a subgraph
  where it is possible) rather than narrowed. The two propositions partition on the
  entity-presence test; neither reading is applied incidentally.
- **FS-ABS-5** (MUST) -- *Entity members.* A member with its own `@key` is individually routable:
  no intersection narrowing applies to it; its fields follow Section 2 entity routing from whichever
  subgraph supplied the instance.
- **FS-ABS-6** (MUST NOT) -- *Dead members.* A member that no position-capable subgraph can produce
  at that position (regardless of entity-ness -- an entity fetch transports existing instances, it
  cannot manufacture one of a type the position never yields) MUST NOT be fetched at that position;
  its fragment appears in no fetch document, and the response renders it per FS-PLAN-3. Witness:
  `union-interface-distributed/case-02/08`, `union-intersection/case-04`,
  `override-type-interface/case-02` (expected `[{}, {}]`).
- **FS-ABS-7** (MUST) -- *Distributed members.* A member possible via some but not all
  position-capable subgraphs MUST be fetched, and MUST be fetched through a subgraph where it is
  possible at that position -- re-entering a shared root there, or riding an entity fetch out of
  it; its `... on Member` fragment appears only in such fetches' documents. Witness:
  `union-intersection/case-08/11/12`, `union-interface-distributed/case-05`.
- **FS-ABS-8** (MUST) -- *Member expansion (type explosion).* When a field selected on an interface
  is not resolvable *on the interface* by any subgraph supplying the position, but is resolvable on
  the position's concrete members (each an entity), the plan MUST fan out per possible member:
  per-member entity fetches (or per-member fragments in one fetch) that together cover every
  possible runtime type, producing the same response keys under member gates. Witness:
  `abstract-types` (`products { reviews }` with `reviews` on `Book`/`Magazine` only).
- **FS-ABS-9** (MUST) -- Fragments whose type condition is itself abstract (`... on Store` under a
  union whose members implement `Store`) MUST be honored whenever the position can produce *any*
  implementer of the condition: narrowing may not treat an abstract condition name as an unknown
  member. Witness: `union-interface-distributed/case-05`.
- **FS-ABS-10** (MAY) -- *Response-equivalent expression freedom.* A plan MAY express one client
  selection either ungated at the abstract level or as per-member gated copies, provided the union
  of variants produces the client tree for every possible runtime type (GraphQL CollectFields
  equivalence). Neither expression is more correct; conformance oracles must compare
  presence-unions, not surface syntax. (`DIVERGENCES.md` DV-008.)
- **FS-ABS-11** (MUST) -- Same-response-key member variants (a key selected both ungated and under
  member gates, with different subtrees) MUST merge per GraphQL field-merging at the *client* level
  (Section 5.3.2 guarantees they are mergeable), and the plan's response shape must reproduce the merged
  tree with each variant's gate evaluated against the *discriminating ancestor's* runtime type --
  not the child object's own type. Witness: the `sibling-conflation` and
  `union-interface-distributed` families; model form `D11.8`.
- **FS-ABS-12** (MUST) -- Per-subgraph output-type divergence at a position (one subgraph's field
  returns `Book`, another's returns the union) MUST be planned per the *supplying* subgraph's
  declared output type: possibility judgments (FS-ABS-4..7) use each subgraph's own output type,
  not the composed one. Witness: `child-type-mismatch`.

### Error conditions

A member-gated selection whose member is possible at the position but reachable in *no* subgraph
(the member's fields resolve nowhere reachable) is unplannable -- FS-PLAN-6 -- distinct from the
dead-member case (FS-ABS-6: never occurs, render empty) and the narrowed case (FS-ABS-4: occurs
but must not be fetched). Conflating these three produces either wrong errors or silent drops; the
trichotomy is the load-bearing content of this section.

### Worked example

Subgraphs A and B both declare entity `Wrapper @key(id)` with shareable `action: Action`;
`union Action = Common | OnlyA` in A, `= Common | OnlyB` in B; all three members value types.
Operation `{ wrapper { action { __typename ... on Common { c } ... on OnlyA { a } ... on OnlyB
{ b } } } }`. Parent-capable set = {A, B}; intersection = {Common}. Obligated shape: one fetch (to
either subgraph, say A) `{ wrapper { action { __typename ... on Common { c } } } }`; `OnlyA`/`OnlyB`
fragments are fetched nowhere (FS-ABS-4) yet remain in the response shape (FS-PLAN-3) -- for any
returned instance the runtime type is `Common`, the other gates never match, and `a`/`b` are
correctly absent. Had the members been entities, each would be individually routable instead
(FS-ABS-5).

---

## 11. Entity resolution: `_entities`, representations, batching (`FS-ENT`)

### Authority classification

- This is the *most* spec-pinned territory in federation planning: the subgraph specification
  defines `_entities(representations: [_Any!]!): [_Entity]!`, mandates that results "correspond to
  the provided representations, in the exact same order", permits null entries ("entries in the
  list can be null if no entity exists for a provided representation"), and fixes the
  representation format (FS-KEY-2's quotes). The `_Entity` union "must include every entity type
  that the subgraph defines" except `resolvable: false` ones.
- Batching *strategy* is **FOLKLORE**: the subgraph specification "does not discuss batching
  strategies" -- that routers coalesce all representations at one response position (e.g. all items
  of a list) into a single `_entities` call is universal observed behavior, and the obligation
  stated here (FS-ENT-4) is the correctness core (every item resolved), with coalescing as MAY.
- Entity fetches being `query` operations even under mutations (FS-ENT-5) is **SPEC-PINNED** by
  construction: `_entities` is defined on `Query` only.

### Composition preconditions

Each subgraph defining resolvable entities exposes `_entities`/`_Entity`/`_Any` per the subgraph
specification (subgraph libraries generate these; the router may assume them).

### Planning obligations

- **FS-ENT-1** (MUST) -- An entity fetch's document selects entity fields via inline fragments on
  the target type(s) under `_entities` (GraphQL requires typed fragments to select on a union), and
  passes one representation per instance to resolve.
- **FS-ENT-2** (MUST) -- The plan MUST consume `_entities` results *by index*: the i-th result is
  the i-th representation's entity (SPEC-PINNED ordering quote above). Result-matching by key value
  is non-conforming (the spec permits null entries and duplicate keys).
- **FS-ENT-3** (MUST) -- A null entry in the `_entities` result is a *permitted* outcome
  (SPEC-PINNED); the plan's response shape must tolerate it per GraphQL null-propagation (Section 6.4.4 --
  a null entity nulls the entity's fields, propagating per nullability). Plans MUST NOT treat a
  null entry as a transport error.
- **FS-ENT-4** (MUST / MAY) -- When the instances to resolve sit at list positions (the entity
  position is or is nested under a list), the plan MUST resolve *every* item -- one representation
  per item -- and MAY coalesce them into a single `_entities` call per (subgraph, position,
  representation type-set). A plan that sends only the first item's representation under a list is
  defective. Null or non-matching items MAY be skipped (their representation omitted) -- they
  produce no entity, per FS-ENT-3.
- **FS-ENT-5** (MUST) -- Entity fetches are `query` operations regardless of the client operation's
  type: `_entities` exists only on `Query`. Only the root fetch of a mutation is a `mutation`
  document (Section 12).
- **FS-ENT-6** (MUST) -- The parent fetch producing representation inputs MUST select `__typename`
  at the entity position (FS-KEY-2 needs its value; for abstract positions FS-ABS-3 already
  requires it; for concrete positions it must still be selected -- the representation is typed by
  the *runtime* value, not assumed).
- **FS-ENT-7** (MAY) -- Representations MAY carry additional data fields beyond the key (FS-REQ-1's
  requires transport is exactly this); targets must ignore fields they don't need. (The subgraph
  spec pins the minimum, not a maximum -- the permissiveness itself is FOLKLORE.)
- **FS-ENT-8** (MUST) -- Two same-position entity fetches whose representations bind the same
  coordinate to *conflicting* values (FS-REQ-5) MUST remain separate calls; otherwise same-target
  same-position entity fetches MAY merge into one call (FS-PLAN-7 quality freedom).

### Error conditions

None specific: `_entities` failures are runtime errors, not plan defects. (A plan *causing* them
deterministically -- untyped representation, missing key field -- violates FS-KEY-2/FS-ENT-6 and is
caught by those propositions.)

### Worked example

See Section 2's worked example -- its fetch-2 embodies FS-ENT-1/2/4/5/6: a single batched `query` entity
fetch at `topProducts`, one representation per item, consumed by index.

---

## 12. Root operation types, including renamed roots (`FS-ROOT`)

### Authority classification

- Serial top-level mutation execution is **SPEC-PINNED** (GraphQL Section 6.2.2: top-level mutation
  fields execute serially). Its planning consequence (FS-ROOT-3) is a direct derivation.
- Single execution of each top-level mutation field is likewise **SPEC-PINNED** (GraphQL
  Section 6.2.2 executes each top-level mutation field exactly once); its federation planning
  consequence (FS-ROOT-6) is **OURS**, a direct derivation: a root fetch selecting a mutation root
  field EXECUTES it, so the number of root fetches selecting the field is the number of times its
  side effect applies.
- Root-type renaming (`schema { query: MyQuery }`) is plain GraphQL (Section 3.3 root operation types may
  be any object type); that a *subgraph* may rename while the composed supergraph uses canonical
  names is **FOLKLORE** (composition accepts it; no document states the planning consequence);
  FS-ROOT-4's obligation is **OURS**, derived from FS-PLAN-1.
- Everything else here is a corollary of Section 1.

### Composition preconditions

The supergraph has composed root operation types; each subgraph exposes its root fields under its
own (possibly renamed) root types.

### Planning obligations

- **FS-ROOT-1** (MUST) -- Each requested root field MUST be resolved by a root fetch to a subgraph
  that declares that root field (FS-PLAN-1/5). Distinct root fields MAY resolve in distinct
  subgraphs (one root fetch each).
- **FS-ROOT-2** (MAY) -- Top-level *query* fields have no mandated order: their root fetches MAY run
  concurrently (GraphQL normal execution order is unconstrained for queries; data dependencies,
  FS-PLAN-2, are the only ordering source).
- **FS-ROOT-3** (MUST) -- Top-level *mutation* fields MUST be planned so their side effects occur
  serially in selection order (GraphQL Section 6.2.2): the root mutation fetches (and any fetch writing an
  earlier top-level field's subtree) are ordered before the next top-level mutation field's root
  fetch. The root fetch of a mutation uses the `mutation` keyword; all dependent entity fetches
  remain queries (FS-ENT-5). Witness: `mutations`.
- **FS-ROOT-4** (MUST) -- A subgraph that renames its root operation types MUST be planned against
  its *own* names' fields: renaming is invisible on the wire (root fields are selected the same
  way), but the planner MUST NOT fail or mis-derive field output types because the subgraph's SDL
  binds root fields under a non-canonical type name. OURS; model form `FORMAL_SPEC.md` `D5pp`.
  (This is a real-world dominant failure class -- subgraphs with `schema { query: AcmeQuery }` --
  invisible to corpora whose fixtures all use standard names.)
- **FS-ROOT-5** (MUST NOT) -- No plan may resolve a root field through an entity fetch: root fields
  have no representations. (`_entities` is itself a root field of the *target's* `Query` type and
  is the sole exception to FS-PLAN-5's selection rule, not to this one.)
- **FS-ROOT-6** (MUST) -- *Single execution of a shared mutation root field.* A top-level
  *mutation* field's ENTIRE selection MUST be resolved through exactly ONE root fetch to ONE
  subgraph. When the field is declared by several subgraphs (a `@shareable` mutation root field),
  the planner MUST choose one declaring subgraph and MUST NOT split the field's selection across
  root fetches to different subgraphs: every root fetch selecting the field executes its resolver,
  so a split applies the side effect once per participating subgraph while the merged data can
  still come out byte-correct -- the silent-wrong-EFFECT class (GraphQL Section 6.2.2 executes each
  top-level mutation field exactly once). Selections the chosen subgraph cannot resolve locally
  ride dependent entity fetches off the mutation's payload (which are `query` operations, FS-ENT-5
  -- re-entry is a read, not a re-execution). Queries are exempt: FS-ROOT-1 deliberately permits a
  shareable QUERY root field's selection to split (reads are idempotent). Witness: the real-audit
  `mutations_3` case (a `@shareable` `addCategory` split across two subgraphs double-applied) and
  the conformance `mutation/shareable-root-pin` case. Model form: `FORMAL_SPEC.md` `D10` amendment
  *mutation-root subgraph pin*.

### Error conditions

A requested root field no subgraph declares cannot be planned (FS-PLAN-6) -- normally impossible
post-composition. A *mutation* root field whose whole selection NO single declaring subgraph can
anchor (FS-ROOT-6's premise unsatisfiable -- e.g. exclusive payload fields split across declaring
subgraphs with no key to relay them) MUST be a planning error (FS-PLAN-6): every executable plan
would re-execute the side effect, and single execution dominates completeness for side effects.

### Worked example

Subgraph A's SDL: `schema { query: AQuery } type AQuery { product: Product }`. The composed
supergraph exposes `Query.product`. Operation `{ product { name } }`. Obligated shape: root fetch
to A `query { product { name } }` -- identical wire form to the unrenamed case (FS-ROOT-4): the
rename must change nothing observable. A planner that errors (or drops `product`'s subtree) on the
rename is non-conforming.

---

## 13. Field arguments and variables (`FS-ARG`)

### Authority classification

- Document-level rules are **SPEC-PINNED** by the GraphQL spec: required arguments must be present
  (Section 5.4.2.1), all defined variables must be used (Section 5.8.4) and all used variables defined (Section 5.8.3),
  same-response-key selections must be mergeable (Section 5.3.2 -- same field and identical arguments), and
  `@skip`/`@include` control inclusion (Section 3.13).
- How a *router* splits/forwards variables per fetch is **FOLKLORE** (every implementation forwards
  client variable values verbatim under the same names); the exactness obligation FS-ARG-2 is a
  derivation from Section 5.8.3/5.8.4.
- Plan-time vs execution-time handling of variable-conditioned `@skip`/`@include` is **FOLKLORE**
  and implementation-divergent (plan-per-variable-values vs conditional plan nodes); FS-ARG-5 pins
  only the observable end state.

### Composition preconditions

Argument definitions agree across subgraphs sharing a field; variables are a client-operation
concern, invisible to composition.

### Planning obligations

- **FS-ARG-1** (MUST) -- Every fetch document selection of a client-requested field MUST carry that
  field's client arguments verbatim (modulo variable renaming, which nothing requires): omitting a
  required argument is invalid (FS-PLAN-1); omitting an optional one silently changes semantics.
  This applies *per document*: a field re-entered in several fetches (FS-SHR-3) repeats its
  arguments in each.
- **FS-ARG-2** (MUST) -- Each fetch document MUST define exactly the variables its selections use --
  no fewer (Section 5.8.3) and no more (Section 5.8.4: unused definitions are invalid) -- with types matching the
  client operation's definitions, and the fetch MUST forward the client's values for them
  unchanged.
- **FS-ARG-3** (MUST) -- Injected selections (keys FS-KEY-3, requires FS-REQ-1/4) render their
  FieldSet-specified literal arguments; client arguments never leak into injected selections and
  vice versa.
- **FS-ARG-4** (MUST) -- Two same-key selections with different arguments at *different* response
  positions are independent (each fetch renders its own position's arguments). At the *same*
  position they are invalid client GraphQL (Section 5.3.2) -- except via aliases, which the plan MUST
  preserve as distinct response keys (FS-PLAN-3), and which fetch documents MUST reproduce (or
  re-alias with a recorded mapping) so both argument bindings coexist validly (cf. FS-REQ-5's
  gathering-side aliasing).
- **FS-ARG-5** (MUST) -- A selection excluded by `@skip`/`@include` (literal `if`, or the
  operation's variable values when known at plan time) MUST NOT be fetched, and an included one
  MUST be -- matching GraphQL Section 3.13 semantics exactly. Whether exclusion happens by planning
  per-variables or by conditional plan structure is free (FOLKLORE note above). Witness:
  `include-skip`.
- **FS-ARG-6** (MUST) -- Routing MUST be argument-independent: which subgraph can resolve `T.f` does
  not depend on `f`'s argument values, and a plan MUST NOT choose different subgraphs for the same
  coordinate based on argument values. (OURS -- `FORMAL_SPEC.md` `D5` A-3 argument-blind edge
  identity; no known schema mechanism makes resolvability argument-dependent.)

### Error conditions

None specific: argument defects surface as FS-PLAN-1 validation failures.

### Worked example

Operation `query ($c: Currency!, $inc: Boolean!) { product { price(currency: $c) name @include(if:
$inc) } }` with `price` in A and `name` in B (entity `Product @key(id)`), `$inc = false` known at
plan time. Obligated shape: fetch-1 -> A `query ($c: Currency!) { product { __typename id
price(currency: $c) } }` -- declares exactly `$c` (FS-ARG-2), forwards its value; no fetch selects
`name` (FS-ARG-5), and no fetch-2 to B exists at all -- the entity fetch's only consumer was
excluded. Response shape omits `name`'s value per `@include` semantics while `price` renders
normally.

---

## 14. Subscriptions (`FS-SUB`)

Federation-level subscription semantics are pinned by *no* federation document: the Federation
directive reference and subgraph specification do not mention subscriptions. What is pinned lives
one level up, in the GraphQL specification's subscription model; the trigger/response split below
is how every known federation router realizes it. Propositions here are implementation-status-
neutral: they state what a correct federated subscription *plan* is, whoever builds it.

### Authority classification

- Single root field (FS-SUB-1): **SPEC-PINNED** -- GraphQL spec Section 5.2.3.1 (*Single Root Field*):
  a subscription operation must have exactly one root field. This is a validation rule, so the
  planner may assume it; it is restated because the whole trigger/response split leans on it.
- Per-event execution-as-query (FS-SUB-6): **SPEC-PINNED** -- GraphQL spec Section 6.2.3.2
  (*ExecuteSubscriptionEvent*): each source-stream event is executed essentially as a query
  against the event's root value. The planning consequence: everything below the subscription root
  field is ordinary query-planning territory, per event.
- The trigger/response split itself -- the subscription root field is resolved by exactly one
  subgraph, which owns the source stream; all other subgraphs contribute per-event via ordinary
  query-shaped fetches (FS-SUB-2/3): **FOLKLORE** -- no document mandates it, and it is the
  architecture of every known implementation (a source stream cannot be joined across subgraphs;
  only one subgraph can own the event source). The impossibility argument is genuine but informal;
  this document records the split as corroborated convention, not as pinned.
- Downstream fetch shape (FS-SUB-4/5): **OURS**, derived from FS-ENT-5 and FS-PLAN-2.

### Composition preconditions

The composed schema has a `Subscription` root type; the subscription root field is declared by at
least one subgraph; that subgraph supports a subscription transport (a deployment concern outside
this document's scope -- the *plan* names the subgraph; transport binding is configuration).

### Planning obligations

- **FS-SUB-1** (MUST) -- A subscription plan has exactly one trigger: the operation's single root
  field (GraphQL Section 5.2.3.1) is resolved by exactly one *trigger fetch*, whose document is a
  `subscription` operation selecting that root field with its client arguments (FS-ARG-1/2 apply).
- **FS-SUB-2** (MUST) -- The trigger fetch targets one subgraph that declares the subscription root
  field. When several subgraphs declare it (a shareable subscription root), the planner MAY choose
  any one, but one request's plan has exactly one trigger subgraph -- a plan MUST NOT fan a single
  client subscription out to multiple source streams.
- **FS-SUB-3** (MUST) -- All selections below the trigger field are planned per Sections 1-13 exactly as if
  the trigger field's payload were a query-supplied position: entity fetches, requires gathering,
  abstract-member routing all apply unchanged, rooted at the event payload.
- **FS-SUB-4** (MUST) -- Every non-trigger fetch in a subscription plan is a `query` operation
  (`_entities` lives on `Query`, FS-ENT-5; and re-subscribing per event would create new streams,
  not resolve the current event).
- **FS-SUB-5** (MUST) -- Every non-trigger fetch depends (directly or transitively) on the trigger
  fetch: nothing can run before an event exists. The dependency order within the non-trigger
  fetches follows FS-PLAN-2 per event.
- **FS-SUB-6** (MUST) -- The response shape obligations (FS-PLAN-3/4) apply *per event*: each event
  produces one response whose shape is the client selection tree (GraphQL Section 6.2.3.2).

### Error conditions

A subscription root field declared by no subgraph is unplannable (FS-PLAN-6). A requested selection
below the trigger that is unplannable under Sections 1-13 fails at *plan* time -- before any stream is
opened -- with the same error the query form would produce.

### Worked example

Subgraph A: `type Subscription { reviewAdded: Review }`, `Review { body product: Product }`,
`Product @key(id) { id }`. Subgraph B: `Product @key(id) { id name }`. Operation
`subscription { reviewAdded { body product { name } } }`. Obligated shape: trigger fetch -> A,
document `subscription { reviewAdded { body product { __typename id } } }` (key injected,
FS-KEY-3); one dependent entity fetch -> B (a `query`, FS-SUB-4) resolving `name` per event,
attached at `reviewAdded.product`. Per event, the response is `{ reviewAdded { body product
{ name } } }` (FS-SUB-6).

---

## 15. `@defer` (`FS-DEF`)

`@defer` is **not part of the ratified GraphQL specification** (October 2021 edition) nor of any
Federation specification text. Its semantics come from the GraphQL incremental-delivery RFC
(graphql/graphql-spec -- the `@defer`/`@stream` proposal), a draft; router support is
implementation-defined. Per Section 0.2, even quoted RFC text carries FOLKLORE authority here. The
propositions state the *planning* obligations a defer-supporting router takes on; a router that
does not support `@defer` and ignores it wholesale is conforming by FS-DEF-1. All propositions are
implementation-status-neutral.

### Authority classification

- Advisory nature (FS-DEF-1): **FOLKLORE** (RFC draft) -- the RFC defines `@defer` as a hint:
  a service "may" defer and is permitted to deliver deferred data in the initial response (e.g.
  when deferral would be more expensive than inlining). This is the single most load-bearing
  clause: it makes every other obligation conditional on the plan *choosing* to defer a fragment.
- Payload bookkeeping (FS-DEF-3): **FOLKLORE** (RFC draft) -- incremental payloads identify their
  insertion `path` and optional `label`.
- The partitioning obligation (FS-DEF-2), completeness of the union (FS-DEF-4), and
  routing-invariance (FS-DEF-6): **OURS** -- derived from the RFC's delivery model plus FS-PLAN-2/3:
  deferral is a *response partitioning*, so the fetch graph must be partitionable such that the
  initial payload is complete and non-blocking, and the deferred parts arrive correct. No document
  states these over *plans*; they are what "a correct plan under @defer" has to mean if the RFC's
  client-visible contract is to hold.
- Defer-at-fetch-boundaries (FS-DEF-5): **FOLKLORE** -- observed router behavior (deferral is
  honored where it aligns with a subgraph fetch boundary, ignored where the data was already
  fetched); consistent with FS-DEF-1's advisory license.
- Subscriptions exclusion (FS-DEF-7): **FOLKLORE** (RFC draft validation) -- the RFC's validation
  forbids `@defer`/`@stream` in subscription operations; recorded here as the draft's position.

### Composition preconditions

None federation-specific: `@defer` is a client-operation executable directive on fragments. Whether
the gateway advertises defer support to clients is a protocol capability, not a schema property.

### Planning obligations

- **FS-DEF-1** (MAY) -- A planner MAY ignore any `@defer` (including `if: true` ones): delivering
  everything in the initial response is always conforming. Consequently no proposition below is
  violated by *not* deferring; they bind only the fragments the plan *does* defer.
- **FS-DEF-2** (MUST) -- For every fragment the plan defers, the initial response MUST be producible
  without executing any fetch whose *only* consumers are deferred selections: the plan partitions
  its fetches into an initial set and per-deferred-fragment sets such that the initial set alone
  covers every non-deferred selection. A plan whose initial payload blocks on a deferred-only fetch
  has deferred nothing (and violated the RFC's latency contract observably).
- **FS-DEF-3** (MUST) -- Each deferred fragment the plan honors is delivered as one or more
  incremental payloads identified by the fragment's response path (and `label` when the client gave
  one), inserting exactly the fragment's selections at that path.
- **FS-DEF-4** (MUST) -- The union of the initial response and all incremental payloads MUST equal
  the undeferred response: same response keys, same values-shape, nothing dropped, nothing
  duplicated into conflict (FS-PLAN-3 applied to the union). Deferral partitions delivery; it never
  changes what is delivered.
- **FS-DEF-5** (MAY) -- A planner MAY round deferral to fetch boundaries: selections under `@defer`
  that the initial fetch set already produces MAY be delivered initially (per FS-DEF-1's advisory
  license), and a deferred fragment whose data spans several fetches MAY be split into several
  incremental payloads (FS-DEF-3 permits one *or more*).
- **FS-DEF-6** (MUST) -- Deferral MUST NOT change routing obligations: every fetch in the deferred
  partition obeys Sections 1-13 exactly as its undeferred counterpart would (same subgraph sources, same
  representations, same dependency constraints). In particular a deferred fragment's entity fetch
  still consumes representations produced by initial-set fetches -- deferral never re-plans *where*
  data comes from, only *when* fetches run.
- **FS-DEF-7** (MUST NOT) -- Per the RFC draft's validation, `@defer` MUST NOT be honored inside
  subscription operations (each event's response is a single payload; see Section 14). A planner receiving
  one (from a client whose validation admitted it) treats it per FS-DEF-1: ignore it.

### Error conditions

None specific: an unplannable deferred selection is unplannable undeferred too (FS-DEF-6 -- same
routing), and fails with the same FS-PLAN-6 error at plan time, not at first-event/flush time.

### Worked example

Schema as Section 2's worked example. Operation
`{ topProducts { name ... @defer(label: "r") { reviews } } }`. A defer-honoring plan: initial set =
fetch-1 -> A `{ topProducts { name __typename id } }` (note: the key fields are in the initial set --
they feed the deferred fetch, FS-DEF-6); deferred set for `"r"` = fetch-2 -> B, the Section 2 entity fetch
for `reviews`. Initial payload: `{ topProducts: [{ name: ... }, ...] }` -- producible from fetch-1 alone
(FS-DEF-2); incremental payload(s): `path: ["topProducts", i]`, `label: "r"`, data
`{ reviews: ... }` (FS-DEF-3); union equals the undeferred response (FS-DEF-4). Equally conforming:
ignore the defer and ship Section 2's plan unchanged (FS-DEF-1).

---

## 16. EDFS event-driven subscription/mutation roots (`FS-EDFS`)

WunderGraph's Event-Driven Federated Subscriptions (EDFS) let a root field be backed by a
message-broker event stream -- Kafka or NATS -- instead of an HTTP subgraph. The composition
directives are `@edfs__natsSubscribe` and `@edfs__kafkaSubscribe` on `Subscription` root fields, and
`@edfs__natsPublish`, `@edfs__kafkaPublish`, `@edfs__natsRequest` on `Mutation` (or `Query`, for
request) root fields. The datasource behind such a field is a **pub/sub trigger** on an **event
source**: it owns no HTTP endpoint and, in the production config, contributes no upstream SDL. This
family states what a correct plan for an EDFS root is. It is the datasource-kind analogue of
`FS-SUB`: everything below an EDFS subscription trigger is ordinary subscription planning
(`FS-SUB-3`), and the EDFS-specific content is the *entrance* (a root field with no HTTP upstream)
and the *transport* (a broker binding, not WebSocket/SSE).

### Authority classification

The EDFS directives and their event source / pub/sub transport are a WunderGraph/Cosmo vendor
mechanism pinned by no GraphQL or Federation specification text -- a FOLKLORE-authority environment
fact (Section 0.1) at the router-subgraph interface. The *planning propositions* below, by contrast,
are all **OURS**: they state what a correct plan for such a root is, derived from the model exactly
as `FS-SUB-4`/`FS-SUB-5` are OURS while the subscription mechanism itself is spec/folklore. All are
implementation-status-neutral.

- FS-EDFS-1/3/4 derive from `FS-SUB` plus the model's transport lowering: an EDFS root is a root
  entrance (`FORMAL_SPEC.md` `D5-EDFS`) and its trigger/publish transport is a broker binding
  (`D11.12-EDFS`); the trigger/response split is `FS-SUB`'s, unchanged.
- FS-EDFS-2/5 derive from `FS-PLAN-1`/`FS-PLAN-6`: an event stream carries no SDL, so the composed
  schema is the only authority for the payload type (FS-EDFS-2); and a metadata-only root field with
  no positive EDFS evidence is ordinary schema drift, which must fail loud rather than be silently
  rescued (FS-EDFS-5).

### Composition preconditions

The composed schema declares the EDFS root field and its payload type (the authority for both). The
owning datasource is an event source: it carries an event source configuration (provider id, event
type, Kafka topics or NATS subjects) naming the root field, and -- in the production router config --
no per-subgraph upstream SDL. A payload that is an entity is declared with a `@key` so it resolves
per event via entity jumps (`FS-KEY`, `FS-ENT`).

### Planning obligations

- **FS-EDFS-1** (MUST) -- *Event-source metadata acceptance.* A supergraph configuration carrying
  EDFS event source datasources MUST be accepted by the planner (not refused): an EDFS root field is
  a first-class root entrance whose datasource resolves events from a broker rather than HTTP. This
  is the (a)-class metadata contract shared with any SDL-less datasource kind.
- **FS-EDFS-2** (MUST) -- *Composed-schema payload typing.* The output type of an EDFS root field --
  and therefore its payload sub-selection -- is resolved from the composed supergraph schema, since
  the event stream owns no SDL. Below the root field the plan is built exactly as `FS-SUB-3`
  mandates (per-event query planning: entity fetches, requires gathering, abstract-member routing),
  rooted at the event payload. Model form: `FORMAL_SPEC.md` `D5-EDFS`.
- **FS-EDFS-3** (MUST) -- *Pub/sub subscription trigger transport.* A `@edfs__*Subscribe` root
  lowers to a subscription trigger (the `FS-SUB-1` trigger/response split, unchanged) whose transport
  is the event source binding -- the provider, the `SUBSCRIBE` event type, and the templated
  Kafka topics or NATS subjects -- not a WebSocket/SSE GraphQL subscription. Model form:
  `FORMAL_SPEC.md` `D11.12-EDFS`.
- **FS-EDFS-4** (MUST) -- *Pub/sub publish/request roots.* A `@edfs__*Publish` root on `Mutation`
  (or `@edfs__natsRequest` on `Mutation`/`Query`) is a root entrance whose root fetch publishes or
  requests over the event source binding instead of an HTTP request. For mutation roots the
  single-execution obligation (`FS-ROOT-6`) is preserved: exactly one root fetch, one publish.
- **FS-EDFS-5** (MUST) -- *No silent drift.* The composed-schema payload-typing fallback of
  FS-EDFS-2 fires ONLY under positive EDFS evidence (an event source configuration naming the root
  field). A metadata-declared root field with no EDFS evidence and no SDL-resolvable output type is
  unplannable and MUST fail loud (`FS-PLAN-6`), never be silently rescued by composed-schema typing --
  ordinary schema drift is a composition bug, not an event source.

### Error conditions

An EDFS root field whose payload type the composed schema does not declare is unplannable
(`FS-PLAN-6`) -- normally impossible post-composition. A selection below the trigger that is
unplannable under Sections 1-13 fails at *plan* time, before any stream is opened, exactly as the
query form would (`FS-SUB` error conditions).

### Worked example

Subgraph P (EDFS NATS event source, no HTTP upstream): composed schema declares
`type Subscription { productUpdated(id: ID!): Product }` with `@edfs__natsSubscribe(subjects:
["products.{{ args.id }}"])`, and `Product @key(fields: "id") { id }`. Subgraph B (HTTP):
`Product @key(fields: "id") { id name }`. Operation
`subscription { productUpdated(id: "1") { name } }`. Obligated shape: subscription trigger -> P,
document `subscription { productUpdated(id: "1") { __typename id } }` (key injected, `FS-KEY-3`),
transport = NATS SUBSCRIBE on subject `products.1` (`FS-EDFS-3`); one dependent entity fetch -> B
(a `query`, `FS-SUB-4`) resolving `name` per event. A planner that refuses the config (FS-EDFS-1) or
drops the payload subtree because P has no HTTP upstream SDL (FS-EDFS-2) is non-conforming.

---

## 17. Proposition index

104 propositions. Authority: **SPEC-PINNED 30 * FOLKLORE 23 * OURS 51**. (Read the split honestly:
under a third of federation query planning is pinned by any specification text; nearly half exists
only as derived, stated semantics -- which is this document's reason to exist.) The FOLKLORE class
is fully dispositioned in `FEDERATION_SEMANTICS_FORMAL.md`: **16 derived * 6 axiomatized (9 named
axioms) * 1 rejected-contingent** -- so the whole-document claim after that pass is **0% folklore**:
every behavior is spec-pinned (30), derived (67), axiomatized (6), or a registered divergence (1).
(The five `FS-EDFS` propositions are OURS -- the EDFS *mechanism* is a vendor FOLKLORE-authority
environment fact, but the planning propositions are derived from the model, as `FS-SUB-4`/`-5` are.)
Mixed-authority
propositions are indexed by their primary classification; the per-section Authority classification
prose is authoritative where it distinguishes parts (e.g. FS-REQ-1's pinned ordering vs folklore
transport).

| Construct | Propositions | SPEC-PINNED | FOLKLORE | OURS |
|---|---|---|---|---|
| Section 1 `FS-PLAN` cross-cutting | 7 | FS-PLAN-3, -4 | -- | FS-PLAN-1, -2, -5, -6, -7 |
| Section 2 `FS-KEY` | 11 | FS-KEY-1, -2, -8 | FS-KEY-4, -6 | FS-KEY-3, -5, -7, -9, -10, -11 |
| Section 3 `FS-REQ` | 9 | FS-REQ-1 | FS-REQ-6, -8, -9 | FS-REQ-2, -3, -4, -5, -7 |
| Section 4 `FS-PROV` | 4 | FS-PROV-1, -2 | FS-PROV-4 | FS-PROV-3 |
| Section 5 `FS-EXT` | 3 | FS-EXT-1 | FS-EXT-2 | FS-EXT-3 |
| Section 6 `FS-OVR` | 6 | FS-OVR-1, -4 | FS-OVR-2, -3, -5 | FS-OVR-6 |
| Section 7 `FS-SHR` | 4 | FS-SHR-1, -2 | -- | FS-SHR-3, -4 |
| Section 8 `FS-INACC` | 3 | FS-INACC-1, -2 | -- | FS-INACC-3 |
| Section 9 `FS-IFO` | 7 | FS-IFO-1 | FS-IFO-2, -4, -5, -7 | FS-IFO-3, -6 |
| Section 10 `FS-ABS` | 12 | FS-ABS-10, -11 | FS-ABS-4, -8 | FS-ABS-1, -2, -3, -5, -6, -7, -9, -12 |
| Section 11 `FS-ENT` | 8 | FS-ENT-1, -2, -3, -5 | FS-ENT-7 | FS-ENT-4, -6, -8 |
| Section 12 `FS-ROOT` | 6 | FS-ROOT-2, -3 | -- | FS-ROOT-1, -4, -5, -6 |
| Section 13 `FS-ARG` | 6 | FS-ARG-1, -2, -4, -5 | -- | FS-ARG-3, -6 |
| Section 14 `FS-SUB` | 6 | FS-SUB-1, -6 | FS-SUB-2, -3 | FS-SUB-4, -5 |
| Section 15 `FS-DEF` | 7 | -- | FS-DEF-1, -3, -5, -7 | FS-DEF-2, -4, -6 |
| Section 16 `FS-EDFS` | 5 | -- | -- | FS-EDFS-1, -2, -3, -4, -5 |
| **Total** | **104** | **30** | **23** | **51** |

### Honest gaps (behaviors this document could not ground)

Recorded per Section 0.2's honesty rule -- these are stated in their sections as *unverified convention*
or explicitly scoped out, and a future revision should ground or retire them:

- **FS-REQ-9** -- whether a fragment-conditioned `@requires` coordinate is a hard or conditional
  input: no document; permissive reading adopted, unverified.
- **FS-OVR-4** -- progressive override's per-request realization (plan variants, cache keying,
  non-`percent` label resolution): intent pinned, protocol unpublished; non-`percent` labels are
  vendor-specific and excluded.
- **FS-OVR-2** -- exactly which capabilities the losing subgraph retains post-override is a
  composition-output detail that varies with composer version; stated as MAY.
- **Section 10 mixed abstract positions** -- a position where some capable subgraphs resolve an
  interface-selected field locally and others do not: no corpus case exercises the mixed shape;
  FS-ABS-8 binds only the nobody-resolves-it-on-the-interface case (`FORMAL_SPEC.md` `D3pppp` records
  the same scope limit).
- **FS-SUB-2's impossibility argument** (a source stream cannot span subgraphs) is informal;
  the split is corroborated convention, not proven necessity.
- **FS-DEF-7** -- rests on a draft validation rule that may change before ratification.
- **Nested-key representation shape** (FS-KEY-4) -- the subgraph specification is silent; the
  mirrored-object wire format is folklore that a spec revision should pin.

### Stage-2 note

Each proposition is phrased to be decidable by the five conformance oracles of Section 0.4 plus, for
FS-SUB/FS-DEF, the per-event/per-payload extensions stated in their sections. A conformance-case
generator can traverse this index: for every proposition, at least one witness (a named corpus
suite or the section's worked example) exists to seed a positive case, and each MUST NOT yields its
negative case by construction.
