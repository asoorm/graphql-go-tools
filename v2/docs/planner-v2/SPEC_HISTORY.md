# Spec History -- Federation Query Planner v2

This file is the planner-v2 spec's **journal**. `FORMAL_SPEC.md` states the model's *current*
semantics -- every definition in its amended form, with no chronology. This file records how those
semantics evolved: for each definition, the original amendment records are preserved **verbatim**
(wave names, defect narratives, witness measurements, commit references, status markers, and
retirement gates included), in the order they were written. Nothing here is normative; where a
record below disagrees in wording with `FORMAL_SPEC.md`, the normative document wins.

Division of labor across the three documents:

- `FORMAL_SPEC.md` -- what the semantics **are** (normative; the reference for `PROOFS.md`, the TLA+
  model, and the Go implementation).
- `SPEC_HISTORY.md` (this file) -- how the semantics **got here** (the amendment chronology).
- `DIVERGENCES.md` -- live divergence adjudications and the residual register (what is currently
  known-divergent or deferred, with owners and gates). A few records below reference register
  entries or retirement gates; the live figures are in `DIVERGENCES.md`, and those records stay
  here (as history) rather than being folded into that register.

Each entry below gives the definition it amends, the wave that produced it, and then the original
record verbatim as it stood in `FORMAL_SPEC.md` before the normative/journal split.

---

## Document header

The status line `FORMAL_SPEC.md` carried before the split:

**Status:** M0 draft (task 4). Canonical reference for `PROOFS.md`, the TLA+ model, and all M1 Go
code. **Branch:** `feat/planner-v2-hypergraph`.

---

## D3 -- Obligation tree

### D3p -- typename-terminal goals (M1.5 gaps wave)

Original record, verbatim:

**D3p -- typename-terminal goals (M1.5 gaps wave).** The *goal* set $G(O)$ counts a field obligation
$\langle T.f\rangle$ as a goal iff it is a *resolution leaf*: it has no *field-obligation* descendant.
This generalizes the plain "leaves" of $G(O)$ above -- a field whose *only* descendants are `__typename`
meta-fields and/or abstract refinements with no resolvable field beneath (`{ union { __typename } }`,
`{ product { samePriceProduct { __typename } } }`, `{ node { ... on X { __typename } } }`) is a
resolution leaf too. The router must still resolve $f$ to produce its `__typename`, but no descendant
carries a goal, so under the old plain-leaf rule the composite was never covered and never selected by
any fetch (an assertion-6 leaf-coverage gap: the whole `__typename`-only subtree dropped, and any
`@requires`-terminal field with a `__typename`-only selection with it). The amendment is a pure
*addition* to $G(O)$ -- disjoint from the existing leaf goals (a plain leaf field already has no
children, hence no field descendant), so every prior goal is preserved and only the previously missing
typename-terminal composites are added. $\mathrm{cand}$ resolves such a goal to $f$'s own field nodes
$(T,s).f$ exactly as for a leaf field; lowering (`D11`) emits `f { __typename }` at its position (a
`__typename` obligation is response-shape bookkeeping that rides with its enclosing object into whatever
fetch resolves it). Because it only *adds* goals, `T2`/`T3` are monotone (more goals => more derivations)
and the search kernel/`SETTLE`/cost are untouched.

### D3ppp -- interface-refinement member fallback (M1.5 IR+D6 wave)

Original record, verbatim:

**D3ppp -- interface-refinement member fallback (M1.5 IR+D6 wave).** For a field obligation
$g=\langle U.f\rangle$ whose owner type $U$ is an *abstract mixin* (an interface/union the operation
selects `f` on via `... on U`, but which is never returned directly, so every object node $(U,s)$ is an
orphan in $H$), the candidate set

$$ \mathrm{cand}(g) \;=\; \{\, (U,s).f \,\} $$

can be *entirely unreachable* -- settle leaves every $(U,s).f$ at $\pi=\infty$ -- while the concrete
member the parent instance actually *is* (an entity, reachable via its `D7` jump) declares $f$. Under
`D3` alone this is a hard `ErrNoValidPlan` (the dominant residual real-customer blocker after `D5pp`,
shape *field on an orphan abstract whose members are entities*). `D3ppp` augments $\mathrm{cand}(g)$ with
the concrete members' reachable field nodes

$$ \mathrm{cand}(g)\;\mathrel{+}=\;\{\, (C,s).f \,:\, C\in\mathrm{Mem}(U)\ (\text{a }\mathsf{TypeMove}\text{ member of }U),\ (C,s).f\text{ reachable}\,\}, $$

so the goal is covered on the reachable member route. It is a *conditional* addition -- it fires *only*
when every primary $(U,s).f$ is unreachable, i.e. on a goal that currently *fails*. A
currently-passing interface case (its $(U,s).f$ reachable) keeps $\mathrm{cand}(g)$ unchanged, so the
`C.4` candidate selection and realized cost of every working plan are untouched -- the conditionality is
exactly what the prior wave flagged an *unconditional* expansion would violate. Because the trigger is a
reachability predicate over $H$ (an over-approximation of the settle route: optimistically-unreachable
$\Rightarrow$ $\pi=\infty$), this realizes at obligation-build time the "fallback when the primary
settles at $\pi=\infty$" the reachability report specified, with the search kernel/`SETTLE`/cost
*literally unchanged* -- `A` merely reads a larger $\mathrm{cand}(g)$ for the failing goal. Fires only for
a non-exempt goal (an exempt goal is a `D6` response-only null, not a routing gap). *Scope:* the fallback
covers the reachable member(s); a genuinely *distributed* abstract whose list mixes several concrete
members each in a different subgraph still needs the M2 per-member expansion at lowering (tracked
separately) -- `D3ppp` closes the single-reachable-member "the concrete parent the parent already is" case
that dominates the corpus.

### D3pp -- exempt-terminal composite goals (M1.5 IR+D6 wave)

Original record, verbatim:

**D3pp -- exempt-terminal composite goals (M1.5 IR+D6 wave).** $G(O)$ additionally counts a composite
field obligation $\langle T.f\rangle$ as a goal when its *entire* field-subtree is `D6` member-narrowed
away -- i.e. it has no field-obligation descendant that is *both* a goal and not $\mathrm{exempt}$.
This generalizes `D3p` from "no field descendant" to "no non-exempt field descendant": a composite
whose only selected members are all value-type members *outside* the intersection (e.g. `{ wrapper {
actions { __typename ... on OnlyB { b } } } }` where `OnlyB` is narrowed out, `partial-union-complex/
case-03`) has every leaf goal exempt, so under `D3p` alone no goal covers the composite -- no fetch
selects it, its ancestors, or its `__typename`, and the whole subtree drops (assertion-6 leaf-coverage
gap on the *parent* fields), even though the router must resolve the composite to produce the surviving
members' `__typename`. The amendment adds $\langle T.f\rangle$ with $\mathrm{cand}$ = $f$'s own field
nodes (typename-terminal, exactly `D3p`); lowering emits `f { __typename }`, and the narrowed members
render as response-only nulls (`D6`, `I4`). **Safety gate.** A goal is exempt-terminal *only* when its
narrowing is a genuine value-type `D6` intersection null -- every exempt descendant has a *reachable*
candidate (it is narrowed *despite* being resolvable, which the reference gateway also nulls). A goal
exempt because its candidate is *unreachable* (an interface refinement whose abstract node is orphan --
the distributed interface-refinement class, `union-interface-distributed/case-05`) is *not*
exempt-terminal: covering it as `{ __typename }` would drop the field (a false plan-level pass with
wrong data), so it is excluded and left as its true gap. This keeps the amendment a *pure addition* of
provably-correct-null composites; because it only adds goals, `T2`/`T3` stay monotone and the search
kernel/`SETTLE`/cost are untouched.

### D3pppp -- abstract-position member expansion (M2 class-C wave)

Original record, verbatim:

**D3pppp -- abstract-position member expansion (type explosion at abstract positions; M2 class-C
wave).** A field obligation $g=\langle U.f\rangle$ whose owner $U$ is abstract can be *locally
unresolvable at its position*: no subgraph that can supply the position's parent instances declares
$f$ on $U$, while the position's concrete members -- entities, each with its `D7` jump -- declare $f$ in
another subgraph (`abstract-types`: `products { reviews }`, where `products` yields the interface
`Product` whose `reviews` lives only on `Book`/`Magazine` in the reviews subgraph). Under `D3` alone
the goal's only reachable candidate is a foreign-root route (the `D10` fall-back class), because no
single walk can serve an interface position whose instances must be *fanned out per concrete member*
to move subgraphs -- the reference routers plan this by **member expansion** (Apollo's type explosion;
v1's abstract-selection rewrite). `D3pppp` performs that expansion on $O(Q)$ itself, keeping the "no AST
past $O(Q)$" isolation: when the trigger below holds, the obligation subtree rooted at
$\langle U.f\rangle$ is replaced by one abstract-refinement obligation $\langle U\triangleright
C\rangle$ per expansion member $C$, each containing a copy of the whole $\langle U.f\rangle$ subtree
with owner types rewritten at the top ($\langle C.f\rangle$; descendants keep their own owner types).
The expanded tree is then classified (`D6pp`), goal-resolved, and lowered exactly as if the client had
written the member fragments (`D11.7` member-qualified placement, `D11.8` sibling response variants --
the class-D machinery this construction deliberately reuses).

Trigger -- ALL of, evaluated per position over the `D6pp` position machinery (P = the route-scoped,
intervening-gated capable subgraphs of the parent field; $\mathrm{pos}_s$ = the position-possible
member sets):

1. $U$ is abstract in the composed schema and $f$ is not `__typename`;
2. *no* $s\in P$ has the field node $(U,s).f$ in $H$ (some capable supplier resolving $f$ locally
   keeps the standing `D6` local-resolution route -- current behavior, untouched);
3. every $\mathrm{pos}_s$ is a *known* set (no $\top$): an `@interfaceObject`-shaped position supplies
   instances whose concrete types the source subgraph cannot name, so member expansion there would
   emit representations the source cannot type -- that configuration is `D3io`/`D7p` territory, never
   expansion;
4. the expansion member set $M=\bigcup_{s\in P}\mathrm{pos}_s$ is non-empty and every $C\in M$ has a
   *reachable* field node $(C,s').f$ in $H$ (optimistic reachability, as `D3ppp`): if any member's copy
   would be unroutable the expansion is skipped whole, leaving the pre-amendment behavior rather than
   trading a plannable (if fallback-served) shape for a hard error.

Monotonicity and scope. The transform is a pure tree rewrite ahead of goal resolution: the goal SET
changes (one goal per member copy replaces each goal of the original subtree), but every prior
derivation in $H$ remains valid -- $H$, the kernel, `SETTLE`, and the cost model are untouched, so
`T2`/`T3` are unaffected (they quantify over $H$, not $O(Q)$). Response shape: the member copies are
member-gated variants of ONE client field, exactly the `D11.8` sibling-variant form (presence-union
equals the client tree; the postprocess fold assembles the runtime response), so `I4` is preserved in
the variant-union reading `D11.8` already established. Honest scope: a MIXED position -- some capable
supplier resolves $f$ locally, others do not -- keeps the local-resolution route (trigger 2) and is
NOT expanded; no corpus case exercises the mixed shape, and it is registered rather than guessed at.

### D3io -- @interfaceObject member-flattening candidates (M2 class-C wave)

Original record, verbatim:

**D3io -- @interfaceObject member-flattening candidates (M2 class-C wave).** The reverse direction of
the same class: a field obligation $g=\langle C.f\rangle$ under a refinement `... on C` where $f$
lives ONLY on an `@interfaceObject` subgraph -- the subgraph declares an interface $I$ (with $C$ among
its composed implementers) as a plain object and serves $f$ for ALL implementers without knowing the
concrete types (`simple-interface-object`: `users { ... on User { username } }`, `username` declared
only on the interface-object `NodeWithName` in subgraph b). $\mathrm{cand}(g)=\{(C,s).f\}$ contains
only orphan propagated nodes (the interface-object subgraph has no producing route to a *concrete*
$(C,s)$ -- its instances are interface-typed), so the goal is a hard `ErrNoValidPlan`. `D3io` augments
the candidate set with the interface-flattened field nodes

$$ \mathrm{cand}(g)\;\mathrel{+}=\;\{\, (I,s).f \,:\, C\in\mathrm{impl}(I)\ (\text{composed}),\ (I,s).f\in V,\ (I,s)\ \text{is an } \mathsf{EntityJump}\ \text{head},\ (I,s).f\ \text{reachable}\,\}, $$

so the goal is covered on the interface-object route -- the member selection *flattens* onto the
interface type, which is exactly the `@interfaceObject` wire contract (the subgraph resolves $f$ for
whatever implementer the instance is). Like `D3ppp` it is *conditional* -- it fires only when every
primary $(C,s).f$ is unreachable, so no working concrete route is ever displaced -- and *additive*
(more candidates => more derivations; kernel/`SETTLE`/cost untouched). The jump-head gate ("$(I,s)$ is
an `EntityJump` head") is what distinguishes a genuinely flattenable interface-object/entity-interface
node (enterable via its key) from a plain interface node another subgraph merely declares. Lowering
realizes the flattened placement per `D11.9`; the response tree keeps the member gate (`OnTypeNames`),
so the flattening is invisible to the client shape. Honest scope: the runtime member DISCRIMINATOR --
a flattened member gate needs the position's concrete `__typename` from a subgraph with member
knowledge, which the interface-object subgraph cannot supply -- is NOT synthesized as an extra goal by
this amendment; the plan carries the gate and any client-selected `__typename` rides the ordinary
mechanism, and the discriminator-jump synthesis is a registered residual (DIVERGENCES register,
class C-disc).

### Argument carry (M1.5 wave 2)

Original record, verbatim:

Argument carry (M1.5 wave 2). A field obligation $\langle T.f\rangle$ additionally records the field's
*rendered arguments* and the *operation variables* those arguments reference -- data, not AST refs, so
the "no AST past $O(Q)$" isolation holds. Because `D2` normalizes with variable extraction, every
argument value is a variable reference, and the recorded form is the argument body ready for `D11`
rendering plus the variable-name set the covering fetch must declare and forward. Arguments are carried
for lowering only: they are *invisible to the search* -- $\mathrm{cand}(g)$, the goal test, and cost are
all argument-independent, because the subgraph that resolves $f$ on $T$ is the same whatever value $f$'s
argument takes. `@skip`/`@include` need no carry: `D2` resolves them (a literal- or default-`false`
`@include`, or `true` `@skip`, deletes the selection before $O(Q)$ is built), so a conditionally-excluded
field is simply absent from $O(Q)$.

---

## D5 -- Field-traversal and descent edges

### D5p -- @external key-field carry (key-tail float, class A1)

Original record, verbatim:

**D5p -- `@external` key-field carry (key-tail float, class A1).** The "external emits no edge" rule
has one exception: an `@external` field $(T,f)$ that is a *key field* of $T$ in subgraph $s$
(i.e. $f$ appears -- at any nesting depth -- in some `@key` `SelectionSet` declared on $T$ in $s$) is
nonetheless *locally producible*, and emits the ordinary in-subgraph `Field` edge
$(T,s)\xrightarrow{f}(T,s).f$ (weight $w_s$), together with its `Descent` when $f$'s output is
composite (nested keys). Justification: when $s$ resolves any $(T,s)$ object, its resolver returns
the entity's *key* in the representation -- the key value *rides with the object node* -- even though
$s$ does not own the field's canonical definition (that is what `@external` marks). This is the
model-native form of Apollo keeping the `@external` key node alive precisely so a `KeyResolution`
edge's condition can reference it (`build_query_graph.rs:573-583`; the recursive-condition semantics
of `handle_key:1320-1345`). An `@external` field that is *not* a key field (a pure
`@requires`/`@provides` input another subgraph owns) still emits no edge. Consequence for `D7`: the
key-field tail $(T,s_1).f_i$ of an `EntityJump` out of $s_1$ is now reachable *from $s_1$* whenever
$s_1$ can produce the entity object, instead of being pinned at $\pi=\infty$; the jump can fire, and
a covering route exists for the class-A1 gaps (`@external` extension keys) that previously fell to the
`D10` foreign-root fall-back. This is a *pure addition* to $E$ (new `Field` edges on nodes that were
otherwise $\pi=\infty$): it can only create derivations, never remove one, so every `T2`/`T3`
monotonicity argument extends additively (see `PROOFS.md`, D5p note) and no existing route or cost
decreases spuriously.

### D5pp -- renamed root operation types (root-field descent, class E)

Original record, verbatim:

**D5pp -- renamed root operation types (root-field descent, class E).** Node identity uses *canonical
composed-schema* names, including the operation root type $Q_{\mathrm{op}}$ (`Query`/`Mutation`) -- so
a root capability $(Q_{\mathrm{op}},f)$ is listed under the composed name in
$\mathrm{Root}_s\cup\mathrm{Child}_s$. But the field's output type $U$ (which decides whether a
`Descent` edge exists, D5) is read from subgraph $s$'s *own upstream SDL*, and a federation subgraph
may rename its root operation types via a `schema { query: ... }` definition (e.g. its query root is
`AcmeQuery`, not `Query`). Resolving $U$ by looking the field up under the *composed* name $Q_{\mathrm{op}}$
then fails -- the SDL has no type by that name -- so the root field's `Descent` into $(U,s)$ is silently
dropped and every composite $(U,s)$ reachable *only* through that root field is orphaned at $\pi=\infty$
(the whole selection under a root-returned type becomes unplannable). D5 therefore resolves a root
field's output type under $s$'s real root operation type name: let $\rho_s(Q_{\mathrm{op}})$ be the
name $s$'s SDL binds to the same operation kind (query/mutation/subscription) as the composed root
$Q_{\mathrm{op}}$ -- read from $s$'s `schema { ... }` root-operation-type definitions, defaulting to
$Q_{\mathrm{op}}$ itself when $s$ declares none (the standard names). The field's output type is
$U=\mathrm{outputType}_s(\rho_s(Q_{\mathrm{op}}),f)$; the emitted node $(U,s)$ keeps the composed
output type name (per-datasource renaming remains a lowering concern, D1 note). Non-root capabilities
are unaffected ($\rho_s$ is identity off the root types). This corrects an omission, not a model
change: the `Descent` D5 *always* mandated exists whenever the root field's output is composite -- it
was merely not emitted when the lookup name mismatched. It is a *pure addition* to $E$ (new `Descent`
edges into nodes previously $\pi=\infty$), so every `T2`/`T3` monotonicity argument extends additively
(see `PROOFS.md`, D5pp note); no existing route or cost changes.

### Edge identity carries no arguments (A-3 adjudication, M1.5 wave 2)

Original record, verbatim:

Edge identity carries no arguments (A-3, M1.5 wave 2). A `Field` edge's identity tuple (Section Edge
identity) is (kind, label, head, tails) -- it does not include the field's client arguments. Two
selections of the same field with different arguments -- `price(currency: X)` from the client and a
`@requires(fields: "price(currency: \"USD\")")` obligation, or two client `price(currency:)` at
different response positions -- denote the *same* `Field` edge and reach the *same* node. This is the
deliberate answer to the A-3 identity question: arguments do not partition reachability or cost (the
resolving subgraph is argument-independent), so distinguishing them at edge identity would only bloat
$H$ without changing any route. Where two argument bindings of one coordinate genuinely must coexist in
one emitted document -- the `requires-with-argument-conflict` oracle, `shippingEstimate` requiring
`price(currency: "USD")` while `shippingEstimateEUR` requires `price(currency: "EUR")` -- the split is a
*lowering* concern (D11 representation injection splits the requiring fields across separate entity
fetches), not an edge-identity one; the hypergraph stays argument-blind.

### @requires argument rendering and the conflict split (M1.5 requires-args wave; DV-006/DV-007)

Original record, verbatim:

The argument-in-`@requires` RENDERING is now built (M1.5 requires-args wave; DV-006 verified at plan
level). Two changes, both argument-blind at the search layer: (1) the D7 selection tokenizer
(`parseSelection`) skips a field's argument group `(...)` whole, so `price(currency: "USD")` parses to
the bare coordinate `Product.price` -- before, `strings.Fields` split it into bogus tokens and the
requires selection was unresolvable; (2) the literal argument values, which the argument-blind `Tails`
cannot carry, are recorded on the `EntityJump` edge (`Edge.Requires`, outside the A-3 identity tuple)
and re-rendered at lowering (D11) into both the source document and the entity fetch's `Requires`
fragment (`price(currency: "USD")`). The same-coordinate CONFLICT split
(`requires-with-argument-conflict`, DV-007) is now BUILT: one entity representation cannot carry both a
USD and a EUR `price`, so -- mirroring v1's `HasArgumentConflictWith` -- lowering PARTITIONS the requiring
fields across separate `_entities` fetches. Four components, all argument-blind at the search layer:
(a) the requiring-field name rides parallel to each requires selection on the jump edge (`Edge.RequiresBy`,
outside the A-3 tuple); (b) the conflict group-split; (c) source-document aliasing so both bindings
coexist in one valid upstream document (`_planv2req_price_1: price(currency: "EUR")`); (d) a
representation value-path indirection so the aliased-binding fetch reads its value from the aliased
response key while presenting the field by its real name. See DIVERGENCES DV-006/007.

---

## D6 -- Type-move edges

### D6p -- route-scoping of P (ADVERSARIAL_REVIEW demand-3b)

Original record, verbatim:

> **D6p -- route-scoping of $P$ (reachability filter; ADVERSARIAL_REVIEW demand-3b).** $P$ ranges
> only over subgraphs that can *actually supply a parent instance* on some route -- not every subgraph
> whose SDL merely *names* $T.f$. Formally, $s\in P$ additionally requires the field-resolution node
> $(T,s).f$ to be *reachable* in $H$: there is an optimistic (condition-blind) hyperpath from a root
> to $(T,s).f$. A subgraph that declares $T.f$ but whose $(T,s)$ has no producing edge -- no root
> reaches it and it is not an entity target (no `D7` jump lands on it) -- can never originate a parent
> instance, so folding its (typically *narrower*) $\mathrm{Mem}_s(U)$ into
> $\bigcap_{s\in P}\mathrm{Mem}_s(U)$ over-narrows: it nulls a member the *only* real route resolves
> (`partial-union/case-02`: `Response` is `@shareable` in a rootless, keyless subgraph $B$ whose
> $\mathrm{Mem}_B=\{$Alpha$\}$, wrongly excluding Beta/Gamma that the single reachable producer $A$
> supplies). Optimistic reachability is an *over-approximation* of the per-operation route, so the
> filter is sound in the safe direction: it removes only subgraphs with **no** producing path at all,
> never one some route could reach, so it can only *reduce* spurious exemptions -- never introduce a
> wrong null. It is a static property of $H$ (no search/settle/cost involvement), computed once at
> classification time. This closes demand-3b: $P$ is route-scoped, not schema-scoped.

### D6pp -- position-possible member sets (M2 class-D wave)

Original record, verbatim:

> **D6pp -- position-possible member sets: dead-member exemption and distributed-member routing
> (M2 class-D wave).** The narrowing rule above tests a refinement's concrete type $C$ against the
> *name* sets $\mathrm{Mem}_s(U)$, and refuses to narrow at all when any member of $U$ is an entity.
> Both simplifications are wrong on distributed abstract types, and together they are the class-D
> model gap. `D6pp` replaces the membership test with a *position-local possibility* judgment.
>
> For the parent field $\langle T.f\rangle$ of a refinement and a capable subgraph $s\in P$ (`D6p`
> route-scoped, intervening-refinement filtered), define the **position-possible member set**
> $\mathrm{pos}_s(T.f)$ -- the concrete runtime types an instance at this position can *be* when $s$
> supplies it:
>
> - $X_s = $ the `Descent` target type of $(T,s).f$ in $H$ ($s$'s own output type for the field --
>   the child-type-mismatch case makes this genuinely per-subgraph: `Viewer.book` is `Book` in one
>   subgraph and `ViewerMedia` in another);
> - if $X_s$ is an *object type in the composed schema*: $\mathrm{pos}_s = \{X_s\}$ (a concrete
>   position can only ever be that type);
> - if $X_s$ is abstract in the composed schema and $s$ has `TypeMove` edges for $X_s$:
>   $\mathrm{pos}_s = \mathrm{Mem}_s(X_s)$;
> - otherwise $\mathrm{pos}_s = \top$ (*unknown* -- $s$ models the position without local member
>   knowledge; the `@interfaceObject` / entity-interface configurations land here, where the
>   subgraph's instances can be concrete members its own schema never names, so assuming any
>   restriction would be unsound). An intervening refinement `... on C_i` narrows
>   $\mathrm{pos}_s$ through its gate ($C_i$ concrete $\Rightarrow \{C_i\}$; $C_i$ abstract
>   $\Rightarrow \mathrm{pos}_s \cap \mathrm{impl}(C_i)$; $\top$ absorbs except under a concrete gate).
>
> A refinement type $C$ is **possible at the position in $s$** iff ($C$ concrete and
> $C\in\mathrm{pos}_s$) or ($C$ abstract and $\mathrm{impl}(C)\cap\mathrm{pos}_s\neq\emptyset$),
> where $\mathrm{impl}(C)$ is $C$'s composed-schema implementer/member set and $\top$ makes every
> $C$ possible. The abstract-$C$ clause corrects a live mis-narrowing: `... on Store` under a union
> whose members implement `Store` was narrowed out because the *name* `Store` is not a member name --
> nulling data the route resolves (`union-interface-distributed/case-05`; the federationtesting
> `histories` scenario).
>
> Three member-narrowing verdicts follow, in order:
>
> 1. **Dead member.** $C$ possible in *no* $s\in P$ (with $P\neq\emptyset$): the position can never
>    produce a $C$ on any route, so every goal under the refinement is *exempt* (`D10` cover
>    exemption; response-only absence per `D11` clause 3) -- *regardless of entity-ness*. The
>    entity gate's rationale ("an entity member is individually reachable via `D7`") does not apply:
>    a `D7` jump transports an *existing* instance between subgraphs; it cannot manufacture a parent
>    instance of a type the position cannot produce. Witnesses: `... on Oven` under `Query.nodes`
>    where only subgraph a (whose `Node` is `{Toaster}`) resolves `nodes`
>    (`union-interface-distributed/case-02/08`); `... on Movie` under `Viewer.aMedia`/`Viewer.song`
>    resolvable only in a subgraph whose member set lacks `Movie` (`union-intersection/case-04/08/11/12`).
>    A dead-member-only composite is coverable via the `D3pp` exempt-terminal promotion, whose gate
>    admits dead-member exemptions *unconditionally*: the member never occurs at the position, so a
>    `{ __typename }` cover is exact regardless of whether the member's own candidates are reachable
>    anywhere (witness: `override-type-interface/case-02`, whose expected response is `[{}, {}]`);
>    value-type intersection narrowings keep the reachable-candidate requirement exactly as `D3pp`
>    states it.
> 2. **Value-type intersection (`D6` unchanged, possibility-tested).** When no member of $U$ at the
>    position is an entity, the original intersection rule applies with "possible" replacing "named
>    member": exempt iff $C$ is impossible in *some* capable $s$.
> 3. **Distributed member.** $C$ possible in a *non-empty, proper subset* of $P$ and entity members
>    present: NOT exempt. The goal must be covered -- and its covering walk must reach $C$ *through a
>    subgraph where $C$ is possible at the position* (the member-declaring-subgraph routing
>    constraint). Realized by `D11.7` member-qualified placement -- the member's fragment is emitted
>    only into a fetch whose subgraph declares it at that position, re-entering a shared root in the
>    declaring subgraph or riding a `D7` jump out of it -- with the `D10` member-scoped $\kappa$ mask
>    specified below (realization status noted there). This is the distributed abstract-member
>    expansion the class-D register entry names.
>
> `D6pp` is a *classification* change over static properties of $H$ and the composed schema -- the
> search kernel, `SETTLE`, and the cost model are untouched (an exempt goal is skipped exactly as
> `D6` exempts always were; a distributed goal's candidate set is unchanged). Soundness direction:
> verdict 1 only *adds* exemptions for goals whose every route was already un-derivable
> path-consistently (they were served by `D10` fall-back routes emitting fragments a subgraph
> rejects -- wrong plans, live 422s at the router); verdict 2 only *removes* exemptions (strictly
> safer: a member wrongly nulled is now fetched); verdict 3 changes nothing at classification. The
> `D10` fall-back count strictly decreases (dead members stop being goals; distributed members gain
> consistent routes), which is the designed direction of the register's retirement gate.

---

## D7 -- Entity-jump B-hyperedges

### D7p -- entity-interface interface-node jump heads (M2 class-C wave)

Original record, verbatim:

**D7p -- entity-interface interface-node jump heads (M2 class-C wave).** The entity-interface variant
above emitted jumps *only* to each concrete implementer $(C,s_2)$ -- correct when the source knows the
concrete type, but unusable from a source position that only knows the interface: an
`@interfaceObject` subgraph's instances are interface-typed (it cannot name concrete members), so a
field of the interface itself resolvable only in the interface-declaring subgraph
(`simple-interface-object/case-01`: `anotherUsers { name }`, `name` on `interface NodeWithName @key`
in subgraph a, position supplied by b's interface-object) had NO usable jump and fell to the `D10`
foreign-root fall-back. The Fed 2.3 entity-interface contract makes the interface itself an
`_entities` target in its declaring subgraph (a representation typed on $I$ resolves the interface
entity), so `D7p` adds, for a key on an entity interface $I$ in target $s_2$, the interface-node head
alongside the concrete heads:

$$ \{\,(I,s_1).f_1,\dots\,\}\ \Longrightarrow\ (I,s_2) \qquad \text{in addition to each } (C,s_2),\ C\in\mathrm{impl}_{s_2}(I). $$

Pure edge addition (new `EntityJump` edges onto nodes that previously had no jump-in): every `T2`/`T3`
monotonicity argument extends additively, no existing route or cost decreases, and the concrete-head
jumps are untouched. Lowering needs no new mechanism: the jump's head type IS the interface, so the
entity fetch's entry fragment (`... on NodeWithName { ... }`) and representation
(`fragment Key on NodeWithName`) are typed on the interface -- valid in the declaring subgraph, where
the interface's implementers populate `_Entity`.

### D7pp -- requires-scoped resolution (M2 requires-chain wave)

Original record, verbatim:

**D7pp -- requires-scoped resolution (M2 requires-chain wave).** Base `D7` modelled `@requires` with
two approximations, both visible in the corpus:

- *Ride-along conditions*: every `EntityJump` into $(T,s_2)$ carried $\Phi_{\mathrm{req}}$ for *all*
  of $T$'s `@requires` fields in $s_2$, each resolved in the single source subgraph $s_1$. A target
  whose several requires draw inputs from *different* subgraphs therefore had *no jump at all*
  (`requires-requires`: `price` lives in a, `hasDiscount` in b -- no single source supplies both, so
  $(Product,c)$ was unreachable outright), and a requires selection spanning two subgraphs
  (distributed `@requires`, `requires-with-argument/02-05`, `requires-circular`) was unreachable.
- *Requires-bypass*: the requires-bearing field itself stayed an ordinary `D5` `Field` edge on the
  plain $(T,s_2)$, so *any* route reaching $(T,s_2)$ -- including a local root descent -- resolved
  $f$ *without its inputs* (the registered `interface-object-with-requires/case-05` residual, and the
  silent wrong-data shape on `requires-circular/case-02`).

`D7pp` replaces both. It is *deliberately non-monotone*: routes that resolved a requires-bearing
field from an input-less position are *removed*; every other change is additive.

1. **Scope construction (`D8` style).** For each subgraph $s_2$ and each field $f$ on entity type $T$
   carrying `@requires` $R_f$ in $s_2$, the `D5` `Field` edge for $f$ is re-rooted: its tail is the
   **Requires Scope** node $(T,s_2\!\mid\!\mathrm{req}{:}T.f)$ -- an object node whose only in-edges
   are the requires-scoped jumps of (3) -- never the plain $(T,s_2)$. The field *node* $(T,s_2).f$ is
   unchanged (`cand`/tail identity is preserved; downstream `Descent` edges still hang off it).
2. **Plain jumps carry keys only.** The `D7` `EntityJump` into the plain $(T,s_2)$ has tails = the
   key fields in $s_1$ and *no* $\Phi_{\mathrm{req}}$. Weakening a tail set is monotone-increasing
   for the non-requires capabilities: a target's plain fields are no longer held hostage by an
   unrelated field's unsatisfiable requires.
3. **Requires-scoped jumps.** Per key $k$ on $T$ in $s_2$, per requires field $f$, per source $s_1$ --
   **including $s_1 = s_2$**, the relay re-entry v1 plans as the b->a->b shape -- there is an edge
   $$ \{\text{key tails in } s_1\}\ \cup\ \Phi_{\mathrm{req}}(f)\ \Longrightarrow\ (T,s_2\!\mid\!\mathrm{req}{:}T.f), \qquad \text{kind } \mathsf{EntityJump}, $$
   with `Edge.Requires`/`RequiresBy` = $(R_f, f)$ and `KeyTails` the key subset (both outside the A-3
   identity tuple, as before).
4. **Distributed requirement tails ($\Phi_{\mathrm{req}}$ generalization).** A leaf coordinate of
   $R_f$ that $s_1$ *resolves* (non-`@external`, or an external key field per `D5p`) contributes its
   source-local tail, exactly as before. A leaf $s_1$ does *not* resolve contributes, per subgraph
   $s_r$ that does resolve it, the foreign tail $(U,s_r).f_{\mathrm{leaf}}$ -- one edge per complete
   assignment vector (deduped, deterministic order, bounded). A *composite path* coordinate of $R_f$
   contributes a tail $(U,s_x).p$ for a resolving subgraph $s_x$ when $s_1$ does not resolve the path
   itself, which forces the gathering walk through a fetch that can actually select the path; a
   source-resolvable path contributes no tail (byte-identical to base `D7` on the source-local
   configurations). Reachability of a foreign tail is the ordinary AND-relaxation over that
   subgraph's own edges and jumps -- `L6` (conditions fully static) is *preserved*: the edge set is
   still fixed at hypergraph-compile time, a nested requires chain settles by relaxation order alone,
   and a genuinely cyclic requirement still surfaces as `ErrNoValidPlan` with no cycle checker.
5. **Kernel scope note.** `D7pp` changes which nodes and edges the builder *compiles* -- SETTLE, the
   cost model `C`, and the trace machinery are untouched, and the brute-force oracle's instance class
   (arbitrary acyclic multi-tail `EntityJump` structures over synthetic nodes) already contains
   requires-dependency shapes: a jump edge whose tail is a field node headed only by another jump IS
   the scope-node configuration, so `I1`-`I3` coverage extends to the new graphs without a generator
   change. Lowering realizes the gathered inputs per `D11.10`.

### D7ppp -- distributed @key (M2 distributed-key wave)

Original record, verbatim:

**D7ppp -- distributed @key (M2 distributed-key wave).** Base `D7` (and `D7pp`(2,3)) emits a jump into
$(T,s_2)$ via key $k$ only from a source $s_1$ that carries *every* coordinate of $k$ (the `keyTails`
premise). A composite key *no single source supplies* -- `complex-entity-call`:
`ProductList @key(fields: "products{id pid category{id tag}} selected{id}")` where `pid` lives only
in link/list and `category` only in products/price -- therefore emits *no* jump at all, and the
target's fields are unreachable outright (`ErrNoValidPlan`). `D7ppp` generalizes the `D7pp`(4)
per-assignment tail machinery from requirement coordinates to the key coordinates themselves. A
**Distributed Key** is a key $k \in K_{s_2}$ with $\mathrm{res}(k)=\text{true}$ such that *no*
subgraph $s_1 \neq s_2$ satisfies the `keyTails` premise for $k$. For each such key there are edges

$$ \{\,\text{assigned coordinate tails of } k\,\}\ \Longrightarrow\ (T,s_2), \qquad \text{kind } \mathsf{EntityJump}, $$

one per complete assignment vector, under these clauses:

1. **Gate (zero drift).** The construction applies *only* to distributed keys. A key any single
   foreign source supplies keeps exactly the base `D7`/`D7pp` edges -- on a supergraph with no
   distributed key the compiled graph is byte-identical.
2. **No source; explicit path tails.** There is no meaningful source subgraph, so *every* coordinate
   of $k$ -- composite path coordinates *included* -- contributes an explicit tail in its assigned
   subgraph: a path coordinate $(U,s_x).p$ forces the gathering walk through a fetch that can
   actually select the path (contrast `keyTails`, where a source-local path contributes no tail).
   Assignment is *path-coherent*: a coordinate nested under a path assigned to $s_x$ is assigned to
   $s_x$ when $s_x$ resolves it (single choice -- the analogue of `D7pp`(4)'s source-local preference,
   and the same completeness-favoring approximation: a resolving-but-unreachable context subgraph is
   not re-assigned), and otherwise, per resolving subgraph, to a foreign subgraph. Top-level
   coordinates have no context and range over every resolving subgraph. `resolvesField` is `D7pp`(4)'s
   (non-`@external` listing, or external key field per `D5p`).
3. **Target excluded (circularity).** No coordinate of $k$ is ever assigned to $s_2$ itself: an edge
   into $(T,s_2)$ whose tail must be produced by $(T,s_2)$ can never fire (its `need` counter
   requires its own head), and admitting it would only widen the assignment product. A coordinate
   resolvable *only* in $s_2$ makes the key unsatisfiable from outside -- no edge, honest
   `ErrNoValidPlan` (`I2` preserved: no valid cover exists through that key).
4. **Assignment product is bounded and deterministic** (`maxRequiresAssignments`, subgraph
   declaration order), exactly as `D7pp`(4); truncation is a completeness-affecting resource guard of
   the same class as the Section 6.3 caps and is similarly deliberate.
5. **`Edge.KeySelection` records the raw key.** The tails of a distributed jump span subgraphs, so
   the tail-up-walk lowering uses to reconstruct a plain jump's key paths (`tailFieldPath`) is
   undefined for them. The builder records the key's raw selection set on the edge -- on *every*
   `D7`-family jump, since the pre-jump anchor path derivation of `D11.11` needs it for plain jumps
   consumed as gather producers too -- together with the `KeyDistributed` marker that gates all new
   lowering behavior (outside the A-3 identity tuple, like `KeyTails`/`Requires`). Lowering realizes
   the gathered key inputs per `D11.11` (the **Key-input Pipeline**).
6. **Requires composition.** A `@requires` field on a distributed-key target gets its `D7pp`(3)
   scoped jumps with the key part = each `D7ppp` assignment vector and the requirement part =
   `D7pp`(4) assignments *with no source* (every requirement coordinate foreign-assigned, target
   excluded), product-bounded as above.
7. **Kernel scope note.** Like `D7pp`, `D7ppp` changes only which edges the builder compiles; SETTLE,
   the cost model, and the trace machinery are untouched, and the oracle's instance class (arbitrary
   acyclic multi-tail `EntityJump` structures) already contains cross-feeding jump shapes, so
   `I1`-`I3` coverage extends without a generator change. The *chain-layered consistent trace* gains
   the tail-reachability retry tier (see the `D10` chain-layered amendment) -- a trace-input change,
   not a kernel change.

---

## C -- Cost model

### C.2 value-function default (M0 adjudication of RESEARCH.md open question 1)

Original record, verbatim (the surrounding C.2 text also described sum-cost as "the M0 default"):

M0 fixes $\oplus = \sum$ (*sum-cost*: total round-trips / resource use). The alternative $\oplus=\max$
(*max-cost*: critical-path latency under parallelism) is also admissible and the search is
parameterized over $\oplus$; both are **superior value function**s (monotone, order-preserving over
non-negative weights), which is the precondition `PROOFS.md` must discharge for Shortest B-Tree
correctness (L3). No cost term that breaks monotonicity may be introduced -- that would invalidate the
optimality proof, not merely slow the search (open question 1 of `RESEARCH.md` is resolved here in
favor of *sum-cost default, parameterized over the superior family*).

---

## D10 -- Hyperpath cover

### Honest scope 1 -- the fall-back (M1 realization)

Original record, verbatim:

> **Honest scope 1 (preference, not guarantee -- the fall-back).** The shipped M1 realization applies
> path-consistency as a *completeness-preserving preference*, NOT a hard cover requirement: a goal
> uses its masked (path-consistent) route **only when the masked graph still reaches some candidate**;
> when it does not -- the candidate is reachable *solely* through a foreign root, because the model
> lacks the edges the consistent route needs (a missing entity jump into the requested root's
> subgraph, an `@external`-key it cannot resolve, an interface-on-union member expansion it does not
> perform) -- the planner **falls back to the unmasked route and admits the path-inconsistent
> foreign-root walk** rather than firing `ErrNoValidPlan`. Consequently it is NOT true that a goal's
> walk never enters a foreign root, NOT true that every emitted fetch selects only client-requested
> root fields, and NOT true that every audit wrong-route case is closed: emitted plans can and do
> contain foreign-root fetches wherever no consistent route exists in $H$. These residuals are a
> *registered model-gap class* (tracked per case in the audit corpus as `GAP`s under the
> root-entry oracle assertion), not a soundness guarantee of this clause; they shrink as the missing
> edges are modelled (M1.5/M2), at which point the fall-back becomes dead code and the strict reading
> above is restored. The fall-back never *creates* a wrong route -- it re-admits exactly the
> pre-amendment route for goals the strict rule cannot serve -- so path-consistency strictly reduces
> the wrong-route surface, and I2 (completeness) is preserved bit-for-bit.

### D10 amendment -- TYPED-LOUD fall-back (M2 D10 disposition; landed)

Original record, verbatim:

> **D10 amendment -- TYPED-LOUD fall-back (M2 D10 disposition; landed).** Honest scope 1 stands
> unchanged as the *semantics* of the fall-back; this amendment fixes its *observability contract*
> (the fall-back previously fired silently -- the posture the owner-commissioned adversarial review
> adjudicated wrong under the L7 no-silent-degrade principle). Every firing is now a typed
> **route fallback** event on the search output: `Result.RouteFallbacks` records, per event, the goal
> (its obligation coordinate `T.f` and the root field it is anchored to), which of the two realization
> branches fired -- *root-pin*, the goal loop's pinned-table reversion in `search/search.go`, or
> *scoped-walk*, the per-field $\kappa$ masked-trace reversion in `search/scope.go` -- and the serving
> node, its subgraph, and the path-inconsistent route actually taken. Fallback events are part of the
> planner's Result contract: a plan produced via fallback is still sound (I1) and complete (I2) --
> recording changes no route selection, and emitted plans are byte-identical to the pre-amendment
> ones -- but the plan is *flagged*, never silent. The facade surfaces one Warn-level diagnostic per
> event by default (`planv2.PlanWithDiagnostics`; `Diagnostics.RouteFallbacks` is the typed record,
> `Diagnostics.Warnings` the loud rendering).
>
> *Two-tier semantics (measurement adjudication, same wave).* A fallback event is a SEARCH-LEVEL
> observation: it reports that a goal's covering walk $\kappa(g)$ is not path-consistent -- NOT that
> the emitted plan is wrong. Since the D11 obligation-driven flip, lowering places selections by
> walking $O(Q)$, so a path-inconsistent walk does not necessarily reach the wire; the audit corpus
> holds three witnesses whose search falls back yet whose emitted plans are correct (named in the
> DIVERGENCES.md D10 register entry). Plan-level correctness therefore stays governed by the
> existing plan-level assertions (fetch validity, root-entry honesty, leaf coverage /
> path correspondence, response paths), and the audit gate on fallbacks is two-tier: any plan-level
> defect keeps failing through those assertions, while a committed register test FREEZES the exact
> set of passing cases with any search-level fallback (a new firing fails loud; a witness dropping
> out is recorded deliberately by shrinking the frozen set in the same commit) and PINS the
> audit-corpus distinct-goal count, so drift is visible in both directions (the measured figures and
> the frozen set live in the DIVERGENCES.md D10 register entry). Retirement gate (the scheduled
> end-state of Honest scope 1): when the audit-corpus distinct-goal count AND the customer-corpus
> fallback count are both zero -- the A2/B/C/D model-gap classes closed -- delete the fall-back in
> that same commit and let unroutable goals fail loud (`ErrNoValidPlan`), restoring the strict
> path-consistency reading above.

### Honest scope 2 -- sibling conflation, the cover function kappa, and THE FLIP (M1.5 waves 1/1b)

Original record, verbatim (this is the full defect narrative: the adversarial-review
reclassification, the 46-case blast-radius measurement, Counterexample A, the
self-referential-friends witness, the wave-1b formalization, and the landed flip status):

> **Honest scope 2 (enforced granularity -- LIVE WRONG-DATA DEFECT, M1.5 wave 1 update; RESOLVED --
> M1.5 obligation-driven lowering flip, see Status below; the chronology beneath is history).** Where it
> applies, path-consistency is enforced only at *root-entry* granularity. It does not disambiguate
> two sibling selections that reach the same $(T,s)$ by different intermediate fields *under the same
> root* (both enter the correct root, so both are admitted onto the one shared node with one
> back-derivation). The M1 register filed this as an M2-low-risk granularity gap. **The
> owner-commissioned adversarial review (`ADVERSARIAL_REVIEW.md`) reclassified it as a live wrong-data
> defect, and the M1.5 leaf-coverage oracle (audit assertion 6) measured its true blast radius: 46
> corpus cases** that passed assertions 1-5 emit a fetch document that DROPS a requested field or
> SELECTS an un-requested one below the top level. The canonical witness is Counterexample A,
> `order { buyer { rating } seller { rating } }`: `buyer` and `seller` are the same `User` entity at
> two sibling response positions; both goals collapse onto $(\mathit{User},b).rating$ and the shared
> $(\mathit{User},a)$ object carries one back-derivation, so the root document selects only `buyer { id }`
> (seller's key never fetched) and a single `_entities` fetch cannot resolve both positions. The class
> is common (`from/to`, `author/assignee`, self-referential `friends { ... }`, or any `id` beside a
> sibling object of a repeated type), not a corner case.
>
> **Corrected cover requirement (spec-first for the fix).** Path-consistency must be strengthened from
> per-*root* to per-parent: a child goal $g$'s covering walk must factor through *its parent
> obligation's CHOSEN terminal* -- the specific object instance the parent obligation resolves to -- not
> merely reach $\mathrm{cand}(g)$ nor merely enter the correct root. Equivalently, the mask removes, per
> goal, not only foreign root-entering `Field` edges but also every sibling `Field`/`Descent` edge that
> reaches $g$'s parent type by a path other than $g$'s own parent obligation. This generalizes the
> existing per-root masking (`buildRootTables`, Section 6.1) to per-parent scoping; T2/T3.2's masked-graph
> framing extends unchanged (masking still only *removes* derivations, so soundness/monotonicity carry).
>
> **Why the search-side mask is necessary but NOT sufficient (M1.5 wave-1 finding).** The cover is an
> edge set $K$ and lowering prints fetch documents by a *structural walk* of $K$ from each object
> node. That representation **cannot express the same edge used at two distinct response positions**:
> the shared $(T,s)$ object is one node. For a repeated or self-referential shape
> (`friends { friends { ... } }`) the structural walk *does terminate* (it walks the finite edge set,
> not the response shape -- M1.5 wave-1b empirical correction to an earlier "does not terminate"
> phrasing), but it terminates *wrongly*: the deepest goal settles onto the shared node by the SHORTEST
> derivation, so the walk collapses the whole nesting. Measured witness
> (`sibling-conflation/case-self-referential-friends`): `order { buyer { friends { friends { id } } } }`
> emits `query { order { buyer { id } } }` -- the two `friends` hops vanish and the plan fetches the
> buyer's own `id` at the wrong response position. That collapse is *why* the current planner conflates
> siblings. The full fix therefore additionally requires **O(Q)-driven lowering**: drive
> fetch-group construction from the obligation tree $O(Q)$ (which has each response position distinctly,
> and is finite, so self-reference terminates *by construction* on query depth, not on graph structure),
> assign each fetch a `ResponsePath` (M1 lowering currently emits every fetch at path `""`), and
> instantiate one entity fetch per response position -- the way v1 does. The path-scoped-object-node
> alternative (splitting $(T,s)$ per entry context, `D8`-style) was considered and rejected for the same
> reason it was before: it enlarges $V$/$E$ and perturbs every cost figure, and it still would not by
> itself give lowering the per-position response paths it needs.
>
> **Cover as a FUNCTION, not a set (fix formalization, M1.5 wave-1b -- realized in lowering; see Status).**
> The corrected object the planner produces is not a single edge set $K$ but a *function*
> $\kappa : G(O) \to \mathcal{P}(E)$ mapping each goal obligation to its own *scoped* covering walk,
> with the *walk-tree-consistency* constraint that a child goal's walk *factors through* its parent
> obligation's walk: $\kappa(g)$ extends $\kappa(\mathrm{parent}(g))$ rather than being an independent
> root walk. Operationally $\kappa(g)$ is the traceback of $g$'s C.4-selected candidate over $g$'s
> *per-parent masked* settle $H \setminus \mathrm{siblingEdges}(g)$, where $\mathrm{siblingEdges}(g)$
> removes, in addition to foreign root-entering `Field` edges (the existing per-root mask), every
> `Field`/`Descent` edge that reaches $g$'s parent object type by a path *other than* $g$'s own parent
> obligation -- so two siblings of a repeated type (`buyer`/`seller`, or successive `friends` hops) each
> obtain a *distinct* derivation reaching their own parent instance. The old edge set is recovered as
> $K = \bigcup_{g} \kappa(g)$; **one edge may now appear in $\kappa(g)$ for several $g$** (the same
> `EntityJump` reused at `order.buyer` and `order.seller`), which is exactly what the set representation
> could not express.
>
> **W2 / C.3 folded accounting is UNCHANGED.** The costed object stays the **edge set under the
> function**, $K=\bigcup_g \kappa(g)$: $C(K)$ (`C.3`) is $\sum_{e\in K} w(e)$ (or its $\oplus$
> generalization), each distinct edge counted once regardless of how many goals reuse it -- W2 folding is
> the statement that the union deduplicates shared edges, and it applies to $\bigcup_g \kappa(g)$
> verbatim. So $\kappa$ changes only which derivations enter the union (adding the previously-dropped
> sibling walks), never how the union is priced; the P1 folded $\le$ tree inequality and I3's realized-
> cost clause are unaffected in form. What changes numerically is that formerly-conflated covers gain the
> sibling edges they were missing, so those specific instances' $C(K)$ rises to the honest value (still
> $\le$ v1, `BENCHMARKS`); no single-position instance moves.
>
> **Status (M1.5 wave-1b, THE FLIP -- landed).** Wave 1 shipped the *oracle* (assertion 6, demand 2)
> and the honest re-audit; wave 1b added the `case-self-referential-friends` witness and this
> formalization, then **implemented the fix and flipped it to the shipping default.** Implementation
> finding: the closing change was **entirely in lowering (the D11 obligation-driven placement below).**
> The per-goal scoped walks $\kappa(g)$ the search already produces were correct and path-consistent per
> subgraph; the collapse was purely lowering's node-keyed *attribution* discarding them. So the
> *search-side per-parent mask* $H \setminus \mathrm{siblingEdges}(g)$ was found **unnecessary in
> practice** -- kernel `settle` and every search golden are untouched -- and the fix is the lowering rewrite
> that walks $O(Q)$ and consults $\kappa(g)$ per obligation position. The formalization above stands as the
> semantic model ($\kappa$ as a function, one edge at several positions); its realization is the shipping
> obligation-driven lowering, and the general defect is closed (both `sibling-conflation` witnesses PASS;
> headline 98/135). Residual GAPs are the narrowed member-scoped subset (posGroup lacks a member-qualified
> position key), not the whole class.

### D10 amendment -- member-scoped kappa masks (M2 class-D wave; SPECIFIED, deferred)

Original record, verbatim (including the measured back-out reason):

> **D10 amendment -- member-scoped $\kappa$ masks (M2 class-D wave).** The per-goal scoped-walk mask
> (`scopeMask`) constrains entry into the *ancestor object types on the goal's obligation chain* -- but
> a goal under an abstract refinement `... on C` has a second family of inconsistent entries the chain
> types never name: `Field` edges whose `Descent` produces the *member type* $C$ (or the goal's owner
> type) *directly*, bypassing the abstract position entirely. Concretely, `Book.title` selected under
> `viewer.media`'s `... on Book` settles through the sibling field `Viewer.book` (whose subgraph-local
> output type is `Book`), so the traced walk enters the wrong field of the right parent -- unflagged,
> because `Book` is not an object type the chain's fields descend into on this goal's chain. The mask
> therefore additionally constrains, for a member-scoped goal, every *owner-candidate type* (the goal
> obligation's own type, each `Refine` ancestor's concrete type, and each `D3ppp`-expanded member
> candidate's type): a `Field` edge descending into a constrained type is masked unless its coordinate
> is on the goal's own chain -- `TypeMove` and `EntityJump` edges are never masked, so the *only*
> admitted refinement route is through the position's abstract node (or a jump out of it), which is
> the member-declaring-subgraph routing constraint of `D6pp` realized at search level. Same containment
> as every kappa mask: masking only removes derivations (`T2`/`T3` monotonicity carries; kernel/`SETTLE`/
> cost untouched; `Cover.Selected`/`Cost` unchanged since selection precedes route scoping), and the
> completeness fall-back (Honest scope 1) still re-admits the unmasked route -- typed and loud -- when
> the member has no consistent route in $H$.
>
> *Realization status (M2 class-D wave -- SPECIFIED, deferred; measured reason).* The wave landed the
> class through `D6pp` classification + `D11.7` placement alone; the mask was implemented, measured,
> and deliberately backed out for this wave. Measurement: with the mask, every audit-corpus plan is
> byte-identical except `union-intersection/case-07` (the sneak-served member splits into a deliberate
> per-member second fetch -- correct both ways, one extra fetch), and exactly one additional benign
> PASS witness fires the typed fall-back (`circular-reference-interface/case-02`: a pre-existing
> path-inconsistent @provides-scope route that pre-mask served the goal *silently*; plans byte-identical
> with and without) -- which would GROW the frozen register set, the direction the class-D wave's gate
> forbids. Interim honesty: a member-scoped goal whose settled walk is a sneak route is *not recorded*
> by `D11.7` (walk/obligation label mismatch) and inherits its parent position's group -- correct
> whenever the member is possible there, which `D6pp` guarantees for every non-distributed member; a
> DISTRIBUTED member served by a sneak route into a subgraph where it is impossible remains a
> theoretical hole no corpus or executed case currently exercises (registered residual, owner: M2
> follow-up together with the register-growth adjudication).

### D10 amendment -- CHAIN-LAYERED consistent trace (M2 class-C wave; landed) + tail-reachability retry tier (M2 distributed-key wave)

Original record, verbatim:

> **D10 amendment -- CHAIN-LAYERED consistent trace (M2 class-C wave; landed).** The per-goal scoped
> walk $\kappa(g)$ is a traceback over a masked settle's back-edge table -- ONE derivation per node,
> object nodes keyed by $(T,s)$ only. When a goal's obligation chain *revisits* a type it already
> passed (a `D3pppp`-expanded member position `products.~Book.reviews.product` whose `product` is the
> same abstract type the chain started on), the cheapest derivation of the shared node serves the
> SHALLOW position and the traced walk collapses: its `Field`-edge labels no longer spell the chain,
> lowering cannot attribute the goal's positions, and the subtree falls through to a parent group in
> the wrong subgraph (the nested class-C signature; before this amendment those routes were served
> silently-phantom or by the typed-loud fall-back). The amendment: when -- and only when -- a goal's
> default $\kappa(g)$ is not *chain-consistent* (its spine's `Field` labels do not spell the goal's
> obligation-chain fields exactly), re-trace it over the **chain-layered product** of the goal's
> masked graph with its obligation chain: states $(v, i)$ where $i$ counts chain fields consumed
> root->leaf. A `Field` edge advances $i$ iff its label is the chain's next field (off-chain `Field`
> edges are never spine steps -- they enter walks only as jump-tail sub-walks); `Descent`/`TypeMove`
> keep $i$; an `EntityJump` keeps $i$, steps from a pre-jump object of the edge (the enclosing object
> of a key tail) to its head, and is usable iff every tail is reachable on the goal's masked table
> (tail sub-walks are traced from that table, exactly as the default trace). On the layered graph the
> depth aliasing disappears -- the same node at two chain depths is two states -- so the minimum-cost
> chain-consistent walk to a candidate is found *exactly* when one exists; the `C.4`-minimal
> candidate reachable at layer $D$ is selected (the consistent route may end on a *different*,
> position-local candidate than the unlayered `C.4` choice -- the selection is repaired to the node
> the walk actually serves). The search records the ordered root->leaf SPINE alongside the walk
> (`Cover.Spines`): a walk is an edge set keyed by head node and cannot express which jump enters at
> which obligation depth once nodes repeat; the spine preserves that order, and lowering derives the
> jump chain, entry depths, and pre-jump source types from it directly (recording during search what
> lowering needs -- never re-deriving routing downstream). Containment: the refinement is
> *conditional* -- a goal whose default walk is chain-consistent is untouched, so every previously
> consistent plan is byte-identical; a goal with no chain-consistent route keeps the default behavior
> (typed-loud fall-back / phantom fall-through) unchanged; the kernel, `SETTLE`, the settle tables,
> and `Cover.Edges`/`Cost` accounting are untouched (the goal loop's costed object stands; a repaired
> goal's stale route edges may remain in the union -- an over-approximation the costed-object doc
> already permits, and the honest direction). The `D10` fall-back register shrinks accordingly (a
> repaired goal is genuinely path-consistent, not fallback-served -- its records are dropped).
>
> *Tail-reachability retry tier (M2 distributed-key wave, realizes `D7ppp`).* The layered trace judges
> a jump usable iff every tail is reachable on the goal's MASKED settle table. A `D7ppp` distributed
> jump's tails are gathering inputs that legitimately live on *sibling* coordinates of the goal's own
> chain -- `first.id`'s consistent route needs the `(ProductList,price)` jump whose key tails sit
> under the sibling field `products`, and the goal's kappa mask (correctly, for spine purposes) removes
> the sibling `Field` edges into the chain's object types, making those tails unreachable and the
> jump unusable. The retry tier: when -- and only when -- the masked-table layered trace finds no
> chain-consistent route, it is re-run with the goal's *unmasked* settle table for jump-tail
> reachability and tail sub-walk tracing, the mask still constraining the SPINE edges (the layered
> product itself already forces the spine to spell the chain, which is the property the mask
> approximates). Containment: a goal repaired on the masked table is byte-identical; the retry fires
> only where the goal would otherwise be fallback-served or phantom, so it can only convert a
> fall-back into a chain-consistent route, never disturb one.

### D10 amendment -- PROVABLE-NON-RESOLVABILITY narrowing (M2 D10-narrow mini-wave; landed)

Original record, verbatim:

> **D10 amendment -- PROVABLE-NON-RESOLVABILITY narrowing (M2 D10-narrow mini-wave; landed).**
> Honest scope 1's fall-back is a COMPLETENESS device: it re-admits a foreign-root route only where
> the consistent route is missing from the MODEL -- the class-A/B gap premise that a better model
> would serve the goal. That premise is refutable from the schema itself for one shape: a goal whose
> every candidate sits behind `@key(resolvable: false)`. This amendment NARROWS the fall-back -- it
> must not fire for a *provably non-resolvable* goal, defined conservatively as: every candidate
> serving node $v \in \mathrm{cand}(g)$ is a plain (unscoped) field node $(T,s).f$ such that
>
> 1. *Schema-level jump prohibition.* At least one `@key` declaration heads $T$ in $s$ (per the `D7`
>    jump-head mapping, so entity-interface and `@interfaceObject` heads carry the same verdict as
>    the jumps they would head), and EVERY declaration heading $T$ in $s$ carries
>    `resolvable: false` -- the subgraph declares it cannot resolve entity references for $T$, so no
>    entity jump into $(T,s)$ may ever be modelled, by this or any future model amendment. This is
>    what separates PROVEN non-resolvability from the class-A/B missing-jump gaps (a resolvable key
>    the model failed to use, or no key declared at all -- both keep the fall-back).
> 2. *Structural root-only entry.* In $H$, every incoming `Field` edge of $v$ enters from the plain
>    object node $(T,s)$, and every incoming edge of $(T,s)$ is a `Descent` edge from a field node
>    whose every derivation enters through an operation-root node -- $(T,s)$ is reachable exclusively
>    through $s$'s OWN root-operation fields (no `TypeMove`, no `EntityJump`, no descent from a
>    non-root field). So every rescue route the fall-back could take is a foreign-root entry into a
>    subgraph the goal's own root can never consistently reach (per 1, not even via a future jump);
>    a candidate additionally reachable through non-root structure (a parent descent an eventual
>    jump could feed, an abstract `TypeMove`) is NOT proven and keeps the fall-back.
>
> When 1  AND  2 hold and either fall-back branch would fire (root-pin, `search/search.go`;
> scoped-walk, `search/scope.go` -- one shared guard), the fall-back is SUPPRESSED and planning fails
> loud with `ErrNoValidPlan` (reason `provably non-resolvable`) -- the honest outcome; the reference
> gateway cannot fetch the field either (witness: `non-resolvable-interface-object/case-02`, an
> expected-errors audit case the root-pin fall-back previously rescued into a wrong plan that
> selected a root field of a foreign subgraph). CONSERVATISM (the customer-safety direction):
> suppression requires PROOF over ALL candidates -- any candidate that is scoped, non-field, keyless,
> headed by ANY resolvable key, or reachable through any non-root structure leaves the fall-back
> untouched, byte-identical; when in doubt, the fall-back stays. Goals whose routes are consistent
> never reach the guard (it runs only at the two firing sites). This is a NARROWING of Honest
> scope 1, not its retirement: the retirement gate (audit-corpus distinct-goal count AND
> customer-corpus fallback count both zero) stands unchanged -- the audit-corpus register drops to
> the two frozen benign witnesses after this amendment, and the measured customer-corpus reliance
> figures the retirement stays gated on live in the DIVERGENCES.md D10 register entry.

---

## D11 -- Lowering to a fetch tree

### Original clauses 1-4 (M0 cover-walk-driven grouping -- superseded by the obligation-driven flip)

Original record, verbatim:

**Lowering** maps a cover $K$ to the existing output contract: a **fetch tree** (`resolve` fetch tree
via the flat `RawFetches` + postprocess layering, L14b) plus the response-shape tree.

1. **Grouping.** Maximal connected sub-walks of $K$ that stay in one subgraph between two
   `EntityJump`/root edges become one *fetch* against that subgraph. Each `EntityJump` boundary starts
   a child fetch keyed by the jump's tail key fields (the representation/input template).
2. **Ordering.** Fetch $u$ precedes fetch $v$ iff an edge in $v$'s group has a tail resolved by $u$'s
   group; independent fetches are siblings (parallelizable). This is the **physical plan** shaping,
   kept separate from search (L12).
3. **Response shape.** The response tree is the client selection tree of `D2` *exactly* (I4);
   response keys, aliases, and `__typename` gates (`OnTypeNames`) are preserved. An obligation in
   $O(Q)$ with no covering walk in $K$ (e.g. a value-type exclusive member narrowed out by `D6`) is
   emitted as a *response-only null*: the selection stays in the response shape, gated on its
   concrete `__typename`, with no fetch producing it.
4. **Cross-subgraph output-type aliasing (definition, not afterthought).** If two edges in $K$ resolve
   the *same response key* under *different concrete types* from *different subgraphs* such that their
   subgraph field selections would collide in one fetch document, lowering assigns each a distinct
   internal alias in the subgraph query and maps both back to the one client response key. Formally:
   for response key $\rho$ with covering edges $e,e'$ where $\mathrm{subgraph}(e)\neq
   \mathrm{subgraph}(e')$ or the concrete parent types differ, emit aliases $\alpha(e),\alpha(e')$
   with $\alpha$ injective within a fetch document, and record $\alpha(e)\mapsto\rho$,
   $\alpha(e')\mapsto\rho$ in the response mapping. This is the mechanism behind the I4 aliasing lemma
   (it generalizes the current planner's ad-hoc `@requires`-alias machinery to abstract-type member
   disambiguation, which the postmortem found *absent*).

### D11 amendment -- OBLIGATION-DRIVEN placement (M1.5 wave-1b; THE FLIP -- landed)

Original record, verbatim (including the commit trail and the 98/135 headline):

> **D11 amendment -- OBLIGATION-DRIVEN placement (M1.5 wave-1b; THE FLIP -- landed as the shipping default).**
> Clauses 1-2 above describe *cover-walk-driven* grouping: partition the edge set $K$ by structural walk
> from each object node. As D10 Honest Scope 2 establishes, that representation cannot place the same
> edge at two response positions, which is the sibling-conflation defect. The fix inverts the driver:
> lowering walks the **obligation tree $O(Q)$** -- the client's response shape, which carries each
> position distinctly and is finite (bounded by query depth) -- and, at each obligation, consults the
> cover *function* $\kappa$ (D10) to place selections:
>
> - **Grouping (1p).** Fetch groups are keyed by *obligation position*, not by cover edge. Walking
>   $O(Q)$, each obligation contributes its scoped walk $\kappa(g)$ into the group of its enclosing
>   subgraph boundary; an `EntityJump` reached from an obligation opens a child fetch *for that
>   obligation position*, so one hypergraph edge reused at two positions yields two fetches
>   (`order.buyer`, `order.seller`) rather than one. Termination is by construction on the finite $O(Q)$,
>   even for self-referential types.
> - **Response paths (3p, closing a pre-existing gap).** Each fetch is emitted at the `ResponsePath` of
>   the obligation position it serves (M1 lowering emits every fetch at `""`). An entity fetch serving
>   response position $p$ (e.g. `order.seller`) carries `ResponsePath = p` and a representation drawn
>   from the entity instance *at that position*, mirroring how v1 sets `FetchItem.ResponsePath` /
>   representation input paths in `graphql_datasource`. Plural positions of one entity => plural fetches
>   (or one fetch with multiple representation paths, matching what postprocess/resolve support).
> - **Shape (unchanged in intent, stronger by construction).** Clause 3's "response tree is $O(Q)$
>   exactly" becomes *by construction* rather than by a separate structural reconstruction, because
>   lowering now walks $O(Q)$ itself; response-only nulls (D6) and `__typename` gates are emitted at the
>   obligation node exactly as clause 3 requires.
>
> Clause 4 (aliasing) and the W2/C.3 costing are unchanged (the priced object stays $\bigcup_g\kappa(g)$,
> D10). Status (M1.5 wave-1b, THE FLIP -- landed): this obligation-driven placement **IS the shipping
> lowering semantics.** `lower.Lower` (the zero-value `LowerConfig`) routes here; grouping is keyed by
> obligation position, entity fetches are instantiated per response position, and `ResponsePath` is set
> from the obligation position each fetch serves. Aliasing (clause 4) is re-derived on this path over the
> actual document composition (D11.4 on `Edge.OutputType`). The legacy node-keyed path survives one
> release cycle behind `lower.LowerConfig{LegacyNodeKeyedGrouping}` as an emergency escape hatch and is
> exercised only by its byte-identical guard test; it carries the sibling / path-conflation defect this
> path fixes and is scheduled for removal. Witnesses: the `buyer/seller` two-sibling case (two entity
> fetches at `order.buyer` / `order.seller`), the self-referential `friends` case (O(Q)-bounded printer,
> full nesting, terminates), the multi-jump/@requires/@interfaceObject/distributed-root witnesses, and
> the Section 7.1/Section 7.2 fixtures. The flip reconciled the conflation markers and re-audited: the plan-level
> headline rose to **98/135** (SCOREBOARD.md). Commit trail: `b6f0f2b4` (multi-jump attribution),
> `0c57fcb4` (interface-object key typing), `0483d31f` (distributed-root re-entry), then the flip +
> marker reconciliation. The residual GAPs are genuine model/feature gaps (member-scoped leaf-coverage;
> foreign-root/missing-jump routing), no longer the whole-class collapse -- see the wave-1b flip report.

### D11 amendment -- ARGUMENT + VARIABLE rendering (M1.5 wave 2 -- landed)

Original record, verbatim:

> **D11 amendment -- ARGUMENT + VARIABLE rendering (M1.5 wave 2 -- landed).** The obligation-driven
> printer renders each selection's arguments (D3 argument carry) into the fetch document and wires the
> variable-forwarding contract:
>
> - **Argument rendering (5).** When a field obligation carries arguments, the printer emits
>   `field(args)` in the subgraph document, verbatim from the D3-recorded body. A shared root field whose
>   children split across subgraphs (a `@shareable` root re-materialized into a sibling subgraph's root
>   document) prints its arguments in *every* document it appears in, so no required argument is silently
>   dropped.
> - **Per-document variable set (6).** A fetch declares, in its operation header
>   (`query($v: T, ...)` / `mutation($v: T, ...)`), exactly the variables its own selections reference -- no
>   more -- with each variable's type read from the operation's variable definitions. Values are forwarded
>   by a `ContextVariable` at the variable's name (the v1 `graphql_datasource` contract), rendered as a
>   `$$N$$` Input segment: for a root fetch the segments start at index 0; for an entity fetch index 0 is
>   the representations object and client variables follow at 1.... This holds the L14b postprocess Input
>   contract (resolve_input_templates).
> - **Operation type (7).** A mutation operation lowers its root fetch with the `mutation` keyword;
>   entity (`_entities`) fetches stay `query` regardless (entity resolution is always a query).
> - **JSON string escaping (8, ledgered Task-8/10 carry-forward).** Fetch documents embed into the JSON
>   Input at `"query": ...`. Because D2 extracts literals into variables, a normalized document's argument
>   values are variable references and carry no embedded quotes -- so the common case embeds verbatim,
>   byte-identical to the pre-wave-2 Input. A document that *does* carry a string/enum literal (a future
>   `@requires` literal argument, or a non-extracted literal) is JSON-encoded so its quotes/backslashes
>   escape correctly. The escaping obligation is discharged at Input assembly, where documents become JSON.
>
> Search invariants are untouched: arguments are lowering-only (D3), the hypergraph is argument-blind
> (D5 A-3 note). Argument-in-`@requires` RENDERING has since landed (requires-args wave, DV-006 verified):
> the D7 tokenizer is argument-aware (skips a field's `(...)` whole), the literal requires argument values
> ride on `Edge.Requires` (outside the A-3 tuple), and lowering re-renders them into the source document
> and the `Requires` fragment (`price(currency: "USD")`). The same-coordinate CONFLICT split
> (`requires-with-argument-conflict`, DV-007) is now BUILT: lowering PARTITIONS the conflicting requiring
> fields across separate `_entities` fetches (v1 `HasArgumentConflictWith`) -- per-field requires
> association (`Edge.RequiresBy`), source-document aliasing, and a representation value-path indirection
> (`repNode.readAs`); MERGE's `argsConflict` enforces the same rule on the legacy path. Still separate
> (NOT argument work): distributed `@requires`
> whose selection spans two subgraphs (`requires-with-argument` cases 02-05), a multi-jump-requires model
> gap. Status: the argument-SKIP bucket plans; see SCOREBOARD.md and the M1.5 requires-args report.

### D11.5 -- TRANSPORT attach (M1.5 transport lowering -- landed)

Original record, verbatim (including the executed-truth 0 -> 18/27 measurement):

> **D11.5 -- TRANSPORT attach (M1.5 transport lowering -- landed).** Lowering produces the *shape* of the
> fetch tree (documents, representations, response object, dependency order); D11.5 makes it
> *executable*. For each fetch (root and entity) lower attaches the two facts the resolve loader needs
> and that v1 obtains inside the datasource planner's `ConfigureFetch`: the `FetchConfiguration.DataSource`
> (the HTTP `resolve.DataSource` that performs the round-trip) and the `Input` wire fields
> `url`/`method`/`header`. These come from a per-subgraph transport table the facade derives from
> `plan.Configuration` at `NewPlanner` (`plan.Configuration` is otherwise consumed by `hypergraph.Build`
> and not retained): the `DataSource` via `graphql_datasource.NewSource`, the URL/method/header via the
> exported `graphql_datasource.Configuration.FetchConfiguration()` accessor, keyed by subgraph name
> (== `DataSourceIdentifier`). The wire fields are spliced as leading top-level keys of the `Input`
> envelope by string composition -- the entity envelope's `$$0$$` representations template is not valid
> JSON, so a JSON setter cannot round-trip it; the body (representations included) stays byte-identical,
> holding the L14b postprocess `resolve_input_templates` contract. The table is OPTIONAL: a nil table
> (`lower.Lower`) attaches nothing and is byte-identical to pre-transport output, so plan-level parity is
> untouched; `lower.LowerExecutable` (planv2.Plan) attaches it. Not modelled by search (transport is
> lowering-only, like arguments). Executed-truth: this is DEFECT 2 in the router report, resolved --
> planv2 plans now execute end-to-end (0 -> 18/27 on `TestFederationIntegrationTest`); the remaining
> executed gaps are a separate lowering class (single-vs-BATCH entity fetch under array positions). See
> SCOREBOARD.md and the M1.5 transport report.

### D11.6 -- BATCH entity fetch under array positions (M1.5 batch wave -- landed)

Original record, verbatim:

> **D11.6 -- BATCH entity fetch under array positions (M1.5 batch wave -- landed).** An `EntityJump`
> whose landing objects are *array items* must lower to a *batch* entity fetch, not a single one.
> This is the v1 `requiresEntityBatchFetch` semantics (`graphql_datasource`,
> `PlannerPathType != PlannerPathObject`): when the entity-fetch attachment path crosses a **list
> boundary** -- the parent that carries the entity keys is itself a list item, or nested anywhere under
> a list -- the loader must gather *one representation per array item* and send them as a single
> `_entities(representations: [$a, $b, ...])` call, then scatter each result back to its item. A single
> fetch (one representation) resolves only the *first* array element, dropping every sibling item's
> data -- the executed-truth defect class the transport report isolated.
>
> - **Batch condition (1).** An entity fetch (`jumpEdge != NoEdge`) is a BATCH fetch iff *any* hop in
>   its response-attachment path is list-typed (`attachHop.array`), including the *top-level* field
>   (a root-type list, e.g. `Query.topProducts: [Product]`, which the prior attach-path derivation
>   suppressed by forcing the root hop's enclosing type to `""` for the `__typename`-gate -- the list
>   detection is now computed from the *actual* enclosing type while the gate stays suppressed). A
>   fetch whose whole attachment path is object-typed stays a single entity fetch. This mirrors v1
>   exactly: object parent => `EntityFetch`, list-crossing parent => `BatchEntityFetch`.
> - **Emission (2).** A batch fetch sets `FetchConfiguration.RequiresEntityBatchFetch` (and
>   `SetTemplateOutputToNullOnVariableNull`, so a null sibling item renders a null representation the
>   batcher skips) instead of `RequiresEntityFetch`; postprocess `createConcreteSingleFetchTypes`
>   then builds a `resolve.BatchEntityFetch` (Header / per-item ResolvableObject / Footer split at the
>   `$$0$$` representations segment, `SkipNullItems`/`SkipEmptyObjectItems`/`SkipErrItems`) rather than
>   an `EntityFetch`. The fetch document, representation fragments, and dependency order are otherwise
>   identical -- batching is a per-item *input assembly* + *result scatter* concern, not a shape change
>   to the query.
> - **Array marker (3).** The attachment path emits an `@` array marker (dotted `ResponsePath`) and an
>   `Array`-kind `FetchPath` element at each list hop (v1 `pushResponsePath`), so the loader iterates
>   the array and merges each `_entities` result into the correct item. A trailing `@` is dropped from
>   the dotted string (validated by field sequence alone), matching v1's `responsePath()`.
> - **`__typename` on concrete key parents (4).** The parent selection that produces the array items
>   must select `__typename` so each item yields a typed representation; the prior injection added it
>   only when the entry position was *abstract*. A concrete entity-key parent (the common list-of-
>   entities shape) now also gets `__typename` injected, alongside its `@key` fields.
>
> Search invariants are untouched: batching is lowering-only (D11), the hypergraph is list-blind. The
> response-path oracle (audit assertion 7) now also validates BATCH fetches (not only single entity
> fetches), so a mis-attached array-landing fetch is caught at plan level. Executed-truth: this closes
> the array-position entity-fetch class the transport report isolated (the 9 remaining executed
> failures). See SCOREBOARD.md and the M1.5 batch report.

### D11.7 -- MEMBER-QUALIFIED position keys (M2 class-D wave)

Original record, verbatim (including the three class-D defect narratives):

> **D11.7 -- MEMBER-QUALIFIED position keys (M2 class-D wave).** The obligation-driven grouping (1p)
> keyed fetch groups and position attributions by the *response path* alone -- the dotted client
> response keys. But `Refine` obligations contribute *no* response segment, so every concrete member
> of one abstract position shares one key: the `wallet` under `... on Purchase` and the `product`
> under `... on Sale` are both attributed at `me.history....`. Three defects follow, all class D: a
> member leaf could not be attributed to its own resolving group without stealing the shared position
> from sibling members (so member leaves *inherited* the parent's group, emitting a member fragment
> into a subgraph document that may not declare the member -- the live-422 wire signature); a re-rooted
> ancestor chain was materialized without its member fragment wrappers (`me { history { product ... } }`
> with `product` selected *directly on the union type*); and key injection navigated the parent
> document by response segments only, landing representation keys outside their `... on Member` scope.
> `D11.7` splits the two path vocabularies:
>
> - The **member-qualified position key** interleaves each `Refine` gate crossed on the obligation
>   chain into the position key (a marker segment per gate, e.g. `me.history.~Sale.product`). Fetch
>   groups (`obGroupKey.entryPath`), the position->group attribution (`posGroup`), and the ancestry
>   materialization all use member-qualified keys, so distinct members of one position are distinct
>   positions for grouping -- a member leaf whose scoped walk `kappa(g)` is path-consistent is recorded to
>   its own resolving group exactly like any other leaf (the blanket member-scoped recording exclusion
>   is retired). A member leaf whose walk fell back (`D10`) is still not recorded -- a fall-back route
>   does not exist under the client's path -- and inherits its parent's group as before.
> - **Fragment materialization.** Emission through a `Refine` extends the current position key and the
>   ancestry with the gate; materializing an ancestor chain in a sibling root group reproduces each
>   gate as an inline fragment (`viewer { media { __typename ... on Movie { title } } }`), selecting
>   `__typename` at the abstract position it refines. Key injection navigates marker segments through
>   the member fragment (`history { __typename ... on Sale { product { __typename upc } } }`), typing
>   the descent by the gate's concrete type.
> - **Wire paths are unchanged.** `ResponsePath`/`FetchPath`/attach hops remain derived from the
>   response-key segments only -- member markers are a grouping vocabulary, never a wire path (the
>   response has no member segments; an entity fetch at a member position is gated by its typed
>   representation, exactly as before). Two member jumps at one response position yield two fetches at
>   the same `ResponsePath` gated by different representation types, which resolve/postprocess already
>   support.
> - **Discriminator placement (weak `__typename`).** The `Refine` emission marks the abstract
>   position's `__typename` *weakly*: it prints only if the document actually carries content at that
>   position (member fragments, fields, or injected keys). Under `D11.7` a position's members can *all*
>   re-root into other groups, and the strong mark left an invalid residue (`me { history { __typename } }`
>   against a subgraph that does not declare `history`); each group that materializes the position
>   selects the discriminator strongly itself. A covered typename-terminal composite goal (`D3p`/`D3pp`)
>   always prints `{ __typename }` -- previously it relied on a client-selected `__typename` sibling and
>   emitted an invalid empty selection (or dropped the fetch entirely) without one.
>
> Search is untouched (this consumes `kappa` and `O(Q)`; it changes *placement*, not routes or cost).
> Output is byte-identical wherever no member-scoped leaf resolves outside its parent position's group
> -- the delta set is exactly the distributed-member class plus the invalid residues above.

### D11.8 -- same-key member variants stay SIBLING fields (M2 class-D wave)

Original record, verbatim:

> **D11.8 -- same-key member variants stay SIBLING fields (v1-parity merge deferral; M2 class-D
> wave).** Clause 3 preserves `__typename` gates on the response tree. A response key selected both
> ungated (interface-level) and under member refinements (`someObject { a }` plus
> `... on SomeType1 { someObject { b } }`) previously had its gated variants *merged into* the ungated
> field at lowering, propagating the member gate onto the folded children at the child's own depth --
> where the resolver evaluates a gate against the *enclosing object's* runtime `__typename`
> (`SomeObject`, not the discriminating parent `SomeType1`), so every folded member field silently
> dropped (the executed-truth `Abstract_object` family: `{a}` where v1 returns `{a,b,c}`). The merge
> is retired: lowering emits the variants as *sibling fields of one response key with their own gates*
> -- exactly v1's visitor output -- and the existing postprocess `merge_fields` stage (which both
> engines always run) performs the depth-correct fold: it propagates gates onto children as
> `ParentOnTypeNames` records carrying the *depth* of the discriminating ancestor, then merges
> ungated-over-gated. Lowering owns faithful shape; postprocess owns merge -- the same division v1
> ships. Plan-level oracles that index response fields by key treat same-key siblings as one
> presence-union (they are one client field's variants), which the audit runner's shape oracle
> implements.

### D11.9 -- flattened member placement (M2 class-C wave)

Original record, verbatim:

> **D11.9 -- flattened member placement (@interfaceObject; M2 class-C wave).** A member-refined goal
> covered via a `D3io` interface-flattened candidate -- its serving node is $(I,s).f$, typed on the
> INTERFACE, not on the member $C$ the client's fragment names -- must not print inside
> `... on C { ... }`: the interface-object subgraph does not declare $C$, so the member fragment is an
> invalid document there (the live signature of this class). `D11.9` places such a field at the
> *interface level* of the document that resolves it, while the RESPONSE tree keeps the member gate
> (`OnTypeNames: [C]`) -- flattening is a fetch-document concern, invisible to the client shape:
>
> - **Jump-group placement (free case).** When the flattened goal re-roots into an entity-fetch group
>   (the jump lands on the interface-object node, `D7` interface-object variant / `D7p`), the existing
>   `D11.7` re-rooting already emits it flat at the entity entry (`_entities { ... on NodeWithName {
>   username } }`) -- a jump group never reproduces member wrappers at its entry. No new mechanism.
> - **Same-document placement.** When the flattened goal resolves in the SAME group as its parent
>   position (the root fetch already sits on the interface-object subgraph:
>   `anotherUsers { ... on User { username } }` planned against b), the member fragment emission is
>   suppressed for exactly the goals whose covering node type equals the refined position's abstract
>   type: the field prints as a direct selection of the interface-typed position
>   (`anotherUsers { username }`). Goals under the same refinement covered by CONCRETE nodes keep
>   their fragment (mixed refinements split: `age` stays under `... on User` in the concrete-knowing
>   fetch, `username` prints flat in the interface-object fetch).
> - **Position keys are unchanged.** The member-qualified position key (`D11.7`) still carries the
>   `~C` gate for grouping/attribution -- flattening changes where the field PRINTS, never which group
>   resolves it or the wire paths.
>
> The discriminator caveat of `D3io` applies: the flattened document's own `__typename` at the
> position (when selected) is the interface-object's type name, not the concrete member -- the
> member-knowing discriminator jump is the registered class C-disc residual.

### D11.10 -- requires-input pipeline (M2 requires-chain wave)

Original record, verbatim:

> **D11.10 -- requires-input pipeline (M2 requires-chain wave; realizes `D7pp`).** Base D11 rendered a
> jump's whole `@requires` selection into the jump's PARENT (source) document -- correct only when the
> source subgraph resolves every required coordinate locally, and silently wrong otherwise (the
> selection either fails document validation or resolves without *its* inputs -- the wire-level
> bypass). Under `D7pp` the search output already contains the truth: every requires tail of a used
> jump is settled through the edges that actually produce it, and `Traceback` folds those tail routes
> into the goal's walk. `D11.10` materializes them as the **Requires-input Pipeline**:
>
> - **Branch groups.** Every `EntityJump` edge in a goal's walk that is NOT on the goal's spine (it
>   feeds a requires tail of a consuming jump) gets its own fetch group. Its entry position is the
>   consuming jump's entry position extended by the requires path segments between the consumed
>   entity and the branch's pre-jump object (read off the walk's `Field` edges); its dependencies are
>   the groups producing its own tails; recursion handles a nested chain (a required field that is
>   itself `@requires`-dependent) with no new mechanism.
> - **Placement follows production.** Each coordinate of a group's `@requires` selection is rendered
>   (with its literal arguments, per the DV-006 contract) into the document of the group that the
>   walk says PRODUCES it -- the parent group when the source resolves it locally (byte-identical to
>   base D11 there), a branch group otherwise. The consuming group depends on every producing group.
> - **Representation unchanged.** The entity fetch's `Requires` fragment still renders the whole
>   selection (`mergeRequiresIntoTrie`); at runtime the representation reads the *merged response
>   object* at the position, which the pipeline's fetches populated -- v1's relay semantics
>   (b->a->b: root ids, gather hop, `_entities` back with the gathered representation).
> - **Scoped/unscoped twins.** A requires-scoped jump and a plain jump into the same subgraph at the
>   same position are distinct groups by construction (distinct opening edges). Merging them into one
>   fetch when the requires inputs are available before the plain group runs is a lowering-level
>   co-location OPTIMIZATION (v1 merges them); deferring it costs an extra valid fetch, never
>   correctness. Registered as a residual where not yet realized.

### D11.11 -- key-input pipeline (M2 distributed-key wave)

Original record, verbatim (the "reverted prototype" it references is the first D7ppp lowering
attempt, backed out before this record was written):

> **D11.11 -- key-input pipeline (M2 distributed-key wave; realizes `D7ppp`).** Base D11 injects a jump
> group's @key selection into its ONE source parent's document (`injectKeys`, paths recovered by the
> tail up-walk) -- undefined for a `D7ppp` distributed jump, whose key tails span subgraphs and whose
> tail up-walk crosses other jumps. `D11.11` places the key selection the way `D11.10` places
> requirement coordinates -- each coordinate rendered into the document of the group that PRODUCES it --
> with three key-specific rules:
>
> - **Structure from `Edge.KeySelection`, not the tail walk.** The raw key selection is walked
>   top-down; each coordinate instance is matched to its assignment tail by (type, field), preferring
>   the enclosing path's assigned subgraph (the `D7ppp`(2) path-coherence realized at placement -- a
>   repeated coordinate under two paths reads its own path's tail). A coordinate whose tail lives in
>   the *inheriting host's own subgraph* stays in that host (the source-local analogue); only a
>   genuinely foreign tail resolves a producer group (branch groups minted as in `D11.10`, with
>   dependency edges). The consuming fetch's `Key` fragment renders from `KeySelection` directly.
> - **Pre-jump anchor positions.** A producing jump consumed at a position ABOVE its own entry (the
>   `products{id pid}`-keyed `ProductList` jump whose pre-jump `Product` objects live one response
>   level below the entity) resolves its branch parent at the position the KEY structure denotes: the
>   pre-jump object's position is the consuming position extended by the key-selection path from the
>   key's anchor type to the pre-jump object's type (read off `KeySelection` -- recorded by the
>   builder precisely because lowering must not re-derive it from the ambiguous cross-goal producer
>   union). With the anchor path, an existing goal-attributed group at the deeper position is found
>   and reused; without it, the old bookkeeping minted a second, mis-positioned group whose own key
>   placement cascaded (the reverted prototype's defect (b)).
> - **Placement is position-aware.** A key coordinate whose response position lies at-or-above a
>   host group's entry is not re-selected in that host's document (the host's document is rooted AT
>   its entry; the coordinate's children land at the document root) -- the case where a gather
>   producer's entry sits BELOW the consuming jump's entry, impossible under single-source keys and
>   routine under `D7ppp`.
>
> The representation VALUE for a list-valued key path (`products { id pid }` under a `[Product!]!`)
> is built with the same name-keyed object builder as `D11.10`'s requires values; its list-nesting
> realization is an executed-truth residual of the same registered class as the requires-fragment
> representation value (plan-level oracles are unaffected; the `Key` fragment and the gather
> placement are exact).

### D5pp/D11.12 -- dual-role subscription root (M3 subscriptions wave, customer-sweep round 3)

The D5pp reverse-direction (root recognition) text went through three forms driven by private-sweep
ground truth: (1) per-subgraph renamed-root recognition with rootNodes/keys/child evidence; (2) an
SDL data-usage evidence tier (output-type references, union membership) after the verified config
showed rootNodes membership carries no operation-root information; (3) the FINAL dual-role
semantics after ground truth showed a composed type that is simultaneously the subscription
operation root AND a data object (billing `schema { subscription: Subscription }` + payload
references + another subgraph's realtime root field). Forms 1-2's classification machinery was
removed: data routing is unconditional, the subscription root is an additive kind-masked ANCHOR,
and identity resolution survives only to aim the anchor. The D11.12 root-scoping clause was
restated to mask anchor edges only.

### D11.12 -- subscription lowering (M3 subscriptions wave)

Introduced as a NEW definition (no amendment chain): subscription operations plan through `D3`-`D10`
unchanged and lowering splits at the root position -- the single root fetch group becomes the
subscription trigger, everything below lowers as ordinary per-event fetches. Before this wave the
facade refused subscriptions with a typed `ErrSubscriptionNotSupported` (the documented M1 boundary;
executed-truth harnesses fell back to v1 for the one subscription scenario). The normative text lives
in `FORMAL_SPEC.md` `D11.12`; clause 7 (operation type) and `D5pp`'s root-type parenthetical were
amended to name subscriptions alongside queries/mutations.

### D11.13 -- `@defer` partitioning (M3 defer wave)

Introduced as a NEW definition (no amendment chain): a query with `@defer` plans through `D3`-`D10`
exactly as its undeferred counterpart (routing invariance, `FS-DEF-6`) and only lowering changes --
fetch-group keys gain the defer scope read off the engine normalization's `@__defer_internal`
stamps, deferred selections lower into scope variants (root re-walk / entity re-entry anchoring,
keys fetched in the parent scope), and the plan is emitted in v1's `DeferResponsePlan` /
`GraphQLDeferResponse` encoding (full response tree with `DeferField` stamps,
`FetchDependencies.DeferID`, descriptors) so the existing postprocess partition and resolve
incremental-delivery machinery run it unchanged. Before this wave planv2 planned a defer operation
as its flattened synchronous equivalent (conforming per `FS-DEF-1` but never delivering
increments, and diverging from v1's executed wire behavior). Obligation carries the scope only for
query operations (`FS-DEF-7`: subscriptions never honor `@defer`; mutations are serial by design).
The deferx`@requires` input-placement tension and the deferxdistributed-key intersection are
registered residuals (see the FORMAL_SPEC honest-scope note and `DIVERGENCES.md`).

---

## Section 6.4 -- MERGE (M0 commitment and the M1 evaluation note)

Original record, verbatim (the whole Section 6.4 as it stood; the "M0 commitment" bullet and the closing
"M1 evaluation note" are the milestone-bound parts):

`MERGE(K)` is *not* part of the exact search. Per the **tractability boundary**, joint cross-branch
**fetch merging** -- the NP-hard **Directed Steiner Tree** problem; the spec therefore defines it as a
distinct phase with an explicitly stated status:

- **M0 commitment (resolves `RESEARCH.md` open question 2, option a):** `MERGE` is a *syntactic*
  dedup pass over the already-computed folded cover $K$. Two fetches merge iff they target the same
  subgraph, share the same entity representation key-set, and have no argument conflict
  (`HasArgumentConflictWith`, `D1`). This relation is polynomial and the merge is **exact for that
  relation** (it removes only provably redundant identical fetches; it never invents new sharing).
- **Co-location risk, acknowledged.** Purely per-obligation routing can split sibling fields across
  subgraphs where the old planner co-locates them: two goals with tied (or near-tied) tree costs may
  resolve to *different* candidate subgraphs, yielding two fetches where one suffices. This is a real
  risk for the M1 "fetch count <= old planner" bar, and dedup alone does not close it (the two fetches
  are not identical).
- **Bounded co-location improvement pass.** `MERGE` therefore includes one bounded pass over the goal
  obligations in `C.4` order: for each goal $g$, if re-routing $g$'s resolution to an alternative
  candidate $v'\in\mathrm{cand}(g)$ with $\pi[v']<\infty$ whose subgraph is already fetched by a
  sibling *strictly reduces* $C(K)$ (which, under the default $w_f\gg w_s$ ordering, coincides with
  reducing the realized folded fetch count) while preserving I1 (the
  alternative walk is valid by construction -- it is a settled B-hyperpath) and I4, the move is
  applied. The pass is a *heuristic improvement*: it never worsens $C(K)$, it terminates (a single
  pass, each goal considered once), and *no exactness is claimed for it* -- it lives on the NP-hard
  side of the boundary and is bounded, not optimal.
- **What is *not* claimed:** `MERGE` does *not* reshape fetches to create sharing across
  differently-keyed branches -- that is the NP-hard side and planner-v2 asserts **no** optimality there
  in M0. Any future optimizing merge is a conservative model extension that MUST (i) carry a stated
  approximation bound (e.g. the quasi-polynomial $O(\log^2 k/\log\log k)$ Directed-Steiner factor) and
  (ii) emit an explicit signal when engaged (L7, L15). It is off by default.
- **M1 evaluation note:** the M1 bar "fetch count <= old planner on every corpus query" is evaluated
  with the co-location pass ON.

Because `MERGE` only removes redundant fetches or applies strictly-improving co-location moves to a
cover that is already sound (I1) and per-obligation tree-optimal (I3), it preserves I1 and never
increases $C(K)$ -- a proof obligation for `PROOFS.md`, not an assumption.

---

## Section 8 -- Conformance (M1 measured status)

The conformance table's audit-corpus row carried the M1 milestone figures; the live headline now
lives in `SCOREBOARD.md`. Original row, verbatim:

| audit corpus | in-repo plan-level corpus; gated router run | target 199/199 at plan level; M1 delivered 111/133 in-scope (see `SCOREBOARD.md` + the M1 Residual Register in `DIVERGENCES.md`); router-level end-to-end = M1.5 harness |

---

## Appendix index (original form)

The definition/invariant index before the split enumerated only the base definitions:

Defined exactly once, referenced by number throughout: `D1` supergraph config * `D2` normalized
operation * `D3` obligation tree * `D4` nodes * `D5` field-traversal edges * `D6` type-move edges
(+ member-narrowing rule) * `D7` entity-jump B-hyperedges * `D8` provided-field scoping * `D9`
walk validity * `D10` hyperpath cover * `D11` lowering (+ aliasing). Well-formedness: `W1` single-head
* `W2` folded accounting. Cost model `C` (`C.1` weight vector * `C.2` node cost / value function *
`C.3` folded plan cost * `C.4` tie-break). Invariants `I1` soundness * `I2` completeness * `I3`
optimality (scoped) * `I4` response-shape preservation. Algorithm `A` (Section 6).

