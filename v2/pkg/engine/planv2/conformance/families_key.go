package conformance

// families_key.go -- the FS-KEY scenario families (FEDERATION_SEMANTICS.md Section 2, Section 11):
//
//	key-chain       FS-KEY-1/2/3/5/11 + FS-ENT-4/6 -- entity hops, same-key fan-out vs mixed-key
//	                transitive chains (keys-mashup class), object and list roots.
//	key-composite   FS-KEY-4 / AX-REP-1 -- nested composite keys; the representation template must
//	                MIRROR the FieldSet nesting (never flattened).
//	key-multi       FS-KEY-5/6/7 -- several qualifying keys: choice-agnostic assertions only, plus
//	                the determinism probe (permutation testing).
//	key-nonresolvable FS-KEY-8/9 -- resolvable:false targets: no entity fetch into them; the
//	                provably-non-resolvable configuration must be a TYPED planning error.
//	key-distributed FS-KEY-10 / D7ppp -- a composite key no single foreign subgraph supplies,
//	                gathered per coordinate, target-excluded. (Earmarked regression family.)
import "fmt"

func init() {
	register(Family{
		Name:         "key-chain",
		Propositions: []string{"FS-KEY-1", "FS-KEY-2", "FS-KEY-3", "FS-KEY-5", "FS-KEY-11", "FS-ENT-1", "FS-ENT-4", "FS-ENT-6", "FS-PLAN-2"},
		Generate:     genKeyChain,
	})
	register(Family{
		Name:         "key-composite",
		Propositions: []string{"FS-KEY-4", "FS-KEY-2"},
		Generate:     genKeyComposite,
	})
	register(Family{
		Name:         "key-multi",
		Propositions: []string{"FS-KEY-5", "FS-KEY-6", "FS-KEY-7"},
		Generate:     genKeyMulti,
	})
	register(Family{
		Name:         "key-nonresolvable",
		Propositions: []string{"FS-KEY-8", "FS-KEY-9", "FS-PLAN-6"},
		Generate:     genKeyNonResolvable,
	})
	register(Family{
		Name:         "key-distributed",
		Propositions: []string{"FS-KEY-10", "FS-KEY-2", "FS-PLAN-2"},
		Generate:     genKeyDistributed,
	})
}

// genKeyChain: subgraph 0 owns the root and hop keys; each further subgraph hosts one leaf field.
//   - mode "fanout": every subgraph declares the same key -- each hop can jump straight from s0.
//   - mode "chain": subgraph i+1 is reachable only via the key that subgraph i introduces
//     (k0 in s0/s1, k1 in s1/s2, ...), forcing the transitive FS-KEY-11 shape.
func genKeyChain(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	for _, hops := range []int{2, 3} {
		for _, mode := range []string{"fanout", "chain"} {
			for _, root := range []string{"one", "list"} {
				scenario := fmt.Sprintf("%s-h%d-%s", mode, hops, root)
				id := caseID("FS-KEY-11", "key-chain", scenario, seed)
				r := newRNG(id, seed)
				n := namer{r}

				// seeded names
				keys := make([]string, hops)
				leaves := make([]string, hops+1)
				for i := range keys {
					keys[i] = n.field("key")
				}
				for i := range leaves {
					leaves[i] = n.field("leaf")
				}
				entity := "Item"
				rootField := n.field("entry")
				rootType := entity
				if root == "list" {
					rootType = "[" + entity + "]"
				}

				subs := make([]SubgraphSpec, hops+1)
				for i := 0; i <= hops; i++ {
					t := &Type{Name: entity, Kind: KindObject}
					var fields []Field
					switch {
					case mode == "fanout":
						t.Keys = []Key{{Fields: keys[0]}}
						fields = append(fields, Field{Name: keys[0], Type: "ID!"})
					case i == 0:
						t.Keys = []Key{{Fields: keys[0]}}
						fields = append(fields, Field{Name: keys[0], Type: "ID!"})
					case i < hops:
						// middle subgraph carries the arriving key and introduces the next
						t.Keys = []Key{{Fields: keys[i-1]}, {Fields: keys[i]}}
						fields = append(fields,
							Field{Name: keys[i-1], Type: "ID!"},
							Field{Name: keys[i], Type: "ID!"})
					default:
						t.Keys = []Key{{Fields: keys[hops-1]}}
						fields = append(fields, Field{Name: keys[hops-1], Type: "ID!"})
					}
					fields = append(fields, Field{Name: leaves[i], Type: "String"})
					t.Fields = fields
					subs[i] = SubgraphSpec{Name: fmt.Sprintf("sg%d", i), Types: []*Type{t}}
				}
				subs[0].Types = append([]*Type{{
					Name: "Query", Kind: KindObject,
					Fields: []Field{{Name: rootField, Type: rootType}},
				}}, subs[0].Types...)

				op := "{ " + rootField + " {"
				for i := 0; i <= hops; i++ {
					op += " " + leaves[i]
				}
				op += " } }"

				expectedItem := "{"
				for i := 0; i <= hops; i++ {
					if i > 0 {
						expectedItem += ","
					}
					expectedItem += fmt.Sprintf("%q: \"x\"", leaves[i])
				}
				expectedItem += "}"
				expected := fmt.Sprintf(`{"data": {%q: %s}}`, rootField, expectedItem)
				if root == "list" {
					expected = fmt.Sprintf(`{"data": {%q: [%s]}}`, rootField, expectedItem)
				}

				oracles := []Oracle{
					OracleEntityFetchCount(hops, hops),
					OracleDocContains("sg0", "__typename"), // FS-ENT-6
				}
				for i := 0; i <= hops; i++ {
					oracles = append(oracles, OracleFieldServedBy(leaves[i], fmt.Sprintf("sg%d", i)))
				}
				for i := 1; i <= hops; i++ {
					sg := fmt.Sprintf("sg%d", i)
					keyUsed := keys[0]
					if mode == "chain" {
						keyUsed = keys[i-1]
					}
					// FS-KEY-2: representation carries __typename + the target-declared key.
					oracles = append(oracles, OracleRepresentationHas(sg, "__typename", keyUsed))
					if mode == "chain" && i > 1 {
						// FS-KEY-11: hop i's key is produced by hop i-1's fetch.
						oracles = append(oracles, OracleFetchBefore(leaves[i-1], leaves[i]))
					}
				}

				out = append(out, GeneratedCase{
					ID:           id,
					Propositions: []string{"FS-KEY-11", "FS-KEY-1", "FS-KEY-2", "FS-KEY-3", "FS-KEY-5", "FS-ENT-1", "FS-ENT-4", "FS-ENT-6"},
					Family:       "key-chain",
					Seed:         seed,
					Case:         BuildAuditCase(scenario, "key-chain", Schema{Subgraphs: subs}, op, expected),
					Oracles:      oracles,
				})
			}
		}
	}
	return out
}

// genKeyComposite: nested composite keys of depth 1 and 2. The representation template must carry
// the nested object mirroring the FieldSet (AX-REP-1), and the source document must select the
// mirrored tree.
func genKeyComposite(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	for _, depth := range []int{1, 2} {
		scenario := fmt.Sprintf("nested-d%d", depth)
		id := caseID("FS-KEY-4", "key-composite", scenario, seed)
		r := newRNG(id, seed)
		n := namer{r}

		leaf := n.field("ext")
		var keyFields string
		orgTypes := []*Type{}
		switch depth {
		case 1:
			keyFields = "id org { id }"
			orgTypes = append(orgTypes, &Type{Name: "Org", Kind: KindObject,
				Fields: []Field{{Name: "id", Type: "ID!"}}})
		default:
			keyFields = "id org { id region { code } }"
			orgTypes = append(orgTypes,
				&Type{Name: "Org", Kind: KindObject,
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "region", Type: "Region"}}},
				&Type{Name: "Region", Kind: KindObject,
					Fields: []Field{{Name: "code", Type: "String!"}}})
		}

		entity := func(extra ...Field) *Type {
			t := &Type{Name: "Account", Kind: KindObject, Keys: []Key{{Fields: keyFields}},
				Fields: append([]Field{{Name: "id", Type: "ID!"}, {Name: "org", Type: "Org"}}, extra...)}
			return t
		}

		subs := []SubgraphSpec{
			{Name: "owner", Types: append([]*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "account", Type: "Account"}}},
				entity(Field{Name: "name", Type: "String"}),
			}, orgTypes...)},
			{Name: "ext", Types: append([]*Type{
				entity(Field{Name: leaf, Type: "String"}),
			}, orgTypes...)},
		}

		repPaths := []string{"__typename", "id", "org", "org.id"}
		if depth == 2 {
			repPaths = append(repPaths, "org.region", "org.region.code")
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-KEY-4", "FS-KEY-2", "FS-KEY-3"},
			Family:       "key-composite",
			Seed:         seed,
			Case: BuildAuditCase(scenario, "key-composite", Schema{Subgraphs: subs},
				"{ account { name "+leaf+" } }",
				fmt.Sprintf(`{"data": {"account": {"name": "x", %q: "y"}}}`, leaf)),
			Oracles: []Oracle{
				OracleEntityFetchCount(1, 1),
				OracleRepresentationHas("ext", repPaths...),
				OracleDocContains("owner", "org"), // mirrored upstream selection
			},
		})
	}
	return out
}

// genKeyMulti: the target declares several keys; the source carries only ONE of them, so FS-KEY-5
// pins the choice; a second scenario gives the source both keys and asserts only choice-agnostic
// properties + determinism (FS-KEY-6/7 -- the choice itself is free).
func genKeyMulti(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	for _, sourceHas := range []string{"one", "both"} {
		scenario := "source-" + sourceHas
		id := caseID("FS-KEY-6", "key-multi", scenario, seed)
		r := newRNG(id, seed)
		n := namer{r}
		leaf := n.field("data")

		sourceFields := []Field{{Name: "sku", Type: "String!"}}
		sourceKeys := []Key{{Fields: "sku"}}
		if sourceHas == "both" {
			sourceFields = append(sourceFields, Field{Name: "id", Type: "ID!"})
			sourceKeys = append(sourceKeys, Key{Fields: "id"})
		}
		subs := []SubgraphSpec{
			{Name: "src", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
				{Name: "Product", Kind: KindObject, Keys: sourceKeys, Fields: sourceFields},
			}},
			{Name: "dst", Types: []*Type{
				{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}, {Fields: "sku"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "sku", Type: "String!"}, {Name: leaf, Type: "String"}}},
			}},
		}
		oracles := []Oracle{
			OracleEntityFetchCount(1, 1),
			OracleFieldServedBy(leaf, "dst"),
		}
		if sourceHas == "one" {
			// FS-KEY-5: only sku is producible on the source side -- the jump MUST use it.
			oracles = append(oracles, OracleRepresentationHas("dst", "__typename", "sku"))
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-KEY-6", "FS-KEY-5", "FS-KEY-7"},
			Family:       "key-multi",
			Seed:         seed,
			Case: BuildAuditCase(scenario, "key-multi", Schema{Subgraphs: subs},
				"{ product { "+leaf+" } }",
				fmt.Sprintf(`{"data": {"product": {%q: "x"}}}`, leaf)),
			Oracles:          oracles,
			CheckDeterminism: true,
		})
	}
	return out
}

// genKeyNonResolvable:
//   - "no-jump": the stub subgraph's only key is resolvable:false and every requested field
//     resolves at the owner -- the plan must contain NO entity fetch into the stub (FS-KEY-8).
//   - "provably-nonresolvable": a field lives ONLY behind resolvable:false keys, reachable only
//     through its own subgraph's unrequested roots -- planning MUST error (FS-KEY-9 / FS-PLAN-6).
func genKeyNonResolvable(seed uint64) []GeneratedCase {
	id1 := caseID("FS-KEY-8", "key-nonresolvable", "no-jump", seed)
	r := newRNG(id1, seed)
	n := namer{r}
	leaf := n.field("val")

	subsA := []SubgraphSpec{
		{Name: "owner", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "thing", Type: "Thing"}}},
			{Name: "Thing", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String"}}},
		}},
		{Name: "stub", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "unrelated", Type: "String"}}},
			{Name: "Thing", Kind: KindObject, Keys: []Key{{Fields: "id", NonResolvable: true}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String", Shareable: true}}},
		}},
	}
	// Mark the owner's copy shareable too (both declare it).
	subsA[0].Types[1].Fields[1].Shareable = true

	id2 := caseID("FS-KEY-9", "key-nonresolvable", "provably-nonresolvable", seed)
	r2 := newRNG(id2, seed)
	n2 := namer{r2}
	hidden := n2.field("hidden")
	subsB := []SubgraphSpec{
		{Name: "owner", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "thing", Type: "Thing"}}},
			{Name: "Thing", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "name", Type: "String"}}},
		}},
		{Name: "island", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "islandThing", Type: "Thing"}}},
			{Name: "Thing", Kind: KindObject, Keys: []Key{{Fields: "id", NonResolvable: true}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: hidden, Type: "String"}}},
		}},
	}

	return []GeneratedCase{
		{
			ID:           id1,
			Propositions: []string{"FS-KEY-8"},
			Family:       "key-nonresolvable",
			Seed:         seed,
			Case: BuildAuditCase("no-jump", "key-nonresolvable", Schema{Subgraphs: subsA},
				"{ thing { "+leaf+" } }",
				fmt.Sprintf(`{"data": {"thing": {%q: "x"}}}`, leaf)),
			Oracles: []Oracle{
				OracleNoEntityFetchInto("stub"),
				OracleFieldServedBy(leaf, "owner"),
			},
		},
		{
			ID:           id2,
			Propositions: []string{"FS-KEY-9", "FS-PLAN-6", "FS-PLAN-5"},
			Family:       "key-nonresolvable",
			Seed:         seed,
			Case: BuildAuditCase("provably-nonresolvable", "key-nonresolvable", Schema{Subgraphs: subsB},
				"{ thing { "+hidden+" } }", ""),
			ExpectPlanError: true,
		},
	}
}

// genKeyDistributed: the target's composite key "pid cid" is supplied by NO single foreign
// subgraph -- pid lives in the root subgraph, cid in a sibling; both share an "id" gathering key.
// (The complex-entity-call class; earmarked regression family.)
func genKeyDistributed(seed uint64) []GeneratedCase {
	scenario := "two-coordinate"
	id := caseID("FS-KEY-10", "key-distributed", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	leaf := n.field("price")

	subs := []SubgraphSpec{
		{Name: "root", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "order", Type: "Order"}}},
			{Name: "Order", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "pid", Type: "ID!"}}},
		}},
		{Name: "sibling", Types: []*Type{
			{Name: "Order", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "cid", Type: "ID!"}}},
		}},
		{Name: "target", Types: []*Type{
			{Name: "Order", Kind: KindObject, Keys: []Key{{Fields: "pid cid"}},
				Fields: []Field{{Name: "pid", Type: "ID!"}, {Name: "cid", Type: "ID!"}, {Name: leaf, Type: "Float"}}},
		}},
	}

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-KEY-10", "FS-KEY-2", "FS-PLAN-2"},
		Family:       "key-distributed",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "key-distributed", Schema{Subgraphs: subs},
			"{ order { "+leaf+" } }",
			fmt.Sprintf(`{"data": {"order": {%q: 1.5}}}`, leaf)),
		Oracles: []Oracle{
			// Both coordinates gathered into the target representation (AX-REP-2 transport).
			OracleRepresentationHas("target", "__typename", "pid", "cid"),
			// The sibling gather must precede the target jump.
			OracleFetchBefore("cid", leaf),
			OracleFieldServedBy(leaf, "target"),
		},
	}}
}
