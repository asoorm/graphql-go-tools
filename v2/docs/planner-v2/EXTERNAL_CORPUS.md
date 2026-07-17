# External-Corpus Harness (M1 Task 12)

`pkg/engine/planv2/external` is an **env-gated, local-only** harness that runs planner-v2's two
existing oracles -- the `differential` package's v1-vs-planv2 response-shape parity check, and the
`audit` package's plan-level assertions (fetch-document validation against subgraph schemas, and
root-entry assertion 5) -- against a **private** directory of customer/production-shaped
supergraph+subgraph SDL and operations that is never committed to this repository.

It exists because the transcribed audit corpus (`pkg/engine/planv2/audit/testdata`) is necessarily
small and hand-curated. This harness lets a developer point planner-v2 at a much larger, real-shaped
corpus on their own machine to look for defects the audit corpus doesn't exercise -- without ever
letting that corpus, or anything derived from it, leave the machine.

## Privacy rules (verbatim -- read before touching this package)

- The harness activates **only** when `PLANNER_V2_EXTERNAL_CORPUS` is set, pointing at a local
  directory of supergraph/subgraph SDL and operations. **When unset: tests SKIP silently** (`t.Skip`
  with a one-line reason). **CI never sets it.**
- **Nothing** derived from the external corpus is ever written inside the repo: no fixtures, no
  golden files, no schema fragments in test names/logs committed. Results (a local
  scoreboard/report) are written to a path **inside the corpus directory or `os.TempDir()`**, never
  the repo.
- **No customer names, domains,** or corpus-identifying strings anywhere in committed code/comments/
  docs. Refer only to **"the external corpus."**
- Before any commit touching this harness: run a leak scan (`grep -ri <corpus-identifying-string>`
  over every file in the diff) and confirm it finds nothing.

These rules are absolute. This repository is open source; a leak here is the one unforgivable
failure for this task.

**The outside-the-repo rule is enforced in code, not just here.** `LoadGraphs` (via
`ensureOutsideAnyRepo` in `loader.go`) resolves the corpus directory (`filepath.Abs` +
`filepath.EvalSymlinks`, so a symlink into a repo cannot dodge the check) and **refuses** -- with a
clear error naming the rule, before reading or writing anything -- any corpus directory that lies
inside a git repository (a `.git` entry anywhere on its resolved ancestor chain). Since the run
report is written *inside* the corpus directory, a corpus inside any repository would put report
files -- including per-case graph/operation names, exactly where a corpus-identifying string would
live -- one `git add` away from a commit. Pointing `PLANNER_V2_EXTERNAL_CORPUS` anywhere under this
repository is therefore rejected at runtime, and as defense-in-depth the repo `.gitignore` also
excludes `.external-corpus-report/`. Keep your private corpus in a plain (non-git) directory.

## Pointing the harness at a corpus

```sh
export PLANNER_V2_EXTERNAL_CORPUS=/absolute/path/to/your/private/corpus
cd v2
go test ./pkg/engine/planv2/external/...
go test -run '^$' -bench BenchmarkExternalCorpusPlanning ./pkg/engine/planv2/external/...
```

With the variable unset (the default, and always true in CI), every test in the package SKIPs and
the benchmark is a no-op.

## Directory convention

The loader (`LoadGraphs` in `loader.go`) discovers one **graph** per immediate subdirectory of the
corpus root:

```
<corpus>/
  <graph-name>/
    subgraphs/
      <subgraph-name>.graphql   # one federation subgraph SDL per file (Federation v2 style;
                                 # a missing @link boilerplate line is auto-prepended)
    supergraph.graphql          # client-facing composed schema (what operations are normalized
                                 # against -- the differential/audit "Definition")
    operations/
      *.graphql                 # one or more operations to run against this graph
```

This mirrors the `audit` package's own `testdata/<suite>/{subgraphs,supergraph.graphql}` convention
(`pkg/engine/planv2/audit/loader.go`) exactly, so the same subgraph -> `plan.DataSourceMetadata`
derivation code path (`audit.BuildDataSources`) applies unchanged to external corpora.

A subdirectory of the corpus root that has no `supergraph.graphql`, or that discovers zero
`operations/*.graphql` files, is silently skipped rather than erroring -- a corpus directory may
contain scratch notes or partially-populated graphs alongside real ones.

`expected.json`-style captured responses are **not** part of this convention (real customer corpora
typically don't have a captured expected response). If you want to layer in the audit package's
expected-response shape assertion for a specific corpus, that's future work, not what this harness
checks today.

## What gets checked, and how cases are classified

For every `(graph, operation)` pair, the harness (`runner.go`):

1. Builds a `plan.Configuration` from the graph's subgraphs via `audit.BuildDataSources` (real
   `graphql_datasource` sources, federation metadata derived from the subgraph SDL).
2. Runs `audit.Run` on the case -- planv2's plan-level assertions: fetch-document validity against
   each subgraph's schema, valid fetch dependency order, and root-entry assertion 5 (no fetch enters
   a root field the client operation never requested). Recorded as `AuditStatus`/`AuditReason`
   (`PASS` / `SKIP` / `FAIL` / `GAP`) -- informational, does not itself fail the differential test.
3. Plans the same `(schema, operation)` with **both** the v1 `plan.Planner` and `planv2.NewPlanner`,
   and compares response shapes with `differential.CompareResponseShapes` -- the same semantic oracle
   (client keys, nesting, gates, leaf kinds) the in-repo differential suite uses.

Each case lands in exactly one `Status`:

| Status                 | Meaning |
|-------------------------|---------|
| `MATCH`                  | Both planners agree on response shape. |
| `EXPECTED_DIVERGENCE`     | Shapes differ, but the `(schema, operation)` content pair is a registered entry in `differential.KnownDivergences` -- an adjudicated, documented divergence (see `DIVERGENCES.md`). |
| `UNEXPLAINED`             | Shapes differ and the pair is **not** allow-listed -- divergence policy prohibits silence here; needs triage (bug or new adjudicated entry). |
| `planv2-error`            | planv2 failed to build a configuration or produce a plan. Always a hard failure -- planv2 must plan every case. |
| `v1-error`                | The v1 planner failed to produce a plan (config build and planv2 succeeded). |

The differential test (`TestExternalCorpusDifferential`) fails on `planv2-error` and `UNEXPLAINED`
only; `v1-error`, `EXPECTED_DIVERGENCE`, and `MATCH` (plus every `AuditStatus`) are recorded in the
report and logged, not treated as hard failures -- they're the exploration signal this harness exists
to surface, not a CI gate (CI never runs this at all).

## Reading the report

Each run writes a local scoreboard to `<corpus>/.external-corpus-report/<UTC timestamp>/`:

- `summary.json` -- generation time, total case count, and counts by `Status`.
- `detail.txt` -- one line per case: `Status`, `graph/operation`, `AuditStatus (AuditReason)`, and
  the `Status` detail (a divergence description or error text).

```json
{
  "generated": "2026-07-14T11:49:09Z",
  "counts": { "MATCH": 118, "EXPECTED_DIVERGENCE": 2, "UNEXPLAINED": 1 },
  "total": 121
}
```

Start triage from `UNEXPLAINED` and `planv2-error` entries in `detail.txt`; everything else is
informational. Nothing in this report -- nor the report's location -- is ever inside the repository.

## Exports this task added (and why)

Two minimal, documented exports made the harness possible without duplicating federation-metadata
derivation or the divergence-adjudication lookup:

- `audit.BuildDataSources(c Case) ([]plan.DataSource, map[string]*ast.Document, error)` -- was
  unexported `buildDataSources`; renamed. Derives `plan.DataSource`s (and their upstream schemas) for
  a `Case`'s subgraphs, the same way `audit.Run` does internally.
- `differential.CaseKey(schema, op string) string` -- was unexported `caseKey`; renamed. The stable
  content-hash key used to look up `differential.KnownDivergences`.

Both packages otherwise remain **read-only** from this harness's perspective: no other exports were
added, and neither package's own behavior changed (only two identifiers were capitalized and their
call sites updated).

## Committed fixture

The only corpus data committed under `pkg/engine/planv2/external/testdata/synthetic/` is a tiny
two-subgraph toy schema -- a toy variation on the classic **public** federation demo shape
(`accounts` + `products`: `User`/`Product`/`topProducts`, the entity-join example this repository
and Apollo's own documentation have long used publicly). It is used by `TestSyntheticCorpusPipeline`
to prove the loader/runner/report pipeline works end-to-end (the test copies it to a temp directory
first, since the in-repo path itself is -- correctly -- refused by the privacy guard). It contains no
external/customer content whatsoever.
