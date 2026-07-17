# Planner v2 -- M0: Research & Formal Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce the research survey, formal specification, glossary, paper proofs, and a model-checked TLA+ model that fully determine the design of the hypergraph query planner before any Go planner code is written.

**Architecture:** Five parallel research subagents feed a synthesis step; the formal spec is then written against the synthesized findings; proofs and a TLA+ model are written against the spec; the final task authors the M1 (Go implementation) plan with all knowledge in hand.

**Tech Stack:** Markdown + Mermaid + GitHub-native LaTeX math (`$...$`/`$$...$$`); TLA+ (TLC model checker via `tla2tools.jar`); Go only for a tiny docs-consistency checker script.

**Spec:** `docs/superpowers/specs/2026-07-13-planner-v2-hypergraph-design.md`

## Global Constraints

- All documentation lives in `v2/docs/planner-v2/`; Markdown + Mermaid + GitHub math only -- no LaTeX toolchain, no HTML docs.
- NOTHING derived from customer schemas (names, field names, domain hints, the corpus location) may appear in any committed file. Customer corpus is referenced only as the env var `PLANNER_V2_EXTERNAL_CORPUS`.
- Every mathematical or CS term used in any committed doc MUST have an entry in `GLOSSARY.md` (enforced by the checker script from Task 3).
- Invariant names I1 (Soundness), I2 (Completeness), I3 (Optimality), I4 (Response-shape preservation) are canonical -- use them verbatim everywhere.
- Commit after every task with a `docs(planner-v2):` or `test(planner-v2):` prefix. No planner Go code in M0.

---

### Task 1: Prior-art research fan-out (5 parallel subagents)

**Files:**
- Create: `v2/docs/planner-v2/research-notes/apollo.md`
- Create: `v2/docs/planner-v2/research-notes/hive-router.md`
- Create: `v2/docs/planner-v2/research-notes/db-optimizers.md`
- Create: `v2/docs/planner-v2/research-notes/hypergraph-theory.md`
- Create: `v2/docs/planner-v2/research-notes/current-planner-postmortem.md`

**Interfaces:**
- Produces: five structured notes files, each following the section template below; consumed by Task 2 synthesis.

Each notes file MUST use this exact template:

```markdown
# <Topic>
## Summary (<=10 bullets)
## The model (how this system/theory represents the planning problem)
## The algorithm (search/optimization procedure, complexity, guarantees)
## What it gets right
## Where it breaks (documented failure modes, exponential cases, bugs)
## What planner-v2 should steal / avoid
## Sources (URLs, papers, file paths with line refs)
```

- [ ] **Step 1: Dispatch five research subagents in parallel** with these briefs (verbatim, plus the template above and the customer-data constraint):
  1. **apollo.md** -- Apollo Federation v2 query planner: query graphs, transitions, options/pruning, `@requires`/`@interfaceObject` handling, documented exponential cases (search apollographql/federation repo, docs, issues; the `query-planner-js`/Rust `apollo-federation` crates).
  2. **hive-router.md** -- The Guild's Hive Router query planner (github.com/graphql-hive/router or hive-gateway; Rust `lib/query-planner/`): graph construction (`graph/mod.rs`), OperationPath walker, `narrow_partial_union_paths`, best-path selection, cost handling. Clone the repo and cite files/lines.
  3. **db-optimizers.md** -- Database optimizers applied to federation: System R/Selinger DP, Volcano/Cascades (logical/physical plan spaces, memo structure, branch-and-bound pruning), Postgres planner + GEQO fallback, why cardinality estimation is the main error source; map each concept to a federation-planning analogue.
  4. **hypergraph-theory.md** -- Directed hypergraph algorithms: Gallo, Longo, Pallottino, Nguyen 1993 "Directed hypergraphs and applications" (B-hyperpaths, shortest hyperpath DP, SBT procedure, complexity), Ausiello et al. follow-ups, AND/OR graph search equivalence, admissible-heuristic (A*) formulations; state precisely which problem classes are polynomial vs NP-hard (e.g. shortest B-hyperpath with additive weights vs general minimum hyperpath cost functions).
  5. **current-planner-postmortem.md** -- Read `v2/pkg/engine/plan/` in THIS repo: document the pass pipeline in order, where each known audit failure class was/would be patched (partial unions, child-type-mismatch aliasing, @interfaceObject cases), which passes share mutable state, and why fixes regress unrelated suites. Cite files/lines.

- [ ] **Step 2: Verify each notes file follows the template**

Run: `for f in v2/docs/planner-v2/research-notes/*.md; do grep -L "## What planner-v2 should steal" "$f"; done`
Expected: no output (every file has every template section).

- [ ] **Step 3: Leak scan**

Run the leak scan: grep the docs tree for the private-corpus keywords (the keyword list lives OUTSIDE this repo, e.g. `grep -ril -f "$PLANNER_V2_LEAK_KEYWORDS" v2/docs/planner-v2/ || echo CLEAN`)
Expected: `CLEAN`

- [ ] **Step 4: Commit**

```bash
git add v2/docs/planner-v2/research-notes/
git commit -m "docs(planner-v2): prior-art research notes (M0 task 1)"
```

### Task 2: RESEARCH.md synthesis

**Files:**
- Create: `v2/docs/planner-v2/RESEARCH.md`

**Interfaces:**
- Consumes: the five research-notes files.
- Produces: `RESEARCH.md` with a decision table consumed by Task 4 (FORMAL_SPEC.md); every design decision in FORMAL_SPEC must trace to a row here.

- [ ] **Step 1: Write RESEARCH.md** with sections: Landscape (one subsection per prior-art system, <=1 page each, Mermaid diagram of its model); Comparative table (model, algorithm, optimality guarantee, complexity, failure modes); Lessons for planner-v2 (numbered L1, L2, ... each: lesson -> consequence for our design); Open questions carried into the formal spec.
- [ ] **Step 2: Verify every notes file is cited**

Run: `for f in apollo hive-router db-optimizers hypergraph-theory current-planner-postmortem; do grep -q "$f" v2/docs/planner-v2/RESEARCH.md || echo "MISSING $f"; done`
Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add v2/docs/planner-v2/RESEARCH.md
git commit -m "docs(planner-v2): research synthesis (M0 task 2)"
```

### Task 3: Glossary + docs-consistency checker

**Files:**
- Create: `v2/docs/planner-v2/GLOSSARY.md`
- Create: `v2/internal/docscheck/docscheck_test.go` (Go test, runs in normal `go test`)

**Interfaces:**
- Produces: `GLOSSARY.md` entry format `### <Term>` followed by *Plain English*, *Example*, *Why it matters here* paragraphs; `TestGlossaryCoversMarkedTerms` which later tasks keep green.

Glossary term marking convention: in every planner-v2 doc, the FIRST use of a glossary term is written `**term**` (bold). The checker asserts every bolded term in `FORMAL_SPEC.md`/`PROOFS.md`/`RESEARCH.md` has a `### ` heading in `GLOSSARY.md` (case-insensitive, singular/plural tolerant by trailing-`s` strip).

- [ ] **Step 1: Write the failing test** `v2/internal/docscheck/docscheck_test.go`:

```go
package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var boldRe = regexp.MustCompile(`\*\*([a-zA-Z][a-zA-Z0-9 \-\*/]{2,40})\*\*`)

func glossaryHeadings(t *testing.T, dir string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "GLOSSARY.md"))
	if err != nil {
		t.Fatalf("read glossary: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if h, ok := strings.CutPrefix(line, "### "); ok {
			out[normalize(h)] = true
		}
	}
	return out
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, "s")
}

func TestGlossaryCoversMarkedTerms(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "planner-v2")
	headings := glossaryHeadings(t, dir)
	for _, doc := range []string{"FORMAL_SPEC.md", "PROOFS.md", "RESEARCH.md"} {
		b, err := os.ReadFile(filepath.Join(dir, doc))
		if err != nil {
			continue // doc not written yet; later tasks make this meaningful
		}
		for _, m := range boldRe.FindAllStringSubmatch(string(b), -1) {
			if !headings[normalize(m[1])] {
				t.Errorf("%s: bolded term %q has no GLOSSARY.md entry", doc, m[1])
			}
		}
	}
	if len(headings) == 0 {
		t.Fatal("GLOSSARY.md missing or has no ### entries")
	}
}
```

- [ ] **Step 2: Run test to verify it fails** -- `cd v2 && go test ./internal/docscheck/` -> FAIL (`GLOSSARY.md missing`).
- [ ] **Step 3: Write GLOSSARY.md** with at minimum these entries (each: Plain English / Example / Why it matters here): graph, directed graph, hypergraph, directed hypergraph, hyperedge, B-hyperedge, hyperpath, B-hyperpath, node, edge, weight, cost model, cost function, dynamic programming, memoization, shortest path, Dijkstra's algorithm, A* search, admissible heuristic, branch and bound, search space, state space, NP-hard, polynomial time, asymptotic complexity, big-O notation, invariant, soundness, completeness, optimality, lemma, theorem, proof by induction, fixpoint, partial order, lattice, AND/OR graph, dominance pruning, memo (Cascades), logical plan, physical plan, cardinality estimation, differential testing, property-based testing, model checking, TLA+, temporal logic, refinement, supergraph, subgraph, entity, entity jump, resolution obligation, hyperpath cover, lowering, fetch tree.
- [ ] **Step 4: Run test to verify it passes** -- `cd v2 && go test ./internal/docscheck/` -> PASS.
- [ ] **Step 5: Commit**

```bash
git add v2/docs/planner-v2/GLOSSARY.md v2/internal/docscheck/
git commit -m "docs(planner-v2): glossary + coverage checker test (M0 task 3)"
```

### Task 4: FORMAL_SPEC.md

**Files:**
- Create: `v2/docs/planner-v2/FORMAL_SPEC.md`

**Interfaces:**
- Consumes: RESEARCH.md lessons L1...Ln; GLOSSARY.md conventions.
- Produces: numbered definitions D1...Dn, invariants I1-I4, cost model C, algorithm A (pseudocode) -- the canonical references used by PROOFS.md, the TLA+ model, and all M1 Go code/comments.

- [ ] **Step 1: Write FORMAL_SPEC.md** with this exact section skeleton, fully filled in:
  1. **Inputs** -- D1 supergraph/datasource configuration (formalize `plan.DataSourceConfiguration`: root nodes, child nodes, keys with resolvability, provides, requires, union membership PER SUBGRAPH); D2 normalized operation; D3 obligation tree.
  2. **The planning hypergraph** -- D4 nodes $(T,s)$ + roots; D5 field-traversal edges; D6 type-move edges with per-subgraph member sets; D7 entity-jump B-hyperedges (tails = key/requires obligations); D8 provided-field scoping; worked Mermaid example built from the audit's partial-union schema (A: Common|OnlyA, B: Common|OnlyB).
  3. **Cost model C** -- edge weight vector (new-fetch $w_f$, in-fetch field $w_s$, depth $w_d$); plan cost = defined aggregation; explicit tie-breaking (deterministic plans); statement that weights are config-tunable with defaults $w_f \gg w_d \gg w_s$.
  4. **Plans** -- D9 walk validity; D10 hyperpath cover; D11 lowering to fetch tree (aliasing rule for cross-subgraph output-type conflicts included as a definition, not an afterthought).
  5. **Invariants I1-I4** -- precise statements quantified over all supergraphs/operations.
  6. **Algorithm A** -- SBT-style shortest-B-hyperpath DP with memo keyed (node, obligation), pseudocode <=60 lines, complexity claim $O(|E|\cdot|Q|\log|V|)$-style with derivation sketch; non-additive fetch-merge handling via bounded branch-and-bound; hard state cap semantics.
  7. **Worked examples** -- partial-union case end-to-end (hypergraph -> search trace -> cover -> lowered fetches -> why OnlyA.a is response-only null), and one entity-jump-with-@requires example.
  8. **Conformance** -- how Go packages/tests map to D/I/C/A numbers.
- [ ] **Step 2: Glossary check passes** -- `cd v2 && go test ./internal/docscheck/` -> PASS (add any missing glossary entries).
- [ ] **Step 3: Leak scan** -- grep the docs tree for the private-corpus keywords (list kept outside the repo) -> `CLEAN`.
- [ ] **Step 4: Commit**

```bash
git add v2/docs/planner-v2/FORMAL_SPEC.md v2/docs/planner-v2/GLOSSARY.md
git commit -m "docs(planner-v2): formal specification (M0 task 4)"
```

### Task 5: PROOFS.md

**Files:**
- Create: `v2/docs/planner-v2/PROOFS.md`

**Interfaces:**
- Consumes: FORMAL_SPEC.md D/I/C/A numbering (verbatim).
- Produces: Theorems T1 (I1 soundness), T2 (I2 completeness), T3 (I3 optimality of Algorithm A w.r.t. C), T4 (I4 shape preservation of lowering), T5 (complexity bound); each with full paper proof (induction on walk length / DP structure), plus explicitly-listed assumptions (e.g. additive segment of C) and known limitations.

- [ ] **Step 1: Write PROOFS.md** (theorem -> proof -> "what the TLA+ model checks of this" -> "what the property tests check of this" for each of T1-T5).
- [ ] **Step 2: Glossary check** -- `cd v2 && go test ./internal/docscheck/` -> PASS.
- [ ] **Step 3: Commit**

```bash
git add v2/docs/planner-v2/PROOFS.md v2/docs/planner-v2/GLOSSARY.md
git commit -m "docs(planner-v2): paper proofs T1-T5 (M0 task 5)"
```

### Task 6: TLA+ model of Algorithm A

**Files:**
- Create: `v2/docs/planner-v2/tla/PlannerSearch.tla`
- Create: `v2/docs/planner-v2/tla/PlannerSearch.cfg`
- Create: `v2/docs/planner-v2/tla/README.md` (how to run; where to get tla2tools.jar)
- Create: `v2/docs/planner-v2/tla/smoke_test.sh`

**Interfaces:**
- Consumes: Algorithm A + I1-I3 from FORMAL_SPEC.md.
- Produces: a TLC-checkable model where CONSTANTS encode a small hypergraph instance; invariants `SoundnessInv`, `OptimalityInv` and property `EventuallyPlans` map to I1/I3/I2; README documents the mapping table (TLA name <-> spec number).

- [ ] **Step 1: Write the model** -- state machine of the DP frontier: variables `settled` (function node->cost), `frontier`, `covers`; CONSTANTS `Nodes, Edges (with tails as sets), Weights, RootNode, Obligations`; include at least TWO .cfg instances: the partial-union example and an entity-jump-with-requires example. `OptimalityInv` asserts settled costs equal TLC-computed minima (encode expected minima as CONSTANTS derived by hand from the spec's worked example).
- [ ] **Step 2: Write smoke_test.sh**

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
JAR="${TLA_TOOLS_JAR:-$HOME/.local/lib/tla2tools.jar}"
[ -f "$JAR" ] || { echo "tla2tools.jar not found; see README.md"; exit 1; }
java -XX:+UseParallelGC -cp "$JAR" tlc2.TLC -deadlock PlannerSearch.tla -config PlannerSearch.cfg
```

- [ ] **Step 3: Run TLC, expect a VIOLATION first** (write `OptimalityInv` with a deliberately wrong expected cost to prove the checker can fail), then fix the constant and re-run -> `Model checking completed. No error has been found.`
- [ ] **Step 4: Commit**

```bash
git add v2/docs/planner-v2/tla/
git commit -m "test(planner-v2): TLA+ model of hyperpath search, TLC-checked (M0 task 6)"
```

### Task 7: ARCHITECTURE.md + M1 plan authoring

**Files:**
- Create: `v2/docs/planner-v2/ARCHITECTURE.md`
- Create: `docs/superpowers/plans/2026-07-XX-planner-v2-m1-core.md` (date of authoring)

**Interfaces:**
- Consumes: everything above.
- Produces: the M1 implementation plan (package layout `hypergraph/`, `obligation/`, `search/`, `lower/`, `planv2.go`; TDD task breakdown; audit-corpus harness; differential harness; property tests per invariant; external-corpus harness gated on `PLANNER_V2_EXTERNAL_CORPUS`).

- [ ] **Step 1: Write ARCHITECTURE.md** -- package map with Mermaid, data flow, conformance table (Go package <-> spec section), concurrency notes (immutable hypergraph, per-plan search state).
- [ ] **Step 2: Author the M1 plan** using the writing-plans skill format (bite-sized TDD tasks, exact code in steps), grounded in FORMAL_SPEC numbering.
- [ ] **Step 3: Full M0 verification** -- `cd v2 && go test ./internal/docscheck/` PASS; `bash v2/docs/planner-v2/tla/smoke_test.sh` PASS; leak scan CLEAN; every doc renders (spot-check Mermaid blocks with ```` ```mermaid ```` fences and math with `$$`).
- [ ] **Step 4: Commit**

```bash
git add v2/docs/planner-v2/ARCHITECTURE.md docs/superpowers/plans/
git commit -m "docs(planner-v2): architecture + M1 implementation plan (M0 task 7)"
```
