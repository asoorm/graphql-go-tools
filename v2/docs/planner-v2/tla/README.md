# TLA+ model of the planner-v2 hyperpath search (Algorithm A / SETTLE)

This directory contains a TLC-checkable TLA+ model of the **SETTLE** state machine of
Algorithm A -- the Shortest B-Tree search -- specified in
[`../FORMAL_SPEC.md`](../FORMAL_SPEC.md) Section 4-Section 7 (spec review-approved at commit `4e741744`,
amended at `e9578b8e`; the model conforms to the amended C.4 priority key `f(e)`).

The model reproduces the search's memoization state and dynamic-programming relaxation
faithfully -- including the **EXTRACT-MIN** extraction discipline of spec Section 6.1 -- and
model-checks three shadows of the spec invariants (I1/I2/I3) on three concrete hypergraph
instances: the spec's two worked examples (Section 7.1, Section 7.2) plus a competing-derivation instance
that stresses min-selection, with the expected cost minima **computed by hand from the spec**
and encoded as constants.

## Files

| File | Purpose |
|---|---|
| `PlannerSearch.tla` | The generic SETTLE model + all three instances (as operators). |
| `PlannerSearch.cfg` | Instance 1 -- Section 7.1 partial union with value-type members. |
| `PlannerSearchEntityJump.cfg` | Instance 2 -- Section 7.2 entity jump with `@requires` + nested key. |
| `PlannerSearchCompeting.cfg` | Instance 3 -- competing derivations (min-selection stressor). |
| `smoke_test.sh` | Runs TLC on **all three** configs; exits 0 iff all are clean. |

## How to run

```bash
bash smoke_test.sh
```

Requires **Java 11+** (tested on OpenJDK 25) and `tla2tools.jar`. The script looks for the jar
at `$TLA_TOOLS_JAR`, defaulting to `~/.local/lib/tla2tools.jar`.

### Getting `tla2tools.jar`

Download the latest release from the TLA+ tools repository:

```bash
mkdir -p ~/.local/lib
curl -L -o ~/.local/lib/tla2tools.jar \
  https://github.com/tlaplus/tlaplus/releases/latest/download/tla2tools.jar
```

Or run a single config directly:

```bash
java -XX:+UseParallelGC -cp ~/.local/lib/tla2tools.jar tlc2.TLC \
  -deadlock PlannerSearch.tla -config PlannerSearch.cfg
```

`-deadlock` **disables** deadlock checking -- the search legitimately terminates when the
`ready` frontier empties (a `Terminating` stutter step keeps behaviours infinite for the
temporal property).

## What the model encodes (spec fidelity)

- **State machine (spec Section 6.1 SETTLE).** Variables `settled` (settled node set), `pi`
  (node -> tree-cost, the DP memo `pi`), `back` (node -> back-edge, for traceback), `need`
  (edge-id -> remaining unsettled non-root tails, the AND-relaxation counter), and `ready`
  (the frontier -- edges whose every tail is settled).
- **CONSTANTS.** `Nodes`, `Edges` (records `[id, tails (a SET), head (single), kind]` --
  **W1**, single head per edge, enforced by construction), `Weights` (edge-id -> `w(e)`),
  `RootNode`, `Goals`, and `ExpectedPi` (the hand-computed minima).
- **Static conditions (D7).** Every edge exists from the start. An `EntityJump`'s `@key`
  and `@requires` selections are ordinary tail nodes of the *one* jump edge; they settle
  under the same AND-relaxation as any other tail -- no separate condition sub-search.
- **`need` initialization (spec Section 6.1 lines 598-601).** `need[e] = |T(e) \ {RootNode}|`;
  roots are pre-settled at `pi = 0`; edges with all-root tails start in `ready`. A tail is
  never mixed root/non-root (the taxonomy invariant the spec notes).
- **EXTRACT-MIN (spec Section 6.1 line 606, C.4 step 1).** A `Settle` step may extract only a
  ready edge whose tentative value `f(e) = w(e) + Sum_{t in T(e)} pi[t]` (`TentF`) is minimal
  among **all** ready edges. Cost *ties* are not resolved by C.4 in the model -- TLC
  explores every min-cost order (see Determinism below).
- **Cost = C.2 TREE-cost recurrence** (not folded cost C.3): on settling head `h` via
  edge `e`, `pi[h] = w(e) + Sum_{t in T(e)} pi[t]`. The sum re-counts a shared tail once *per
  occurrence*, which is exactly why Section 7.2's `pi((Product,B)) = 6019` (the enter-A fetch is
  re-counted across all five jump tails), not the folded `C(K) = 2018`.
- **Single `RootNode`.** The spec pre-settles the set `Roots(H)`; every instance here is a
  single-operation instance with exactly one root, so the model takes one `RootNode`.
  Generalizing to a root set only changes `Init`.
- **`Inf == 1000000` sentinel.** Sound for these instances because the maximum reachable
  tree-`pi` is 6020 (instance 2's goal); any future instance must keep `pi` well below it.

## Invariant / property <-> spec mapping

| TLA+ name | Spec item | Meaning in the model |
|---|---|---|
| `SoundnessInv` | **I1** (soundness) | **Structural derivation validity is what is checked:** every settled non-root node has a `back` edge that **exists in `Edges`**, whose head is that node and whose tails are **all settled** -- the model shadow of I1's minimal-model derivation (nothing settles except via a real edge over already-settled tails). The final `pi = w(back[n]) + Sum pi(tails)` clause is a *definitional consistency check only* -- it restates the expression `Settle` assigns, so it cannot fail on its own; the numeric burden (is `pi` the *minimum*?) is carried by `OptimalityInv`. |
| `OptimalityInv` | **I3** (tree-optimality) | Every settled node's `pi` equals `ExpectedPi[node]`, the tree-cost minimum **computed by hand from the spec's worked example**. |
| `EventuallyPlans` | **I2** (completeness) | `<>(Goals  subseteq  settled)` -- every reachable goal candidate node is eventually settled (under weak fairness of `Settle`). |
| `need` init | Section 6.1 L598-601 | `need[e] = |T(e) \ {RootNode}|`; root pre-settled; all-root-tail edges seed `ready`. |
| `Weights` / `W(e)` | **C.1** | Edge weight vector; `w_f=1000, w_s=1, w_d=10`; `EntityJump` weight `w_f+w_d=1010`, subgraph-entering `Field` `w_f`, in-subgraph `Field`/`TypeMove` `w_s`, `Descent` `0`. |
| `TypeOK` | -- | Structural type invariant on the state variables. |
| `TentF` / min-extraction | Section 6.1 L606, **C.4** step 1 | `EXTRACT-MIN`: only a ready edge of minimal tentative value may be extracted; ties explored by TLC. |

## The three instances (expected minima derived by hand)

### Instance 1 -- Section 7.1 partial union (`PlannerSearch.cfg`)

Query `{ wrapper { action { __typename ...Common{c} ...OnlyA{a} ...OnlyB{b} } } }` over the
partial-union schema. `SETTLE` from `r_Query` reproduces the spec's step table exactly -- the
**1000 -> 1001 -> 1002 -> 1003** progression:

| node kind | via | `pi` |
|---|---|---|
| `(Query,*).wrapper` (enter A/B) | `Field` `w_f` | 1000 |
| `(Wrapper,*)` | `Descent` `0` | 1000 |
| `(Wrapper,*).action` | `Field` `w_s` | 1001 |
| `(Action,*)` | `Descent` `0` | 1001 |
| `(Common,*)`,`(OnlyA,A)`,`(OnlyB,B)` | `TypeMove` `w_s` | 1002 |
| `(Common,*).c`,`(OnlyA,A).a`,`(OnlyB,B).b` | `Field` `w_s` | 1003 |

Both `(Common,A).c` and `(Common,B).c` settle at **1003** -- the spec's **A/B tie**, which the
cover step then resolves by the C.4 tie-break (subgraph `A` < `B`). The model asserts the tie
directly (`ExpectedPi["cA"] = ExpectedPi["cB"] = 1003`). All four refinement leaves are
*reachable* and settle (`EventuallyPlans`); which are *covered* is a post-settle D6 / C.4
decision (out of scope for the SETTLE state machine -- see the `PU_Goals` comment).

### Instance 2 -- Section 7.2 entity jump with `@requires` (`PlannerSearchEntityJump.cfg`)

Query `{ product { shippingEstimate } }`; `Product @key(id organization { id })`;
`B.shippingEstimate @requires(dimensions { length width height })`. The `EntityJump` into
`(Product,B)` has a **5-node tail** (nested-key nodes `(Product,A).id`, `(Organization,A).id`
+ `@requires` nodes `(Dimensions,A).{length,width,height}`) and fires only when all five settle.

Hand computation under `w_f=1000, w_s=1, w_d=10, (+)=Sum` (spec Section 7.2):

```
pi((Product,A))            = 1000            (= w_f)
pi((Product,A).id)         = 1001            (= w_f + w_s)
pi((Organization,A).id)    = 1002            (= w_f + 2 w_s)
pi((Dimensions,A).{l,w,h}) = 1002 each       (= w_f + 2 w_s)
Sum of the five jump tails  = 1001 + 4*1002 = 5009   (= 5 w_f + 9 w_s)
pi((Product,B))            = (w_f + w_d) + 5009 = 1010 + 5009 = 6019
pi((Product,B).shippingEstimate) = 6019 + w_s     = 6020
```

The model asserts **`ExpectedPi["ProdB"] = 6019`** and **`ExpectedPi["se"] = 6020`**, matching
the spec's hand-derived tree-`pi`. (The realized folded cost `C(K)=2018` of C.3 is *not* the
search objective and is not modelled here -- the model checks the TREE objective the search
actually minimizes.)

### Instance 3 -- competing derivations (`PlannerSearchCompeting.cfg`)

Instances 1 and 2 give every node exactly **one** incoming edge, so min-selection is never
actually exercised there. This synthetic minimal instance exists precisely to stress it:
node `fx` has **two** incoming derivations --

- direct: `enter A (1000) -> Descent (0) -> Field fx (1)` -- tentative value **1001**;
- detour: `... -> Field id (1001) -> EntityJump to FooB (1010, pi=2011) -> Field fx (1)` --
  tentative value **2012**.

`EXTRACT-MIN` must settle `fx` at the minimum, **1001** (`ExpectedPi["fx"] = 1001`,
`ExpectedPi["FooB"] = 2011`). Verified both ways: with the min-extraction constraint removed
(first-writer-wins, in a scratch copy) TLC finds the interleaving that settles `fx` at
**2012** and `OptimalityInv` fires; with min-extraction the instance is green. The losing
edge `eViaB` is discarded at *fire* time (the `fired` filter excludes edges whose head is
already settled, so `eViaB` never enters `ready`) -- behaviorally equivalent to the
pseudocode's push-then-`continue`, which drops the stale edge at extraction time instead.

## Determinism: min-extraction plus tie exploration

The spec makes `EXTRACT-MIN` deterministic via the **C.4** total order: minimal cost first
(step 1), remaining ties broken by subgraph/kind/label/identifier (steps 2-5). The model
hard-codes **step 1 only**: `Settle` may extract a ready edge `e` only if its tentative value
`TentF(e)` is minimal among all ready edges. Cost *ties* (C.4 steps 2-5) are left
nondeterministic, so TLC explores **every min-cost extraction order**. `SoundnessInv` and
`OptimalityInv` holding across all reachable states (47 / 37 / 7 distinct per instance) shows
the settled `pi` table is **order-independent under min-extraction** -- which is exactly the
property the C.4 tie-break's determinism argument rests on (C.4 then pins *one* of these
equivalent orders, and the back-pointer/plan choice, deterministically). Note the model does
*not* check unrestricted extraction orders: without min-extraction the settled `pi` would not
be the minimum (see Instance 3), so "all orders" would be checking a different, unsound
algorithm, not a stronger property.

## Deliberate violation (proving the checker can fail)

Per the task brief's mandatory Step 3, before trusting a green result we first proved TLC
*can* fail. Pointing the entity-jump config's `ExpectedPi` at the built-in wrong variant
`EJ_ExpectedPi_WRONG` (which claims the goal `shippingEstimate` settles at **6019** instead of
the correct **6020**) yields:

```
Error: Invariant OptimalityInv is violated.
Error: The behavior up to this point is:
 ... State 14: <Settle ...>  \* the step that settles "se"
 /\ ready = {[id |-> "eSE", tails |-> {"ProdB"}, head |-> "se", kind |-> "FieldIn"]}
70 states generated, 37 distinct states found, 0 states left on queue.
```

TLC finds the counterexample: the search computes `pi("se") = 6020`, contradicting the planted
`6019`. Restoring the correct constant (`EJ_ExpectedPi`, `se |-> 6020`) then gives
`Model checking completed. No error has been found.`

The same discipline was repeated for instance 3: running with `CD_ExpectedPi_WRONG` (which
claims `fx` settles via the losing derivation at **2012**) yields

```
Error: Invariant OptimalityInv is violated.
 ... State 4: <Settle ...>
 /\ pi = [ r |-> 0, QfooA |-> 1000, FooA |-> 1000, ..., fx |-> 1001 ]
 /\ back = [ ..., fx |-> [id |-> "eDirect", ...] ]
5 states generated, 5 distinct states found, 1 states left on queue.
```

-- min-extraction settles `fx` at the true minimum **1001** via `eDirect`, contradicting the
planted `2012`. Restoring `CD_ExpectedPi` gives green. The wrong variants
(`PU_ExpectedPi_WRONG`, `EJ_ExpectedPi_WRONG`, `CD_ExpectedPi_WRONG`) remain in the `.tla` as
documentation but are **not** wired into any committed `.cfg`.

## Results (all three configs clean)

```
PlannerSearch.cfg            : 90 states generated, 47 distinct -- No error has been found.
PlannerSearchEntityJump.cfg  : 71 states generated, 37 distinct -- No error has been found.
PlannerSearchCompeting.cfg   :  9 states generated,  7 distinct -- No error has been found.
```

(State counts are smaller than a first-writer-wins model would give: min-extraction prunes
the extraction interleavings to the min-cost ones, as the spec's priority queue does.)
