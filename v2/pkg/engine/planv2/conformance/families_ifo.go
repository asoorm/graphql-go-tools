package conformance

// families_ifo.go -- the @interfaceObject / entity-interface scenario family
// (FEDERATION_SEMANTICS.md Section 9; FEDERATION_SEMANTICS_FORMAL.md Section 2.9-2.10, AX-IFO-1/2):
//
//	contributed-field  FS-IFO-1/2/3 -- a field contributed by the interface-object subgraph is
//	                   fetched via an entity fetch typed on the INTERFACE; concrete-member
//	                   fragments never appear in documents to that subgraph.
//	reverse-hop        FS-IFO-4 -- interface-typed instances from the interface-object subgraph
//	                   hop to the DEFINING subgraph for interface-declared fields.
//	member-gated       FS-IFO-3/6 (plan half) -- client member gates served by the member-knowing
//	                   subgraph; the interface-object document stays member-free.
//	with-requires      FS-IFO-5 -- @requires on an interface-object field: representation typed on
//	                   the interface, requirement gathered from the member-knowing side.
import "fmt"

func init() {
	register(Family{
		Name: "interface-object",
		Propositions: []string{
			"FS-IFO-1", "FS-IFO-2", "FS-IFO-3", "FS-IFO-4", "FS-IFO-5", "FS-IFO-6", "FS-IFO-7",
		},
		Generate: genInterfaceObject,
	})
}

// ifoSchema builds the canonical two-subgraph entity-interface shape:
// definer: interface Node @key(id) { id name }, User implements Node (+ age),
// Query.users: [Node] (interface-typed) and Query.usersConcrete: [User] (concrete-typed --
// the registered concrete-position gap witness);
// ifo: type Node @interfaceObject @key(id) { id username [disc @requires(name)] }
// plus Query.recent: [Node] on the ifo side for the reverse hop.
func ifoSchema(nameF, ageF, usernameF, discF string, withRequires bool) Schema {
	definer := SubgraphSpec{Name: "definer", Types: []*Type{
		{Name: "Query", Kind: KindObject, Fields: []Field{
			{Name: "users", Type: "[Node]"},
			{Name: "usersConcrete", Type: "[User]"},
		}},
		{Name: "Node", Kind: KindInterface, Keys: []Key{{Fields: "id"}},
			Fields: []Field{{Name: "id", Type: "ID!"}, {Name: nameF, Type: "String"}}},
		{Name: "User", Kind: KindObject, Implements: []string{"Node"}, Keys: []Key{{Fields: "id"}},
			Fields: []Field{{Name: "id", Type: "ID!"}, {Name: nameF, Type: "String"}, {Name: ageF, Type: "Int"}}},
	}}
	ifoFields := []Field{{Name: "id", Type: "ID!"}, {Name: usernameF, Type: "String"}}
	if withRequires {
		ifoFields = append(ifoFields,
			Field{Name: nameF, Type: "String", External: true},
			Field{Name: discF, Type: "String", Requires: nameF})
	}
	ifo := SubgraphSpec{Name: "ifo", Types: []*Type{
		{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "recent", Type: "[Node]"}}},
		{Name: "Node", Kind: KindObject, InterfaceObject: true, Keys: []Key{{Fields: "id"}},
			Fields: ifoFields},
	}}
	return Schema{Subgraphs: []SubgraphSpec{definer, ifo}}
}

func genInterfaceObject(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	mk := func(prop, scenario string) (string, namer) {
		id := caseID(prop, "interface-object", scenario, seed)
		return id, namer{newRNG(id, seed)}
	}

	// contributed-field: { users { username } } -- jump into ifo typed on Node.
	{
		id, n := mk("FS-IFO-1", "contributed-field")
		nameF, ageF, usernameF := n.field("name"), n.field("age"), n.field("username")
		schema := ifoSchema(nameF, ageF, usernameF, "", false)
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-IFO-1", "FS-IFO-2", "FS-IFO-3"},
			Family:       "interface-object",
			Seed:         seed,
			Case: BuildAuditCase("contributed-field", "interface-object", schema,
				"{ users { "+usernameF+" } }",
				fmt.Sprintf(`{"data": {"users": [{%q: "x"}]}}`, usernameF)),
			Oracles: []Oracle{
				// FS-IFO-2 / AX-IFO-1: interface-typed entry and representation.
				OracleDocContains("ifo", "... on Node"),
				OracleRepresentationHas("ifo", "__typename", "id"),
				// FS-IFO-3: no concrete implementer fragment against the ifo subgraph.
				OracleNoDocContains("ifo", "... on User"),
				OracleFieldServedBy(usernameF, "ifo"),
			},
		})
	}

	// contributed-field-concrete: the SAME contributed field selected at a CONCRETE-typed
	// position ({ usersConcrete: [User] }). FS-IFO-1 makes no position-abstractness distinction:
	// composition added the field to User, so the plan MUST fetch it from the ifo subgraph via
	// the interface-keyed entity fetch. Registered planv2 gap: the D3io flattening only fires at
	// abstract positions, so this shape is ErrNoValidPlan today (the triage register + the
	// DIVERGENCES conformance-wave entry carry it).
	{
		id, n := mk("FS-IFO-1", "contributed-field-concrete")
		nameF, ageF, usernameF := n.field("name"), n.field("age"), n.field("username")
		schema := ifoSchema(nameF, ageF, usernameF, "", false)
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-IFO-1", "FS-IFO-2"},
			Family:       "interface-object",
			Seed:         seed,
			Case: BuildAuditCase("contributed-field-concrete", "interface-object", schema,
				"{ usersConcrete { "+usernameF+" } }",
				fmt.Sprintf(`{"data": {"usersConcrete": [{%q: "x"}]}}`, usernameF)),
			Oracles: []Oracle{
				OracleDocContains("ifo", "... on Node"),
				OracleFieldServedBy(usernameF, "ifo"),
			},
		})
	}

	// reverse-hop: { recent { name } } -- ifo supplies instances; definer resolves the
	// interface field via an interface-typed entity fetch (FS-IFO-4; AX-IFO-2 runtime half).
	{
		id, n := mk("FS-IFO-4", "reverse-hop")
		nameF, ageF, usernameF := n.field("name"), n.field("age"), n.field("username")
		schema := ifoSchema(nameF, ageF, usernameF, "", false)
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-IFO-4", "FS-IFO-2"},
			Family:       "interface-object",
			Seed:         seed,
			Case: BuildAuditCase("reverse-hop", "interface-object", schema,
				"{ recent { "+nameF+" } }",
				fmt.Sprintf(`{"data": {"recent": [{%q: "x"}]}}`, nameF)),
			Oracles: []Oracle{
				OracleFieldServedBy(nameF, "definer"),
				OracleDocContains("definer", "... on Node"),
				OracleEntityFetchCount(1, 1),
			},
		})
	}

	// member-gated: { recent { __typename ... on User { age } } } -- the member gate is served by
	// the member-knowing subgraph; the ifo document stays member-free (FS-IFO-3/6 plan half).
	{
		id, n := mk("FS-IFO-6", "member-gated")
		nameF, ageF, usernameF := n.field("name"), n.field("age"), n.field("username")
		schema := ifoSchema(nameF, ageF, usernameF, "", false)
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-IFO-6", "FS-IFO-3"},
			Family:       "interface-object",
			Seed:         seed,
			Case: BuildAuditCase("member-gated", "interface-object", schema,
				"{ recent { __typename ... on User { "+ageF+" } } }",
				fmt.Sprintf(`{"data": {"recent": [{"__typename": "User", %q: 30}]}}`, ageF)),
			Oracles: []Oracle{
				OracleNoDocContains("ifo", "... on User"),
				OracleFieldServedBy(ageF, "definer"),
			},
		})
	}

	// indirect-extension (FS-IFO-7): a THIRD subgraph extends the concrete implementer with an
	// ordinary keyed field -- the interface-object contribution and the concrete extension compose
	// in ONE plan: an interface-typed jump (ifo) and a concrete-typed jump (ext), each valid.
	{
		id, n := mk("FS-IFO-7", "indirect-extension")
		nameF, ageF, usernameF, cityF := n.field("name"), n.field("age"), n.field("username"), n.field("city")
		schema := ifoSchema(nameF, ageF, usernameF, "", false)
		schema.Subgraphs = append(schema.Subgraphs, SubgraphSpec{Name: "ext", Types: []*Type{
			{Name: "User", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: cityF, Type: "String"}}},
		}})
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-IFO-7", "FS-IFO-1", "FS-IFO-2"},
			Family:       "interface-object",
			Seed:         seed,
			Case: BuildAuditCase("indirect-extension", "interface-object", schema,
				"{ users { "+usernameF+" ... on User { "+cityF+" } } }",
				fmt.Sprintf(`{"data": {"users": [{%q: "x", %q: "y"}]}}`, usernameF, cityF)),
			Oracles: []Oracle{
				OracleFieldServedBy(usernameF, "ifo"),
				OracleFieldServedBy(cityF, "ext"),
				OracleDocContains("ifo", "... on Node"),
				OracleDocContains("ext", "... on User"),
				OracleEntityFetchCount(2, 2),
			},
		})
	}

	// with-requires: { users { disc } } -- disc @requires(name) on the interface-object side;
	// gather name from the definer, re-enter ifo typed on Node with name in the representation.
	{
		id, n := mk("FS-IFO-5", "with-requires")
		nameF, ageF, usernameF, discF := n.field("name"), n.field("age"), n.field("username"), n.field("disc")
		schema := ifoSchema(nameF, ageF, usernameF, discF, true)
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-IFO-5", "FS-IFO-2", "FS-REQ-1"},
			Family:       "interface-object",
			Seed:         seed,
			Case: BuildAuditCase("with-requires", "interface-object", schema,
				"{ users { "+discF+" } }",
				fmt.Sprintf(`{"data": {"users": [{%q: "x"}]}}`, discF)),
			Oracles: []Oracle{
				OracleRepresentationHas("ifo", "__typename", "id", nameF),
				OracleDocContains("ifo", "... on Node"),
				OracleFetchBefore(nameF, discF),
				OracleFieldServedBy(discF, "ifo"),
			},
		})
	}
	return out
}
