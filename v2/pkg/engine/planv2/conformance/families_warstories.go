package conformance

// families_warstories.go -- the earmarked production regression shapes, generated as first-class
// conformance cases (each one is a class that shipped as a customer-visible defect somewhere and
// that hand-curated corpora structurally cannot contain -- standard-named fixtures, no data types
// named Subscription, no self-referential entities):
//
//	dual-role-subscription  a data entity literally named `Subscription` (billing/commerce),
//	                        with and without a genuine subscription operation root coexisting
//	                        (the D5pp dual-role model; re-sweep regression class).
//	rootnodes-only          the billing shape whose `Subscription`-named type has NO keys and NO
//	                        child evidence -- only SDL output-type usage marks it as data.
//	entity-cycle            a self-nested entity type (Employee -> manager: Employee) crossing a
//	                        subgraph boundary at several depths.
//
// Distributed @key and argument-conflict @requires -- the other two earmarked classes -- live in
// their own families (key-distributed, requires-conflict).
import "fmt"

func init() {
	register(Family{
		Name:         "dual-role-subscription",
		Propositions: []string{"FS-ROOT-1", "FS-SUB-2", "FS-KEY-1"},
		Generate:     genDualRoleSubscription,
	})
	register(Family{
		Name:         "rootnodes-only",
		Propositions: []string{"FS-ROOT-1", "FS-KEY-1"},
		Generate:     genRootNodesOnly,
	})
	register(Family{
		Name:         "entity-cycle",
		Propositions: []string{"FS-KEY-1", "FS-KEY-11", "FS-PLAN-3"},
		Generate:     genEntityCycle,
	})
}

// billingSchema: the billing subgraph returns a keyed entity named `Subscription` from Query and
// Mutation fields; the holders subgraph resolves Holder.name. withRealtime adds a third
// subgraph with a GENUINE default-named subscription root.
func billingSchema(withRealtime bool) Schema {
	billing := SubgraphSpec{Name: "billing", Types: []*Type{
		{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "currentPlan", Type: "Subscription"}}},
		{Name: "Mutation", Kind: KindObject,
			Fields: []Field{{Name: "planCancel", Type: "Subscription", Args: "id: ID!"}}},
		{Name: "Subscription", Kind: KindObject, Keys: []Key{{Fields: "id"}},
			Fields: []Field{
				{Name: "id", Type: "ID!"},
				{Name: "endsAt", Type: "String"},
				{Name: "region", Type: "String"},
				{Name: "holder", Type: "Holder"},
			}},
		{Name: "Holder", Kind: KindObject, Keys: []Key{{Fields: "id"}},
			Fields: []Field{{Name: "id", Type: "ID!"}}},
	}}
	holders := SubgraphSpec{Name: "holders", Types: []*Type{
		{Name: "Holder", Kind: KindObject, Keys: []Key{{Fields: "id"}},
			Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "name", Type: "String"}}},
	}}
	subs := []SubgraphSpec{billing, holders}
	if withRealtime {
		subs = append(subs, SubgraphSpec{Name: "realtime", Types: []*Type{
			{Name: "Subscription", Kind: KindObject,
				Fields: []Field{{Name: "tick", Type: "Tick"}}},
			{Name: "Tick", Kind: KindObject,
				Fields: []Field{{Name: "value", Type: "Float"}}},
		}})
	}
	return Schema{Subgraphs: subs, OmitSubscriptionRoot: !withRealtime}
}

func genDualRoleSubscription(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	queryOp := `{ currentPlan { endsAt region holder { name } } }`
	mutationOp := `mutation Cancel($id: ID!) { planCancel(id: $id) { endsAt holder { name } } }`

	for _, realtime := range []bool{false, true} {
		suffix := "data-only"
		if realtime {
			suffix = "with-realtime-root"
		}
		schema := billingSchema(realtime)

		out = append(out,
			GeneratedCase{
				ID:           caseID("FS-ROOT-1", "dual-role-subscription", "query-"+suffix, seed),
				Propositions: []string{"FS-ROOT-1", "FS-KEY-1"},
				Family:       "dual-role-subscription",
				Seed:         seed,
				Case: BuildAuditCase("query-"+suffix, "dual-role-subscription", schema, queryOp,
					`{"data": {"currentPlan": {"endsAt": "x", "region": "emea", "holder": {"name": "n"}}}}`),
				Oracles: []Oracle{
					OracleZeroFallbacks(),
					OracleFieldServedBy("name", "holders"),
					OracleFieldServedBy("endsAt", "billing"),
				},
			},
			GeneratedCase{
				ID:           caseID("FS-ROOT-3", "dual-role-subscription", "mutation-"+suffix, seed),
				Propositions: []string{"FS-ROOT-1", "FS-ROOT-3", "FS-KEY-1"},
				Family:       "dual-role-subscription",
				Seed:         seed,
				Case: BuildAuditCase("mutation-"+suffix, "dual-role-subscription", schema, mutationOp,
					`{"data": {"planCancel": {"endsAt": "x", "holder": {"name": "n"}}}}`),
				Oracles: []Oracle{
					OracleZeroFallbacks(),
					OracleRootFetchKeyword("mutation"),
					OracleFieldServedBy("name", "holders"),
				},
			},
		)
	}

	// The genuine realtime root must still plan as a subscription (the merged dual-role shape).
	out = append(out, GeneratedCase{
		ID:           caseID("FS-SUB-2", "dual-role-subscription", "realtime-trigger", seed),
		Propositions: []string{"FS-SUB-2", "FS-SUB-1"},
		Family:       "dual-role-subscription",
		Seed:         seed,
		Case: BuildAuditCase("realtime-trigger", "dual-role-subscription", billingSchema(true),
			`subscription { tick { value } }`, ""),
		Subscription: true,
		Oracles: []Oracle{
			OracleTriggerSubgraph("realtime"),
			OracleDocContains("realtime", "tick"),
		},
	})
	return out
}

// genRootNodesOnly: the `Subscription`-named data type has NO @key and NO child-node evidence --
// derived metadata lists it as a keyless root node with its full field list; only the SDL's
// output-type references (Query.currentPlan, PlanCancelPayload.subscription) mark it
// as data. Its fields must still object-tail for query AND mutation shapes.
func genRootNodesOnly(seed uint64) []GeneratedCase {
	subs := []SubgraphSpec{
		{Name: "billing", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{
				{Name: "currentPlan", Type: "Subscription"},
				{Name: "other", Type: "String"},
			}},
			{Name: "Mutation", Kind: KindObject,
				Fields: []Field{{Name: "planCancel", Type: "PlanCancelPayload", Args: "id: ID!"}}},
			{Name: "PlanCancelPayload", Kind: KindObject,
				Fields: []Field{{Name: "subscription", Type: "Subscription"}}},
			{Name: "Subscription", Kind: KindObject, // keyless: rootNodes-only evidence shape
				Fields: []Field{
					{Name: "id", Type: "ID!"},
					{Name: "availablePlans", Type: "[String]"},
					{Name: "queueSize", Type: "Int"},
					{Name: "endsAt", Type: "String"},
					{Name: "holder", Type: "Holder"},
				}},
			{Name: "Holder", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}}},
		}},
		{Name: "holders", Types: []*Type{
			{Name: "Holder", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: "name", Type: "String"}}},
		}},
	}
	schema := Schema{Subgraphs: subs, OmitSubscriptionRoot: true}

	return []GeneratedCase{
		{
			ID:           caseID("FS-ROOT-1", "rootnodes-only", "query", seed),
			Propositions: []string{"FS-ROOT-1", "FS-KEY-1"},
			Family:       "rootnodes-only",
			Seed:         seed,
			Case: BuildAuditCase("query", "rootnodes-only", schema,
				`{ currentPlan { endsAt holder { name } } }`,
				`{"data": {"currentPlan": {"endsAt": "x", "holder": {"name": "n"}}}}`),
			Oracles: []Oracle{
				OracleZeroFallbacks(),
				OracleFieldServedBy("name", "holders"),
			},
		},
		{
			ID:           caseID("FS-ROOT-3", "rootnodes-only", "mutation", seed),
			Propositions: []string{"FS-ROOT-1", "FS-ROOT-3"},
			Family:       "rootnodes-only",
			Seed:         seed,
			Case: BuildAuditCase("mutation", "rootnodes-only", schema,
				`mutation Cancel($id: ID!) { planCancel(id: $id) { subscription { endsAt queueSize availablePlans } } }`,
				`{"data": {"planCancel": {"subscription": {"endsAt": "x", "queueSize": 1, "availablePlans": ["p"]}}}}`),
			Oracles: []Oracle{
				OracleZeroFallbacks(),
				OracleRootFetchKeyword("mutation"),
			},
		},
	}
}

// genEntityCycle: a self-referential entity split across subgraphs -- the org subgraph owns the
// structure (manager/peers), the hr subgraph owns the leaf. Nested positions of the SAME type
// must each get their own correctly-attached entity fetch (the sibling/path-conflation class).
func genEntityCycle(seed uint64) []GeneratedCase {
	var out []GeneratedCase
	for _, depth := range []int{2, 3} {
		scenario := fmt.Sprintf("manager-depth-%d", depth)
		id := caseID("FS-KEY-1", "entity-cycle", scenario, seed)
		r := newRNG(id, seed)
		n := namer{r}
		leaf := n.field("ename")

		subs := []SubgraphSpec{
			{Name: "org", Types: []*Type{
				{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "me", Type: "Employee"}}},
				{Name: "Employee", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{
						{Name: "id", Type: "ID!"},
						{Name: "manager", Type: "Employee"},
						{Name: "peers", Type: "[Employee]"},
					}},
			}},
			{Name: "hr", Types: []*Type{
				{Name: "Employee", Kind: KindObject, Keys: []Key{{Fields: "id"}},
					Fields: []Field{{Name: "id", Type: "ID!"}, {Name: leaf, Type: "String"}}},
			}},
		}

		// { me { <leaf> manager { <leaf> manager { ... } } peers { <leaf> } } }
		inner := leaf
		for i := 0; i < depth; i++ {
			inner = leaf + " manager { " + inner + " }"
		}
		op := "{ me { " + inner + " peers { " + leaf + " } } }"

		expInner := fmt.Sprintf("{%q: \"x\"}", leaf)
		for i := 0; i < depth; i++ {
			expInner = fmt.Sprintf("{%q: \"x\", \"manager\": %s}", leaf, expInner)
		}
		expected := fmt.Sprintf(`{"data": {"me": %s}}`, mergePeers(expInner, leaf))

		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-KEY-1", "FS-KEY-11", "FS-PLAN-3"},
			Family:       "entity-cycle",
			Seed:         seed,
			Case:         BuildAuditCase(scenario, "entity-cycle", Schema{Subgraphs: subs}, op, expected),
			Oracles: []Oracle{
				OracleZeroFallbacks(),
				OracleFieldServedBy(leaf, "hr"),
				// one entity fetch per nested position (me, each manager level, peers)
				OracleEntityFetchCount(depth+2, depth+2),
			},
		})
	}
	return out
}

// mergePeers rewrites the innermost expected object to also carry the peers list at the top
// level of `me` (kept out of the recursion above for clarity).
func mergePeers(expInner, leaf string) string {
	// expInner is `{"<leaf>": "x", "manager": {...}}`; insert peers before the closing brace.
	return expInner[:len(expInner)-1] + fmt.Sprintf(`, "peers": [{%q: "x"}]}`, leaf)
}
