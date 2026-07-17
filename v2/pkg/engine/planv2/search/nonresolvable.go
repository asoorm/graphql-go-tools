package search

// nonresolvable.go is the D10 fall-back's NARROWING guard (FORMAL_SPEC D10 amendment --
// provable-non-resolvability narrowing): the completeness fall-back re-admits a foreign-root route
// on the premise that the consistent route is merely missing from the MODEL (the class-A/B gaps --
// a better model would serve the goal). That premise is refutable from the schema itself when a
// goal's every candidate sits behind @key(resolvable: false) and is reachable only through its own
// subgraph's operation roots: no entity jump into the candidate may EVER be modelled, so every
// rescue route the fall-back could take is a provably wrong foreign-root fetch. For such a goal
// the fall-back is suppressed at both firing sites (root-pin, search.go; scoped-walk, scope.go)
// and planning fails loud with ErrNoValidPlan instead -- the honest outcome (the reference gateway
// cannot fetch the field either; witness: audit non-resolvable-interface-object/case-02).
//
// CONSERVATISM (the customer-safety direction): suppression requires PROOF over ALL candidates.
// Any candidate that is scoped, non-field, keyless, headed by ANY resolvable key, missing key
// metadata (hand-built graphs), or reachable through any non-root structure (a parent descent an
// eventual jump could feed, an abstract TypeMove) keeps the fall-back, byte-identical. This file
// NARROWS the fall-back; its retirement gate (FORMAL_SPEC D10 typed-loud amendment) is unchanged.

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"

// reasonProvablyNonResolvable is the ErrNoValidPlan reason for a suppressed fall-back rescue.
const reasonProvablyNonResolvable = "provably non-resolvable: every candidate sits behind @key(resolvable: false), reachable only through its own subgraph's operation roots"

// provablyNonResolvable reports whether EVERY candidate serving node of a goal is provably
// non-resolvable (both amendment conditions hold for each). False on an empty candidate set --
// the goal loop's plain unreachable branch owns that case, not this guard.
func provablyNonResolvable(h *hypergraph.Hypergraph, cand []hypergraph.NodeID) bool {
	if len(cand) == 0 {
		return false
	}
	for _, v := range cand {
		if !candidateProvablyNonResolvable(h, v) {
			return false
		}
	}
	return true
}

// candidateProvablyNonResolvable checks one candidate against both amendment conditions:
//
//  1. schema-level jump prohibition -- the candidate's enclosing type declares @key(s) in its
//     subgraph and every declaration heading it carries resolvable:false, so no entity jump into
//     it can ever be modelled (Hypergraph.OnlyNonResolvableKeys);
//  2. structural root-only entry -- every derivation of the candidate runs through the plain
//     object node (T,s), which is entered ONLY by Descent from field nodes anchored directly on
//     an operation root: the candidate's subgraph is enterable solely through its own roots.
//
// Everything not exactly this shape returns false (fall-back kept).
func candidateProvablyNonResolvable(h *hypergraph.Hypergraph, v hypergraph.NodeID) bool {
	n := h.Node(v)
	if n.Kind != hypergraph.NodeField || n.Scope != "" || n.Subgraph == 0 {
		return false // only a plain field candidate has the proven shape
	}
	if !h.OnlyNonResolvableKeys(n.Type, n.Subgraph) {
		return false // no key, a resolvable key, or no metadata: nothing is proven
	}
	incoming := h.Incoming(v)
	if len(incoming) == 0 {
		return false // an unreachable candidate is the goal loop's business, not this guard's
	}
	for _, e := range incoming {
		if h.EdgeKind(e) != hypergraph.EdgeField {
			return false
		}
		tails := h.EdgeTails(e)
		if len(tails) != 1 {
			return false
		}
		obj := h.Node(tails[0])
		if obj.Kind != hypergraph.NodeObject || obj.Scope != "" ||
			obj.Type != n.Type || obj.Subgraph != n.Subgraph {
			return false // scoped/requires variants or root-typed fields: not the proven shape
		}
		if !objectRootOnlyEntry(h, tails[0]) {
			return false
		}
	}
	return true
}

// objectRootOnlyEntry reports whether every entry into an object node is a Descent from a field
// node anchored directly on an operation root (condition 2's structural core). Any TypeMove or
// EntityJump entry, or a Descent from a non-root field, disproves root-only entry.
func objectRootOnlyEntry(h *hypergraph.Hypergraph, obj hypergraph.NodeID) bool {
	for _, e := range h.Incoming(obj) {
		if h.EdgeKind(e) != hypergraph.EdgeDescent {
			return false
		}
		for _, t := range h.EdgeTails(e) { // a Descent edge has one tail: the descending field node
			if !fieldRootAnchored(h, t) {
				return false
			}
		}
	}
	return true
}

// fieldRootAnchored reports whether every derivation of a field node enters from an operation-root
// node (the field is a root-operation field and nothing else). A field node with no derivation at
// all is unreachable -- nothing is proven, so it answers false (conservative).
func fieldRootAnchored(h *hypergraph.Hypergraph, f hypergraph.NodeID) bool {
	incoming := h.Incoming(f)
	if len(incoming) == 0 {
		return false
	}
	for _, e := range incoming {
		if h.EdgeKind(e) != hypergraph.EdgeField {
			return false
		}
		for _, t := range h.EdgeTails(e) {
			if h.NodeKind(t) != hypergraph.NodeRoot {
				return false
			}
		}
	}
	return true
}
