# Audit corpus fixtures -- attribution & scope

These fixtures are **extracted test data** from The Guild's
[`graphql-federation-gateway-audit`](https://github.com/the-guild-org/graphql-federation-gateway-audit)
(commit `7956ca1`) -- the FULL corpus: all 46 suites, all 199 cases -- used here as a
plan-level oracle for planner-v2.

## License / attribution

The source corpus is MIT-licensed -- Copyright (c) 2024 The Guild. The MIT license
requires that the copyright notice and permission notice accompany all copies or
substantial portions of the Software: the **full verbatim license text ships alongside
these fixtures as [`LICENSE.the-guild`](./LICENSE.the-guild)** (copied from the source
repository at the pinned commit `7956ca1`). The fixtures in this directory tree are
redistributed under those terms; everything else in this repository remains under the
repository's own MIT license (Copyright (c) 2022 WunderGraph UG).

Only the **test data** is extracted: per suite, the subgraph SDLs (verbatim
`typeDefs` from `*.subgraph.ts`), the operations and expected responses (from
`test.ts`), and a `supergraph.graphql` produced by composing the suite's subgraphs
with Apollo's own `@apollo/composition` and printing the **client API schema**
(`Schema.toAPISchema()`) -- the same schema the audit's gateway serves. The audit's
runner, gateway adapters, scoring code, and Node/TypeScript harness are **not**
vendored. Expected responses are used for their **key structure only** (presence /
nesting / list-ness), not their concrete scalar values.

## Fixture format

```
testdata/<suite>/
  subgraphs/<name>.graphql   one federation subgraph SDL per file (verbatim typeDefs)
  supergraph.graphql         Apollo-composed CLIENT API schema (the Plan definition)
  skip.txt                   optional whole-suite SKIP reason (M1 feature gap)
  <case>/operation.graphql   one operation per case directory (print(parse(query)))
  <case>/expected.json       the audit's expected response (structure oracle)
  <case>/skip.txt            optional per-case SKIP reason
  <case>/expect-fail.txt     optional KNOWN planv2 defect: the case still runs; a FAIL
                             reports as GAP (an in-scope, reported finding). If the case
                             starts PASSING, the corpus test fails until the marker is
                             removed -- markers must track reality.
```

The `plan.DataSourceMetadata` for each subgraph is **derived** from its SDL by
`deriver.go` (the composition step): capability node lists with @external
partitioning, @key (incl. `resolvable: false`), @requires (incl. argument capture),
@provides, entity interfaces (`interface X @key`), `@interfaceObject` (concrete
implementers propagated from the composed client schema), and a cross-subgraph
`@override` post-pass.

## Scope (see `runner.go` package doc for the full plan-level contract)

A case **PASSES** at plan level when planner-v2 plans it, every emitted fetch
document validates against its subgraph's schema, fetch dependencies form a valid
order, and the plan's response shape can produce the audit's expected response.
**GAP** marks a known planv2 defect (see `expect-fail.txt` files) -- in scope,
counted against the bar, reported as a finding. **SKIP** marks a case planner-v2
cannot yet plan (an M1 feature gap -- e.g. field-argument lowering, argument-aware
`@requires`, chained `@requires`, parts of `@interfaceObject` refinement) -- out of
the in-scope bar, always with a reason. Error-expectation cases (`errors: true`,
`data: null`) PASS when planv2 correctly rejects the operation (and also when it
plans -- the expected error may be a runtime one, which plan level cannot
adjudicate). Router-level end-to-end execution (real subgraph servers, executed
fetches, value equality) is the separate M1.5 harness.

## Adjudication

Where an expectation would contradict planner-v2's derived semantics, the
resolution is governed by `v2/docs/planner-v2/DIVERGENCES.md` (audit expectations
are adjudicated oracles). DV-005 (`keys-mashup`) is VERIFIED at plan level --
this suite is a registered divergence; see `DIVERGENCES.md` DV-005.
DV-006/DV-007 (`requires-with-argument*`)
are deferred behind the M1 argument-lowering gap (see those suites' `skip.txt`).
No fixture is silently skipped: every non-PASS carries a reason, and every GAP is
a precise, reported planv2 finding.

## Regeneration

`extract.mts` (in this directory) regenerates the fixtures: run it from a clone of
the audit repo (`npm install && tsx extract.mts <outdir>`) -- it imports each
suite's `*.subgraph.ts` + `test.ts`, composes with `@apollo/composition`, prints
the client API schema, and dumps this layout. To refresh against a newer audit
commit, re-run it, re-apply the `skip.txt` / `expect-fail.txt` markers, then
`go test ./pkg/engine/planv2/audit/ -run TestAudit_Corpus -update`.
