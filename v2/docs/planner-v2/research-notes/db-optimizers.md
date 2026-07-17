# Database Query Optimizers -- Concept Map for Federation Query Planning

## Summary (<=10 bullets)

- **System R / Selinger (1979)**: dynamic programming (DP) over join subsets, extended with *interesting orders*, is provably cost-optimal **for the stated cost model** -- the direct analogue for planner-v2 is DP over `(resolved field-set, satisfied key/representation obligations)`.
- **Volcano/Cascades**: separates *logical* algebra (what the plan computes) from *physical* implementation (how); the **memo** is a DAG of equivalence groups + multi-expressions; branch-and-bound with cost upper bounds and "promise" heuristics prunes the search without full enumeration. This maps almost directly onto a logical fetch-graph vs. physical fetch-tree split for federation.
- **DPccp / DPhyp** (Moerkotte & Neumann, 2006/2008) generalize System R's DP to arbitrary **hypergraphs** (join predicates spanning >2 relations) by enumerating connected-subgraph/complement pairs (csg-cmp). Since planner-v2 is *already* a directed-hypergraph model, this is close to the literal algorithm to reuse for the search core, not just an analogy.
- **PostgreSQL**: exhaustive DP below `geqo_threshold` (default 12 relations), falls back to a genetic algorithm (GEQO) above it -- an explicit, documented admission that DP is exponential and that a bounded-effort heuristic beats exhaustiveness at scale.
- **MySQL**: primarily a **greedy**, depth-limited left-deep search (`optimizer_search_depth`, default 62) rather than full DP; MySQL 8.0.23+ added a DPhyp-based "hypergraph optimizer" as the modern, more complete successor.
- **Leis et al., "How Good Are Query Optimizers, Really?" (VLDB 2015)**: using the Join Order Benchmark, the paper isolates plan enumeration, cost model, and cardinality estimation as independent variables and shows **cardinality estimation error is the dominant, compounding source of bad real-world plans** -- not the search algorithm or the cost model.
- Federation planning has **no row cardinalities to estimate**. Cost should be structural (fetch count, round-trip/waterfall depth, redundant field re-fetches, batching opportunities) -- this sidesteps the JOB paper's core failure mode entirely, *if* the design resists importing a statistics/estimation subsystem that solves a problem federation planning doesn't have.
- What transfers cleanly: memoization keyed on `(requirement, obligations)`; "interesting orders" -> "interesting obligations" (already-resolved key fields); branch-and-bound with a monotonic lower bound; logical/physical plan separation.
- What does **not** transfer: row-count selectivity estimation, histograms/statistics, disk-I/O-based cost units, GEQO's non-deterministic genetic mutation model (though "abandon exhaustiveness past a size threshold, with a bound" does transfer).
- Practical implication: build the DP/memo core like System R + DPhyp with a **structural, statistics-free** cost function; keep the logical/physical split like Cascades; and decide the exhaustive-vs-bounded cutover deliberately (Postgres/MySQL-style), with a provable approximation bound rather than an unprincipled genetic fallback.

## The model (how this system/theory represents the planning problem)

### System R (Selinger et al., 1979)

A query is a set of base relations plus join predicates connecting them. The search space is the set of ways to order the joins and choose an access path (index scan, sequential scan) per relation. System R restricts the space to **left-deep join trees** (the right input of every join is a base relation) and defers Cartesian products to the end. Each candidate plan additionally carries a **physical property**: the sort order (if any) of its output.

**Federation analogue**: a query is a set of requested fields; the "relations" are `(subgraph, type)` capability nodes that can resolve subsets of those fields; "join predicates" are `@key`/`@requires` dependency edges. A candidate plan is not a join order but a **fetch order** -- which subgraph fetches which fields, and in what sequence, given that later fetches may depend on representation variables produced by earlier ones.

### Volcano / Cascades (Graefe, 1990s)

Queries are expressions in a relational algebra. **Logical operators** (Join, Select, Project) describe *what*; **physical operators** (HashJoin, NestedLoopJoin, IndexScan) describe *how*. A **group** is an equivalence class of logically-equivalent expressions; the **memo** is the set of all groups plus, per group, all discovered logical and physical *multi-expressions*. Transformation rules rewrite logical expressions into other logical expressions (exploration); implementation rules turn logical expressions into physical ones (implementation). Physical properties (e.g., required sort order, distribution) are demanded top-down and satisfied bottom-up.

**Federation analogue**: a **logical plan** is "field F is resolved by subgraph S, dependent on key K being available" -- capability-level, subgraph-agnostic of *how* the fetch is executed. A **physical plan** is the concrete **fetch tree**: which HTTP/gRPC calls happen, in what order, batched or not, parallel or sequential. The memo group <-> the set of all subgraphs/paths capable of resolving a given field-set under a given obligation; physical multi-expressions <-> concrete fetch-tree shapes for that group.

### PostgreSQL planner

Builds `RelOptInfo` nodes bottom-up per (subset of) joined relations; each `RelOptInfo` accumulates a set of `Path`s (physical alternatives, e.g. different join algorithms/orders reaching that relation set), pruned by cost. Below `geqo_threshold` (default 12 `FROM` items) this is exhaustive DP (`join_search_one_level`); above it, `geqo_main` encodes join orders as integer permutation strings and runs a genetic algorithm (edge-recombination crossover, mutation, multiple generations) to find a good -- not guaranteed optimal -- order.

**Federation analogue**: `RelOptInfo` per join-subset <-> memo entry per resolved-field-subset; `geqo_threshold` <-> an explicit subgraph/entity-count cutover above which planner-v2 would need a bounded fallback (supergraphs with 50-100+ subgraphs are increasingly common, unlike most OLTP join queries which rarely exceed a handful of tables).

### MySQL optimizer

Historically a **greedy** search (`best_extension_by_limited_search`): pick a starting table, then repeatedly extend the current partial join order by whichever next table looks cheapest, considering only prefixes up to `optimizer_search_depth`. Left-deep only; no bushy plans in the classic optimizer. MySQL 8.0.23 (2021) introduced a new **hypergraph-based join optimizer** (opt-in, later default in later releases) built on DPhyp-style enumeration over a hypergraph of join conditions, explicitly to support bushy plans and complex (non-equi, multi-table) predicates that the greedy optimizer handled poorly.

**Federation analogue**: MySQL's own trajectory -- greedy heuristic first, later replaced by a hypergraph DP optimizer -- is a cautionary/validating data point: the industry's own answer to "how do you order joins over a graph with complex, multi-node predicates" converged on exactly the hypergraph-DP model planner-v2 is starting from directly.

### Leis et al., "How Good Are Query Optimizers, Really?" (VLDB 2015)

Not an optimizer itself but an **empirical audit methodology**. Introduces the Join Order Benchmark (JOB) over the real, correlated IMDB dataset (deliberately *not* the synthetic, uniform, independent TPC-H/TPC-DS data most optimizers are tuned against). Treats each production DBMS as a black box and independently varies: (a) plan enumeration completeness, (b) cost model, (c) cardinality estimates (true vs. estimated), to attribute end-to-end regressions to their actual cause.

**Federation analogue**: the methodology (not the data model) transfers -- planner-v2 should have its own "JOB-style" adversarial benchmark: a supergraph with deliberately correlated/skewed structure (deep `@requires` chains, overlapping key sets, entities resolvable by many subgraphs) used to empirically attribute planning regressions to enumeration-completeness bugs vs. cost-model bugs, exactly as this paper attributes DB regressions to cardinality-estimation bugs.

## The algorithm (search/optimization procedure, complexity, guarantees)

### System R's DP recurrence

$$\text{OptPlan}(S) = \min_{S_1 \cup S_2 = S,\; S_1 \cap S_2 = \emptyset,\; S_1,S_2 \text{ joinable}} \big[\text{cost}(\text{OptPlan}(S_1)) + \text{cost}(\text{OptPlan}(S_2)) + \text{joinCost}(S_1,S_2)\big]$$

computed bottom-up by increasing $|S|$, memoizing the best plan **per interesting order** for each subset $S$ (not just the single cheapest plan -- a plan that costs slightly more but avoids a later sort can win overall). With the left-deep restriction the space is $O(n \cdot 2^n)$ subsets/extensions rather than the full $O(3^n)$ needed for arbitrary bushy trees; still exponential, which is why every later system (Postgres, MySQL, DPccp/DPhyp) had to either bound $n$ or change the enumeration strategy.

### Volcano / Cascades search

Cascades performs **top-down, goal-directed** branch-and-bound search via a task stack (`O_GROUP` -- optimize a group to satisfy required properties; `O_EXPR` -- optimize one multi-expression; `E_EXPR` -- explore/apply transformation rules to generate alternatives; `O_INPUTS` -- recursively optimize child groups and combine costs). Each group memoizes its best-cost plan per required physical property so shared subexpressions are optimized once. A running **upper bound** (cost of the best complete plan found so far) prunes any partial plan whose already-accumulated cost exceeds it -- classic branch-and-bound -- and "promise" heuristics order which rules/tasks to try first to find a good upper bound early, which is what makes the pruning effective in practice. Volcano's own bound is NP-hard in general (as is any complete plan-space search over an NP-hard problem), but the memo guarantees no equivalent subexpression is re-optimized, which is the actual asymptotic win over naive recursive search.

### PostgreSQL

`standard_join_search` performs the System-R-style exhaustive DP when `#FROM items < geqo_threshold` (default 12; also gated by `join_collapse_limit`/`from_collapse_limit`). Above that, `geqo_main` runs a genetic algorithm: candidate join orders are integer permutation strings, evaluated by the normal cost model, evolved via edge-recombination crossover across `Pop_size` generations with a fixed `Generations` budget. This is **polynomial-time but not complete** -- no optimality guarantee, only "expected to be good, bounded computation budget."

### MySQL

Legacy optimizer: greedy prefix search, complexity governed by `optimizer_search_depth` -- small depth is closer to $O(n^2)$, `depth = n` degenerates toward exhaustive $O(n!)$/$O(2^n)$-class search. No optimality guarantee at low depth. Hypergraph optimizer (8.0.23+): models the join graph as a hypergraph and applies DPhyp-style connected-subgraph enumeration -- worst case still $O(3^n)$-class but with a far smaller practical constant than naive subset enumeration, and correctly handles predicates spanning more than two tables (which the classic pairwise-join model could not represent at all).

### DPccp / DPhyp (Moerkotte & Neumann)

DPccp enumerates, via DP over a graph representation of the query, all pairs of **connected subgraphs and their connected complements** (`csg-cmp` pairs) that correspond to valid joins, guaranteeing every optimal plan is considered while skipping cross-product-only combinations. It handles only simple (binary, edge-per-predicate) join graphs. DPhyp generalizes this to true **hypergraphs**, where a single join predicate can reference more than two relations at once (exactly the shape of a multi-parent `@requires`/`@key` dependency), by reasoning about connected subgraphs/complements of the hypergraph directly rather than requiring predicates to be decomposed into pairwise edges first. Complexity remains $O(3^n)$ worst case (same asymptotic class as full bushy DP) but is output-sensitive in practice -- cost is dominated by the actual number of connected subgraphs in the query graph, which for typical (sparse, star/chain-shaped) queries is far below $3^n$.

### Leis et al.'s experiment design

The paper re-executes JOB queries under every combination of {true cardinalities, estimated cardinalities} x {actual cost model, simplified cost model} x {full enumeration, restricted enumeration}, then measures the ratio of actual runtime to the runtime of the truly-optimal plan. Key empirical result: **q-error (the ratio between estimated and true cardinality) grows exponentially with the number of joins**, because errors at the base-table level compound multiplicatively as they propagate up the join tree. Even when equipped with the exact same cost model and a complete enumeration, plans built on estimated cardinalities were regularly **10x-1000x** slower than the plans built on true cardinalities -- i.e. the search algorithm found the (cost-model-)optimal plan for the *wrong* cardinalities, and no amount of search improvement fixes that.

## What it gets right

- DP (System R, DPccp/DPhyp) gives a **provable optimality guarantee relative to the stated cost model** -- this is exactly the property planner-v2's design goal ("provable optimality") needs, and DP/memo is the established way to get it without brute-force enumeration.
- Cascades' logical/physical separation buys **extensibility for free**: new physical strategies (batched fetch, entity-interface fetch, subscription fetch) become new implementation rules without touching the search core or invalidating the optimality argument.
- "Interesting orders" is a genuinely reusable idea outside sorting: it is the general pattern of "carry forward a free byproduct of an earlier subplan instead of re-deriving it," which for federation is exactly an already-resolved representation/key field.
- DPccp/DPhyp's hypergraph formulation is not just an analogy for planner-v2 -- it is close to literally the right data structure and enumeration algorithm, since federation dependency edges (a field `@requires`-ing multiple parent fields resolved by different subgraphs) are genuine hyperedges, not decomposable pairwise predicates.
- A statistics-free, structural cost model in federation removes the *entire* class of error that Leis et al. show dominates real-world DB optimizer failures. This is a real structural advantage of the domain, not a simplification to apologize for -- worth stating explicitly in the design rather than treating cost modeling as "the easy part we'll get to later."

## Where it breaks (documented failure modes, exponential cases, bugs)

- **Left-deep-only restriction** (System R, classic MySQL) misses bushy plans that can be strictly cheaper when two subtrees are independent and can run in parallel. This matters *more* for federation than for SQL joins: independent subgraph fetches parallelize over the network, so a left-deep-only fetch tree needlessly serializes work a bushy/parallel tree would overlap. **Do not copy this restriction.**
- **Exhaustive DP/memo is combinatorially explosive** past a modest size -- Postgres bails to GEQO at 12 `FROM` items by default; this is a small number. Federation supergraphs commonly have 30-100+ subgraphs/entity types, well past where naive subset-DP is tractable. The mitigation is not "make DP faster" in the abstract but specifically **DPccp/DPhyp-style connected-subgraph enumeration**, which is output-sensitive on the actual (typically sparse) dependency graph rather than the full powerset.
- Leis et al.: cardinality misestimation **compounds multiplicatively** with query/join depth, producing 10x-1000x regressions in production systems despite provably-optimal search over the (wrong) estimated cost. This is the strongest argument in the whole literature for keeping cost inputs to planner-v2 **exact/structural** (measured or counted, not estimated/predicted) -- the moment planner-v2 introduces any predicted/estimated quantity (e.g. predicted subgraph latency from a moving average) it re-imports this exact failure class, and the paper documents how bad that failure class gets even inside "correct" search algorithms.
- Volcano/Cascades memo correctness depends on the transformation rule set being **terminating and confluent enough** to avoid infinite rewrite loops or double-counting; production implementations track "already-applied" rule bitmaps per expression specifically to guard against this. Planner-v2's hypergraph traversal needs the equivalent termination guarantee (e.g., DAG-acyclicity of the obligation-satisfaction relation) designed in from the start, not patched in after an infinite-loop bug report.
- GEQO/greedy fallbacks **give up the optimality proof**, not just optimality in practice -- Postgres and MySQL's own docs describe this as a deliberate, unproven trade for runtime; genetic search is additionally **non-deterministic** run-to-run. Neither property is acceptable if planner-v2's core claim is "provably optimal": a fallback that silently degrades to "no guarantee at all" undermines the whole premise the moment a supergraph crosses the threshold.
- MySQL's legacy greedy optimizer is documented (and was the direct motivation for the 8.0.23 hypergraph optimizer rewrite) to produce poor plans specifically on queries with complex, multi-table join predicates -- i.e., exactly the case (hyperedges) that pairwise-greedy/pairwise-DP approaches structurally cannot represent well. This is direct evidence that "generalize to hypergraphs early" is not gold-plating for planner-v2 but the thing that prevents a whole class of known-bad plans.

## What planner-v2 should steal / avoid

**Steal:**
- **DPccp/DPhyp's csg-cmp hypergraph enumeration** as the literal join/fetch-enumeration algorithm: since the model is already a directed hypergraph, represent subgraph capabilities and multi-parent key/`@requires` dependencies as hyperedges and reuse connected-subgraph/complement-pair enumeration for the search core. This gives completeness ($O(3^n)$ worst case, output-sensitive in practice) for free instead of re-deriving a bespoke enumeration scheme.
- **Cascades' memo + logical/physical split**: a memoized `(field-set, obligations) -> best physical fetch-plan` table, with logical "capability" rules kept separate from physical "fetch-tree-shape" rules (batching, parallelism, entity interfaces), so new fetch strategies are additive.
- **"Interesting orders" -> "interesting obligations"**: make already-resolved key/representation fields a first-class dimension of the DP state, exactly as System R tracks sort order, to avoid redundant re-fetching of representation variables.
- **Branch-and-bound with a monotonic structural lower bound** (Cascades-style promise/pruning) so the hypergraph search stays practical on large supergraphs without needing to abandon completeness.
- An **explicit, designed-in exhaustiveness cutover** (a la `geqo_threshold`) for pathological supergraph sizes -- but unlike GEQO, the fallback should carry a **provable approximation bound**, so "provably optimal" degrades gracefully to "provably within factor K of optimal" instead of silently becoming "no guarantee."
- Leis et al.'s **audit methodology**: build an internal, deliberately-adversarial benchmark supergraph (deep `@requires` chains, overlapping/ambiguous key resolution paths, high subgraph fan-out) to independently attribute planner regressions to enumeration bugs vs. cost-model bugs, the way JOB isolates cardinality-estimation error from the rest of the DB optimizer.

**Avoid:**
- The **left-deep-only** heuristic -- federation gains real, measurable latency wins from bushy/parallel fetch trees that left-deep-only search cannot represent.
- **Row-cardinality-style estimation** of any kind. Federation planning's "unknowns" (field counts requested, number of subgraphs, structural fan-out) are exact/countable at plan time, not statistically estimated -- importing a cardinality-estimation subsystem would be solving a problem this domain doesn't have, while also importing the JOB paper's documented dominant failure mode into a system that could otherwise structurally avoid it.
- **GEQO-style non-deterministic genetic fallback** as the answer to scale. Non-determinism in a compiled/cached query plan is user-hostile (same operation could compile to different fetch plans across cache misses) and, per above, forfeits the optimality proof without a compensating bound.
- **Unbounded/non-terminating transformation rules.** The hypergraph formalism must guarantee the equivalent of DAG-acyclicity so rule/edge traversal always terminates and the memo is provably complete -- a known, documented Volcano/Cascades implementation hazard, not a hypothetical one.

## Sources

- Selinger, P.G. et al. "Access Path Selection in a Relational Database Management System." SIGMOD 1979. [ACM DL](https://dl.acm.org/doi/10.1145/582095.582099) * [PDF (Duke CPS 216 course mirror)](https://courses.cs.duke.edu/compsci516/cps216/spring03/papers/selinger-etal-1979.pdf) * [IBM Research page](https://research.ibm.com/publications/access-path-selection-in-a-relational-database-management-system)
- Graefe, G. "The Cascades Framework for Query Optimization." IEEE Data Engineering Bulletin, 1995. [PDF (CMU 15-721 mirror)](https://15721.courses.cs.cmu.edu/spring2016/papers/graefe-ieee1995.pdf)
- CMU 15-721 course notes on the Cascades/Volcano optimizer (task-based search, memo structure, branch-and-bound). [Lecture #3 notes](https://www.cs.cmu.edu/~15721-f24/notes/03_QO2.pdf) * [Optimizer Implementation slides](https://15721.courses.cs.cmu.edu/spring2017/slides/15-optimizer2.pdf) * [CMU 15-799 Volcano notes](https://15799.courses.cs.cmu.edu/spring2025/notes/04-volcano.pdf)
- Moerkotte, G. and Neumann, T. "Dynamic Programming Strikes Back" (DPccp/DPhyp, hypergraph join enumeration). SIGMOD 2008. [ACM DL](https://dl.acm.org/doi/10.1145/1376616.1376672) * [PDF (CMU 15-721 mirror)](https://15721.courses.cs.cmu.edu/spring2020/papers/20-optimizer2/p539-moerkotte.pdf)
- PostgreSQL Documentation, "Query Planning" (`geqo_threshold`, `join_collapse_limit`). [postgresql.org/docs/current/runtime-config-query.html](https://www.postgresql.org/docs/current/runtime-config-query.html)
- PostgreSQL Documentation, "Genetic Query Optimization (GEQO) in PostgreSQL." [postgresql.org/docs/current/geqo-pg-intro.html](https://www.postgresql.org/docs/current/geqo-pg-intro.html)
- PostgreSQL Documentation, "Planner/Optimizer" internals overview. [postgresql.org/docs/current/planner-optimizer.html](https://www.postgresql.org/docs/current/planner-optimizer.html)
- Alibaba Cloud Community, "Analysis of the MySQL Join Reorder Algorithm" (greedy search, `optimizer_search_depth`). [alibabacloud.com/blog/analysis-of-the-mysql-join-reorder-algorithm_601895](https://www.alibabacloud.com/blog/analysis-of-the-mysql-join-reorder-algorithm_601895)
- Alibaba Cloud Community, "Code Explanation of MySQL 8.0.23 Hypergraph Join Optimizer." [alibabacloud.com/blog/code-explanation-of-mysql-8-0-23-hypergraph-join-optimizer_600430](https://www.alibabacloud.com/blog/code-explanation-of-mysql-8-0-23-hypergraph-join-optimizer_600430)
- Leis, V., Gubichev, A., Mirchev, A., Boncz, P., Kemper, A., Neumann, T. "How Good Are Query Optimizers, Really?" PVLDB 9(3), 2015 (Join Order Benchmark). [dblp entry](https://dblp.org/rec/journals/pvldb/LeisGMBK015.html)
- Leis, V. "Still Asking: How Good Are Query Optimizers, Really?" PVLDB 18, 2025 (follow-up revisiting the 2015 findings a decade later). [PDF](http://www.vldb.org/pvldb/vol18/p5531-viktor.pdf) * [ACM DL](https://dl.acm.org/doi/10.14778/3750601.3760521)
- ACM SIGMOD Blog, "The Case for Cardinality Bounds: Principled Conservatism in Query Optimization" (context on cardinality-estimation-driven optimizer failure). [wp.sigmod.org/?p=3707](https://wp.sigmod.org/?p=3707)
