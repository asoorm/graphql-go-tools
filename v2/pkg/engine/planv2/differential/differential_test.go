package differential

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// TestDifferential_PartialUnionOldWrongNewRight is Section 7.1: the old planner mis-handles the partial
// union -- on this fixture it deadlocks at PLANNING (see TestV1PartialUnionCorroboration; same family
// as v1 Cosmo's audit failures, DIVERGENCES.md "v1 calibration"). planv2 must plan it; any resulting
// divergence MUST be on the allow-list, not a test failure.
func TestDifferential_PartialUnionOldWrongNewRight(t *testing.T) {
	div := runBoth(t, PartialUnionSchema, PartialUnionOp, partialUnionSubgraphs())
	if div == nil {
		// Would mean v1 planned it AND the shapes agree -- fine, but the corroboration test would
		// then be failing (it pins v1's planning deadlock), so this branch is effectively vestigial.
		t.Log("partial union: response shapes equivalent")
		return
	}
	if _, allowed := KnownDivergences[CaseKey(PartialUnionSchema, PartialUnionOp)]; !allowed {
		t.Fatalf("undocumented divergence on partial union: %s", div)
	}
	t.Logf("partial union: EXPECTED_DIVERGENCE %s -- %s", div, KnownDivergences[CaseKey(PartialUnionSchema, PartialUnionOp)])
}

// TestDifferential_PermutationParityOnAgreedCases: on cases where the old planner is correct, planv2
// must be response-shape equivalent under EVERY datasource ordering (permutations.Generate), per the
// L17 semantic oracle.
func TestDifferential_PermutationParityOnAgreedCases(t *testing.T) {
	for _, c := range agreedCases() {
		t.Run(c.name, func(t *testing.T) {
			if div := runBothOpts(t, c.schema, c.op, c.subgraphs, c.deferOp, c.fields...); div != nil {
				if _, allowed := KnownDivergences[CaseKey(c.schema, c.op)]; allowed {
					t.Skipf("agreed case unexpectedly diverged but is allow-listed: %s", div)
				}
				t.Fatalf("%s: UNEXPLAINED divergence on agreed case: %s", c.name, div)
			}
		})
	}
}

// TestDifferential_Classification is the harness's summary pass: it runs every case and classifies the
// outcome as MATCH / EXPECTED_DIVERGENCE(ref) / UNEXPLAINED, and fails only on UNEXPLAINED -- the
// structured output the milestone reads.
//
// SCOPE: MATCH here certifies response-shape agreement ONLY (client keys, nesting, gates, leaf node
// kinds). Fetch documents, fetch counts, and fetch dependency structure are deliberately out of
// scope until Task 13 -- never quote these counts as "planv2 == v1".
func TestDifferential_Classification(t *testing.T) {
	type result struct {
		name  string
		klass string
		note  string
	}
	var results []result

	all := append([]differentialCase{{name: "partial-union", schema: PartialUnionSchema, op: PartialUnionOp, subgraphs: partialUnionSubgraphs()}}, agreedCases()...)
	for _, c := range all {
		div := runBothOpts(t, c.schema, c.op, c.subgraphs, c.deferOp, c.fields...)
		switch {
		case div == nil:
			results = append(results, result{c.name, "MATCH", ""})
		default:
			if ref, allowed := KnownDivergences[CaseKey(c.schema, c.op)]; allowed {
				results = append(results, result{c.name, "EXPECTED_DIVERGENCE", ref})
			} else {
				results = append(results, result{c.name, "UNEXPLAINED", div.String()})
			}
		}
	}

	var match, expected, unexplained int
	for _, r := range results {
		switch r.klass {
		case "MATCH":
			match++
			t.Logf("MATCH               %s", r.name)
		case "EXPECTED_DIVERGENCE":
			expected++
			t.Logf("EXPECTED_DIVERGENCE %s -- %s", r.name, r.note)
		case "UNEXPLAINED":
			unexplained++
			t.Errorf("UNEXPLAINED         %s -- %s", r.name, r.note)
		}
	}
	t.Logf("differential summary (response-shape agreement only; fetch docs/counts/deps land in Task 13): MATCH=%d EXPECTED_DIVERGENCE=%d UNEXPLAINED=%d", match, expected, unexplained)
}

// TestCompareResponseShapes_Unit exercises the oracle directly: identical shapes match; a key
// difference, a gate difference, a composite/leaf difference, and a LEAF-KIND difference each
// diverge. The leaf-kind case is the false-MATCH regression pin: two trees identical except for one
// leaf's resolve node kind (Float vs String) must NOT match -- leaf kind is execution-visible (walker
// dispatch), and it is precisely the dimension the typed-leaves fix landed, so the oracle must be
// able to regression-guard it.
func TestCompareResponseShapes_Unit(t *testing.T) {
	mk := func(fields ...*resolveField) plan.Plan {
		return synthPlan(fields...)
	}
	if div := CompareResponseShapes(mk(leaf("a"), leaf("b")), mk(leaf("b"), leaf("a"))); div != nil {
		t.Fatalf("sibling reorder must be equivalent, got %s", div)
	}
	if div := CompareResponseShapes(mk(leaf("a")), mk(leaf("a"), leaf("b"))); div == nil {
		t.Fatal("missing field must diverge")
	}
	if div := CompareResponseShapes(mk(gatedLeaf("a", "OnlyA")), mk(gatedLeaf("a", "OnlyB"))); div == nil {
		t.Fatal("differing __typename gate must diverge")
	}
	if div := CompareResponseShapes(mk(leaf("a")), mk(obj("a", leaf("x")))); div == nil {
		t.Fatal("composite-vs-leaf must diverge")
	}
	// Identical shapes differing ONLY in one leaf's node kind (Float vs String) -> NOT MATCH.
	oldP := mk(obj("product", typedLeaf("price", &resolve.Float{}), leaf("name")))
	newP := mk(obj("product", typedLeaf("price", &resolve.String{}), leaf("name")))
	div := CompareResponseShapes(oldP, newP)
	if div == nil {
		t.Fatal("leaf node-kind difference (Float vs String) must diverge -- the false-MATCH pin")
	}
	if div.Path != "product.price" {
		t.Fatalf("leaf-kind divergence must point at the leaf, got path %q (%s)", div.Path, div)
	}
	// Same typed kinds on both sides -> still a match (kind comparison must not over-trigger).
	if div := CompareResponseShapes(
		mk(typedLeaf("n", &resolve.Integer{})),
		mk(typedLeaf("n", &resolve.Integer{})),
	); div != nil {
		t.Fatalf("identical typed leaves must match, got %s", div)
	}
}

// TestCompareResponseShapes_LeafGateSubsumption pins the M15 field-drop refinement: a member-gated
// LEAF present in one plan but not the other is response-equivalent when the other plan covers that
// response key (ungated, or a member-gate union  superseteq  the gate) with the same leaf shape -- the redundant
// duplicate v1 emits over an ungated selection. Negative controls confirm disjoint gates, an ungated
// vs strictly-gated asymmetry, a differently-aliased leaf, and a leaf-kind change under a gate all
// STILL diverge (the refinement must not mask a real drop or a shape change).
func TestCompareResponseShapes_LeafGateSubsumption(t *testing.T) {
	mk := func(fields ...*resolveField) plan.Plan { return synthPlan(fields...) }

	// SUBSUMED (MATCH): ungated `t` + redundant gated `t@Product` in old; only ungated `t` in new.
	if div := CompareResponseShapes(
		mk(obj("r", leaf("t"), gatedLeaf("t", "Product"))),
		mk(obj("r", leaf("t"))),
	); div != nil {
		t.Fatalf("ungated leaf must subsume a redundant member-gated duplicate, got %s", div)
	}
	// SUBSUMED (MATCH), reverse direction: new carries the redundant duplicate.
	if div := CompareResponseShapes(
		mk(obj("r", leaf("id"))),
		mk(obj("r", leaf("id"), gatedLeaf("id", "Invoice"))),
	); div != nil {
		t.Fatalf("redundant member-gated leaf in new must be subsumed by ungated id, got %s", div)
	}
	// SUBSUMED (MATCH): a member-gate UNION covers the dropped gate (no ungated present).
	oldUnion := mk(obj("r", gatedLeaf("id", "Invoice"), gatedLeaf("id", "Receipt")))
	newUnion := mk(obj("r", gatedLeaf("id", "Invoice")))
	// new drops id@Receipt; new covers only {Invoice} -- NOT a superset of {Receipt} -> DIVERGE.
	if div := CompareResponseShapes(oldUnion, newUnion); div == nil {
		t.Fatal("dropping a member-gated leaf whose type is NOT covered must diverge")
	}

	// NEG: disjoint gates (a@OnlyA vs a@OnlyB) -- already pinned in _Unit; re-assert under subsumption.
	if div := CompareResponseShapes(mk(gatedLeaf("a", "OnlyA")), mk(gatedLeaf("a", "OnlyB"))); div == nil {
		t.Fatal("disjoint member gates must still diverge")
	}
	// NEG: ungated in old, only strictly-gated in new -- conservative, must diverge.
	if div := CompareResponseShapes(mk(obj("r", leaf("id"))), mk(obj("r", gatedLeaf("id", "Invoice")))); div == nil {
		t.Fatal("ungated leaf vs only strictly-gated must diverge (cannot prove full coverage)")
	}
	// NEG: differently-aliased member leaf must NOT be collapsed (different response keys).
	if div := CompareResponseShapes(
		mk(obj("r", leaf("a"), gatedLeaf("b", "Product"))),
		mk(obj("r", leaf("a"))),
	); div == nil {
		t.Fatal("a distinct response key (b) dropped must diverge, not be subsumed by ungated a")
	}
	// NEG: leaf-kind change under a gate must not be masked by an ungated same-key leaf.
	if div := CompareResponseShapes(
		mk(obj("r", typedLeaf2("id", &resolve.String{}, "Invoice"))),
		mk(obj("r", typedLeaf2("id", &resolve.Integer{}, ""))),
	); div == nil {
		t.Fatal("leaf-kind change under a gate must diverge (shape mismatch), not be subsumed")
	}
}

// typedLeaf2 is a gated typed leaf helper for the subsumption neg controls.
func typedLeaf2(name string, v resolve.Node, gate string) *resolveField {
	return &resolveField{name: name, gate: gate, value: v}
}

// TestV1PartialUnionCorroboration is the IMPORTANT-2 corroboration, kept as a permanent pin: the v1
// planner's partial-union failure is a GENUINE v1 limitation, not a harness-config artifact. The
// config here is built through the same graphql_datasource federation conventions the fed tests use
// (NewSchemaConfiguration with federation ServiceSDL, real Factory, DisableResolveFieldPositions).
// Controls on the SAME config prove the config is sound: v1 plans the shared-member-only query, a
// single-exclusive-member query, and a no-union query fine -- it fails at PLANNING ("failed to obtain
// planning paths ... field waiting for dependency", a planning deadlock, not the runtime
// strip-fragments->null mode) exactly when both mutually-exclusive members are co-selected, under
// BOTH datasource orders.
//
// If v1 is ever fixed, the both-exclusive assertions below fail -- that is the signal to remove the
// KnownDivergences entry and re-run the differential classification (divergence policy: register
// entries must track reality).
func TestV1PartialUnionCorroboration(t *testing.T) {
	runV1 := func(t *testing.T, ds []plan.DataSource, op string) *operationreport.Report {
		t.Helper()
		cfg := plan.Configuration{DataSources: ds, DisableResolveFieldPositions: true}
		p, err := plan.NewPlanner(cfg)
		if err != nil {
			t.Fatalf("v1 NewPlanner: %v", err)
		}
		opDoc, defDoc, report := parseAndNormalize(t, PartialUnionSchema, op)
		p.Plan(opDoc, defDoc, "", report)
		return report
	}

	// Controls: the config is sound -- v1 plans everything except the both-exclusive selection.
	for _, c := range []struct{ name, op string }{
		{"control-common-only", `{ wrapper { action { __typename ... on Common { c } } } }`},
		{"control-one-exclusive", `{ wrapper { action { __typename ... on OnlyA { a } } } }`},
		{"control-no-union", `{ wrapper { id } }`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if report := runV1(t, buildConfig(t, partialUnionSubgraphs()).DataSources, c.op); report.HasErrors() {
				t.Fatalf("control must plan on the same config (config-soundness proof), got: %s", report.Error())
			}
		})
	}

	// The corroborated failure: both exclusive members co-selected -> v1 planning deadlock, under
	// BOTH datasource orders (order-independence rules out an ordering artifact).
	for _, perm := range [][]int{{0, 1}, {1, 0}} {
		t.Run(fmt.Sprintf("both-exclusive-order-%v", perm), func(t *testing.T) {
			base := buildConfig(t, partialUnionSubgraphs()).DataSources
			ds := []plan.DataSource{base[perm[0]], base[perm[1]]}
			report := runV1(t, ds, PartialUnionOp)
			if !report.HasErrors() {
				t.Fatal("v1 now PLANS the partial union -- remove the KnownDivergences entry and " +
					"re-run the differential classification (register must track reality)")
			}
			if !strings.Contains(report.Error(), "failed to obtain planning paths") {
				t.Fatalf("v1 failure mode changed (was the planning-paths deadlock) -- update the "+
					"allow-list attribution: %s", report.Error())
			}
		})
	}
}

// --- cases -----------------------------------------------------------------------------------

type differentialCase struct {
	name      string
	schema    string
	op        string
	subgraphs []subgraph
	// fields carries v1's per-field argument metadata (plan.Configuration.Fields) for cases whose
	// operations use field arguments -- v1's printOperation validates against it; planv2 ignores it.
	fields plan.FieldConfigurations
	// deferOp normalizes with WithEnableDefer (the D11.13 input contract) and routes the comparison
	// through CompareDeferPlans. False for every non-defer case, keeping their pipeline byte-identical.
	deferOp bool
}

// agreedCases are those where the v1 planner is correct; planv2 must match its response shape under
// every datasource ordering. It includes the base fixtures plus the Task-10-review-mandated
// differential set (task11DifferentialFixtures) run through the same oracle, plus the D11.12
// subscription set (compared trigger + per-event shape via CompareSubscriptionPlans).
func agreedCases() []differentialCase {
	cases := append(append(baseAgreedCases(), task11DifferentialFixtures()...), leafGateSubsumptionCases()...)
	cases = append(cases, subscriptionCases()...)
	return append(cases, deferCases()...)
}

// subscriptionCases pin D11.12 against v1: v1 plans each subscription through its datasource
// planner's ConfigureSubscription; planv2 must produce the same trigger semantics (url, subscription
// document modulo print formatting, forwarded-variable count) and the same per-event response shape,
// under every datasource ordering.
func subscriptionCases() []differentialCase {
	return []differentialCase{
		{
			// Trigger-only: the whole selection is served by the subgraph owning the root field.
			name: "subscription-plain",
			schema: `
schema { query: Query subscription: Subscription }
type Query { tick: Tick }
type Subscription { tickUpdated: Tick }
type Tick { id: ID! value: Float }
`,
			op: `subscription { tickUpdated { id value } }`,
			subgraphs: []subgraph{
				{
					name: "ticker",
					sdl: `
type Query { tick: Tick }
type Subscription { tickUpdated: Tick }
type Tick { id: ID! value: Float }
`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{
							{TypeName: "Query", FieldNames: []string{"tick"}},
							{TypeName: "Subscription", FieldNames: []string{"tickUpdated"}},
						},
						ChildNodes: plan.TypeFields{
							{TypeName: "Tick", FieldNames: []string{"id", "value"}},
						},
					},
				},
			},
		},
		{
			// Root-field ARGUMENT forwarded as a trigger variable ($$0$$ context variable).
			name: "subscription-root-argument",
			schema: `
schema { query: Query subscription: Subscription }
type Query { product(upc: String!): Product }
type Subscription { productUpdated(upc: String!): Product }
type Product { upc: String! price: Float }
`,
			op: `subscription($upc: String!) { productUpdated(upc: $upc) { upc price } }`,
			// v1 forwards field arguments only when the field carries an argument configuration
			// (printOperation validates against it); planv2 reads them off the operation.
			fields: plan.FieldConfigurations{
				{
					TypeName:  "Subscription",
					FieldName: "productUpdated",
					Arguments: plan.ArgumentsConfigurations{
						{Name: "upc", SourceType: plan.FieldArgumentSource},
					},
				},
			},
			subgraphs: []subgraph{
				{
					name: "products",
					sdl: `
type Query { product(upc: String!): Product }
type Subscription { productUpdated(upc: String!): Product }
type Product { upc: String! price: Float }
`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{
							{TypeName: "Query", FieldNames: []string{"product"}},
							{TypeName: "Subscription", FieldNames: []string{"productUpdated"}},
						},
						ChildNodes: plan.TypeFields{
							{TypeName: "Product", FieldNames: []string{"upc", "price"}},
						},
					},
				},
			},
		},
		{
			// @openfed__subscriptionFilter (M4.4): the root field carries a SubscriptionFilterCondition
			// whose {{ args.min }} template resolves against the forwarded argument. Both planners must
			// build the SAME resolve.SubscriptionFilter (CompareSubscriptionPlans compares it via
			// canonFilter) -- a filter divergence silently changes which events SkipEvent skips.
			name: "subscription-filter-in-argument",
			schema: `
schema { query: Query subscription: Subscription }
type Query { productUpdates(min: Int!): Product }
type Subscription { productUpdates(min: Int!): Product }
type Product { id: ID! price: Int }
`,
			op: `subscription($min: Int!) { productUpdates(min: $min) { id price } }`,
			fields: plan.FieldConfigurations{
				{
					TypeName:  "Subscription",
					FieldName: "productUpdates",
					Arguments: plan.ArgumentsConfigurations{
						{Name: "min", SourceType: plan.FieldArgumentSource},
					},
					SubscriptionFilterCondition: &plan.SubscriptionFilterCondition{
						In: &plan.SubscriptionFieldCondition{
							FieldPath: []string{"price"},
							Values:    []string{"{{ args.min }}"},
						},
					},
				},
			},
			subgraphs: []subgraph{
				{
					name: "products",
					sdl: `
type Query { productUpdates(min: Int!): Product }
type Subscription { productUpdates(min: Int!): Product }
type Product { id: ID! price: Int }
`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{
							{TypeName: "Query", FieldNames: []string{"productUpdates"}},
							{TypeName: "Subscription", FieldNames: []string{"productUpdates"}},
						},
						ChildNodes: plan.TypeFields{
							{TypeName: "Product", FieldNames: []string{"id", "price"}},
						},
					},
				},
			},
		},
		{
			// The FS-SUB worked example: an entity jump BELOW the subscription root -- the per-event
			// response tree carries an ordinary _entities fetch to the second subgraph.
			name: "subscription-entity-jump",
			schema: `
schema { query: Query subscription: Subscription }
type Query { review: Review }
type Subscription { reviewAdded: Review }
type Review { body: String product: Product }
type Product { id: ID! name: String }
`,
			op: `subscription { reviewAdded { body product { name } } }`,
			subgraphs: []subgraph{
				{
					name: "reviews",
					sdl: `
type Query { review: Review }
type Subscription { reviewAdded: Review }
type Review { body: String product: Product }
type Product @key(fields: "id") { id: ID! }
`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{
							{TypeName: "Query", FieldNames: []string{"review"}},
							{TypeName: "Subscription", FieldNames: []string{"reviewAdded"}},
							{TypeName: "Product", FieldNames: []string{"id"}},
						},
						ChildNodes: plan.TypeFields{
							{TypeName: "Review", FieldNames: []string{"body", "product"}},
						},
						FederationMetaData: plan.FederationMetaData{
							Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
						},
					},
				},
				{
					name: "products",
					sdl: `
type Query { _dummy: String }
type Product @key(fields: "id") { id: ID! name: String }
`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{
							{TypeName: "Query", FieldNames: []string{"_dummy"}},
							{TypeName: "Product", FieldNames: []string{"id", "name"}},
						},
						FederationMetaData: plan.FederationMetaData{
							Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
						},
					},
				},
			},
		},
	}
}

// leafGateSubsumptionCases reproduce the customer-corpus field-drop divergence class (M15
// FIELD-DROP root-cause wave) with invented Product/Review/Node types. v1 emits a member-gated
// leaf (`__typename` / an interface field) REDUNDANTLY on top of an ungated selection at the SAME
// response key; planv2 dedups it to one selection. The client response is byte-identical (the
// ungated selection already writes that response key for every concrete type), so the refined oracle
// (leafGateCovered) certifies MATCH. These pin that the dedup is response-equivalence-preserving and
// that a differently-aliased or shape-changing member leaf is NOT collapsed (the neg controls live
// in TestCompareResponseShapes_LeafGateSubsumption).
func leafGateSubsumptionCases() []differentialCase {
	resultUnionSubgraphs := func() []subgraph {
		return []subgraph{
			{
				name: "A",
				sdl: `
extend schema @link(url: "https://specs.apollo.dev/federation/v2.3", import: ["@key"])
type Query { productResult: ProductResult }
union ProductResult = Product | NotFound
type Product @key(fields: "id") { id: ID! name: String }
type NotFound { message: String }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{TypeName: "Query", FieldNames: []string{"productResult"}},
						{TypeName: "Product", FieldNames: []string{"id", "name"}},
					},
					ChildNodes: plan.TypeFields{{TypeName: "NotFound", FieldNames: []string{"message"}}},
					FederationMetaData: plan.FederationMetaData{
						Keys: plan.FederationFieldConfigurations{{TypeName: "Product", SelectionSet: "id"}},
					},
				},
			},
		}
	}
	nodeInterfaceSubgraphs := func() []subgraph {
		return []subgraph{
			{
				name: "A",
				sdl: `
extend schema @link(url: "https://specs.apollo.dev/federation/v2.3", import: ["@key"])
type Query { node: Node }
interface Node { id: ID! }
type Invoice implements Node @key(fields: "id") { id: ID! total: Int }
type Receipt implements Node @key(fields: "id") { id: ID! paid: Boolean }
`,
				meta: &plan.DataSourceMetadata{
					RootNodes: plan.TypeFields{
						{TypeName: "Query", FieldNames: []string{"node"}},
						{TypeName: "Invoice", FieldNames: []string{"id", "total"}},
						{TypeName: "Receipt", FieldNames: []string{"id", "paid"}},
					},
					ChildNodes: plan.TypeFields{{TypeName: "Node", FieldNames: []string{"id"}}},
					FederationMetaData: plan.FederationMetaData{
						Keys: plan.FederationFieldConfigurations{
							{TypeName: "Invoice", SelectionSet: "id"},
							{TypeName: "Receipt", SelectionSet: "id"},
						},
					},
				},
			},
		}
	}
	return []differentialCase{
		{
			// __typename bucket: an ungated `__typename` on a result union PLUS a redundant member-gated
			// `... on Product { __typename }`. planv2 keeps one; v1 keeps both. Response-identical.
			name: "leaf-subsumption-typename-on-union",
			schema: `
schema { query: Query }
type Query { productResult: ProductResult }
union ProductResult = Product | NotFound
type Product { id: ID! name: String }
type NotFound { message: String }
`,
			op:        `{ productResult { __typename ... on Product { __typename name } ... on NotFound { message } } }`,
			subgraphs: resultUnionSubgraphs(),
		},
		{
			// real-leaf bucket: an ungated interface field `id` PLUS a redundant member-gated
			// `... on Invoice { id }`. planv2 dedups to the interface-level `id`. Response-identical.
			name: "leaf-subsumption-interface-id",
			schema: `
schema { query: Query }
type Query { node: Node }
interface Node { id: ID! }
type Invoice implements Node { id: ID! total: Int }
type Receipt implements Node { id: ID! paid: Boolean }
`,
			op:        `{ node { id ... on Invoice { id total } ... on Receipt { paid } } }`,
			subgraphs: nodeInterfaceSubgraphs(),
		},
	}
}

func baseAgreedCases() []differentialCase {
	return []differentialCase{
		{
			name: "entity-jump",
			schema: `
schema { query: Query }
type Query { me: User }
type User { id: ID! name: String age: Int }
`,
			op: `{ me { name age } }`,
			subgraphs: []subgraph{
				{
					name: "users",
					sdl: `
type Query { me: User }
type User @key(fields: "id") { id: ID! name: String }
`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{
							{TypeName: "Query", FieldNames: []string{"me"}},
							{TypeName: "User", FieldNames: []string{"id", "name"}},
						},
						FederationMetaData: plan.FederationMetaData{
							Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
						},
					},
				},
				{
					name: "ages",
					sdl: `
type Query { _dummy: String }
type User @key(fields: "id") { id: ID! age: Int }
`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{
							{TypeName: "Query", FieldNames: []string{"_dummy"}},
							{TypeName: "User", FieldNames: []string{"id", "age"}},
						},
						FederationMetaData: plan.FederationMetaData{
							Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
						},
					},
				},
			},
		},
		{
			// Leaf-kind parity (M1.5 leaf-kind fix): enum leaves must lower to resolve.Enum (not
			// generic String), root __typename to resolve.StaticString, and nested __typename to
			// resolve.String{IsTypeName} -- exactly what v1 emits. The oracle compares leaf NodeKind,
			// so any regression to String on the enum leaves or to String on the root __typename
			// re-surfaces here as a leaf-kind divergence.
			name: "enum-and-typename-leaves",
			schema: `
schema { query: Query }
type Query { me: User }
type User { id: ID! role: Role status: Status }
enum Role { ADMIN USER GUEST }
enum Status { ACTIVE INACTIVE }
`,
			op: `{ __typename me { __typename id role status } }`,
			subgraphs: []subgraph{
				{
					name: "accounts",
					sdl: `
type Query { me: User }
type User { id: ID! role: Role status: Status }
enum Role { ADMIN USER GUEST }
enum Status { ACTIVE INACTIVE }
`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{
							{TypeName: "Query", FieldNames: []string{"me"}},
						},
						ChildNodes: plan.TypeFields{
							{TypeName: "User", FieldNames: []string{"id", "role", "status"}},
						},
					},
				},
			},
		},
		{
			name: "cross-subgraph-roots",
			schema: `
schema { query: Query }
type Query { hello: String world: String }
`,
			op: `{ hello world }`,
			subgraphs: []subgraph{
				{
					name: "a",
					sdl:  `type Query { hello: String }`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"hello"}}},
					},
				},
				{
					name: "b",
					sdl:  `type Query { world: String }`,
					meta: &plan.DataSourceMetadata{
						RootNodes: plan.TypeFields{{TypeName: "Query", FieldNames: []string{"world"}}},
					},
				},
			},
		},
	}
}

// --- oracle unit-test synthetics -------------------------------------------------------------

type resolveField struct {
	name     string
	gate     string
	value    resolve.Node // explicit leaf node; nil leaf defaults to resolve.String
	children []*resolveField
}

func leaf(name string) *resolveField            { return &resolveField{name: name} }
func gatedLeaf(name, gate string) *resolveField { return &resolveField{name: name, gate: gate} }
func typedLeaf(name string, v resolve.Node) *resolveField {
	return &resolveField{name: name, value: v}
}
func obj(name string, kids ...*resolveField) *resolveField {
	return &resolveField{name: name, children: kids}
}

func synthPlan(fields ...*resolveField) plan.Plan {
	return &plan.SynchronousResponsePlan{
		Response: &resolve.GraphQLResponse{Data: synthObject(fields)},
	}
}

func synthObject(fields []*resolveField) *resolve.Object {
	o := &resolve.Object{}
	for _, f := range fields {
		rf := &resolve.Field{Name: []byte(f.name)}
		if f.gate != "" {
			rf.OnTypeNames = [][]byte{[]byte(f.gate)}
		}
		switch {
		case len(f.children) > 0:
			rf.Value = synthObject(f.children)
		case f.value != nil:
			rf.Value = f.value
		default:
			rf.Value = &resolve.String{}
		}
		o.Fields = append(o.Fields, rf)
	}
	return o
}
