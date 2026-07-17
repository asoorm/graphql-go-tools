# Mathematically Defining the Folklore -- Formal Dispositions for the 23 FOLKLORE Propositions

**Status:** normative companion to `FEDERATION_SEMANTICS.md` (Stage 1.5 of the formal federation
semantics). That document classifies its 104 propositions as SPEC-PINNED (30) / FOLKLORE (23) /
OURS (51), where FOLKLORE means: behavior no document mandates, established by the reference
implementation's observable output. This document answers the standing question -- *is it possible
to mathematically define the parts of federation which are folklore?* -- affirmatively, by doing it:
every one of the 23 FOLKLORE propositions receives exactly one formal disposition,

- **DERIVED** -- the behavior follows from the formal model (`FORMAL_SPEC.md` `D1`-`D11`,
  `W1`/`W2`, `C`, `I1`-`I4`; `PROOFS.md` L1-L7, T1-T5). The entry states the derivation, in
  `PROOFS.md` sketch style, honest about gaps.
- **AXIOM** -- the behavior is not derivable from anything deeper. It is adopted as a named,
  formally stated axiom over the model's objects, with its justification and a statement of what
  would break if a router rejected it. An axiom is not a weakness; it is an honest foundation.
- **REJECTED-CONTINGENT** -- the observed behavior is a reference-implementation artifact a correct
  planner may deviate from; the entry states what this model does instead, under the standing
  `DIVERGENCES.md` policy.

Conventions follow `FORMAL_SPEC.md` Section 0 ($H=(V,E)$, $\pi$, $C(K)$, $\kappa$, $G(O)$,
$\mathrm{cand}(g)$, $\mathrm{Mem}_s(U)$, $\mathrm{pos}_s$) and `PROOFS.md` Section 0 (derivations,
standing assumptions A-0-A-6). Terminology follows `GLOSSARY.md`. Like `FEDERATION_SEMANTICS.md`,
this document is not scanned by the `docscheck` bold-term test; bold marks RFC-2119 keywords,
axiom/gap identifiers, and glossary terms only. Reference-implementation behavior is cited in the
neutral form of the `FEDERATION_SEMANTICS.md` authority hierarchy (Section 0.1, level 3).

---

## 0. Method: where folklore lives, and why it factors cleanly

The formal model $H$ is *router-internal*: nodes, edges, walks, obligations, covers, costs are all
objects the planner alone constructs and controls. The folklore propositions, by contrast, live at
the **router-subgraph interface** (what representations look like on the wire, what deployed
subgraph servers resolve) or at the **router-client interface** (incremental delivery), or they
are *choices* the reference implementation made where several behaviors would be correct. That
observation is what makes the disposition exercise well-posed -- each folklore proposition is one
of:

1. a *router-internal consequence* of definitions already in the model -> **DERIVED**;
2. an *assumption about the environment* (deployed subgraph servers, the `_entities` contract as
   implemented, the incremental-delivery client contract) that the model's soundness consumes but
   cannot prove -> **AXIOM**, sub-classified as an **environment axiom**;
3. a *choice point* where multiple behaviors are correct and no observation pins one -- the
   underdetermined folklore -> **AXIOM**, sub-classified as a **policy axiom** (the most valuable
   findings of this pass; Section 3);
4. a reference-implementation artifact -> **REJECTED-CONTINGENT**.

A derivation must actually derive: where the deductive distance from an axiom to the proposition
is zero (the proposition *is* the axiom's content restated), the disposition is AXIOM, not
DERIVED. Where a derivation exposes that the model does *not* entail a behavior the implementation
carries, the gap is registered (Section 4), never papered over. Axioms may serve as premises of several
derivations; the census (Section 5) counts propositions, and the axiom register (Section 1) counts axioms.

**The 23 propositions** (from `FEDERATION_SEMANTICS.md` Section 17): FS-KEY-4, FS-KEY-6, FS-REQ-6,
FS-REQ-8, FS-REQ-9, FS-PROV-4, FS-EXT-2, FS-OVR-2, FS-OVR-3, FS-OVR-5, FS-IFO-2, FS-IFO-4,
FS-IFO-5, FS-IFO-7, FS-ABS-4, FS-ABS-8, FS-ENT-7, FS-SUB-2, FS-SUB-3, FS-DEF-1, FS-DEF-3,
FS-DEF-5, FS-DEF-7.

---

## 1. The axiom register

Nine named axioms. Six are the direct content of an AXIOM-dispositioned proposition; three
(AX-EXT-KEY, AX-IFO-2, AX-ABS-LOCAL) are premises consumed by DERIVED dispositions. Each states:
the formal statement, the justification, and the rejection consequence (what breaks if a router
refuses it).

Throughout, fix an entity type $T$, a subgraph $s$, a key $k\in K_s$ with parsed FieldSet
$\sigma_k$ (a selection tree of field coordinates, possibly nested), and write $J$ for the merged
response object at a response position (the object the resolve engine has assembled at that
position when a dependent fetch fires). Define the **representation projection**
$\rho_\sigma(J)$ of $J$ under a selection tree $\sigma$ recursively:

$$
\rho_\sigma(J) \;=\; \big\{\, f \mapsto J[f] \;:\; f\in\sigma \text{ a leaf} \,\big\}
\;\cup\; \big\{\, f \mapsto \rho_{\sigma_f}(J[f]) \;:\; f\in\sigma \text{ composite, subtree } \sigma_f \,\big\},
$$

with list values mapped element-wise. $\rho_\sigma(J)$ *mirrors the nesting of* $\sigma$: it is
the sub-object of $J$ indexed by exactly $\sigma$'s coordinates, in $\sigma$'s shape.

### 1.1 Environment axioms (interoperability with deployed subgraph servers and clients)

**AX-REP-1 (projection representation -- the representation wire shape).**
For every entity fetch into subgraph $s_2$ resolving instances of $T$ via key $k$, the
representation sent for an instance with merged response object $J$ is

$$
r \;=\; \{\texttt{\_\_typename} \mapsto t\} \,\cup\, \rho_{\sigma_k}(J),
$$

where $t$ is the instance's runtime type name, and $s_2$'s `_entities` resolves $r$ to that
instance. In particular a *nested* key coordinate is transported as the nested object mirroring
the FieldSet -- never flattened, never re-encoded.
*Justification.* The subgraph specification pins the representation's *content* ("`__typename`"
plus "all fields included in the fieldset of a `@key`") but not its *shape* for nested FieldSets;
the mirrored-object shape is the reference implementation's observable wire format, reproduced
cross-vendor, and the only shape deployed subgraph libraries' reference resolvers decode
(`FEDERATION_SEMANTICS.md` Section 17, honest gap "Nested-key representation shape"). The model
*realizes* it: `D7` maps nested coordinates to nested-type field nodes, `Edge.KeySelection`
carries the raw FieldSet, and `D11.11`'s name-keyed object builder constructs exactly
$\rho_{\sigma_k}(J)$.
*Rejection consequence.* Nested `@key` FieldSets become unusable against every deployed subgraph
library: the server's reference resolver reads the representation by the FieldSet's paths and
finds nothing. There is no fallback encoding -- the subgraph spec types representations as `_Any`.

**AX-REP-2 (representation tolerance).**
For representations $r \subseteq r'$ (as partial maps; $r'$ carries additional data fields), a
subgraph's `_entities` resolves $r'$ to the same entity as $r$; the additional fields are readable
by the target's resolvers (notably `@requires` resolvers) and otherwise ignored.
*Justification.* This is FS-ENT-7's content: the subgraph specification pins the representation's
*minimum*, not a maximum; the permissiveness is universal deployed behavior. It is the load-bearing
premise of the `@requires` transport (FS-REQ-1's folklore half, `FEDERATION_SEMANTICS.md` Section 3): the
required values ride the representation as $\rho_{R_f}(J)$ alongside $\rho_{\sigma_k}(J)$, which
`D11.10`'s pipeline assembles.
*Rejection consequence.* `@requires` becomes unimplementable outright -- the subgraph specification
provides no input channel to `_entities` other than the representation -- and `D7ppp` distributed-key
assembly loses its transport with it.

**AX-EXT-KEY (external key-field carry).**
For every subgraph $s$, entity type $T$, key $k$ declared on $T$ in $s$, and coordinate $f$ of
$\sigma_k$ (at any nesting depth): when $s$ materializes an instance of $T$, $s$ resolves $f$ on
that instance when a fetch document selects it -- *including when $f$ is marked `@external` in*
$s$.
*Justification.* The entity's own resolver returns its key with the instance, so the key value
rides with the object even where the field's canonical definition is foreign; this is the
reference implementation's query-graph behavior (`FEDERATION_SEMANTICS.md` Section 5 authority
classification cites the source location) and the behavior of every subgraph library. The model
encodes it as `D5p` (the key-tail float).
*Rejection consequence.* Extension subgraphs (the Federation-1 `extend type` + `@external` idiom
and its Fed-2 equivalents) could never be *sources* of entity jumps -- their key tails would sit at
$\pi=\infty$ -- making the entire `fed1-external-*`/`mysterious-external` witness family
unroutable.

**AX-IFO-1 (interface-typed entity resolution -- the Federation >= 2.3 entity-interface contract).**
Let $I$ be an entity interface with key $k$, declared in subgraph $s$ either as the defining
interface or as an `@interfaceObject`. Then $I$ is an `_entities` target in $s$: a representation
$\{\texttt{\_\_typename}\mapsto I\}\cup\rho_{\sigma_k}(J)$ -- typed by the *interface* name --
resolves the instance, and the entity fetch's entry fragment `... on I { ... }` is valid and
resolving in $s$.
*Justification.* The subgraph specification's `_Entity` union covers object entity types only;
interfaces as `_entities` targets are the Federation >= 2.3 entity-interface contract as observed
in the reference implementation and reproduced by subgraph libraries (witnesses:
`simple-interface-object`, `interface-object-with-requires`). The model encodes the router half as
`D7p` (interface-node jump heads) and `D3io` (flattened candidates); lowering types entry fragment
and representation on $I$ (`D7p` note, `D11.9`).
*Rejection consequence.* `@interfaceObject` is unusable end to end: the interface-object subgraph
knows no concrete implementer types, so there exists *no* representation typing under which it
could be addressed at all.

**AX-IFO-2 (member-knowing resolution returns concrete types).**
For the *defining* subgraph $s_d$ of entity interface $I$: the `_entities` result for an
interface-typed representation is concretely typed -- its runtime type (GraphQL Section 4.4
`__typename`) is the instance's concrete implementer $C \in \mathrm{impl}(I)$, and
member-conditioned selections in the fetch document evaluate against $C$.
*Justification.* GraphQL Section 4.4 requires `__typename` to name an object type, so *some* concrete
answer is spec-forced; that the defining subgraph is the place that knows it is the observable
reference-runtime behavior (`FEDERATION_SEMANTICS.md` Section 9's `__typename`-rewrite note) and the
premise of FS-IFO-6 (OURS). Consumed by the FS-IFO-4 derivation.
*Rejection consequence.* The reverse hop (FS-IFO-4) could never satisfy member-gated client
selections; every `... on C` under an interface-object-supplied position would be undecidable.

**AX-DEF-ADV (advisory deferral).**
Let $\varepsilon$ be the **defer erasure**: the map on normalized operations that deletes every
`@defer` directive (leaving the fragment's selections in place). For every operation $Q$:
$\mathrm{plan}(\varepsilon(Q))$, delivered as a single (initial) response, is a conforming
response to $Q$. Equivalently: honoring the empty set of deferred fragments is always admissible,
and -- per fragment -- honoring any subset is.
*Justification.* The incremental-delivery RFC draft defines `@defer` as a hint a service **MAY**
ignore, including delivering deferred data in the initial response; per
`FEDERATION_SEMANTICS.md` Section 0.2/Section 15 even the quoted draft text carries FOLKLORE authority, which is
why this is an axiom and not spec-pinned. The clients' side of the contract -- tolerating
fully-inlined delivery -- is what the axiom actually consumes.
*Rejection consequence.* A router without an incremental-delivery transport could not serve any
defer-bearing operation at all; every non-supporting router in deployment today would be
non-conforming.

**AX-DEF-WIRE (incremental payload addressing).**
For every deferred fragment $\varphi$ a plan *does* honor, delivery is a finite set of incremental
payloads $\{(p_i, d_i)\}_{i\ge 1}$, each addressed by the response path of $\varphi$'s attachment
position (extended by list indices) and by the client's `label` when given, with
$\bigcup_i d_i$ = the $\varphi$-restriction of the response tree at that path -- nothing else may
carry deferred data to the client.
*Justification.* RFC-draft payload bookkeeping (FS-DEF-3's content), FOLKLORE authority as above.
Vacuously satisfied by this model's realization (which honors no fragment; see FS-DEF-1).
*Rejection consequence.* Clients cannot re-assemble the response: the union condition (FS-DEF-4)
is unverifiable without payload addressing.

### 1.2 Policy axioms (formalized choice points -- see Section 3)

**AX-ABS-LOCAL (local resolution of abstract positions).**
An abstract-typed field is resolved *locally* on whichever parent-capable subgraph supplies each
parent instance: for a position $\langle T.f\rangle$ yielding abstract $U$, with route-scoped
capable set $P$ (`D6p`), the plan commits to one static treatment of $U$'s members that is valid
for *every* origin $s\in P$ -- it does not re-route parent instances to a chosen subgraph to widen
the member set.
*Justification.* This is the standing assumption `D6` names (`FORMAL_SPEC.md` Section 2, Section 7.1):
the canonical federation semantics, corroborated cross-vendor and adjudicated in the corpus
(`DIVERGENCES.md`, "Partial-union narrowing -- CLOSED"). It is a *policy* axiom because it is not
the only I1-sound behavior -- `FORMAL_SPEC.md` Section 7.1 exhibits the sound alternative (commit every
parent instance to one subgraph via its entity key) and rejects it as non-canonical and
cost-dominated. The axiom is exactly where that choice lives; FS-ABS-4 is then a theorem (Section 2.11).
*Rejection consequence.* Plans and expected responses for the whole partial-union family change
(members outside the intersection would resolve or not depending on planner routing choices);
interoperating routers would disagree on which members a mixed-origin list can produce.

**AX-REQ-COND (conditional requires inputs -- the underdetermined case, Section 3.1).**
Let $f$ carry `@requires` with FieldSet $R_f$ in $s_2$, and let $c$ be a coordinate of $R_f$
appearing under an inline fragment `... on B` (a fragment-conditioned coordinate). Then $c$ is a
*conditional input*: it contributes **no** static AND-tail to the `D7pp` requires-scoped jumps for
$f$ (formally: $\Phi_{\mathrm{req}}(f)$ ranges over the unconditional coordinates of $R_f$ only),
while the gathering documents render the fragment with $c$ inside it *where $c$ is resolvable* --
document validity (FS-PLAN-1) dominates the rendering clause, so a $c$ resolvable *nowhere*
renders *nowhere* (the Stage-2 probe's second finding; `D7pp`(4) states the pruning) -- and the
value rides the representation ($\rho_{R_f}(J)$, AX-REP-2) exactly when the instance's runtime
type is $B$ and the gather route produced it.
*Justification.* No document states whether a fragment-conditioned requires coordinate is a hard
input (its unavailability blocks the jump) or a conditional one; observed reference behavior is
permissive but not decisively probed (`FEDERATION_SEMANTICS.md` FS-REQ-9: *unverified
convention*). The completeness-favoring reading is forced by mixed populations: a hard reading
makes *every* `@requires` with a member-conditioned branch unplannable whenever any capable
instance can fail the type condition -- i.e. almost always, since type conditions exist precisely
because the population is mixed. Witnesses: `requires-with-fragments`,
`requires-interface/case-03`.
*Rejection consequence (adopting the hard reading instead).* Strictly fewer plannable operations,
with no compensating soundness gain for unconditioned instances. *Residual honestly stated:* under
the conditional reading, a $B$-typed instance whose conditioned value had no gather route reaches
$s_2$ with a representation lacking $c$ -- the resolver sees an absent optional input. This
residual is implementation-acknowledged (see MG-2, Section 4) and is the precise content a future spec
revision should pin.

---

## 2. Dispositions, construct by construct

Format per entry: disposition; the folklore content being dispositioned; formal statement;
derivation or justification; what Stage 2's conformance-case generator should assert.

### 2.1 FS-KEY-4 -- nested-key representation shape

**Disposition: AXIOM (AX-REP-1).**
*Folklore content.* For composite/nested keys the representation carries the nested object with
its selected sub-fields -- the mirrored-object wire format; the subgraph specification is silent.
*Formal statement.* The representation for a jump via key $k$ is
$\{\texttt{\_\_typename}\mapsto t\}\cup\rho_{\sigma_k}(J)$ -- AX-REP-1 verbatim.
*Why AXIOM, not DERIVED.* The upstream-selection half of FS-KEY-4 ("the upstream selection ...
MUST mirror the FieldSet's nesting") *is* derived: `D7` maps a nested coordinate to the nested
type's field node ($(\mathit{Organization},s_1).id$ for `id organization { id }`), so FS-KEY-3's
injection machinery (`D11.1`/`D11.11` key placement) selects the mirrored tree by construction.
But the *representation shape* half has zero deductive distance from AX-REP-1 -- the model's
builder (`D11.11`, name-keyed object builder over `Edge.KeySelection`) is the axiom's
*realization*, not its proof. Both sides of the wire must agree on $\rho$; only an axiom can say
the subgraph side does.
*Stage 2.* For every entity fetch whose key FieldSet nests: assert the representation template is
$\rho_{\sigma_k}$-shaped (nested objects mirroring the FieldSet, no flattening), and the upstream
document selects the mirrored tree at the jump position. Witness seed: `complex-entity-call`,
`FORMAL_SPEC.md` Section 7.2.

### 2.2 FS-KEY-6 -- key choice among several qualifying keys

**Disposition: REJECTED-CONTINGENT.**
*Folklore content.* The reference implementation selects among reachable keys by internal cost
heuristics; no document states a selection rule. (The FS-KEY-6 proposition itself -- *any*
FS-KEY-5-qualifying key MAY be used -- is uncontested and trivially satisfied below; what is
rejected as contingent is the reference's particular choice function.)
*What this model does instead.* Key choice is fully determined by the model, and not by imitation:
each qualifying key contributes its own `D7` `EntityJump` edge; `A` settles all of them; the
serving jump is the one on the $\pi$-minimal walk to the `C.4`-selected candidate, with the
`C.4` total order breaking exact ties (subgraph name, then kind, label, head, tails). By L5
(`PROOFS.md` determinism), the choice is a deterministic function of $(\mathcal{S},Q)$ -- this is
FS-KEY-7's SHOULD, discharged as a theorem for this planner.
*Divergence policy.* No live `DV` entry exists because no corpus expectation pins a key choice --
the audit oracles are key-choice-agnostic (any FS-KEY-5-qualifying key yields validating
documents, satisfiable dependencies, and the same response shape). Should a corpus ever encode the
reference's heuristic choice as an expectation, the adjudication lands in `DIVERGENCES.md` under
the standing policy at level 4 vs level 3 -- the DV-009 pattern (a registered, deliberate
divergence from a reference artifact).
*Stage 2.* Generate multi-key schemas where the qualifying keys differ; assert only
choice-agnostic properties (FS-KEY-5 producibility of the chosen key's coordinates, document
validity, dependency order) plus determinism (permutation testing: semantically equal inputs give
identical plans). Do **not** assert which key.

### 2.3 FS-REQ-6 -- nested (chained) `@requires`

**Disposition: DERIVED** (from `D7pp`, `D9`, `A`'s AND-relaxation, `D11.10`, A-0; proof by
induction on derivation height, the L3 pattern).
*Folklore content.* A required coordinate that is itself `@requires`-dependent is planned as a
chain -- inner gathering before the coordinate's producer, before the outer entity fetch -- at any
nesting depth.
*Formal statement.* Let $f$ carry `@requires` $R_f$ in $s_2$ and let coordinate $c\in R_f$ carry
`@requires` $R_c$ in its resolving subgraph $s_r$. Every valid cover of $\langle T.f\rangle$
contains, for the requires-scoped jump $e_f$ it uses, a derivation of each tail in
$\Phi_{\mathrm{req}}(f)$; the tail for $c$ is producible only through $c$'s own requires-scoped
structure, whose tails include $\Phi_{\mathrm{req}}(c)$ -- recursively, to any finite depth.
*Derivation.* By `D7pp`(1), $c$'s `Field` edge in $s_r$ hangs off the scope node
$(U,s_r\!\mid\!\mathrm{req}{:}U.c)$, never the plain object node; by `D7pp`(3) that scope node's
only in-edges are $c$'s requires-scoped jumps, whose tails include $\Phi_{\mathrm{req}}(c)$.
By `D9` (walk validity = forward-chaining derivability), any valid walk containing $e_f$ contains
a derivation of its tail for $c$, hence of $\Phi_{\mathrm{req}}(c)$ -- the chain is forced
structurally, with no per-depth machinery: `A`'s AND-relaxation settles inner heads before outer
`need` counters reach zero (`D7`, "conditions are fully static"). Finiteness at arbitrary depth is
A-0 (finite $H$, finite derivations); *no* iteration cap exists to be exceeded (`D3`: no fixpoint
re-walk, lesson L13). Fetch ordering is `D11.2` (a group precedes the group consuming its tails);
`D11.10`'s branch-group recursion places the chain ("recursion handles a nested chain with no new
mechanism"). A genuine cycle -- $R$ depending on the value it feeds with no acyclic relay -- leaves
`need` counters positive forever: the head stays $\pi=\infty$ and planning fails
`ErrNoValidPlan` (I2's contrapositive), which is exactly FS-REQ's error condition.
*Stage 2.* Generate requires-chains of depth $d\in\{2,3,4\}$ (the corpus witnesses depth <= 3;
`requires-requires`); assert the topological order inner-gather -> producer -> outer entity fetch,
and representation content $\rho_{R_f}(J)$ at each link. Negative case: a true cycle must yield a
typed planning error, not a plan.

### 2.4 FS-REQ-8 -- the same-subgraph relay ($s_2 \to s_1 \to s_2$)

**Disposition: DERIVED** (from `D7pp`(3) with $s_1=s_2$ admitted, `D9`, I2/T2).
*Folklore content.* A relay that exits the subgraph holding the instances, gathers the
requirement elsewhere, and re-enters the same subgraph via `_entities` is valid -- and obligatory
when it is the only input-complete route.
*Formal statement.* For root instances of $T$ materialized in $s_2$ and $f$ carrying
`@requires` $R_f$ in $s_2$ with $R_f$ resolvable (only) in $s_1\ne s_2$: the walk
$r \to \cdots \to (T,s_2) \to [\text{key tails in } s_2] \to e_{\mathrm{gather}} \to \cdots \to
e_{\mathrm{scoped}} \to (T,s_2\!\mid\!\mathrm{req}{:}T.f) \to (T,s_2\!\mid\!\cdot).f$ is `D9`-valid,
and every valid cover of $\langle T.f\rangle$ contains a requires-scoped jump.
*Derivation.* Validity: `D7pp`(3) constructs scoped jumps *per source, including* $s_1=s_2$ (the
clause names the relay shape explicitly); the walk is acyclic because the scope node
$(T,s_2\!\mid\!\mathrm{req}{:}T.f)$ is distinct from the plain $(T,s_2)$ -- re-entering the same
*subgraph* is not revisiting the same *node*, which is the formal content of "circular-looking
relays are acyclic once unrolled". Obligation: `D7pp` is deliberately non-monotone -- the plain
$(T,s_2)$ carries no `Field` edge for $f$, so *every* route to $\langle T.f\rangle$'s candidates
passes a requires-scoped jump whose tails include $\Phi_{\mathrm{req}}(f)$ (this is FS-REQ-3, the
bypass prohibition, seen from the model side); by I2/T2, when such a route exists the planner
returns one -- when the only input-complete assignment gathers in $s_1$, the returned cover *is*
the relay. Witnesses: `requires-circular/case-01`, `interface-object-with-requires/case-05`.
*Stage 2.* Assert the relay's three-fetch dependency chain and that the re-entry fetch's
representation carries $\rho_{R_f}$; negative case: a plan resolving $f$ in the local-descent
fetch (no gathered inputs) is a defect even though its document validates (the FS-REQ-3 bypass).

### 2.5 FS-REQ-9 -- fragment-conditioned requires coordinates

**Disposition: AXIOM (AX-REQ-COND) -- UNDERDETERMINED FOLKLORE, formalized (Section 3.1).**
*Folklore content.* Composition accepts fragments in `@requires` FieldSets; the gathering document
must render them; whether the conditioned branch is a hard or conditional input is stated by no
document and not decisively observable.
*Formal statement.* AX-REQ-COND: fragment-conditioned coordinates contribute no static AND-tail
($\Phi_{\mathrm{req}}$ quantifies over unconditional coordinates only); gathering documents render
the fragment; the value rides $\rho_{R_f}(J)$ when produced.
*Justification.* Section 1.2. The choice is realized in the implementation (the requires coordinate
walker returns the empty tail-choice for fragment nodes, with the residual stated in place) and --
since the reachability-gaps wave -- **stated in `FORMAL_SPEC.md`**: `D7pp`(4) carries the
AX-REQ-COND clause plus the FS-PLAN-1 rendering-validity rule (MG-2 CLOSED, Section 4).
*Stage 2.* Positive: `requires-with-fragments`-shaped cases must plan, with the fragment rendered
in the gathering document and the jump *not* gated on the conditioned coordinate's availability.
Probe case (the underdetermination made falsifiable): a schema where the conditioned coordinate is
resolvable *nowhere* -- under AX-REQ-COND the operation still plans (jump fires, conditioned value
absent); under the hard reading it errors. Freeze the AX-REQ-COND expectation and revisit only if
a specification revision pins the opposite.

### 2.6 FS-PROV-4 -- `@provides` across abstract-typed fields

**Disposition: DERIVED** (prohibition half, from `D5` + `D8` + I1) **with registered model gap
MG-1** (capability half, fragment-conditioned coordinates -- Section 4).
*Folklore content.* A `@provides` FieldSet crossing an abstract-typed field (provides on
interface/union members, nested provides) grants exactly the coordinates the FieldSet names,
under the type conditions it names, path-scoped.
*Formal statement.* Split the proposition: (i) *at most* $\sigma$, on the providing path only --
no fetch may select a provided-only coordinate outside the providing traversal or beyond
$\sigma$'s named coordinates/conditions; (ii) *at least* $\sigma$ -- the providing subgraph may
resolve each $\sigma$ coordinate inline on the providing path.
*Derivation of (i).* A provided-only coordinate is `@external` in $s$, so `D5` emits **no**
unscoped `Field` edge for it ("external emits no edge"); the only edges resolving it in $s$ are
`D8`'s scope-tagged edges, whose scope object $(U,s\!\mid\!f)$ is reachable *only* through the
providing field's `Descent` (`D8`: "reachable only via the descent of the specific providing
traversal"). By I1 (every emitted walk step is an existing edge of $H$), no plan can select the
coordinate anywhere else -- FS-PROV-2's path-scoping and FS-PROV-4's "exactly the coordinates it
names" both fall out of edge *absence*. The grant's type-condition scoping in the response is
`D11.3`'s gate preservation.
*The gap in (ii).* `D8` as written constructs scope-internal `Field`/`Descent` edges only; it
states no rule for a FieldSet node that is an *inline fragment* (`media @provides(fields: "... on
Book { title }")`), and the implementation emits no in-scope alternative for fragment-conditioned
provided coordinates (MG-1, Section 4, with the source location). Consequence -- stated honestly: for
those coordinates the *inline grant* (FS-PROV-1's MAY) is not available in the model; plans remain
sound and complete because `D8` is a monotone extension (`PROOFS.md` L6 -- provides only *adds*
routes) and the owning subgraph's entity route covers the coordinate, at the cost of an extra
fetch. The `provides-on-union`/`provides-on-interface`/`nested-provides` witnesses pass on exactly
that basis. FS-PROV-3 (MAY, never obligatory) is what makes this a quality gap, not a correctness
gap.
*Stage 2.* Assert (i) as a negative family: any fetch selecting a provided-only coordinate
outside the providing path is a defect. Assert (ii) positively only for fragment-free FieldSets
until MG-1 closes; for fragment-crossing FieldSets assert plan correctness (coverage via owning
route admitted) and *flag* fetch-count so the MG-1 closure is measurable.

### 2.7 FS-EXT-2 -- `@external` key fields as jump sources

**Disposition: DERIVED** (from AX-EXT-KEY via `D5p`, then `D7`; `PROOFS.md` L6p additivity).
*Folklore content.* An `@external` field appearing in some `@key` FieldSet of its type in the same
subgraph is treated as producible there for representation-building; a subgraph holding only an
entity extension is a valid source of entity jumps.
*Formal statement.* For $f$ `@external` on $T$ in $s$ with $f\in\sigma_k$ for some
$k\in K_s$: $(T,s).f$ is reachable in $H$ whenever $(T,s)$ is, and every `D7` jump out of $s$
whose key tails include $(T,s).f$ can fire.
*Derivation.* AX-EXT-KEY says $s$ *actually resolves* $f$ on instances it materializes -- the
environment fact. `D5p` encodes it as the in-subgraph `Field` edge
$(T,s)\xrightarrow{f}(T,s).f$ (with `Descent` for nested keys), which is exactly the edge whose
absence would pin the tail at $\pi=\infty$; the edge is sound *because* AX-EXT-KEY holds (an edge
must correspond to a real capability -- I1's reading). With the tail reachable, `D7`'s `keyTails`
premise is satisfiable from $s$ and the jump participates in `A` like any other. `PROOFS.md` L6p
gives additivity: `D5p` only adds derivations, so no other route or cost is disturbed. The
non-key `@external` case emits no edge -- FS-EXT-1's prohibition stays intact, which is why the
derivation does not overshoot.
*Stage 2.* From the `fed1-external-*`/`fed2-external-*`/`mysterious-external` family: assert
jumps *out of* extension subgraphs fire (their documents select the `@external` key fields), and
negatively: a fetch sourcing a non-key `@external` field's response value from the declaring
subgraph is a defect.

### 2.8 FS-OVR-2, FS-OVR-3, FS-OVR-5 -- `@override` interactions

**Disposition: all three DERIVED**, via one observation:

> **Override-blindness (FL-OVR).** The formal model contains no `@override` construct: `D1`
> consumes *post-composition* capabilities ("planning consumes the post-composition capabilities;
> hence most `@override` semantics are precondition, not plan-time logic" --
> `FEDERATION_SEMANTICS.md` Section 6), and no definition `D2`-`D11` mentions override. Hence every
> plan-level `@override` proposition is the image, under the `D1` input mapping, of an
> already-dispositioned proposition about capability sets.

**FS-OVR-2** (losing subgraph's retained non-output capabilities). *Formal statement.* If
composition retains coordinate $(T,f)$ in the losing subgraph $A$ as an input capability (e.g.
`@external`-listed key coordinate), then its plan-time usability is exactly the `D5p`/`D7`
key-carry and `D7pp`(4) gather semantics -- nothing more (no `Field` edge => never a response-value
source, the FS-EXT-1 analogue). *Derivation.* Immediate from FL-OVR: the model cannot distinguish
"field lost to override" from "field marked external"; both arrive as the same `D1` shape, and
Section 2.7's derivation applies verbatim. *Honesty.* The antecedent -- precisely *which* capabilities the
composer retains -- is composer-version-contingent (`FEDERATION_SEMANTICS.md` Section 17 honest gap);
the derivation is conditional on the `D1` input, which is the right formal boundary: the planner
must be correct for *whatever* the composer emits (Section 3.2).
**FS-OVR-3** (unavailable override is inert). *Formal statement.* When `from` names no subgraph
in $\mathcal{S}$, the composed `D1` lists the declaring subgraph's field as an ordinary
capability and leaves the other subgraph's identical declaration unaffected; planning then follows
Section 7 (`FS-SHR`) sharing rules verbatim. *Derivation.* Plan-level content is vacuous under FL-OVR --
there is nothing override-shaped left in the input; the witness (`unavailable-override`) evidences
the *composition* precondition, which this document's scope treats as input, exactly as
`FEDERATION_SEMANTICS.md` scopes composition to preconditions.
**FS-OVR-5** (override + `@requires`). *Formal statement.* With the overriding subgraph $B$
declaring $f$ with $R_f$, the `D7pp` construction instantiates at $s_2=B$; the overridden subgraph
$A$ participates as a source $s_1$ or foreign tail subgraph $s_r$ in `D7pp`(3)/(4) wherever `D1`
retains its input capabilities. *Derivation.* FL-OVR + Section 2.3/Section 2.4: `D7pp` quantifies over "each
subgraph $s_2$ and each field ... carrying `@requires`" with no ownership-history term, so the
override case is not a case at all -- it is the general construction at a particular input.
Witness: `override-with-requires`.
*Stage 2.* FS-OVR-1's exclusion (the pinned half) is the assertive core: the losing subgraph never
sources the overridden field's response value. For -2/-5 assert input-side usage (representation
coordinates drawn from the losing subgraph where retained; requires gathered per Section 2.3-2.4 with
$s_2$ = overriding subgraph). For -3 assert byte-equivalence of plans against the same supergraph
with the inert `@override` textually removed.

### 2.9 FS-IFO-2 -- interface-typed entry into interface-object / entity-interface subgraphs

**Disposition: AXIOM (AX-IFO-1)**, with the document half derived.
*Folklore content.* Entity fetches into such subgraphs type entry fragments and representations on
the interface name; no subgraph-spec text covers interfaces in `_Entity`.
*Formal statement.* AX-IFO-1 (Section 1.1). The document half -- `... on I` rather than `... on C` -- is
DERIVED from FS-PLAN-1/I1: the target subgraph's schema has no implementer types, so a
member-typed fragment does not validate; the representation half -- that an interface-typed
representation *resolves* -- has zero deductive distance from the axiom (nothing router-internal
can make the target's `_entities` accept it).
*Model realization.* `D7p` (the jump's head *is* the interface node, so lowering's entry fragment
and key fragment are typed on $I$ by construction); `D3io` for the flattening direction; `D11.9`
for placement.
*Stage 2.* Assert every fetch into an interface-object/entity-interface subgraph has
representations `__typename: "I"` and entry fragments on $I$; negative: any concrete-member
fragment in such a document is a defect (`simple-interface-object` seeds both directions).

### 2.10 FS-IFO-4, FS-IFO-5, FS-IFO-7 -- entity-interface routing compositions

**FS-IFO-4 (reverse hop to the member-knowing subgraph). Disposition: DERIVED** (from `D7p` +
AX-IFO-1 + AX-IFO-2). *Derivation.* `D7p` adds the interface-node head $(I,s_d)$ in the defining
subgraph $s_d$ alongside concrete heads, so a source position that knows only $I$ (an
interface-object subgraph's instances) has a usable jump: its key tails are $I$'s key fields,
producible at the source per `D5`/`D5p`; the entry is valid by AX-IFO-1; and the *result* is
concretely typed by AX-IFO-2, which is what lets member-gated selections and onward `D6`
`TypeMove`/`D7` member routing fire in $s_d$. Witness: `simple-interface-object`
(`anotherUsers { age }`).
**FS-IFO-5 (`@requires` on interface-object fields). Disposition: DERIVED** (composition of
`D7pp` with `D7p`/`D3io`). *Derivation.* `D7pp` quantifies over requires-bearing fields with no
concreteness assumption, and `D7`'s variant clause makes entity-interface/interface-object jumps
`EntityJump` *variants, not new kinds* -- so the scope construction, the distributed-tail
machinery, and the Section 2.3/Section 2.4 derivations instantiate unchanged with the scope node typed on $I$
and representations per AX-IFO-1 ($\{\texttt{\_\_typename}\mapsto I\}\cup\rho_{\sigma_k}(J)\cup
\rho_{R_f}(J)$, AX-REP-1/2). Relay shapes included (FS-REQ-8's derivation is type-agnostic).
Witness: `interface-object-with-requires` (including `case-05`, the relay).
**FS-IFO-7 (indirect extension composes). Disposition: DERIVED** (per-goal independence +
additivity). *Derivation.* A subgraph extending implementer $C$ contributes ordinary `D7` jumps
on $C$; the interface-object subgraph contributes `D7p`/`D3io` structure on $I$. Both edge
families coexist in one $H$; `A` covers each goal independently and
$K=\bigcup_g\kappa(g)$ unions the walks -- there is no interaction *to* prove, which is itself the
proof: every extension of $E$ in this family is additive (`PROOFS.md` L6-family monotonicity), so
neither family disturbs the other's routes or costs. Witness:
`interface-object-indirect-extension`.
*Stage 2.* For -4: assert the hop lands in the defining subgraph with interface-typed entry and
that member-gated selections below it are served. For -5: Section 2.3/Section 2.4 assertions with
interface-typed representations. For -7: one plan containing both an $I$-typed jump and an
ordinary $C$-typed jump, dependency-consistent.

### 2.11 FS-ABS-4 -- value-type member narrowing (intersection nulls)

**Disposition: DERIVED** (from AX-ABS-LOCAL + `D6`/`D6p`/`D6pp` + I1/I4; necessity *and*
sufficiency).
*Folklore content.* At an abstract position whose members are value types, a member not declared
by every route-scoped parent-capable subgraph is not fetched; its selections render as
type-condition non-matches. Corroborated cross-vendor; adjudicated in `DIVERGENCES.md`
("Partial-union narrowing -- CLOSED").
*Formal statement.* For refinement $\langle U\triangleright C\rangle$ under parent
$\langle T.f\rangle$ with route-scoped capable set $P$ (`D6p`), all members value types: $C$ is
coverable iff $C\in\bigcap_{s\in P}\mathrm{Mem}_s(U)$; otherwise the goal is exempt and lowers to
a response-only null (`D11.3`, I4).
*Derivation.* *(Necessity -- why narrowing is forced.)* Suppose $C\notin\mathrm{Mem}_{s'}(U)$ for
some $s'\in P$. By `D6p`, $s'$ has a producing route, so some admissible instance population has
parents supplied by $s'$. By AX-ABS-LOCAL the plan's one static treatment must be valid for that
origin; resolving $C$'s selections for $s'$-supplied parents would require refining $U$ to $C$
*in* $s'$ -- but no `TypeMove` edge $(U,s')\to(C,s')$ exists (`D6` emits per
$\mathrm{Mem}_{s'}$), and value types have no `D7` edge to reconcile the instance in another
subgraph (no key => no jump; and a jump transports *existing* instances only -- `D6pp` verdict 1's
rationale). So no `D9`-valid walk covers the goal for every origin: exemption is the only sound
static treatment (I1). *(Sufficiency -- why the intersection is not over-narrowed.)* If
$C\in\bigcap_{s\in P}\mathrm{Mem}_s(U)$, every capable origin has the local `TypeMove` route, so
whichever subgraph the parent walk enters, the refinement continues in place -- coverable soundly
on every route. *(Route-scoping.)* AX-ABS-LOCAL quantifies over subgraphs that *supply* parents;
`D6p` restricts $P$ accordingly (a declaring-but-unreachable subgraph supplies none --
`partial-union/case-02`), so the derivation neither uses nor permits schema-scoped narrowing.
*(Shape.)* I4/`D11.3`: the narrowed selections stay in the response shape as `__typename`-gated
entries that never match -- FS-PLAN-3's "never a missing key". The sound-but-rejected alternative
(commit each parent to one subgraph) violates AX-ABS-LOCAL, not I1 -- `FORMAL_SPEC.md` Section 7.1's
honesty note, now located precisely at the axiom.
*Stage 2.* The `partial-union` family: assert narrowed members appear in no fetch document while
their gates remain in the response shape; assert route-scoping (a rootless declaring subgraph must
not narrow); entity-member cases must *not* narrow (FS-ABS-5, `D6pp` verdict 3).

### 2.12 FS-ABS-8 -- member expansion (type explosion) when the interface cannot resolve

**Disposition: DERIVED** (from `D3pppp` + `D6pp` + `D11.7`/`D11.8` + I2/I4).
*Folklore content.* When a field selected on an interface is resolvable on no position-capable
subgraph *on the interface*, but is resolvable on the position's concrete (entity) members, the
plan fans out per possible member, covering every possible runtime type.
*Formal statement.* Under `D3pppp`'s trigger (abstract $U$; no $s\in P$ has $(U,s).f$; all
$\mathrm{pos}_s$ known; $M=\bigcup_{s\in P}\mathrm{pos}_s\ne\emptyset$ with every $C\in M$'s
$(C,s').f$ reachable), the obligation subtree is replaced by one refinement
$\langle U\triangleright C\rangle$ per $C\in M$, each covered per Section 2's entity routing and placed
member-qualified.
*Derivation.* The proposition's antecedent is `D3pppp`'s trigger read back: "not resolvable on the
interface by any subgraph supplying the position" is trigger 2 over the `D6pp` machinery; "each an
entity" gives the reachable member field nodes of trigger 4. *Coverage of every possible runtime
type:* $M$ is, by `D6pp`'s construction, exactly the set of concrete types instances at the
position can be under any capable origin -- the fan-out over $M$ is therefore neither under- nor
over-inclusive (dead members are outside every $\mathrm{pos}_s$ and correctly outside $M$;
FS-ABS-6). Each member copy is a goal; I2 forces its cover; `D6pp` verdict 3 and `D11.7`
member-qualified placement force the member's fragment into member-possible fetches only
(FS-ABS-1). *Same response keys under gates:* the copies are `D11.8` sibling variants of one
client field whose presence-union equals the client tree -- the I4 reading `D11.8` established,
and the plan-expression freedom FS-ABS-10/DV-008 adjudicated. *Honest scope carried over:* the
mixed position (some capable subgraph resolves $f$ on $U$ locally, others do not) is excluded by
trigger 2, exactly matching `FEDERATION_SEMANTICS.md` Section 17's honest gap -- the derivation covers
FS-ABS-8's stated antecedent and nothing more.
*Stage 2.* `abstract-types`-shaped seeds: assert per-member fragments appear only in
member-possible fetches, the member set fanned over equals $\bigcup\mathrm{pos}_s$, and the
response presence-union equals the client tree. Register the mixed shape as
generator-out-of-scope until the model closes it.

### 2.13 FS-ENT-7 -- representations may carry extra fields

**Disposition: AXIOM (AX-REP-2).**
*Folklore content.* The subgraph spec pins the representation minimum, not a maximum; targets
ignore fields they do not need. The `@requires` transport is exactly a use of this permission.
*Formal statement and justification.* Section 1.1. Zero deductive distance: the model's `D11.10`/
`D11.11` pipelines *use* the permission; nothing router-internal can grant it.
*Stage 2.* Assert requires-transport representations ($\rho_{\sigma_k}\cup\rho_{R_f}$) against
subgraph fixtures that validate strictly (a fixture that rejects unknown representation fields is
itself non-conforming *to this axiom* -- encode that in the harness contract, since the axiom is
the interoperability floor the corpus already assumes).

### 2.14 FS-SUB-2, FS-SUB-3 -- the trigger/response split

**FS-SUB-2 (single trigger, no fan-out). Disposition: DERIVED** (from `D3`/`D10` kappa-functionality
+ `C.4`/L5 + `D11.12`).
*Derivation.* The operation has exactly one root field obligation (GraphQL Section 5.2.3.1, a
validation-supplied precondition `D11.12` re-checks). $\kappa$ is a *function*
$G(O)\to\mathcal{P}(E)$: the root goal receives exactly one covering walk, whose root-entering
edge names one subgraph -- the `C.4`-minimal candidate among the declaring subgraphs, unique by L5.
`D11.12` lowers exactly that one root group as the trigger and nothing else as a `subscription`
document. Fan-out is therefore not merely forbidden but *inexpressible* in the plan object: there
is no second trigger slot. *Honesty note (a finding).* This derivation does **not** need the
informal "a source stream cannot span subgraphs" impossibility argument that
`FEDERATION_SEMANTICS.md` Section 17 flags as a gap: within this model, single-trigger is a consequence
of cover functionality and determinism. The impossibility argument would be needed only to prove
*all* routers must behave so -- out of this document's scope, and the corroborated-convention
status for other implementations stands unchanged.
**FS-SUB-3 (below the trigger, ordinary planning per event). Disposition: DERIVED** (from
`D11.12` + I1-I4 applicability). *Derivation.* `D11.12`'s first clause is the statement: a
subscription plans through `D3`-`D10` *identically* to a query -- the obligation tree, goal
resolution, jumps, requires gathering, and abstract-member machinery contain no operation-kind
term (the only kind-sensitive parts are `D5pp` root anchoring and `D11.12`'s lowering split, both
at the root position). The invariants clause of `D11.12` records that I1-I3 quantify over covers
(which do not see the split) and I4 holds per event, matching FS-SUB-6's spec-pinned per-event
frame (GraphQL Section 6.2.3.2). Non-trigger fetches are `query` documents by `D11.7`-clause reference
(`D11` clause 7; FS-ENT-5), and depend on the trigger by `D11.2` (their inputs are event-payload
values) -- FS-SUB-4/5 (OURS) already cite these.
*Stage 2.* Assert exactly one `subscription`-keyword document per plan, targeting a declaring
subgraph; all other fetches `query`-keyword and transitively trigger-dependent; below-root
structure byte-comparable to the query-form plan of the same selection (the discriminating
assertion for FS-SUB-3).

### 2.15 FS-DEF-1, FS-DEF-3, FS-DEF-5, FS-DEF-7 -- `@defer`

The model contains no deferral construct: `D2`/`D3` are `@defer`-blind, so the realized planner
computes $\mathrm{plan}(\varepsilon(Q))$ -- the **defer erasure** realization (see MG-3, Section 4, for
the one-sentence `D2` amendment this pass recommends).

> **Realization update (M3 defer wave, landed in the same release as this pass):** `D11.13`
> replaced the erasure realization for QUERY operations -- planv2 now honors `@defer` (defer scope
> on `D3` obligations, scope-variant fetch grouping, v1 `DeferResponsePlan` encoding). The
> dispositions below are written against the erasure realization and remain correct as
> propositions; their REALIZATION notes update as follows. FS-DEF-3 (AX-DEF-WIRE) is no longer
> vacuous: it now binds, and the realization discharges it via `resolve.DeferDescriptor`
> path/label bookkeeping (differential descriptor oracle + executed-truth frame oracle,
> SCOREBOARD defer-wave section). FS-DEF-7's flagged re-derivation obligation is discharged:
> the defer scope is recorded ONLY for query operations (`obligation.Build`'s operation-kind
> gate), so subscriptions (and mutations) keep the erasure realization by construction -- exactly
> FS-DEF-1's prescribed handling. FS-DEF-2/4/5/6 hold non-vacuously per `D11.13`'s normative
> statement (scope variants partition the fetch set; the response tree is the union object; the
> cover is scope-blind). MG-3's recommended `D2` sentence is superseded for `@defer` by `D11.13`
> (which states the treatment normatively); it stands for other executable directives.

**FS-DEF-1 (advisory; ignoring is conforming). Disposition: AXIOM (AX-DEF-ADV).**
Zero deductive distance: the erasure realization is *conforming because* the RFC draft says
deferral is advisory; nothing router-internal makes it so. Under AX-DEF-ADV the erasure
realization satisfies the whole section: FS-DEF-2 and FS-DEF-4 (OURS) hold with the deferred
partition empty (initial set = all fetches; union = the one response, which is I4's tree), and
FS-DEF-6 holds since there is nothing re-routed.
**FS-DEF-3 (payload path/label bookkeeping). Disposition: AXIOM (AX-DEF-WIRE)**, vacuously
satisfied by the erasure realization (no honored fragments); binding on any future defer-honoring
realization. Not derivable: it is the client-facing wire contract.
**FS-DEF-5 (rounding to fetch boundaries). Disposition: DERIVED** (from AX-DEF-ADV +
AX-DEF-WIRE + FS-DEF-2/4). *Derivation.* Let a plan honor deferred-fragment set $F$ with initial
fetch set $X$. (a) Moving any deferred selection already produced by $X$ into the initial payload
only shrinks the deferred remainder: FS-DEF-2's partition condition is preserved ($X$ still covers
all non-deferred selections and now more), and FS-DEF-4's union is unchanged -- admissible by
AX-DEF-ADV's per-selection advisory license. (b) Splitting one fragment's data across several
payloads at fetch boundaries conforms to AX-DEF-WIRE's "one or more payloads" with the same
$(path, label)$ address and the same union. Hence *every* rounding of deferral to fetch
boundaries is conforming -- the MAY is a closure property, with the erasure realization as the
extremal point (everything rounds to initial).
**FS-DEF-7 (no deferral inside subscriptions). Disposition: DERIVED** (trivially, from the
erasure realization + `D11.12`). *Derivation.* The realized planner honors no `@defer` anywhere,
subscriptions included -- FS-DEF-7's MUST NOT is satisfied by construction, and the treatment is
exactly FS-DEF-1's prescribed handling of a client whose validation admitted the directive.
*Honesty:* this is a realization-level derivation. A future defer-honoring planner must re-derive
it from `D11.12`'s per-event single-response contract (each event's response is one payload;
GraphQL Section 6.2.3.2) -- recorded so the derivation is not silently over-trusted past its premise.
*Stage 2.* For every defer-bearing operation: assert the emitted plan equals
$\mathrm{plan}(\varepsilon(Q))$ byte-identically (the erasure realization pinned); assert
subscriptions with `@defer` plan identically to their erased form. When a defer-honoring
realization lands, replace with FS-DEF-2/3/4/6 oracles (partition producibility, payload
addressing, union equality, routing invariance).

---

## 3. Underdetermined-folklore findings

The exercise's most valuable outputs: places where the observed reference behavior does not
determine a semantics, and this model had to *choose* -- each choice now a named, revisable axiom
rather than an unexamined imitation.

### 3.1 FS-REQ-9 -- hard vs conditional fragment-conditioned requires inputs (the genuine case)

No document states the semantics; the reference implementation's permissiveness has not been
decisively probed (a probe requires a schema where the conditioned coordinate is unresolvable --
Section 2.5's Stage-2 probe case); `FEDERATION_SEMANTICS.md` already flagged it *unverified convention*.
Formalized here as **AX-REQ-COND** (conditional-input reading) with: the forcing argument (hard
inputs make mixed populations unplannable), the residual under our reading (a $B$-typed instance
may reach the resolver without the conditioned value when no gather route produced it), and the
model gap that the choice currently lives in implementation comments, not in `D7pp` (MG-2). This
is the single highest-value target for a future specification revision to pin.

### 3.2 FS-OVR-2 -- what the losing subgraph retains (underdetermined *input*, not planner folklore)

The retained-capability set varies with composer version. The formalization relocates the
underdetermination to where it belongs: the planner-side semantics are fully determined
*conditional on `D1`* (Section 2.8), so the open question is a composition-output contract question, out
of planner scope by this document's boundary. A conforming planner must be correct for whatever
capability set the composer emits -- which the FL-OVR reduction guarantees structurally.

### 3.3 FS-DEF-* -- axioms resting on an unratified draft

AX-DEF-ADV and AX-DEF-WIRE quote a draft that may change before ratification
(`FEDERATION_SEMANTICS.md` Section 17 flags FS-DEF-7's validation rule specifically). The axioms are
marked revisable; the derivations built on them (FS-DEF-5, FS-DEF-7) fail loudly with them, which
is the correct dependency direction.

---

## 4. Model gaps exposed (registered, not papered over)

Found by attempting the derivations; each names the honest consequence and the recommended
amendment. These are registered here as the record of this pass; `FORMAL_SPEC.md` amendments are
Stage-2-adjacent work, deliberately not made in this docs-only pass.

- **MG-1 -- `D8` has no fragment-conditioned provided-coordinate clause.** `D8` constructs
  scope-internal `Field`/`Descent` edges only; a `@provides` FieldSet node that is an inline
  fragment (`"... on Book { title }"`, `"animals { ... on Dog { name } }"`) gets no scope-internal
  `TypeMove`, and the builder emits no in-scope alternative for coordinates beneath it
  (`v2/pkg/engine/planv2/hypergraph/builder.go`, `emitProvidedFields` -- no `Frag` branch; the
  fragment node falls through as a dead label-less node). Consequence: FS-PROV-1's inline grant is
  unavailable for those coordinates; plans stay sound/complete via the owning route (L6
  monotonicity + FS-PROV-3), at fetch-count cost -- the `provides-on-union`/`provides-on-interface`
  witnesses pass on exactly that basis. *Recommended amendment:* `D8` clause adding scope-internal
  `TypeMove` edges per fragment type condition (gated by $\mathrm{Mem}_s$), with the L6 monotone-
  extension proof note extending additively.
- **MG-2 -- `D7pp`(4) is silent on fragment-conditioned requires coordinates. CLOSED
  (reachability-gaps wave).** The conditional-input semantics (AX-REQ-COND) were realized only in
  the implementation (`v2/pkg/engine/planv2/hypergraph/builder.go`, `reqCoordChoices` -- fragment
  nodes contribute the empty tail-choice). `D7pp`(4) now carries the recommended sentence adopting
  AX-REQ-COND's statement, PLUS the validity clause the Stage-2 probe forced: the gathering
  document renders a conditioned branch only where its coordinates are resolvable -- a coordinate
  resolvable *nowhere* renders *nowhere* (FS-PLAN-1 dominance; the un-pruned render was an invalid
  fetch document, witness `FS-REQ-9/requires-conditional/probe-unresolvable`). Realized at edge
  emission (`filterConditionalRequires` -- `Edge.Requires` carries the pruned selection; an
  unconditional composite left childless renders `{ __typename }`). AX-REQ-COND's Section 1.2 statement
  is updated to match.
- **MG-3 -- `D2` does not state the `@defer` treatment.** The defer erasure $\varepsilon$ is
  implicit (the obligation builder consumes fields regardless of fragment-level executable
  directives beyond `@skip`/`@include`). *Recommended amendment:* one `D2` sentence -- "executable
  directives other than `@skip`/`@include` are erased by normalization; for `@defer` this is the
  AX-DEF-ADV-licensed null realization" -- so FS-DEF-1's conformance rests on stated text.
  *Superseded for `@defer` (M3 defer wave):* `D11.13` now states the `@defer` treatment
  normatively (query operations honor it via scope-variant partitioning; mutations/subscriptions
  keep the erasure). The amendment stands as a recommendation for OTHER executable directives.

None of the three affects I1-I4 as proven: MG-1/MG-2 sit in additive/completeness-favoring
territory (the gaps only *withhold* optional routes or *relax* tails), and MG-3 is a statement
gap, not a behavior gap.

---

## 5. The updated census

Disposition of the 23 FOLKLORE propositions:

| Disposition | n | Propositions |
|---|---|---|
| **DERIVED** | **16** | FS-REQ-6, FS-REQ-8, FS-PROV-4 (with MG-1), FS-EXT-2, FS-OVR-2, FS-OVR-3, FS-OVR-5, FS-IFO-4, FS-IFO-5, FS-IFO-7, FS-ABS-4, FS-ABS-8, FS-SUB-2, FS-SUB-3, FS-DEF-5, FS-DEF-7 |
| **AXIOM** | **6** | FS-KEY-4 (AX-REP-1), FS-ENT-7 (AX-REP-2), FS-IFO-2 (AX-IFO-1), FS-REQ-9 (AX-REQ-COND), FS-DEF-1 (AX-DEF-ADV), FS-DEF-3 (AX-DEF-WIRE) |
| **REJECTED-CONTINGENT** | **1** | FS-KEY-6 (this model: `C.2`/`C.4`; divergence-policy class of DV-009) |

Named axioms: 9 total -- 7 environment (AX-REP-1, AX-REP-2, AX-EXT-KEY, AX-IFO-1, AX-IFO-2,
AX-DEF-ADV, AX-DEF-WIRE), 2 policy (AX-ABS-LOCAL, AX-REQ-COND). Three of the nine (AX-EXT-KEY,
AX-IFO-2, AX-ABS-LOCAL) carry no AXIOM-dispositioned proposition of their own: they are premises
that make DERIVED dispositions honest instead of circular.

**Whole-document census.** With the 46 OURS propositions already derivation-cited in
`FEDERATION_SEMANTICS.md`, the reference document's authority claim becomes:

> **0% folklore.** Every behavior is spec-pinned (**30**), derived (**62** = 46 OURS + 16 from
> this pass), axiomatized (**6**, over 9 named axioms), or a registered divergence (**1**) --
> 99/99. No proposition rests on "the reference implementation does it" without either a
> derivation from the formal model or a named, justified axiom stating exactly what is being
> assumed and what would break without it.

The residue that is *not* closed -- and is recorded rather than hidden: the three model gaps of
Section 4, the `FEDERATION_SEMANTICS.md` Section 17 honest gaps that are composition- or draft-contingent
(FS-OVR-4's realization protocol, the mixed abstract position, FS-DEF-7's draft dependence), and
AX-REQ-COND's probe case awaiting a specification to pin it.

### Stage-2 note

Every disposition entry above ends with the generator assertion for its proposition; together
with `FEDERATION_SEMANTICS.md` Section 17's Stage-2 note (witness seeding, negative cases by
construction) these are the conformance-case generator's work orders. The axioms additionally
induce *harness contracts*: subgraph fixtures must themselves conform to AX-REP-1/2 and
AX-IFO-1/2 (a fixture rejecting extra representation fields or interface-typed representations is
testing the wrong side of the interface).
