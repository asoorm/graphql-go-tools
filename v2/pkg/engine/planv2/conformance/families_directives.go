package conformance

// families_directives.go -- the @provides / @external / @override / @shareable / @inaccessible
// scenario families (FEDERATION_SEMANTICS.md Sections 4-8):
//
//	provides       FS-PROV-1/2/3/4 -- the inline grant on the providing path (fragment-free
//	               FieldSets asserted positively per FEDERATION_SEMANTICS_FORMAL Section 2.6; the
//	               fragment-crossing capability is MG-1 territory and asserted for correctness
//	               only), and the path-scoping prohibition.
//	external       FS-EXT-1/2/3 / AX-EXT-KEY -- extension subgraphs as jump SOURCES (the external
//	               key carry) and the response-value prohibition.
//	override       FS-OVR-1/3/5 -- exclusion of the losing subgraph, the inert unavailable
//	               override (plan byte-equal to the directive-free twin), override + requires.
//	shareable      FS-SHR-1/2/3 + FS-ARG-1 -- the split shared root, arguments repeated per
//	               root document.
//	inaccessible   FS-INACC-1/2 -- inaccessible key coordinates still ride fetch documents and
//	               representations; the response shape never carries them.
import "fmt"

func init() {
	register(Family{
		Name:         "provides",
		Propositions: []string{"FS-PROV-1", "FS-PROV-2", "FS-PROV-3", "FS-PROV-4"},
		Generate:     genProvides,
	})
	register(Family{
		Name:         "external",
		Propositions: []string{"FS-EXT-1", "FS-EXT-2", "FS-EXT-3"},
		Generate:     genExternal,
	})
	register(Family{
		Name:         "override",
		Propositions: []string{"FS-OVR-1", "FS-OVR-3", "FS-OVR-5"},
		Generate:     genOverride,
	})
	register(Family{
		Name:         "shareable",
		Propositions: []string{"FS-SHR-1", "FS-SHR-2", "FS-SHR-3", "FS-SHR-4", "FS-ARG-1"},
		Generate:     genShareable,
	})
	register(Family{
		Name:         "inaccessible",
		Propositions: []string{"FS-INACC-1", "FS-INACC-2"},
		Generate:     genInaccessible,
	})
}

func genProvides(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	// Scenario 1 -- "inline-grant": the operation traverses the providing field; the provided
	// route dominates under the cost model, so the plan is ONE fetch (FS-PROV-1 realized; the
	// fetch-count assertion is planv2's I3 quality bar, deliberate for our own suite).
	// Scenario 2 -- "path-scoped": the operation reaches the same entity via ANOTHER path; the
	// provided-only field must NOT be read in the providing subgraph (FS-PROV-2).
	{
		id := caseID("FS-PROV-1", "provides", "inline-grant", seed)
		r := newRNG(id, seed)
		n := namer{r}
		granted := n.field("uname")

		makeSubs := func() []SubgraphSpec {
			return []SubgraphSpec{
				{Name: "feedsg", Types: []*Type{
					{Name: "Query", Kind: KindObject, Fields: []Field{
						{Name: "feed", Type: "[Post]"},
						{Name: "someUser", Type: "User"},
					}},
					{Name: "Post", Kind: KindObject, Keys: []Key{{Fields: "id"}},
						Fields: []Field{
							{Name: "id", Type: "ID!"},
							{Name: "author", Type: "User", Provides: granted},
						}},
					{Name: "User", Kind: KindObject, Keys: []Key{{Fields: "id"}},
						Fields: []Field{
							{Name: "id", Type: "ID!"},
							{Name: granted, Type: "String", External: true},
						}},
				}},
				{Name: "userssg", Types: []*Type{
					{Name: "User", Kind: KindObject, Keys: []Key{{Fields: "id"}},
						Fields: []Field{{Name: "id", Type: "ID!"}, {Name: granted, Type: "String"}}},
				}},
			}
		}

		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-PROV-1", "FS-PROV-3"},
			Family:       "provides",
			Seed:         seed,
			Case: BuildAuditCase("inline-grant", "provides", Schema{Subgraphs: makeSubs()},
				"{ feed { author { "+granted+" } } }",
				fmt.Sprintf(`{"data": {"feed": [{"author": {%q: "x"}}]}}`, granted)),
			Oracles: []Oracle{
				OracleFetchCount(1, 1), // the provided route dominates: no entity fetch needed
				OracleFieldServedBy(granted, "feedsg"),
			},
		})

		id2 := caseID("FS-PROV-2", "provides", "path-scoped", seed)
		out = append(out, GeneratedCase{
			ID:           id2,
			Propositions: []string{"FS-PROV-2", "FS-EXT-1"},
			Family:       "provides",
			Seed:         seed,
			Case: BuildAuditCase("path-scoped", "provides", Schema{Subgraphs: makeSubs()},
				"{ someUser { "+granted+" } }",
				fmt.Sprintf(`{"data": {"someUser": {%q: "x"}}}`, granted)),
			Oracles: []Oracle{
				// The someUser path carries no grant: the value must come from the owner.
				OracleRootDocLacks("feedsg", granted),
				OracleFieldServedBy(granted, "userssg"),
				OracleEntityFetchCount(1, 1),
			},
		})
	}

	// Scenario 3 -- "fragment-fieldset" (FS-PROV-4, MG-1 flagged): @provides whose FieldSet crosses
	// an abstract type. Correctness only (coverage via the owning route is admitted; the inline
	// grant for fragment coordinates is the registered MG-1 model gap).
	{
		id := caseID("FS-PROV-4", "provides", "fragment-fieldset", seed)
		r := newRNG(id, seed)
		n := namer{r}
		granted := n.field("title")

		subs := []SubgraphSpec{
			{Name: "feedsg", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "shelf", Type: "Shelf"}}},
				{Name: "Shelf", Kind: KindObject,
					Fields: []Field{{Name: "media", Type: "Media", Provides: "... on Book { " + granted + " }"}}},
				{Name: "Media", Kind: KindUnion, Members: []string{"Book", "Song"}},
				{Name: "Book", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: granted, Type: "String", External: true}}},
				{Name: "Song", Kind: KindObject,
					Fields: []Field{{Name: "length", Type: "Int"}}},
			}},
			{Name: "bookssg", Types: []*Type{
				{Name: "Book", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: granted, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-PROV-4"},
			Family:       "provides",
			Seed:         seed,
			Case: BuildAuditCase("fragment-fieldset", "provides", Schema{Subgraphs: subs},
				"{ shelf { media { ... on Book { "+granted+" } } } }",
				fmt.Sprintf(`{"data": {"shelf": {"media": {%q: "x"}}}}`, granted)),
			Oracles: []Oracle{
				// Correctness only: the granted coordinate is served (either inline via the grant
				// or via the owning route -- both conforming; MG-1 tracks the fetch-count quality).
				OracleFieldServedBy(granted, "feedsg", "bookssg"),
			},
		})
	}
	return out
}

func genExternal(seed uint64) []GeneratedCase {
	scenario := "extension-source"
	id := caseID("FS-EXT-2", "external", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	rating, name := n.field("rating"), n.field("pname")

	// The EXTENSION subgraph owns the root; its Product keys are @external (the fed2 extension
	// idiom). AX-EXT-KEY: the key value rides its instances, so the jump to the owner fires.
	subs := []SubgraphSpec{
		{Name: "reviews", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "myProducts", Type: "[Product]"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{
					{Name: "id", Type: "ID!", External: true},
					{Name: rating, Type: "Float"},
				}},
		}},
		{Name: "products", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: name, Type: "String"}}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-EXT-2", "FS-EXT-1", "FS-EXT-3"},
		Family:       "external",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "external", Schema{Subgraphs: subs},
			"{ myProducts { "+rating+" "+name+" } }",
			fmt.Sprintf(`{"data": {"myProducts": [{%q: 4.5, %q: "x"}]}}`, rating, name)),
		Oracles: []Oracle{
			// AX-EXT-KEY: the extension's root fetch selects its @external key field.
			OracleDocContains("reviews", "id"),
			// FS-EXT-1: the extension never sources the owner-declared field's response value.
			OracleFieldServedBy(name, "products"),
			OracleFieldServedBy(rating, "reviews"),
			OracleEntityFetchCount(1, 1),
		},
	}}
}

func genOverride(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	// Scenario 1 -- "exclusion" (FS-OVR-1): the losing subgraph never sources the field.
	{
		id := caseID("FS-OVR-1", "override", "exclusion", seed)
		r := newRNG(id, seed)
		n := namer{r}
		field := n.field("createdAt")

		subs := []SubgraphSpec{
			{Name: "a", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "feed", Type: "[Post]"}}},
				{Name: "Post", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: field, Type: "String"}}},
			}},
			{Name: "b", Types: []*Type{
				{Name: "Post", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: field, Type: "String", OverrideFrom: "a"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-OVR-1"},
			Family:       "override",
			Seed:         seed,
			Case: BuildAuditCase("exclusion", "override", Schema{Subgraphs: subs},
				"{ feed { "+field+" } }",
				fmt.Sprintf(`{"data": {"feed": [{%q: "x"}]}}`, field)),
			Oracles: []Oracle{
				OracleFieldServedBy(field, "b"),
				OracleEntityFetchCount(1, 1),
			},
		})
	}

	// Scenario 2 -- "inert" (FS-OVR-3): @override(from:) names a subgraph absent from the
	// supergraph. The plan must be byte-equal to the twin whose SDL simply lacks the directive.
	{
		id := caseID("FS-OVR-3", "override", "inert", seed)
		r := newRNG(id, seed)
		n := namer{r}
		field := n.field("stamp")

		makeSubs := func(withOverride bool) []SubgraphSpec {
			f := Field{Name: field, Type: "String"}
			if withOverride {
				f.OverrideFrom = "phantom" // no such subgraph
			}
			return []SubgraphSpec{
				{Name: "a", Types: []*Type{
					{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "doc", Type: "Doc"}}},
					{Name: "Doc", Kind: KindObject, Keys: []Key{{Fields: "id"}},
						Fields: []Field{{Name: "id", Type: "ID!"}, f}},
				}},
			}
		}
		twin := BuildAuditCase("inert-twin", "override", Schema{Subgraphs: makeSubs(false)},
			"{ doc { "+field+" } }", "")
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-OVR-3"},
			Family:       "override",
			Seed:         seed,
			Case: BuildAuditCase("inert", "override", Schema{Subgraphs: makeSubs(true)},
				"{ doc { "+field+" } }",
				fmt.Sprintf(`{"data": {"doc": {%q: "x"}}}`, field)),
			Oracles: []Oracle{OracleTwinPlanEqual(twin, "", "")},
		})
	}

	// Scenario 3 -- "with-requires" (FS-OVR-5): the OVERRIDING subgraph is the requiring side;
	// the requirement is gathered from the overridden subgraph, which still owns the input.
	{
		id := caseID("FS-OVR-5", "override", "with-requires", seed)
		r := newRNG(id, seed)
		n := namer{r}
		input, output := n.field("weight"), n.field("estimate")

		subs := []SubgraphSpec{
			{Name: "a", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
				{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{
						{Name: "id", Type: "ID!"},
						{Name: input, Type: "Float"},
						{Name: output, Type: "Float"},
					}},
			}},
			{Name: "b", Types: []*Type{
				{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{
						{Name: "id", Type: "ID!"},
						{Name: input, Type: "Float", External: true},
						{Name: output, Type: "Float", OverrideFrom: "a", Requires: input},
					}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-OVR-5", "FS-OVR-2", "FS-REQ-1"},
			Family:       "override",
			Seed:         seed,
			Case: BuildAuditCase("with-requires", "override", Schema{Subgraphs: subs},
				"{ product { "+output+" } }",
				fmt.Sprintf(`{"data": {"product": {%q: 1.5}}}`, output)),
			Oracles: []Oracle{
				OracleFieldServedBy(output, "b"),
				OracleRepresentationHas("b", "__typename", "id", input),
				OracleFetchBefore(input, output),
			},
		})
	}
	return out
}

func genShareable(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	for _, k := range []int{2, 3} {
		scenario := fmt.Sprintf("split-root-%d", k)
		id := caseID("FS-SHR-3", "shareable", scenario, seed)
		r := newRNG(id, seed)
		n := namer{r}

		children := make([]string, k)
		for i := range children {
			children[i] = n.field(fmt.Sprintf("child%d", i))
		}

		subs := make([]SubgraphSpec, k)
		for i := 0; i < k; i++ {
			subs[i] = SubgraphSpec{
				Name: fmt.Sprintf("sg%d", i),
				Types: []*Type{
					{Name: "Query", Kind: KindObject,
						Fields: []Field{{Name: "product", Type: "Product", Args: "id: ID!", Shareable: true}}},
					{Name: "Product", Kind: KindObject, // value type, no key: split-root is the ONLY shape
						Fields: []Field{{Name: children[i], Type: "String"}}},
				},
			}
		}

		op := "query($id: ID!) { product(id: $id) {"
		expected := ""
		for i, c := range children {
			op += " " + c
			if i > 0 {
				expected += ", "
			}
			expected += fmt.Sprintf("%q: \"x\"", c)
		}
		op += " } }"

		oracles := []Oracle{OracleFetchCount(k, k)}
		for i, c := range children {
			sg := fmt.Sprintf("sg%d", i)
			oracles = append(oracles,
				OracleFieldServedBy(c, sg),
				// FS-SHR-3/FS-ARG-1: every re-entered root document repeats the argument.
				OracleDocContains(sg, "product(id: $id)"),
				// FS-ARG-2: and declares the variable it uses.
				OracleDocContains(sg, "$id: ID!"),
			)
		}

		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-SHR-3", "FS-SHR-1", "FS-SHR-2", "FS-ARG-1", "FS-ARG-2"},
			Family:       "shareable",
			Seed:         seed,
			Case: BuildAuditCase(scenario, "shareable", Schema{Subgraphs: subs},
				op, `{"data": {"product": {`+expected+`}}}`),
			Oracles: oracles,
		})
	}
	return out
}

func genInaccessible(seed uint64) []GeneratedCase {
	scenario := "inaccessible-key"
	id := caseID("FS-INACC-2", "inaccessible", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	name, reviews := n.field("pname"), n.field("reviews")

	subs := []SubgraphSpec{
		{Name: "a", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "products", Type: "[Product]"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "sku"}},
				Fields: []Field{
					{Name: "sku", Type: "String!", Inaccessible: true},
					{Name: name, Type: "String"},
				}},
		}},
		{Name: "b", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "sku"}},
				Fields: []Field{
					{Name: "sku", Type: "String!", Inaccessible: true},
					{Name: reviews, Type: "[String]"},
				}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-INACC-2", "FS-INACC-1"},
		Family:       "inaccessible",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "inaccessible", Schema{Subgraphs: subs},
			"{ products { "+name+" "+reviews+" } }",
			fmt.Sprintf(`{"data": {"products": [{%q: "x", %q: ["r"]}]}}`, name, reviews)),
		Oracles: []Oracle{
			// FS-INACC-2: the inaccessible key coordinate still rides documents + representation.
			OracleDocContains("a", "sku"),
			OracleRepresentationHas("b", "__typename", "sku"),
			// FS-INACC-1: it never appears in the response shape.
			OracleResponseShapeLacks("products.sku"),
		},
	}}
}
