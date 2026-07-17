// Package external is the ENV-GATED external-corpus harness: differential (v1-vs-planv2
// response-shape parity, reusing package differential's oracle) and plan-level (fetch-document
// validation + root-entry assertion 5, reusing package audit's Run/BuildDataSources) runs against a
// PRIVATE, local-only directory of supergraph/subgraph SDL and operations -- never a fixture
// committed to this repository.
//
// # Activation
//
// The harness activates ONLY when the PLANNER_V2_EXTERNAL_CORPUS environment variable is set to a
// local directory. When unset (the default -- CI never sets it), every test in this package SKIPS
// with a one-line reason. See externalCorpusDir in loader.go, the ONLY gateway to that directory.
//
// # Privacy (read before touching this package)
//
// This repository is open source. The following rules are absolute:
//
//  1. Nothing derived from the external corpus is ever written inside the repo: no fixtures, no
//     golden files, no schema fragments in test names or logs that get committed. A local report
//     (see report.go) is written to a path INSIDE the corpus directory or os.TempDir() -- never a
//     path under the repository. ENFORCED IN CODE, not just here: LoadGraphs refuses (via
//     ensureOutsideAnyRepo in loader.go) any corpus directory that resolves inside a git
//     repository, and the repo .gitignore excludes .external-corpus-report/ as defense-in-depth.
//  2. No customer names, domains, or corpus-identifying strings appear in committed code, comments,
//     or docs. Refer only to "the external corpus".
//  3. The only committed fixture this package ships is a tiny two-subgraph toy schema under
//     testdata/synthetic -- a toy variation on the classic PUBLIC federation demo shape
//     (accounts/products: User/Product/topProducts). It contains no external/customer data and is
//     used to prove the loader/runner/report pipeline works without ever touching real data.
//
// See v2/docs/planner-v2/EXTERNAL_CORPUS.md for the directory convention, how to point the env var
// at a corpus, and how to read the report.
package external
