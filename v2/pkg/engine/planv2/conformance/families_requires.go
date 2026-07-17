package conformance

// families_requires.go -- the FS-REQ scenario families (FEDERATION_SEMANTICS.md Section 3;
// FEDERATION_SEMANTICS_FORMAL.md Section 2.3-2.5):
//
//	requires-basic       FS-REQ-1/2/3 -- the transport (requirement rides the representation,
//	                     AX-REP-2), shape invisibility, and the local-descent bypass prohibition.
//	requires-relay       FS-REQ-8 -- the s2->s1->s2 relay: root instances in the requiring subgraph.
//	requires-chain       FS-REQ-6 -- nested requires at depth 2..3, ordered as a chain.
//	requires-distributed FS-REQ-7 -- requirement coordinates gathered from several subgraphs.
//	requires-args        FS-REQ-4 / FS-ARG-3 -- literal arguments rendered in the gathering doc.
//	requires-conflict    FS-REQ-5 / FS-ENT-8 -- same-coordinate different-argument bindings kept
//	                     in separate entity fetches, aliased in the shared gathering document.
//	                     (Earmarked regression family: argument-conflict @requires.)
//	requires-conditional FS-REQ-9 / AX-REQ-COND -- fragment-conditioned coordinates render the
//	                     fragment; the PROBE case (coordinate resolvable nowhere) must still plan
//	                     under the conditional-input reading.
import "fmt"

func init() {
	register(Family{
		Name:         "requires-basic",
		Propositions: []string{"FS-REQ-1", "FS-REQ-2", "FS-REQ-3", "FS-ENT-7"},
		Generate:     genRequiresBasic,
	})
	register(Family{
		Name:         "requires-relay",
		Propositions: []string{"FS-REQ-8", "FS-REQ-3"},
		Generate:     genRequiresRelay,
	})
	register(Family{
		Name:         "requires-chain",
		Propositions: []string{"FS-REQ-6"},
		Generate:     genRequiresChain,
	})
	register(Family{
		Name:         "requires-distributed",
		Propositions: []string{"FS-REQ-7"},
		Generate:     genRequiresDistributed,
	})
	register(Family{
		Name:         "requires-args",
		Propositions: []string{"FS-REQ-4", "FS-ARG-3"},
		Generate:     genRequiresArgs,
	})
	register(Family{
		Name:         "requires-conflict",
		Propositions: []string{"FS-REQ-5", "FS-ENT-8"},
		Generate:     genRequiresConflict,
	})
	register(Family{
		Name:         "requires-conditional",
		Propositions: []string{"FS-REQ-9"},
		Generate:     genRequiresConditional,
	})
}

// requiresProduct builds the standard two-subgraph @requires shape:
// owner: Query.product, Product @key(id) { id <input> }; calc: Product { <input> @external,
// <output> @requires(<input>) }.
func requiresProduct(input, output, inputType string) []SubgraphSpec {
	return []SubgraphSpec{
		{Name: "owner", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: input, Type: inputType}}},
		}},
		{Name: "calc", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{
					{Name: "id", Type: "ID!"},
					{Name: input, Type: inputType, External: true},
					{Name: output, Type: "Float", Requires: input},
				}},
		}},
	}
}

func genRequiresBasic(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	for _, shape := range []string{"flat", "nested"} {
		scenario := shape
		id := caseID("FS-REQ-1", "requires-basic", scenario, seed)
		r := newRNG(id, seed)
		n := namer{r}
		input, output := n.field("weight"), n.field("estimate")

		var subs []SubgraphSpec
		repPaths := []string{"__typename", "id", input}
		if shape == "flat" {
			subs = requiresProduct(input, output, "Float")
		} else {
			// nested requirement: dims { l w } -- the representation mirrors the nesting.
			subs = []SubgraphSpec{
				{Name: "owner", Types: []*Type{
					{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
					{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
						Fields: []Field{{Name: "id", Type: "ID!"}, {Name: input, Type: "Dims"}}},
					{Name: "Dims", Kind: KindObject,
						Fields: []Field{{Name: "l", Type: "Float"}, {Name: "w", Type: "Float"}}},
				}},
				{Name: "calc", Types: []*Type{
					{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
						Fields: []Field{
							{Name: "id", Type: "ID!"},
							{Name: input, Type: "Dims", External: true},
							{Name: output, Type: "Float", Requires: input + " { l w }"},
						}},
					{Name: "Dims", Kind: KindObject,
						Fields: []Field{{Name: "l", Type: "Float", External: true}, {Name: "w", Type: "Float", External: true}}},
				}},
			}
			repPaths = append(repPaths, input+".l", input+".w")
		}

		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-REQ-1", "FS-REQ-2", "FS-REQ-3", "FS-ENT-7"},
			Family:       "requires-basic",
			Seed:         seed,
			Case: BuildAuditCase(scenario, "requires-basic", Schema{Subgraphs: subs},
				"{ product { "+output+" } }",
				fmt.Sprintf(`{"data": {"product": {%q: 1.5}}}`, output)),
			Oracles: []Oracle{
				// FS-REQ-1 / AX-REP-2: requirement values ride the representation.
				OracleRepresentationHas("calc", repPaths...),
				// FS-REQ-2: the injected requirement never appears in the response shape.
				OracleResponseShapeLacks("product." + input),
				// FS-REQ-1 ordering: the owner's gather precedes the calc jump.
				OracleFetchBefore(input, output),
				OracleFieldServedBy(output, "calc"),
			},
		})
	}
	return out
}

// genRequiresRelay: the root instances live in the REQUIRING subgraph -- the plan must exit,
// gather, and re-enter via `_entities` (FS-REQ-8), never resolve the field input-less in the
// local descent (FS-REQ-3).
func genRequiresRelay(seed uint64) []GeneratedCase {
	scenario := "s2-s1-s2"
	id := caseID("FS-REQ-8", "requires-relay", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	input, output := n.field("weight"), n.field("estimate")

	subs := []SubgraphSpec{
		{Name: "calc", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "localProduct", Type: "Product"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{
					{Name: "id", Type: "ID!"},
					{Name: input, Type: "Float", External: true},
					{Name: output, Type: "Float", Requires: input},
				}},
		}},
		{Name: "owner", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: input, Type: "Float"}}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-REQ-8", "FS-REQ-3", "FS-REQ-1"},
		Family:       "requires-relay",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "requires-relay", Schema{Subgraphs: subs},
			"{ localProduct { "+output+" } }",
			fmt.Sprintf(`{"data": {"localProduct": {%q: 1.5}}}`, output)),
		Oracles: []Oracle{
			// The relay's three fetches: calc root -> owner gather -> calc re-entry.
			OracleFetchCount(3, 3),
			OracleEntityFetchCount(2, 2),
			// FS-REQ-3: the local-descent ROOT fetch must NOT resolve the requiring field.
			OracleRootDocLacks("calc", output),
			// The re-entry representation carries the gathered requirement.
			OracleRepresentationHas("calc", "__typename", "id", input),
			OracleFetchBefore(input, output),
		},
	}}
}

// genRequiresChain: g_d in subgraph d requires g_{d-1}, which requires g_{d-2}, ... down to the
// plain g_0 in the root subgraph. Depths 2 and 3 (the corpus witnesses <= 3; FS-REQ-6 forbids any
// fixed-depth assumption).
func genRequiresChain(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	for _, depth := range []int{2, 3} {
		scenario := fmt.Sprintf("depth-%d", depth)
		id := caseID("FS-REQ-6", "requires-chain", scenario, seed)
		r := newRNG(id, seed)
		n := namer{r}

		g := make([]string, depth+1)
		for i := range g {
			g[i] = n.field(fmt.Sprintf("g%d", i))
		}

		subs := make([]SubgraphSpec, depth+1)
		for i := 0; i <= depth; i++ {
			fields := []Field{{Name: "id", Type: "ID!"}}
			if i == 0 {
				fields = append(fields, Field{Name: g[0], Type: "Float"})
			} else {
				fields = append(fields,
					Field{Name: g[i-1], Type: "Float", External: true},
					Field{Name: g[i], Type: "Float", Requires: g[i-1]})
			}
			subs[i] = SubgraphSpec{Name: fmt.Sprintf("sg%d", i), Types: []*Type{
				{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}}, Fields: fields},
			}}
		}
		subs[0].Types = append([]*Type{{
			Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}},
		}}, subs[0].Types...)

		oracles := []Oracle{
			OracleFieldServedBy(g[depth], fmt.Sprintf("sg%d", depth)),
			OracleRepresentationHas(fmt.Sprintf("sg%d", depth), "__typename", "id", g[depth-1]),
		}
		for i := 1; i <= depth; i++ {
			oracles = append(oracles, OracleFetchBefore(g[i-1], g[i]))
			if i < depth {
				oracles = append(oracles, OracleResponseShapeLacks("product."+g[i]))
			}
		}
		oracles = append(oracles, OracleResponseShapeLacks("product."+g[0]))

		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-REQ-6", "FS-REQ-1", "FS-REQ-2"},
			Family:       "requires-chain",
			Seed:         seed,
			Case: BuildAuditCase(scenario, "requires-chain", Schema{Subgraphs: subs},
				"{ product { "+g[depth]+" } }",
				fmt.Sprintf(`{"data": {"product": {%q: 1.5}}}`, g[depth])),
			Oracles: oracles,
		})
	}
	return out
}

// genRequiresDistributed: the requirement "a b" is supplied by TWO different subgraphs -- each
// coordinate gathered where it resolves, both riding the one representation (FS-REQ-7).
func genRequiresDistributed(seed uint64) []GeneratedCase {
	scenario := "two-source"
	id := caseID("FS-REQ-7", "requires-distributed", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	inA, inB, output := n.field("alpha"), n.field("beta"), n.field("total")

	subs := []SubgraphSpec{
		{Name: "rootsg", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: inA, Type: "Float"}}},
		}},
		{Name: "sideb", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: inB, Type: "Float"}}},
		}},
		{Name: "calc", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{
					{Name: "id", Type: "ID!"},
					{Name: inA, Type: "Float", External: true},
					{Name: inB, Type: "Float", External: true},
					{Name: output, Type: "Float", Requires: inA + " " + inB},
				}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-REQ-7", "FS-REQ-1"},
		Family:       "requires-distributed",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "requires-distributed", Schema{Subgraphs: subs},
			"{ product { "+output+" } }",
			fmt.Sprintf(`{"data": {"product": {%q: 3.0}}}`, output)),
		Oracles: []Oracle{
			OracleRepresentationHas("calc", "__typename", "id", inA, inB),
			OracleFieldServedBy(inB, "sideb"),
			OracleFetchBefore(inB, output),
			OracleFieldServedBy(output, "calc"),
		},
	}}
}

// genRequiresArgs: an argument-bearing requires coordinate -- the gathering document must render
// the literal arguments (FS-REQ-4/DV-006), and the value rides the representation under the
// coordinate's field name.
func genRequiresArgs(seed uint64) []GeneratedCase {
	scenario := "literal-currency"
	id := caseID("FS-REQ-4", "requires-args", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	output := n.field("estimate")

	subs := []SubgraphSpec{
		{Name: "owner", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "price", Type: "Float", Args: "currency: String!"}}},
		}},
		{Name: "calc", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{
					{Name: "id", Type: "ID!"},
					{Name: "price", Type: "Float", Args: "currency: String!", External: true},
					{Name: output, Type: "Float", Requires: `price(currency: "USD")`},
				}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-REQ-4", "FS-ARG-3"},
		Family:       "requires-args",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "requires-args", Schema{Subgraphs: subs},
			"{ product { "+output+" } }",
			fmt.Sprintf(`{"data": {"product": {%q: 1.5}}}`, output)),
		Oracles: []Oracle{
			// FS-REQ-4: the literal argument renders in the gathering (owner) document.
			OracleDocContains("owner", `price(currency: "USD")`),
			OracleRepresentationHas("calc", "__typename", "id", "price"),
			OracleFieldServedBy(output, "calc"),
		},
	}}
}

// genRequiresConflict: two fields bind the same coordinate to DIFFERENT argument values -- the
// bindings must stay distinct (separate entity fetches; aliased gathering selections).
func genRequiresConflict(seed uint64) []GeneratedCase {
	scenario := "usd-eur"
	id := caseID("FS-REQ-5", "requires-conflict", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	outUSD, outEUR := n.field("estimate"), n.field("estimateEur")

	subs := []SubgraphSpec{
		{Name: "owner", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "price", Type: "Float", Args: "currency: String!"}}},
		}},
		{Name: "calc", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{
					{Name: "id", Type: "ID!"},
					{Name: "price", Type: "Float", Args: "currency: String!", External: true},
					{Name: outUSD, Type: "Float", Requires: `price(currency: "USD")`},
					{Name: outEUR, Type: "Float", Requires: `price(currency: "EUR")`},
				}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-REQ-5", "FS-ENT-8", "FS-REQ-4"},
		Family:       "requires-conflict",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "requires-conflict", Schema{Subgraphs: subs},
			"{ product { "+outUSD+" "+outEUR+" } }",
			fmt.Sprintf(`{"data": {"product": {%q: 1.5, %q: 1.4}}}`, outUSD, outEUR)),
		Oracles: []Oracle{
			// FS-REQ-5/FS-ENT-8: conflicting bindings cannot share one representation -- at least
			// two entity fetches into calc.
			OracleEntityFetchCount(2, 2),
			// Both bindings rendered in the shared gathering document (one of them aliased).
			OracleDocContains("owner", `(currency: "USD")`),
			OracleDocContains("owner", `(currency: "EUR")`),
		},
	}}
}

// genRequiresConditional: a fragment-conditioned requires coordinate.
//   - "renders-fragment": the conditioned branch IS resolvable -- the gathering document must
//     render the fragment (FS-REQ-9's MUST half).
//   - "probe-unresolvable": the conditioned coordinate is resolvable NOWHERE -- under AX-REQ-COND
//     (conditional-input reading) the operation still PLANS; under the rejected hard reading it
//     would error. This is the FEDERATION_SEMANTICS_FORMAL Section 3.1 probe case, frozen here.
func genRequiresConditional(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	for _, probe := range []bool{false, true} {
		scenario := "renders-fragment"
		if probe {
			scenario = "probe-unresolvable"
		}
		id := caseID("FS-REQ-9", "requires-conditional", scenario, seed)
		r := newRNG(id, seed)
		n := namer{r}
		output := n.field("disc")

		mediaTypes := func(withTitle bool, external bool) []*Type {
			bookFields := []Field{{Name: "kind", Type: "String"}}
			if withTitle {
				bookFields = append(bookFields, Field{Name: "title", Type: "String", External: external})
			}
			return []*Type{
				{Name: "Media", Kind: KindInterface, Fields: []Field{{Name: "kind", Type: "String"}}},
				{Name: "Book", Kind: KindObject, Implements: []string{"Media"}, Fields: bookFields},
			}
		}

		ownerTypes := append([]*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "media", Type: "Media"}}},
		}, mediaTypes(!probe, false)...) // probe: title resolvable NOWHERE (owner does not declare it)

		calcTypes := append([]*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{
					{Name: "id", Type: "ID!"},
					{Name: "media", Type: "Media", External: true},
					{Name: output, Type: "String", Requires: "media { ... on Book { title } }"},
				}},
		}, mediaTypes(true, true)...) // declared @external for the FieldSet reference

		subs := []SubgraphSpec{
			{Name: "owner", Types: ownerTypes},
			{Name: "calc", Types: calcTypes},
		}
		// The composed schema needs Book.title even in the probe shape (the FieldSet references
		// it); external fields are composed in by the spec model regardless.
		schema := Schema{Subgraphs: subs}

		oracles := []Oracle{OracleFieldServedBy(output, "calc")}
		if !probe {
			oracles = append(oracles, OracleDocContains("owner", "... on Book"))
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-REQ-9"},
			Family:       "requires-conditional",
			Seed:         seed,
			Case: BuildAuditCase(scenario, "requires-conditional", schema,
				"{ product { "+output+" } }",
				fmt.Sprintf(`{"data": {"product": {%q: "x"}}}`, output)),
			Oracles: oracles,
		})
	}
	return out
}
