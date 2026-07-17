-------------------------- MODULE PlannerSearch --------------------------
(***************************************************************************)
(* TLA+ model of Algorithm A (SETTLE, the Shortest B-Tree search) from     *)
(* v2/docs/planner-v2/FORMAL_SPEC.md, sections 4-7 (spec commit 4e741744,  *)
(* amended at e9578b8e; TentF implements the amended C.4 priority key).    *)
(*                                                                         *)
(* This is a bounded model of the SETTLE state machine (spec 6.1).  It     *)
(* faithfully reproduces:                                                  *)
(*   - the memo state pi[node] / back[node]           (FORMAL_SPEC 6.1)    *)
(*   - the AND-relaxation counters need[edge]         (FORMAL_SPEC 6.1)    *)
(*   - the ready frontier of edges whose tails are all settled            *)
(*   - the C.2 TREE-cost recurrence  pi(h(e)) = w(e) + SUM_t pi(t)         *)
(*                                                                         *)
(* W1 (single head per edge) is enforced by construction: every edge is a  *)
(* record with a single `head` and a SET of `tails`.  Static conditions    *)
(* (D7): every edge exists from the start; @key / @requires tails are      *)
(* ordinary tail nodes of the one EntityJump edge, settled by the same     *)
(* AND-relaxation (no separate sub-search).                                *)
(*                                                                         *)
(* EXTRACT-MIN semantics (spec 6.1 line 606): a Settle step may extract    *)
(* only a ready edge whose tentative value f(e) = w(e) + SUM_t pi(t) is    *)
(* MINIMAL among all ready edges.  Cost ties (C.4 steps 2-5) are NOT       *)
(* hard-coded: TLC explores every min-cost extraction order, and the       *)
(* invariants holding across all of them shows the settled pi table is     *)
(* order-independent under min-extraction -- the property the C.4           *)
(* determinism argument rests on.  See README.md "Determinism".            *)
(*                                                                         *)
(* Invariant / property <-> spec-number mapping (see README.md table):     *)
(*   SoundnessInv   <->  I1  (soundness; model shadow of the minimal-model *)
(*                            derivation: settled via existing edges only,  *)
(*                            all tails settled)                            *)
(*   OptimalityInv  <->  I3  (tree-optimality; settled pi equals the       *)
(*                            hand-computed tree-cost minima, ExpectedPi)   *)
(*   EventuallyPlans <-> I2  (completeness; every reachable goal is         *)
(*                            eventually settled)                           *)
(*   need init            spec 6.1 lines 598-601  (|T(e) \ Roots(H)|)       *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Nodes,       \* set of node identifiers (D4)
    Edges,       \* set of edge records [id, tails (SET), head, kind] (D5-D8, W1)
    Weights,     \* [edge-id -> Nat] : the tree weight w(e) (C.1)
    RootNode,    \* the single operation root r_op (pi = 0).  The spec's SETTLE
                 \* pre-settles the set Roots(H); both worked examples (and any
                 \* single-operation instance) have exactly one root, so the
                 \* model takes a single RootNode.  Generalizing to a root SET
                 \* only changes Init (settled/pi/need/ready seeds).
    Goals,       \* set of goal candidate nodes cand(G(O)) (D3) that must settle
    ExpectedPi   \* [Nodes -> Nat] : hand-computed tree-cost minima (C.2), the I3 shadow

VARIABLES
    settled,     \* subset of Nodes that have been settled
    pi,          \* [Nodes -> Nat] : settled tree-cost (Inf = not yet settled)
    back,        \* [Nodes -> Edges \cup {NilEdge}] : back-edge (traceback)
    need,        \* [EdgeIds -> Nat] : remaining unsettled non-root tails
    ready        \* subset of Edges : the frontier (every tail settled)

vars == <<settled, pi, back, need, ready>>

Inf     == 1000000    \* sentinel "infinity".  Sound for these instances because the
                      \* maximum reachable tree-pi is 6020 (instance 2's goal) << 1000000;
                      \* any instance added later must keep max pi well below this bound.
EdgeIds == { e.id : e \in Edges }
NilEdge == [id |-> "NIL", tails |-> {}, head |-> "NIL", kind |-> "Nil"]

\* w(e): the tree weight of edge e (C.1).
W(e) == Weights[e.id]

\* Sum of f[x] over x in S. TLA+ has no builtin sum; fold with CHOOSE.
RECURSIVE SumFn(_, _)
SumFn(f, S) == IF S = {}
               THEN 0
               ELSE LET x == CHOOSE y \in S : TRUE
                    IN f[x] + SumFn(f, S \ {x})

-------------------------------------------------------------------------------
(* Initial state: root pre-settled at pi=0; need[e] = |T(e) \ {RootNode}|      *)
(* (spec 6.1 line 598); ready = edges whose every tail is the root.            *)

Init ==
    /\ settled = {RootNode}
    /\ pi   = [n \in Nodes |-> IF n = RootNode THEN 0 ELSE Inf]
    /\ back = [n \in Nodes |-> NilEdge]
    /\ need = [id \in EdgeIds |->
                 LET e == CHOOSE ee \in Edges : ee.id = id
                 IN Cardinality(e.tails \ {RootNode})]
    /\ ready = { e \in Edges : (e.tails \ {RootNode}) = {} }

-------------------------------------------------------------------------------
(* SETTLE step (spec 6.1 lines 603-613): EXTRACT-MIN a ready edge e.           *)
(*  - only an edge whose tentative value f(e) = w(e) + SUM_t pi(t) is minimal  *)
(*    among ALL ready edges may be extracted (spec line 606's EXTRACT-MIN;     *)
(*    C.4 step 1).  Cost TIES are left to TLC to explore in every order.      *)
(*  - if head already settled: drop it (the pseudocode's "continue").          *)
(*  - else settle head h with the C.2 tree relaxation, decrement need[] for    *)
(*    every edge with h in its tails, and push edges whose need hits 0.        *)

\* Tentative value of a ready edge.  Well-defined and finite: every edge in
\* `ready` has all tails settled, so no Inf sentinel enters the sum.
TentF(e) == W(e) + SumFn(pi, e.tails)

Settle ==
    \E e \in ready :
        /\ \A e2 \in ready : TentF(e) <= TentF(e2)   \* EXTRACT-MIN (C.4 step 1)
        /\ LET h == e.head IN
           IF h \in settled
           THEN /\ ready' = ready \ {e}
                /\ UNCHANGED <<settled, pi, back, need>>
           ELSE LET newSettled == settled \cup {h}
                    newNeed == [id \in EdgeIds |->
                                 IF \E ee \in Edges : (ee.id = id) /\ (h \in ee.tails)
                                 THEN need[id] - 1
                                 ELSE need[id]]
                    fired == { e2 \in Edges : /\ h \in e2.tails
                                              /\ newNeed[e2.id] = 0
                                              /\ e2.head \notin newSettled }
                IN /\ settled' = newSettled
                   /\ pi'   = [pi   EXCEPT ![h] = W(e) + SumFn(pi, e.tails)]
                   /\ back' = [back EXCEPT ![h] = e]
                   /\ need' = newNeed
                   /\ ready' = (ready \ {e}) \cup fired

\* Stutter once the frontier is empty (search complete) so behaviours are infinite.
Terminating == /\ ready = {}
               /\ UNCHANGED vars

Next == Settle \/ Terminating

Spec == Init /\ [][Next]_vars /\ WF_vars(Settle)

-------------------------------------------------------------------------------
(* Invariants and the liveness property.                                       *)

TypeOK ==
    /\ settled \subseteq Nodes
    /\ pi   \in [Nodes -> Nat]
    /\ back \in [Nodes -> Edges \cup {NilEdge}]
    /\ need \in [EdgeIds -> Nat]
    /\ ready \subseteq Edges

\* I1 shadow: every settled non-root node was derived by an EXISTING edge whose
\* head is that node and whose tails are ALL settled.  The structural clauses
\* (back[n] \in Edges, head/tails frontier discipline) carry I1's teeth.  The
\* final pi clause is a DEFINITIONAL consistency check only: it is the same
\* expression Settle assigns, so it can never fail on its own -- the numeric
\* burden (is pi the MINIMUM?) is carried by OptimalityInv, not here.
SoundnessInv ==
    \A n \in settled :
        \/ n = RootNode
        \/ /\ back[n] \in Edges
           /\ back[n].head = n
           /\ back[n].tails \subseteq settled
           /\ pi[n] = W(back[n]) + SumFn(pi, back[n].tails)

\* I3-tree shadow: every settled cost equals the hand-computed tree-cost minimum.
OptimalityInv ==
    \A n \in settled : pi[n] = ExpectedPi[n]

\* I2 shadow: every reachable goal is eventually settled (completeness).
EventuallyPlans == <>(Goals \subseteq settled)

-------------------------------------------------------------------------------
(* ========================================================================= *)
(* INSTANCE 1 -- Section 7.1: partial union with value-type members.         *)
(*   Query { wrapper { action { ...Common{c} ...OnlyA{a} ...OnlyB{b} } } }    *)
(*   Weights: w_f=1000, w_s=1 (w_d unused: no EntityJump here).               *)
(*   Expected progression 1000 -> 1001 -> 1002 -> 1003, with (Common,A).c and *)
(*   (Common,B).c both 1003 (the A/B tie resolved by C.4 in the cover step).  *)
(* ========================================================================= *)

PU_Root == "r"

PU_Nodes ==
    { "r",
      "QwrapA", "QwrapB", "WrapA", "WrapB",
      "ActFA", "ActFB", "ActA", "ActB",
      "ComA", "OnlyA", "ComB", "OnlyB",
      "cA", "aA", "cB", "bB" }

PU_Edges ==
    { [id |-> "eWrapA",  tails |-> {"r"},     head |-> "QwrapA", kind |-> "FieldEnter"],
      [id |-> "eWrapB",  tails |-> {"r"},     head |-> "QwrapB", kind |-> "FieldEnter"],
      [id |-> "eDescWA", tails |-> {"QwrapA"},head |-> "WrapA",  kind |-> "Descent"],
      [id |-> "eDescWB", tails |-> {"QwrapB"},head |-> "WrapB",  kind |-> "Descent"],
      [id |-> "eActA",   tails |-> {"WrapA"}, head |-> "ActFA",  kind |-> "FieldIn"],
      [id |-> "eActB",   tails |-> {"WrapB"}, head |-> "ActFB",  kind |-> "FieldIn"],
      [id |-> "eDescAA", tails |-> {"ActFA"}, head |-> "ActA",   kind |-> "Descent"],
      [id |-> "eDescAB", tails |-> {"ActFB"}, head |-> "ActB",   kind |-> "Descent"],
      [id |-> "eTmComA", tails |-> {"ActA"},  head |-> "ComA",   kind |-> "TypeMove"],
      [id |-> "eTmOnlyA",tails |-> {"ActA"},  head |-> "OnlyA",  kind |-> "TypeMove"],
      [id |-> "eTmComB", tails |-> {"ActB"},  head |-> "ComB",   kind |-> "TypeMove"],
      [id |-> "eTmOnlyB",tails |-> {"ActB"},  head |-> "OnlyB",  kind |-> "TypeMove"],
      [id |-> "ecA",     tails |-> {"ComA"},  head |-> "cA",     kind |-> "FieldIn"],
      [id |-> "eaA",     tails |-> {"OnlyA"}, head |-> "aA",     kind |-> "FieldIn"],
      [id |-> "ecB",     tails |-> {"ComB"},  head |-> "cB",     kind |-> "FieldIn"],
      [id |-> "ebB",     tails |-> {"OnlyB"}, head |-> "bB",     kind |-> "FieldIn"] }

PU_Weights ==
    ( "eWrapA"  :> 1000 @@ "eWrapB"  :> 1000 @@
      "eDescWA" :> 0    @@ "eDescWB" :> 0    @@
      "eActA"   :> 1    @@ "eActB"   :> 1    @@
      "eDescAA" :> 0    @@ "eDescAB" :> 0    @@
      "eTmComA" :> 1    @@ "eTmOnlyA":> 1    @@
      "eTmComB" :> 1    @@ "eTmOnlyB":> 1    @@
      "ecA"     :> 1    @@ "eaA"     :> 1    @@
      "ecB"     :> 1    @@ "ebB"     :> 1 )

PU_ExpectedPi ==
    ( "r"      :> 0    @@
      "QwrapA" :> 1000 @@ "QwrapB" :> 1000 @@
      "WrapA"  :> 1000 @@ "WrapB"  :> 1000 @@
      "ActFA"  :> 1001 @@ "ActFB"  :> 1001 @@
      "ActA"   :> 1001 @@ "ActB"   :> 1001 @@
      "ComA"   :> 1002 @@ "OnlyA"  :> 1002 @@
      "ComB"   :> 1002 @@ "OnlyB"  :> 1002 @@
      "cA"     :> 1003 @@ "aA"     :> 1003 @@
      "cB"     :> 1003 @@ "bB"     :> 1003 )

\* All four refinement leaves are reachable and must settle (I2).  Which of
\* them are COVERED is a post-settle D6/C.4 decision, out of scope for SETTLE.
\* __typename (also in G(O), spec 7.1) is likewise omitted: it is satisfied at
\* lowering with no hypergraph node of its own, so SETTLE never sees it.
PU_Goals == { "cA", "aA", "cB", "bB" }

(* Deliberately-wrong variant used ONCE to prove TLC can fail (brief Step 3).  *)
(* (Common,A).c is claimed to settle at 9999 instead of 1003.)  Not wired into *)
(* the committed .cfg; see README "Deliberate violation".                      *)
PU_ExpectedPi_WRONG ==
    [ PU_ExpectedPi EXCEPT !["cA"] = 9999 ]

(* ========================================================================= *)
(* INSTANCE 2 -- Section 7.2: entity jump requiring @requires + nested key.   *)
(*   Query { product { shippingEstimate } }; Product @key(id organization{id});*)
(*   B.shippingEstimate @requires(dimensions{length width height}).           *)
(*   Weights: w_f=1000, w_s=1, w_d=10; EntityJump weight = w_f + w_d = 1010.   *)
(*   The 5-tail EntityJump re-counts the enter-A fetch per tail => TREE-pi.    *)
(*   Expected pi((Product,B)) = 6019, pi((Product,B).shippingEstimate) = 6020. *)
(* ========================================================================= *)

EJ_Root == "r"

EJ_Nodes ==
    { "r", "QprodA", "ProdA", "pid", "porg", "OrgA", "oid",
      "pdim", "DimA", "len", "wid", "hei", "ProdB", "se" }

EJ_Edges ==
    { [id |-> "eProduct", tails |-> {"r"},      head |-> "QprodA", kind |-> "FieldEnter"],
      [id |-> "eDescP",   tails |-> {"QprodA"}, head |-> "ProdA",  kind |-> "Descent"],
      [id |-> "epid",     tails |-> {"ProdA"},  head |-> "pid",    kind |-> "FieldIn"],
      [id |-> "eporg",    tails |-> {"ProdA"},  head |-> "porg",   kind |-> "FieldIn"],
      [id |-> "eDescOrg", tails |-> {"porg"},   head |-> "OrgA",   kind |-> "Descent"],
      [id |-> "eoid",     tails |-> {"OrgA"},   head |-> "oid",    kind |-> "FieldIn"],
      [id |-> "epdim",    tails |-> {"ProdA"},  head |-> "pdim",   kind |-> "FieldIn"],
      [id |-> "eDescDim", tails |-> {"pdim"},   head |-> "DimA",   kind |-> "Descent"],
      [id |-> "elen",     tails |-> {"DimA"},   head |-> "len",    kind |-> "FieldIn"],
      [id |-> "ewid",     tails |-> {"DimA"},   head |-> "wid",    kind |-> "FieldIn"],
      [id |-> "ehei",     tails |-> {"DimA"},   head |-> "hei",    kind |-> "FieldIn"],
      [id |-> "eJump",    tails |-> {"pid","oid","len","wid","hei"},
                          head |-> "ProdB",  kind |-> "EntityJump"],
      [id |-> "eSE",      tails |-> {"ProdB"},  head |-> "se",     kind |-> "FieldIn"] }

EJ_Weights ==
    ( "eProduct" :> 1000 @@ "eDescP" :> 0 @@
      "epid" :> 1 @@ "eporg" :> 1 @@ "eDescOrg" :> 0 @@ "eoid" :> 1 @@
      "epdim" :> 1 @@ "eDescDim" :> 0 @@
      "elen" :> 1 @@ "ewid" :> 1 @@ "ehei" :> 1 @@
      "eJump" :> 1010 @@ "eSE" :> 1 )

EJ_ExpectedPi ==
    ( "r"     :> 0    @@
      "QprodA":> 1000 @@ "ProdA" :> 1000 @@
      "pid"   :> 1001 @@ "porg"  :> 1001 @@
      "OrgA"  :> 1001 @@ "oid"   :> 1002 @@
      "pdim"  :> 1001 @@ "DimA"  :> 1001 @@
      "len"   :> 1002 @@ "wid"   :> 1002 @@ "hei" :> 1002 @@
      "ProdB" :> 6019 @@ "se"    :> 6020 )

EJ_Goals == { "se" }

(* Deliberately-wrong variant for brief Step 3 (goal claimed at 6019 not 6020).*)
EJ_ExpectedPi_WRONG ==
    [ EJ_ExpectedPi EXCEPT !["se"] = 6019 ]

(* ========================================================================= *)
(* INSTANCE 3 -- Competing derivations (min-selection stressor).              *)
(*   Synthetic minimal instance whose whole point is that one node has TWO    *)
(*   incoming derivations at DIFFERENT costs, so EXTRACT-MIN is actually      *)
(*   exercised (instances 1 and 2 give every node exactly one incoming edge). *)
(*   Node "fx" is derivable directly in A (Field, f = 1001) AND via an        *)
(*   EntityJump detour through FooB (f = 2012).  The spec's EXTRACT-MIN       *)
(*   settles fx at the MINIMUM, 1001; a first-writer-wins extraction could    *)
(*   settle it at 2012 (the divergence the review demonstrated).  The losing  *)
(*   edge eViaB is discarded at fire time (the `fired` filter skips edges     *)
(*   whose head is already settled, so eViaB never enters `ready`) --          *)
(*   behaviorally equivalent to the pseudocode's push-then-"continue".        *)
(*   Weights: w_f=1000, w_s=1, w_d=10 (EntityJump = 1010), as in C.1 defaults.*)
(* ========================================================================= *)

CD_Root == "r"

CD_Nodes == { "r", "QfooA", "FooA", "fid", "FooB", "fx" }

CD_Edges ==
    { [id |-> "eFoo",    tails |-> {"r"},     head |-> "QfooA", kind |-> "FieldEnter"],
      [id |-> "eDescF",  tails |-> {"QfooA"}, head |-> "FooA",  kind |-> "Descent"],
      [id |-> "efid",    tails |-> {"FooA"},  head |-> "fid",   kind |-> "FieldIn"],
      [id |-> "eDirect", tails |-> {"FooA"},  head |-> "fx",    kind |-> "FieldIn"],
      [id |-> "eJump",   tails |-> {"fid"},   head |-> "FooB",  kind |-> "EntityJump"],
      [id |-> "eViaB",   tails |-> {"FooB"},  head |-> "fx",    kind |-> "FieldIn"] }

CD_Weights ==
    ( "eFoo" :> 1000 @@ "eDescF" :> 0 @@ "efid" :> 1 @@
      "eDirect" :> 1 @@ "eJump" :> 1010 @@ "eViaB" :> 1 )

\* Hand computation: QfooA/FooA = 1000; fid = 1001; fx = min(1000+1, 2011+1) = 1001
\* (direct Field beats the jump detour); FooB = 1010 + 1001 = 2011.
CD_ExpectedPi ==
    ( "r" :> 0 @@ "QfooA" :> 1000 @@ "FooA" :> 1000 @@
      "fid" :> 1001 @@ "FooB" :> 2011 @@ "fx" :> 1001 )

CD_Goals == { "fx", "FooB" }

(* Deliberately-wrong variant for the violation discipline: claims fx settles  *)
(* via the losing derivation (2012 instead of the minimum 1001).               *)
CD_ExpectedPi_WRONG ==
    [ CD_ExpectedPi EXCEPT !["fx"] = 2012 ]

===============================================================================
