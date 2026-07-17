package conformance

// families_abstract.go -- the abstract-type scenario families (FEDERATION_SEMANTICS.md Section 10;
// FEDERATION_SEMANTICS_FORMAL.md Section 2.11-2.12):
//
//	abstract-narrowing  FS-ABS-4/5/6/7 + AX-ABS-LOCAL -- seeded partial unions: value-type members
//	                    outside the route-scoped intersection are fetched NOWHERE while their
//	                    gates stay in the response shape; dead members are never fetched; entity
//	                    members are NOT narrowed; unreachable declaring subgraphs do not narrow.
//	abstract-explosion  FS-ABS-8 -- the interface-unresolvable member fan-out (type explosion).
//	abstract-typename   FS-ABS-2/3 -- __typename-only selections still obligate a fetch.
//	abstract-mismatch   FS-ABS-12 -- per-subgraph output-type divergence at one position.
import "fmt"

func init() {
	register(Family{
		Name:         "abstract-narrowing",
		Propositions: []string{"FS-ABS-1", "FS-ABS-3", "FS-ABS-4", "FS-ABS-5", "FS-ABS-6", "FS-ABS-7"},
		Generate:     genAbstractNarrowing,
	})
	register(Family{
		Name:         "abstract-explosion",
		Propositions: []string{"FS-ABS-8", "FS-ABS-1"},
		Generate:     genAbstractExplosion,
	})
	register(Family{
		Name:         "abstract-typename",
		Propositions: []string{"FS-ABS-2"},
		Generate:     genAbstractTypename,
	})
	register(Family{
		Name:         "abstract-mismatch",
		Propositions: []string{"FS-ABS-12"},
		Generate:     genAbstractMismatch,
	})
}

// genAbstractNarrowing seeds the Section 10 worked-example shape: subgraphs A and B both declare the
// shareable parent field yielding `union Action`, with per-subgraph member sets
// {Common, OnlyA} / {Common, OnlyB}. Scenarios:
//
//	value-members    all members value types -- OnlyA/OnlyB narrowed (FS-ABS-4), Common fetched;
//	                 the narrowed selections stay in the response shape (FS-PLAN-3).
//	entity-member    OnlyA carries its own @key and its field resolves in A only -- individually
//	                 routable, NOT narrowed (FS-ABS-5); its fragment appears only in fetches to a
//	                 subgraph where it is possible (FS-ABS-1/7).
//	route-scoped     a THIRD subgraph declares the union with a smaller member set but has NO
//	                 producing route (resolvable:false key, unrequested root) -- it must not
//	                 narrow (FS-ABS-4's route-scoping; partial-union/case-02 class).
//	unobtainable-key-ghost  the REGISTERED residual of the same class (triage: planv2-gap): the
//	                 ghost's key is resolvable:TRUE but unobtainable at the requested position --
//	                 condition-blind jump transport admits it and over-narrows (M3 review I-1).
func genAbstractNarrowing(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	build := func(scenario string, entityMember, withGhost bool) GeneratedCase {
		primary := "FS-ABS-4"
		if entityMember {
			primary = "FS-ABS-5"
		}
		id := caseID(primary, "abstract-narrowing", scenario, seed)
		r := newRNG(id, seed)
		n := namer{r}
		cField, aField, bField := n.field("c"), n.field("a"), n.field("b")

		memberTypes := func(inA bool) []*Type {
			common := &Type{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}}
			if inA {
				only := &Type{Name: "OnlyA", Kind: KindObject, Fields: []Field{{Name: aField, Type: "String"}}}
				if entityMember {
					only.Keys = []Key{{Fields: "oid"}}
					only.Fields = append([]Field{{Name: "oid", Type: "ID!"}}, only.Fields...)
				}
				return []*Type{common, only}
			}
			return []*Type{common, {Name: "OnlyB", Kind: KindObject, Fields: []Field{{Name: bField, Type: "String"}}}}
		}

		mkSub := func(name string, inA bool) SubgraphSpec {
			members := []string{"Common", "OnlyB"}
			if inA {
				members = []string{"Common", "OnlyA"}
			}
			return SubgraphSpec{Name: name, Types: append([]*Type{
				{Name: "Query", Kind: KindObject,
					Fields: []Field{{Name: "wrapper", Type: "Wrapper", Shareable: true}}},
				{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
				{Name: "Action", Kind: KindUnion, Members: members},
			}, memberTypes(inA)...)}
		}

		subs := []SubgraphSpec{mkSub("suba", true), mkSub("subb", false)}
		if entityMember {
			// OnlyA is an entity: declare its stub (key only) in subb so B-origin instances are
			// reconcilable -- the FS-ABS-5 individually-routable shape.
			subs[1].Types = append(subs[1].Types, &Type{
				Name: "OnlyA", Kind: KindObject, Keys: []Key{{Fields: "oid"}},
				Fields: []Field{{Name: "oid", Type: "ID!"}},
			})
			// and subb's union then admits OnlyA as possible at the position
			subs[1].Types[2].Members = []string{"Common", "OnlyA", "OnlyB"}
		}
		ghostLeaf := n.field("g")
		if withGhost {
			// The ghost declares the parent + its own members but is unreachable (its only key is
			// resolvable:false; its root is never requested): it must not narrow the reachable
			// intersection (FS-ABS-4 route-scoping), and its EXCLUSIVE member GhostOnly is a DEAD
			// member at the position (FS-ABS-6): fetched nowhere, rendered per shape.
			subs = append(subs, SubgraphSpec{Name: "ghost", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "ghostEntry", Type: "Wrapper"}}},
				{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id", NonResolvable: true}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
				{Name: "Action", Kind: KindUnion, Members: []string{"OnlyB", "GhostOnly"}},
				{Name: "OnlyB", Kind: KindObject, Fields: []Field{{Name: bField, Type: "String", Shareable: true}}},
				{Name: "GhostOnly", Kind: KindObject, Fields: []Field{{Name: ghostLeaf, Type: "String"}}},
			}})
		}

		op := fmt.Sprintf("{ wrapper { action { __typename ... on Common { %s } ... on OnlyA { %s } ... on OnlyB { %s } } } }",
			cField, aField, bField)
		if withGhost {
			op = fmt.Sprintf("{ wrapper { action { __typename ... on Common { %s } ... on OnlyA { %s } ... on OnlyB { %s } ... on GhostOnly { %s } } } }",
				cField, aField, bField, ghostLeaf)
		}
		expected := fmt.Sprintf(`{"data": {"wrapper": {"action": {"__typename": "Common", %q: "x"}}}}`, cField)

		oracles := []Oracle{
			OracleDocContains("", "... on Common"),
			// FS-ABS-3: __typename selected for the abstract position.
			OracleDocContains("", "__typename"),
		}
		if entityMember {
			// FS-ABS-5: the entity member is NOT narrowed; its fragment appears only in fetches
			// to a subgraph where the member is possible AND its field resolves (suba; FS-ABS-1).
			//
			// BOUNDARY -- ADJUDICATED (reachability-gaps wave; FS-ABS-4's boundary clause): with an
			// entity member (OnlyA) in the position's member population, instances carry
			// reconcilable identity, so the FS-ABS-4 value-intersection premise ("no identity
			// anywhere") fails and FS-ABS-7 governs the subset-possible value member: OnlyB
			// (declared only by subb) is DISTRIBUTED -- fetched, and only through a subgraph where
			// it is possible (subb). In the all-value scenario the same member is narrowed
			// (FS-ABS-4). The two readings partition on the entity-presence test, deliberately.
			oracles = append(oracles,
				OracleDocContains("", "... on OnlyA"),
				OracleFieldServedBy(aField, "suba"),
				OracleNoDocContains("suba", "... on OnlyB"),
				OracleDocContains("subb", "... on OnlyB"),
				OracleFieldServedBy(bField, "subb"),
			)
		} else {
			// FS-ABS-4: both exclusive members narrowed -- fetched nowhere, gates in shape.
			oracles = append(oracles,
				OracleNoDocContains("", "... on OnlyA"),
				OracleNoDocContains("", "... on OnlyB"),
				OracleResponseShapeHas("wrapper.action."+aField),
				OracleResponseShapeHas("wrapper.action."+bField),
			)
		}
		if withGhost {
			// FS-ABS-6: the ghost-exclusive member is DEAD at the position -- no fetch anywhere.
			oracles = append(oracles,
				OracleNoDocContains("", "... on GhostOnly"),
				OracleResponseShapeHas("wrapper.action."+ghostLeaf),
			)
		}

		props := []string{primary, "FS-ABS-1", "FS-ABS-3", "FS-PLAN-3"}
		if entityMember {
			props = append(props, "FS-ABS-7")
		}
		if withGhost {
			props = append(props, "FS-ABS-6")
		}
		return GeneratedCase{
			ID:           id,
			Propositions: props,
			Family:       "abstract-narrowing",
			Seed:         seed,
			Case: BuildAuditCase(scenario, "abstract-narrowing",
				Schema{Subgraphs: subs}, op, expected),
			Oracles: oracles,
		}
	}

	out = append(out,
		build("value-members", false, false),
		build("entity-member", true, false),
		build("route-scoped-ghost", false, true),
		buildUnobtainableKeyGhost(seed),
		buildDoubleGhost(seed),
		buildGhostCycle(seed),
		buildPartialCompositeKeyGhost(seed),
		buildTwoHopNarrower(seed),
	)
	return out
}

// buildUnobtainableKeyGhost is the M3 final-review I-1 witness (triage-REGISTERED planv2-gap --
// see triageRegister and the DIVERGENCES conformance-findings entry): the route-scoped-ghost
// class with the ghost admitted via CONDITION-BLIND ENTITY-JUMP TRANSPORT instead of a root.
// Three subgraphs, composition-valid:
//
//	suba   owns the requested root `Query.wrapper`, entity Wrapper @key(id), the abstract field
//	       `Wrapper.action`, and BOTH value members (Common.c, OnlyB.b) -- the only real origin.
//	subc   Wrapper @key(gid), rooted at a DIFFERENT position (`Query.otherWrapper`, unrequested).
//	ghost  Wrapper @key(gid) resolvable:TRUE (so, unlike route-scoped-ghost, it DOES enter
//	       jumpHeadSubs), declares `Wrapper.action` with members {Common} only, NO roots.
//
// `gid` is UNOBTAINABLE at the requested position: suba has no gid and no jump into subc or
// ghost exists from suba -- ghost can never supply a `Query.wrapper` parent. Correct behavior
// (FS-ABS-4 route-scoping, per-position): ghost is not position-capable, the intersection is
// suba's own member set, and `... on OnlyB { b }` is fetched from suba. Current behavior:
// `positionSubgraphs` (obligation/narrow.go) admits ghost at the `Wrapper.action` level via the
// condition-blind jumpHeadSubs transport and verdict 2 narrows OnlyB away -- a response-only null
// with ZERO fallbacks while the only real origin resolves it (silent wrong data, the FS-PLAN-6/L7
// class). The id-keyed (genuinely position-capable) ghost twin narrows CORRECTLY per FS-ABS-4, so
// this case discriminates real over-narrowing from prescribed narrowing.
func buildUnobtainableKeyGhost(seed uint64) GeneratedCase {
	scenario := "unobtainable-key-ghost"
	id := caseID("FS-ABS-4", "abstract-narrowing", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	cField, bField := n.field("c"), n.field("b")

	subs := []SubgraphSpec{
		{Name: "suba", Types: []*Type{
			{Name: "Query", Kind: KindObject,
				Fields: []Field{{Name: "wrapper", Type: "Wrapper"}}},
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common", "OnlyB"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
			{Name: "OnlyB", Kind: KindObject, Fields: []Field{{Name: bField, Type: "String"}}},
		}},
		{Name: "subc", Types: []*Type{
			{Name: "Query", Kind: KindObject,
				Fields: []Field{{Name: "otherWrapper", Type: "Wrapper"}}},
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "gid"}},
				Fields: []Field{{Name: "gid", Type: "ID!"}}},
		}},
		{Name: "ghost", Types: []*Type{ // no roots -- reachable only by the gid jump
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "gid"}},
				Fields: []Field{{Name: "gid", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
		}},
	}

	op := fmt.Sprintf("{ wrapper { action { __typename ... on Common { %s } ... on OnlyB { %s } } } }",
		cField, bField)
	expected := fmt.Sprintf(`{"data": {"wrapper": {"action": {"__typename": "Common", %q: "x"}}}}`, cField)

	return GeneratedCase{
		ID:           id,
		Propositions: []string{"FS-ABS-4", "FS-ABS-1", "FS-ABS-3", "FS-PLAN-3"},
		Family:       "abstract-narrowing",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "abstract-narrowing",
			Schema{Subgraphs: subs}, op, expected),
		Oracles: []Oracle{
			OracleDocContains("", "__typename"),
			OracleDocContains("", "... on Common"),
			// The assertion the defect breaks: OnlyB's only real origin (suba) must fetch it --
			// the position-incapable ghost must not join the intersection.
			OracleDocContains("suba", "... on OnlyB"),
			OracleFieldServedBy(bField, "suba"),
		},
	}
}

// buildDoubleGhost is the unobtainable-key-ghost class CHAINED through two ghosts (M4 blockers
// wave; the D6pppp adversarial variant the M3 reviewer's "subtler than the first fix" warning
// demanded): the narrowing ghost (ghost2, members {Common} only) is fully REACHABLE -- subc's
// unrequested root carries ghost1's key k1, ghost1 carries ghost2's key k2 -- so D6p route-scoping
// admits it and a one-hop obtainability check would too (ghost2's key IS carried by the reachable
// ghost1). But at the REQUESTED position neither key is obtainable: suba carries neither k1 nor
// k2, so no chain seeded at the position's real holders ever enters the ghost pair. The D6pppp
// least fixpoint (seeded {suba}) admits neither; condition-blind head admission admitted ghost2
// and silently narrowed OnlyB away (measured red under the old admission). Correct behavior: the
// intersection is suba's own member set and `... on OnlyB` is fetched from suba.
func buildDoubleGhost(seed uint64) GeneratedCase {
	scenario := "double-ghost"
	id := caseID("FS-ABS-4", "abstract-narrowing", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	cField, bField := n.field("c"), n.field("b")

	subs := []SubgraphSpec{
		{Name: "suba", Types: []*Type{
			{Name: "Query", Kind: KindObject,
				Fields: []Field{{Name: "wrapper", Type: "Wrapper"}}},
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common", "OnlyB"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
			{Name: "OnlyB", Kind: KindObject, Fields: []Field{{Name: bField, Type: "String"}}},
		}},
		{Name: "subc", Types: []*Type{ // seeds the ghost chain's reachability -- at an UNREQUESTED position
			{Name: "Query", Kind: KindObject,
				Fields: []Field{{Name: "otherWrapper", Type: "Wrapper"}}},
			{Name: "Wrapper", Kind: KindObject,
				Fields: []Field{{Name: "k1", Type: "ID!"}}},
		}},
		{Name: "ghost1", Types: []*Type{ // hop 1: enterable from subc by k1; carries ghost2's key
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "k1"}},
				Fields: []Field{{Name: "k1", Type: "ID!"}, {Name: "k2", Type: "ID!"}}},
		}},
		{Name: "ghost2", Types: []*Type{ // hop 2: the narrowing ghost -- reachable, position-unobtainable
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "k2"}},
				Fields: []Field{{Name: "k2", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
		}},
	}

	op := fmt.Sprintf("{ wrapper { action { __typename ... on Common { %s } ... on OnlyB { %s } } } }",
		cField, bField)
	expected := fmt.Sprintf(`{"data": {"wrapper": {"action": {"__typename": "Common", %q: "x"}}}}`, cField)

	return GeneratedCase{
		ID:           id,
		Propositions: []string{"FS-ABS-4", "FS-ABS-1", "FS-ABS-3", "FS-PLAN-3"},
		Family:       "abstract-narrowing",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "abstract-narrowing",
			Schema{Subgraphs: subs}, op, expected),
		Oracles: []Oracle{
			OracleDocContains("", "__typename"),
			OracleDocContains("", "... on Common"),
			OracleDocContains("suba", "... on OnlyB"),
			OracleFieldServedBy(bField, "suba"),
		},
	}
}

// buildGhostCycle pins the MUTUAL-ADMISSION CYCLE shape (two rootless ghosts, each holding the
// key field the OTHER's resolvable:true key needs -- no root seeds either). This shape is caught
// UPSTREAM of transport admission: D6p optimistic reachability is itself a least fixpoint, so the
// unrooted cycle never becomes reachable and neither ghost enters the level's field-global set
// (verified: the case passes under condition-BLIND transport too). Pinned as a regression guard
// for that reachability property -- weakening optimisticReachable to a non-fixpoint
// approximation would surface here as an over-narrowed OnlyB.
func buildGhostCycle(seed uint64) GeneratedCase {
	scenario := "ghost-cycle"
	id := caseID("FS-ABS-4", "abstract-narrowing", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	cField, bField := n.field("c"), n.field("b")

	ghost := func(name, ownKey, otherKey string) SubgraphSpec {
		return SubgraphSpec{Name: name, Types: []*Type{ // no roots -- "reachable" only via the cycle
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: ownKey}},
				Fields: []Field{
					{Name: ownKey, Type: "ID!"}, {Name: otherKey, Type: "ID!"},
					{Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
		}}
	}
	subs := []SubgraphSpec{
		{Name: "suba", Types: []*Type{
			{Name: "Query", Kind: KindObject,
				Fields: []Field{{Name: "wrapper", Type: "Wrapper"}}},
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common", "OnlyB"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
			{Name: "OnlyB", Kind: KindObject, Fields: []Field{{Name: bField, Type: "String"}}},
		}},
		ghost("ghost1", "g1", "g2"),
		ghost("ghost2", "g2", "g1"),
	}

	op := fmt.Sprintf("{ wrapper { action { __typename ... on Common { %s } ... on OnlyB { %s } } } }",
		cField, bField)
	expected := fmt.Sprintf(`{"data": {"wrapper": {"action": {"__typename": "Common", %q: "x"}}}}`, cField)

	return GeneratedCase{
		ID:           id,
		Propositions: []string{"FS-ABS-4", "FS-ABS-1", "FS-ABS-3", "FS-PLAN-3"},
		Family:       "abstract-narrowing",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "abstract-narrowing",
			Schema{Subgraphs: subs}, op, expected),
		Oracles: []Oracle{
			OracleDocContains("", "__typename"),
			OracleDocContains("", "... on Common"),
			OracleDocContains("suba", "... on OnlyB"),
			OracleFieldServedBy(bField, "suba"),
		},
	}
}

// buildPartialCompositeKeyGhost is the unobtainable-key-ghost class with a PARTIALLY-obtainable
// composite key (the second D6pppp adversarial variant): the ghost's resolvable:true key is
// `id org`; the requested position's only real origin (suba) carries `id` but NOT `org`, so the
// only jump into the ghost is sourced from subc -- an unrequested different-position root that can
// never hold the instance at THIS position. Partial obtainability must not admit: the ghost's
// smaller member set stays out of the intersection and `... on OnlyB` is fetched from suba.
func buildPartialCompositeKeyGhost(seed uint64) GeneratedCase {
	scenario := "partial-composite-key-ghost"
	id := caseID("FS-ABS-4", "abstract-narrowing", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	cField, bField := n.field("c"), n.field("b")

	subs := []SubgraphSpec{
		{Name: "suba", Types: []*Type{
			{Name: "Query", Kind: KindObject,
				Fields: []Field{{Name: "wrapper", Type: "Wrapper"}}},
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common", "OnlyB"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
			{Name: "OnlyB", Kind: KindObject, Fields: []Field{{Name: bField, Type: "String"}}},
		}},
		{Name: "subc", Types: []*Type{ // the full key exists here -- at an UNREQUESTED position
			{Name: "Query", Kind: KindObject,
				Fields: []Field{{Name: "otherWrapper", Type: "Wrapper"}}},
			{Name: "Wrapper", Kind: KindObject,
				Fields: []Field{{Name: "id", Type: "ID!", Shareable: true}, {Name: "org", Type: "ID!"}}},
		}},
		{Name: "ghost", Types: []*Type{ // no roots; enterable only by the composite jump from subc
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id org"}},
				Fields: []Field{
					{Name: "id", Type: "ID!"}, {Name: "org", Type: "ID!"},
					{Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
		}},
	}

	op := fmt.Sprintf("{ wrapper { action { __typename ... on Common { %s } ... on OnlyB { %s } } } }",
		cField, bField)
	expected := fmt.Sprintf(`{"data": {"wrapper": {"action": {"__typename": "Common", %q: "x"}}}}`, cField)

	return GeneratedCase{
		ID:           id,
		Propositions: []string{"FS-ABS-4", "FS-ABS-1", "FS-ABS-3", "FS-PLAN-3"},
		Family:       "abstract-narrowing",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "abstract-narrowing",
			Schema{Subgraphs: subs}, op, expected),
		Oracles: []Oracle{
			OracleDocContains("", "__typename"),
			OracleDocContains("", "... on Common"),
			OracleDocContains("suba", "... on OnlyB"),
			OracleFieldServedBy(bField, "suba"),
		},
	}
}

// buildTwoHopNarrower pins D6pppp's LENIENT direction (the fixpoint must not under-admit): a
// subgraph genuinely position-capable only through a TWO-HOP key relay (suba -id-> mid -gid-> far)
// must STILL join the intersection and narrow -- `far` resolves the abstract field for instances
// relayed into it and declares only {Common}, so `... on OnlyB` is prescribed narrowing
// (FS-ABS-4: not declared by every parent-capable subgraph -- fetched NOWHERE, gate stays in the
// response shape). A transport admission that only looked one hop deep would wrongly keep OnlyB.
func buildTwoHopNarrower(seed uint64) GeneratedCase {
	scenario := "two-hop-narrower"
	id := caseID("FS-ABS-4", "abstract-narrowing", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	cField, bField := n.field("c"), n.field("b")

	subs := []SubgraphSpec{
		{Name: "suba", Types: []*Type{
			{Name: "Query", Kind: KindObject,
				Fields: []Field{{Name: "wrapper", Type: "Wrapper"}}},
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common", "OnlyB"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
			{Name: "OnlyB", Kind: KindObject, Fields: []Field{{Name: bField, Type: "String"}}},
		}},
		{Name: "mid", Types: []*Type{ // hop 1: enterable from suba by id; carries far's key
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "gid", Type: "ID!"}}},
		}},
		{Name: "far", Types: []*Type{ // hop 2: enterable from mid by gid; declares {Common} only
			{Name: "Wrapper", Kind: KindObject, Keys: []Key{{Fields: "gid"}},
				Fields: []Field{{Name: "gid", Type: "ID!"}, {Name: "action", Type: "Action", Shareable: true}}},
			{Name: "Action", Kind: KindUnion, Members: []string{"Common"}},
			{Name: "Common", Kind: KindObject, Fields: []Field{{Name: cField, Type: "String", Shareable: true}}},
		}},
	}

	op := fmt.Sprintf("{ wrapper { action { __typename ... on Common { %s } ... on OnlyB { %s } } } }",
		cField, bField)
	expected := fmt.Sprintf(`{"data": {"wrapper": {"action": {"__typename": "Common", %q: "x"}}}}`, cField)

	return GeneratedCase{
		ID:           id,
		Propositions: []string{"FS-ABS-4", "FS-ABS-1", "FS-ABS-3", "FS-PLAN-3"},
		Family:       "abstract-narrowing",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "abstract-narrowing",
			Schema{Subgraphs: subs}, op, expected),
		Oracles: []Oracle{
			OracleDocContains("", "__typename"),
			OracleDocContains("", "... on Common"),
			// Prescribed narrowing survives the condition-aware transport: OnlyB fetched NOWHERE,
			// its gate stays in the response shape.
			OracleNoDocContains("", "... on OnlyB"),
			OracleResponseShapeHas("wrapper.action." + bField),
		},
	}
}

// genAbstractExplosion: `items { <leaf> }` selected on interface I, resolvable on NO
// position-capable subgraph on the interface, resolvable on both concrete (entity) members in a
// sibling subgraph -- the plan must fan out per possible member (FS-ABS-8 / D3pppp).
func genAbstractExplosion(seed uint64) []GeneratedCase {
	scenario := "member-fanout"
	id := caseID("FS-ABS-8", "abstract-explosion", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	leaf := n.field("extra")

	subs := []SubgraphSpec{
		{Name: "cat", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "items", Type: "[I]"}}},
			{Name: "I", Kind: KindInterface, Fields: []Field{{Name: "id", Type: "ID!"}}},
			{Name: "Book", Kind: KindObject, Implements: []string{"I"}, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}}},
			{Name: "Mag", Kind: KindObject, Implements: []string{"I"}, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}}},
		}},
		{Name: "detail", Types: []*Type{
			{Name: "I", Kind: KindInterface, Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String"}}},
			{Name: "Book", Kind: KindObject, Implements: []string{"I"}, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String"}}},
			{Name: "Mag", Kind: KindObject, Implements: []string{"I"}, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String"}}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-ABS-8", "FS-ABS-1"},
		Family:       "abstract-explosion",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "abstract-explosion", Schema{Subgraphs: subs},
			"{ items { "+leaf+" } }",
			fmt.Sprintf(`{"data": {"items": [{%q: "x"}]}}`, leaf)),
		Oracles: []Oracle{
			// The fan-out covers every position-possible member, member-typed, in the declaring
			// subgraph's entity fetches (FS-ABS-1: member fragments only where possible).
			OracleDocContains("detail", "... on Book"),
			OracleDocContains("detail", "... on Mag"),
			OracleFieldServedBy(leaf, "detail"),
		},
	}}
}

// genAbstractTypename: a selection of ONLY __typename at an abstract position still obligates
// the plan to fetch the position (FS-ABS-2).
func genAbstractTypename(seed uint64) []GeneratedCase {
	scenario := "typename-only"
	id := caseID("FS-ABS-2", "abstract-typename", scenario, seed)

	subs := []SubgraphSpec{
		{Name: "media", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "item", Type: "M"}}},
			{Name: "M", Kind: KindUnion, Members: []string{"Xa", "Yb"}},
			{Name: "Xa", Kind: KindObject, Fields: []Field{{Name: "x", Type: "String"}}},
			{Name: "Yb", Kind: KindObject, Fields: []Field{{Name: "y", Type: "String"}}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-ABS-2"},
		Family:       "abstract-typename",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "abstract-typename", Schema{Subgraphs: subs},
			"{ item { __typename } }",
			`{"data": {"item": {"__typename": "Xa"}}}`),
		Oracles: []Oracle{
			OracleFetchCount(1, 1),
			OracleDocContains("media", "__typename"),
		},
	}}
}

// genAbstractMismatch: one subgraph declares the position as the CONCRETE member, the other as
// the union -- possibility judgments run per the supplying subgraph's own output type (FS-ABS-12,
// the child-type-mismatch class).
func genAbstractMismatch(seed uint64) []GeneratedCase {
	scenario := "output-type-divergence"
	id := caseID("FS-ABS-12", "abstract-mismatch", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	title, length := n.field("title"), n.field("length")

	subs := []SubgraphSpec{
		{Name: "narrow", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "viewer", Type: "Viewer", Shareable: true}}},
			{Name: "Viewer", Kind: KindObject,
				Fields: []Field{{Name: "media", Type: "Book", Shareable: true}}},
			{Name: "Book", Kind: KindObject, Fields: []Field{{Name: title, Type: "String", Shareable: true}}},
		}},
		{Name: "wide", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "viewer", Type: "Viewer", Shareable: true}}},
			{Name: "Viewer", Kind: KindObject,
				Fields: []Field{{Name: "media", Type: "Media", Shareable: true}}},
			{Name: "Media", Kind: KindUnion, Members: []string{"Book", "Song"}},
			{Name: "Book", Kind: KindObject, Fields: []Field{{Name: title, Type: "String", Shareable: true}}},
			{Name: "Song", Kind: KindObject, Fields: []Field{{Name: length, Type: "Int"}}},
		}},
	}

	op := fmt.Sprintf("{ viewer { media { ... on Book { %s } ... on Song { %s } } } }", title, length)
	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-ABS-12", "FS-ABS-4"},
		Family:       "abstract-mismatch",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "abstract-mismatch",
			Schema{
				Subgraphs:          subs,
				ComposedFieldTypes: map[string]string{"Viewer.media": "Media"},
			},
			op,
			fmt.Sprintf(`{"data": {"viewer": {"media": {%q: "x"}}}}`, title)),
		Oracles: []Oracle{
			// Book is possible on BOTH declared output types (Book itself; a Media member) --
			// its selection must be served; Song is possible only via the wide subgraph.
			OracleFieldServedBy(title, "narrow", "wide"),
			responseShapeHasEither("viewer.media." + title),
		},
	}}
}

// responseShapeHasEither wraps OracleResponseShapeHas for positions whose key may render either
// gated or ungated (FS-ABS-10 expression freedom): presence at the path is all that is asserted.
func responseShapeHasEither(path string) Oracle {
	inner := OracleResponseShapeHas(path)
	return func(a *Artifacts) error {
		if err := inner(a); err != nil {
			return fmt.Errorf("%w (FS-ABS-10 note: either gated or ungated expression is conforming; neither found)", err)
		}
		return nil
	}
}
