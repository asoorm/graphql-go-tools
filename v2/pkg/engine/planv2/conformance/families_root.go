package conformance

// families_root.go -- root-operation-type and argument scenario families
// (FEDERATION_SEMANTICS.md Sections 12-13):
//
//	root-renamed  FS-ROOT-4 -- subgraphs renaming their root operation types (query and
//	              subscription; the real-world dominant failure class corpora cannot see).
//	              (Earmarked regression family: renamed roots.)
//	root-split    FS-ROOT-1/2 + FS-PLAN-5 -- distinct root fields in distinct subgraphs, one
//	              root fetch each, never a foreign root.
//	mutation      FS-ROOT-3/6 + FS-ENT-5 -- mutation keyword on the root fetch, `query` keyword on
//	              dependent entity fetches, serial top-level order, and the shareable-root
//	              single-execution pin (one root fetch per mutation root field; typed refusal
//	              when no single subgraph can anchor the whole selection).
//	arguments     FS-ARG-1/2/5/6 -- argument fidelity, exact variable declarations, @include/@skip
//	              exclusion at literal conditions.
import "fmt"

func init() {
	register(Family{
		Name:         "root-renamed",
		Propositions: []string{"FS-ROOT-4"},
		Generate:     genRootRenamed,
	})
	register(Family{
		Name:         "root-split",
		Propositions: []string{"FS-ROOT-1", "FS-ROOT-2", "FS-ROOT-5", "FS-PLAN-5"},
		Generate:     genRootSplit,
	})
	register(Family{
		Name:         "mutation",
		Propositions: []string{"FS-ROOT-3", "FS-ROOT-6", "FS-ENT-5"},
		Generate:     genMutation,
	})
	register(Family{
		Name:         "arguments",
		Propositions: []string{"FS-ARG-1", "FS-ARG-2", "FS-ARG-5", "FS-ARG-6"},
		Generate:     genArguments,
	})
}

func genRootRenamed(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	// renamed QUERY root: the wire form must be identical to the unrenamed twin.
	{
		id := caseID("FS-ROOT-4", "root-renamed", "query", seed)
		r := newRNG(id, seed)
		n := namer{r}
		field, leaf := n.field("product"), n.field("pname")

		mkSubs := func(renamed bool) []SubgraphSpec {
			qName := ""
			tName := "Query"
			if renamed {
				qName, tName = "AcmeQuery", "AcmeQuery"
			}
			return []SubgraphSpec{{
				Name:      "acme",
				QueryName: qName,
				Types: []*Type{
					{Name: tName, Kind: KindObject, Fields: []Field{{Name: field, Type: "Product"}}},
					{Name: "Product", Kind: KindObject, Fields: []Field{{Name: leaf, Type: "String"}}},
				},
			}}
		}
		twin := BuildAuditCase("query-twin", "root-renamed", Schema{Subgraphs: mkSubs(false)},
			"{ "+field+" { "+leaf+" } }", "")
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ROOT-4"},
			Family:       "root-renamed",
			Seed:         seed,
			Case: BuildAuditCase("query", "root-renamed", Schema{Subgraphs: mkSubs(true)},
				"{ "+field+" { "+leaf+" } }",
				fmt.Sprintf(`{"data": {%q: {%q: "x"}}}`, field, leaf)),
			Oracles: []Oracle{
				// The rename must be invisible on the wire: identical plan to the unrenamed twin.
				OracleTwinPlanEqual(twin, "", ""),
				OracleFieldServedBy(leaf, "acme"),
			},
		})
	}

	// renamed SUBSCRIPTION root, with a cross-subgraph per-event jump.
	{
		id := caseID("FS-ROOT-4", "root-renamed", "subscription", seed)
		r := newRNG(id, seed)
		n := namer{r}
		trigger, leaf := n.field("tickerUpdated"), n.field("tname")

		subs := []SubgraphSpec{
			{
				Name:             "rt",
				SubscriptionName: "AcmeSub",
				Types: []*Type{
					{Name: "AcmeSub", Kind: KindObject, Fields: []Field{{Name: trigger, Type: "Ticker"}}},
					{Name: "Ticker", Kind: KindObject, Keys: []Key{{Fields: "id"}},
						Fields: []Field{{Name: "id", Type: "ID!"}}},
				},
			},
			{Name: "meta", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop", Type: "String"}}},
				{Name: "Ticker", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ROOT-4", "FS-SUB-2", "FS-SUB-4"},
			Family:       "root-renamed",
			Seed:         seed,
			Case: BuildAuditCase("subscription", "root-renamed", Schema{Subgraphs: subs},
				"subscription { "+trigger+" { "+leaf+" } }", ""),
			Subscription: true,
			Oracles: []Oracle{
				OracleTriggerSubgraph("rt"),
				OracleDocContains("rt", trigger),
				OracleFieldServedBy(leaf, "meta"),
			},
		})
	}
	return out
}

func genRootSplit(seed uint64) []GeneratedCase {
	scenario := "two-subgraphs"
	id := caseID("FS-ROOT-1", "root-split", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	fa, fb := n.field("alpha"), n.field("beta")

	subs := []SubgraphSpec{
		{Name: "asg", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: fa, Type: "String"}}},
		}},
		{Name: "bsg", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: fb, Type: "String"}}},
		}},
	}
	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-ROOT-1", "FS-ROOT-2", "FS-ROOT-5", "FS-PLAN-5", "FS-PLAN-2"},
		Family:       "root-split",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "root-split", Schema{Subgraphs: subs},
			"{ "+fa+" "+fb+" }",
			fmt.Sprintf(`{"data": {%q: "x", %q: "y"}}`, fa, fb)),
		Oracles: []Oracle{
			OracleFetchCount(2, 2),
			OracleEntityFetchCount(0, 0), // FS-ROOT-5: roots never resolve via _entities
			OracleFieldServedBy(fa, "asg"),
			OracleFieldServedBy(fb, "bsg"),
			// FS-PLAN-5 is the audit baseline's assertion 5; restated here by the field-source
			// oracles (each subgraph sees only its own root field).
			OracleNoDocContains("asg", fb),
			OracleNoDocContains("bsg", fa),
		},
	}}
}

func genMutation(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	// one-subgraph mutation with an entity jump: mutation keyword on the root document,
	// query keyword on the dependent entity fetch (FS-ENT-5).
	{
		id := caseID("FS-ENT-5", "mutation", "entity-jump", seed)
		r := newRNG(id, seed)
		n := namer{r}
		mut, leaf := n.field("save"), n.field("audit")

		subs := []SubgraphSpec{
			{Name: "writer", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop", Type: "String"}}},
				{Name: "Mutation", Kind: KindObject, Fields: []Field{{Name: mut, Type: "Doc"}}},
				{Name: "Doc", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}}},
			}},
			{Name: "meta", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop2", Type: "String"}}},
				{Name: "Doc", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ENT-5", "FS-ROOT-3"},
			Family:       "mutation",
			Seed:         seed,
			Case: BuildAuditCase("entity-jump", "mutation", Schema{Subgraphs: subs},
				"mutation { "+mut+" { "+leaf+" } }",
				fmt.Sprintf(`{"data": {%q: {%q: "x"}}}`, mut, leaf)),
			Oracles: []Oracle{
				OracleRootFetchKeyword("mutation"),
				OracleEntityFetchCount(1, 1),
				OracleFieldServedBy(leaf, "meta"),
			},
		})
	}

	// serial top-level mutation fields across TWO subgraphs (FS-ROOT-3): the second root
	// mutation fetch must be ordered after the first's writes.
	{
		id := caseID("FS-ROOT-3", "mutation", "serial-two-subgraphs", seed)
		r := newRNG(id, seed)
		n := namer{r}
		m1, m2 := n.field("first"), n.field("second")

		subs := []SubgraphSpec{
			{Name: "w1", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop", Type: "String"}}},
				{Name: "Mutation", Kind: KindObject, Fields: []Field{{Name: m1, Type: "String"}}},
			}},
			{Name: "w2", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop2", Type: "String"}}},
				{Name: "Mutation", Kind: KindObject, Fields: []Field{{Name: m2, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ROOT-3"},
			Family:       "mutation",
			Seed:         seed,
			Case: BuildAuditCase("serial-two-subgraphs", "mutation", Schema{Subgraphs: subs},
				"mutation { "+m1+" "+m2+" }",
				fmt.Sprintf(`{"data": {%q: "x", %q: "y"}}`, m1, m2)),
			Oracles: []Oracle{
				OracleRootFetchKeyword("mutation"),
				OracleFieldServedBy(m1, "w1"),
				OracleFieldServedBy(m2, "w2"),
				// FS-ROOT-3: serial order -- the second top-level mutation field's fetch is
				// emitted after the first's (fetch-tree serial execution follows emission order;
				// dependency-encoded seriality is executed-truth territory).
				OracleFetchIndexOrder(m1, m2),
			},
		})
	}

	// shareable mutation root field, single-execution pin (FS-ROOT-6; the real-audit mutations_3
	// class): the field is declared in BOTH wa and wb, its payload entity carries alpha only in wa
	// and beta only in wb. The whole selection must enter through ONE subgraph's root fetch (the
	// per-goal cheapest split -- alpha via wa's root, beta via wb's -- would execute the side effect
	// twice); the cross-subgraph field rides the entity jump. The joint choice ties on cost
	// (symmetric shape) and breaks to the lexicographically first subgraph name: wa.
	{
		id := caseID("FS-ROOT-6", "mutation", "shareable-root-pin", seed)
		r := newRNG(id, seed)
		n := namer{r}
		mut, alpha, beta := n.field("submit"), n.field("alpha"), n.field("beta")

		subs := []SubgraphSpec{
			{Name: "wa", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop", Type: "String"}}},
				{Name: "Mutation", Kind: KindObject,
					Fields: []Field{{Name: mut, Type: "Report", Shareable: true}}},
				{Name: "Report", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: alpha, Type: "String"}}},
			}},
			{Name: "wb", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop2", Type: "String"}}},
				{Name: "Mutation", Kind: KindObject,
					Fields: []Field{{Name: mut, Type: "Report", Shareable: true}}},
				{Name: "Report", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: beta, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ROOT-6", "FS-ROOT-3", "FS-ENT-5"},
			Family:       "mutation",
			Seed:         seed,
			Case: BuildAuditCase("shareable-root-pin", "mutation", Schema{Subgraphs: subs},
				"mutation { "+mut+" { "+alpha+" "+beta+" } }",
				fmt.Sprintf(`{"data": {%q: {%q: "x", %q: "y"}}}`, mut, alpha, beta)),
			Oracles: []Oracle{
				OracleRootFetchKeyword("mutation"),
				// Exactly ONE root fetch (2 fetches total, 1 of them an entity fetch): the
				// side-effecting root field executes once.
				OracleFetchCount(2, 2),
				OracleEntityFetchCount(1, 1),
				// The pin's deterministic choice: wa's root fetch carries the root field and its
				// local half; wb NEVER sees the root field (its half arrives via _entities).
				OracleFieldServedBy(alpha, "wa"),
				OracleFieldServedBy(beta, "wb"),
				OracleNoDocContains("wb", mut),
			},
		})
	}

	// unpinnable shareable mutation root (FS-ROOT-6 negative -> FS-PLAN-6): both subgraphs declare
	// the root field, the payload's exclusive halves are split across them, and the payload's only
	// keys are resolvable:false -- no jump can relay the missing half off either subgraph's payload,
	// so NO single subgraph anchors the whole selection. Every executable plan would split the root
	// field and re-execute the side effect (both halves ARE root-reachable -- the pre-FS-ROOT-6
	// planner emitted exactly that silently-double-executing plan); the obligated outcome is a
	// typed refusal.
	{
		id := caseID("FS-ROOT-6", "mutation", "shareable-root-unpinnable", seed)
		r := newRNG(id, seed)
		n := namer{r}
		mut, alpha, beta := n.field("apply"), n.field("left"), n.field("right")

		subs := []SubgraphSpec{
			{Name: "wa", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop", Type: "String"}}},
				{Name: "Mutation", Kind: KindObject,
					Fields: []Field{{Name: mut, Type: "Change", Shareable: true}}},
				{Name: "Change", Kind: KindObject, Keys: []Key{{Fields: "id", NonResolvable: true}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: alpha, Type: "String"}}},
			}},
			{Name: "wb", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop2", Type: "String"}}},
				{Name: "Mutation", Kind: KindObject,
					Fields: []Field{{Name: mut, Type: "Change", Shareable: true}}},
				{Name: "Change", Kind: KindObject, Keys: []Key{{Fields: "id", NonResolvable: true}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: beta, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ROOT-6", "FS-PLAN-6"},
			Family:       "mutation",
			Seed:         seed,
			Case: BuildAuditCase("shareable-root-unpinnable", "mutation", Schema{Subgraphs: subs},
				"mutation { "+mut+" { "+alpha+" "+beta+" } }", ""),
			ExpectPlanError: true,
		})
	}
	return out
}

func genArguments(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	// variable forwarding + exact declarations (FS-ARG-1/2): the argument-bearing selection's
	// fetch declares exactly $c; the dependent entity fetch never sees it.
	{
		id := caseID("FS-ARG-2", "arguments", "variable-forwarding", seed)
		r := newRNG(id, seed)
		n := namer{r}
		leaf := n.field("pname")

		subs := []SubgraphSpec{
			{Name: "prices", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
				{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{
						{Name: "id", Type: "ID!"},
						{Name: "price", Type: "Float", Args: "currency: String!"},
					}},
			}},
			{Name: "names", Types: []*Type{
				{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ARG-1", "FS-ARG-2", "FS-ARG-6"},
			Family:       "arguments",
			Seed:         seed,
			Case: BuildAuditCase("variable-forwarding", "arguments", Schema{Subgraphs: subs},
				"query($c: String!) { product { price(currency: $c) "+leaf+" } }",
				fmt.Sprintf(`{"data": {"product": {"price": 1.5, %q: "x"}}}`, leaf)),
			Oracles: []Oracle{
				OracleDocContains("prices", "price(currency: $c)"),
				OracleDocContains("prices", "$c: String!"),
				// FS-ARG-2's no-more half: the entity fetch to names declares no $c.
				OracleNoDocContains("names", "$c"),
				OracleFieldServedBy(leaf, "names"),
			},
		})
	}

	// @include(if: false) at a literal condition (FS-ARG-5): the excluded selection is fetched
	// NOWHERE, and the entity fetch whose only consumer it was disappears with it.
	{
		id := caseID("FS-ARG-5", "arguments", "include-false", seed)
		r := newRNG(id, seed)
		n := namer{r}
		excluded := n.field("zname")

		subs := []SubgraphSpec{
			{Name: "prices", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
				{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "price", Type: "Float"}}},
			}},
			{Name: "names", Types: []*Type{
				{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: excluded, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ARG-5"},
			Family:       "arguments",
			Seed:         seed,
			Case: BuildAuditCase("include-false", "arguments", Schema{Subgraphs: subs},
				"{ product { price "+excluded+" @include(if: false) } }",
				`{"data": {"product": {"price": 1.5}}}`),
			Oracles: []Oracle{
				OracleNoDocContains("", excluded),
				OracleFetchCount(1, 1),
			},
		})
	}

	// @skip(if: true) twin.
	{
		id := caseID("FS-ARG-5", "arguments", "skip-true", seed)
		r := newRNG(id, seed)
		n := namer{r}
		excluded := n.field("zskip")

		subs := []SubgraphSpec{
			{Name: "solo", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "product", Type: "Product"}}},
				{Name: "Product", Kind: KindObject,
					Fields: []Field{{Name: "price", Type: "Float"}, {Name: excluded, Type: "String"}}},
			}},
		}
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-ARG-5"},
			Family:       "arguments",
			Seed:         seed,
			Case: BuildAuditCase("skip-true", "arguments", Schema{Subgraphs: subs},
				"{ product { price "+excluded+" @skip(if: true) } }",
				`{"data": {"product": {"price": 1.5}}}`),
			Oracles: []Oracle{
				OracleNoDocContains("", excluded),
			},
		})
	}
	return out
}
