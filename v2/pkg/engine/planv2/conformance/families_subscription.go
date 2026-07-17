package conformance

// families_subscription.go -- subscription and @defer scenario families
// (FEDERATION_SEMANTICS.md Sections 14-15; FEDERATION_SEMANTICS_FORMAL.md Section 2.14-2.15):
//
//	subscription  FS-SUB-1..5 -- the trigger/response split: one `subscription` trigger document
//	              against a declaring subgraph, per-event fetches as `query` documents.
//	defer         FS-DEF-1/2/3/6 -- the D11.13 realization: scope-variant partition (primary
//	              fetches never select deferred-only data and vice versa), descriptor path/label
//	              bookkeeping (AX-DEF-WIRE), routing invariance vs the erased twin.
//	defer-erasure FS-DEF-7 -- @defer inside subscriptions (and mutations) is ignored: the plan is
//	              identical to the erased twin.
import (
	"fmt"
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
)

func init() {
	register(Family{
		Name:         "subscription",
		Propositions: []string{"FS-SUB-1", "FS-SUB-2", "FS-SUB-3", "FS-SUB-4", "FS-SUB-5", "FS-SUB-6"},
		Generate:     genSubscription,
	})
	register(Family{
		Name:         "defer",
		Propositions: []string{"FS-DEF-1", "FS-DEF-2", "FS-DEF-3", "FS-DEF-4", "FS-DEF-5", "FS-DEF-6"},
		Generate:     genDefer,
	})
	register(Family{
		Name:         "defer-erasure",
		Propositions: []string{"FS-DEF-7"},
		Generate:     genDeferErasure,
	})
}

// subSchema: rt declares Subscription.<trigger>: Review { body product: Product@key(id) };
// products resolves Product.name. When shared is true, a second subgraph also declares the
// trigger root field (the FS-SUB-2 shareable-trigger shape).
func subSchema(trigger, body, pname string, shared bool) Schema {
	rt := SubgraphSpec{Name: "rt", Types: []*Type{
		{Name: "Subscription", Kind: KindObject,
			Fields: []Field{{Name: trigger, Type: "Review", Shareable: shared}}},
		{Name: "Review", Kind: KindObject,
			Fields: []Field{{Name: body, Type: "String"}, {Name: "product", Type: "Product"}}},
		{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
			Fields: []Field{{Name: "id", Type: "ID!"}}},
	}}
	products := SubgraphSpec{Name: "products", Types: []*Type{
		{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop", Type: "String"}}},
		{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
			Fields: []Field{{Name: "id", Type: "ID!"}, {Name: pname, Type: "String"}}},
	}}
	subs := []SubgraphSpec{rt, products}
	if shared {
		subs = append(subs, SubgraphSpec{Name: "rt2", Types: []*Type{
			{Name: "Subscription", Kind: KindObject,
				Fields: []Field{{Name: trigger, Type: "Review", Shareable: true}}},
			{Name: "Review", Kind: KindObject,
				Fields: []Field{{Name: body, Type: "String", Shareable: true}, {Name: "product", Type: "Product"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}}},
		}})
	}
	return Schema{Subgraphs: subs}
}

func genSubscription(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	// trigger-split: the Section 14 worked example.
	{
		id := caseID("FS-SUB-3", "subscription", "trigger-split", seed)
		r := newRNG(id, seed)
		n := namer{r}
		trigger, body, pname := n.field("reviewAdded"), n.field("body"), n.field("pname")
		schema := subSchema(trigger, body, pname, false)
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-SUB-1", "FS-SUB-2", "FS-SUB-3", "FS-SUB-4", "FS-SUB-5", "FS-SUB-6"},
			Family:       "subscription",
			Seed:         seed,
			Case: BuildAuditCase("trigger-split", "subscription", schema,
				"subscription { "+trigger+" { "+body+" product { "+pname+" } } }", ""),
			Subscription: true,
			Oracles: []Oracle{
				OracleTriggerSubgraph("rt"),
				// FS-KEY-3 below the trigger: the trigger document injects the entity key.
				OracleDocContains("rt", "__typename"),
				OracleFieldServedBy(pname, "products"),
				OracleEntityFetchCount(1, 1),
			},
		})
	}

	// shared-trigger (FS-SUB-2): two declaring subgraphs; the plan picks exactly one.
	{
		id := caseID("FS-SUB-2", "subscription", "shared-trigger", seed)
		r := newRNG(id, seed)
		n := namer{r}
		trigger, body, pname := n.field("reviewAdded"), n.field("body"), n.field("pname")
		schema := subSchema(trigger, body, pname, true)
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-SUB-2", "FS-SUB-1"},
			Family:       "subscription",
			Seed:         seed,
			Case: BuildAuditCase("shared-trigger", "subscription", schema,
				"subscription { "+trigger+" { "+body+" } }", ""),
			Subscription: true,
			Oracles: []Oracle{
				OracleTriggerSubgraph("rt", "rt2"),
			},
		})
	}
	return out
}

func genDefer(seed uint64) []GeneratedCase {
	scenario := "entity-boundary"
	id := caseID("FS-DEF-2", "defer", scenario, seed)
	r := newRNG(id, seed)
	n := namer{r}
	name, reviews := n.field("pname"), n.field("reviews")

	subs := []SubgraphSpec{
		{Name: "products", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "top", Type: "Product"}}},
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: name, Type: "String"}}},
		}},
		{Name: "reviews", Types: []*Type{
			{Name: "Product", Kind: KindObject, Keys: []Key{{Fields: "id"}},
				Fields: []Field{{Name: "id", Type: "ID!"}, {Name: reviews, Type: "[String]"}}},
		}},
	}
	schema := Schema{Subgraphs: subs}
	deferOp := fmt.Sprintf(`{ top { %s ... @defer(label: "r") { %s } } }`, name, reviews)
	erasedOp := fmt.Sprintf("{ top { %s %s } }", name, reviews)

	return []GeneratedCase{{
		ID:           id,
		Propositions: []string{"FS-DEF-1", "FS-DEF-2", "FS-DEF-3", "FS-DEF-4", "FS-DEF-5", "FS-DEF-6"},
		Family:       "defer",
		Seed:         seed,
		Case: BuildAuditCase(scenario, "defer", schema, deferOp,
			fmt.Sprintf(`{"data": {"top": {%q: "x", %q: ["r"]}}}`, name, reviews)),
		Defer:             true,
		BaselineOperation: erasedOp,
		Oracles: []Oracle{
			oracleDeferPartition(name, reviews),
			oracleDeferDescriptor("r", "top"),
			oracleDeferRoutingInvariance(BuildAuditCase("erased-twin", "defer", schema, erasedOp, "")),
		},
	}}
}

// oracleDeferPartition (FS-DEF-2/5): scope-0 fetches never select the deferred-only field, and
// the deferred scope's fetches never select the non-deferred one -- the partition at fetch
// boundaries, with the key fields free to ride either side (FS-DEF-6 keeps them in the initial
// set as inputs).
func oracleDeferPartition(initialField, deferredField string) Oracle {
	return func(a *Artifacts) error {
		if a.Defer == nil {
			return fmt.Errorf("expected a DeferResponsePlan (D11.13); got a non-defer plan")
		}
		sawDeferred := false
		for _, f := range singleFetches(a) {
			doc := f.doc()
			if f.sf.DeferID == 0 {
				if strings.Contains(doc, deferredField) {
					return fmt.Errorf("FS-DEF-2: initial (scope-0) fetch %d selects deferred-only field %q: %s", f.i, deferredField, doc)
				}
				continue
			}
			sawDeferred = true
			if strings.Contains(doc, initialField) {
				return fmt.Errorf("FS-DEF-2: deferred fetch %d re-selects non-deferred field %q: %s", f.i, initialField, doc)
			}
		}
		if !sawDeferred {
			return fmt.Errorf("no deferred-scope fetch found (partition empty; FS-DEF-1 would allow it only via the erasure realization, which D11.13 replaced)")
		}
		return nil
	}
}

// oracleDeferDescriptor (FS-DEF-3 / AX-DEF-WIRE): a descriptor exists with the client's label,
// mounted at the fragment's response path.
func oracleDeferDescriptor(label, pathHead string) Oracle {
	return func(a *Artifacts) error {
		if a.Defer == nil {
			return fmt.Errorf("expected a DeferResponsePlan")
		}
		for _, d := range a.Defer.DeferDescriptors {
			if d.Label == label && len(d.Path) > 0 && d.Path[0] == pathHead {
				return nil
			}
		}
		return fmt.Errorf("FS-DEF-3: no defer descriptor with label %q at path head %q (have %+v)",
			label, pathHead, a.Defer.DeferDescriptors)
	}
}

// oracleDeferRoutingInvariance (FS-DEF-6): the defer plan's SET of (subgraph, entity-ness) fetch
// routes equals the erased twin's -- deferral may split fetches across scope variants (more
// fetches), but it never re-routes data: no new subgraph source, no lost one.
func oracleDeferRoutingInvariance(erased audit.Case) Oracle {
	return func(a *Artifacts) error {
		twinArts, err := planCase(erased, false)
		if err != nil {
			return fmt.Errorf("erased twin failed to plan: %w", err)
		}
		mine := fetchRouteSet(a)
		theirs := fetchRouteSet(twinArts)
		if mine != theirs {
			return fmt.Errorf("FS-DEF-6: routing differs from the erased twin:\n defer:  %s\n erased: %s", mine, theirs)
		}
		return nil
	}
}

func fetchRouteSet(a *Artifacts) string {
	set := map[string]bool{}
	for _, f := range singleFetches(a) {
		k := f.subgraph()
		if f.isEntity() {
			k += "/_entities"
		}
		set[k] = true
	}
	routes := make([]string, 0, len(set))
	for k := range set {
		routes = append(routes, k)
	}
	sort.Strings(routes)
	return strings.Join(routes, " ")
}

// genDeferErasure (FS-DEF-7): @defer inside a subscription (and a mutation) is ignored -- the
// plan is identical to the erased twin.
func genDeferErasure(seed uint64) []GeneratedCase {
	var out []GeneratedCase

	// subscription
	{
		id := caseID("FS-DEF-7", "defer-erasure", "subscription", seed)
		r := newRNG(id, seed)
		n := namer{r}
		trigger, body, pname := n.field("reviewAdded"), n.field("body"), n.field("pname")
		schema := subSchema(trigger, body, pname, false)
		deferOp := "subscription { " + trigger + " { " + body + " ... @defer { product { " + pname + " } } } }"
		erasedOp := "subscription { " + trigger + " { " + body + " product { " + pname + " } } }"
		twin := BuildAuditCase("sub-erased-twin", "defer-erasure", schema, erasedOp, "")
		out = append(out, GeneratedCase{
			ID:           id,
			Propositions: []string{"FS-DEF-7"},
			Family:       "defer-erasure",
			Seed:         seed,
			Case:         BuildAuditCase("subscription", "defer-erasure", schema, deferOp, ""),
			Subscription: true,
			Defer:        true,
			Oracles: []Oracle{
				OracleTwinPlanEqual(twin, "", ""),
			},
		})
	}

	// mutation: flattens to a synchronous plan identical to the erased twin.
	{
		id := caseID("FS-DEF-7", "defer-erasure", "mutation", seed)
		r := newRNG(id, seed)
		n := namer{r}
		mut, x, y := n.field("save"), n.field("xa"), n.field("yb")

		subs := []SubgraphSpec{{Name: "w", Types: []*Type{
			{Name: "Query", Kind: KindObject, Fields: []Field{{Name: "noop", Type: "String"}}},
			{Name: "Mutation", Kind: KindObject, Fields: []Field{{Name: mut, Type: "Doc"}}},
			{Name: "Doc", Kind: KindObject,
				Fields: []Field{{Name: x, Type: "String"}, {Name: y, Type: "String"}}},
		}}}
		schema := Schema{Subgraphs: subs}
		deferOp := "mutation { " + mut + " { " + x + " ... @defer { " + y + " } } }"
		erasedOp := "mutation { " + mut + " { " + x + " " + y + " } }"
		twin := BuildAuditCase("mut-erased-twin", "defer-erasure", schema, erasedOp, "")
		out = append(out, GeneratedCase{
			ID:                id,
			Propositions:      []string{"FS-DEF-7"},
			Family:            "defer-erasure",
			Seed:              seed,
			Case:              BuildAuditCase("mutation", "defer-erasure", schema, deferOp, ""),
			Defer:             true,
			BaselineOperation: erasedOp,
			Oracles: []Oracle{
				OracleTwinPlanEqual(twin, "", ""),
			},
		})
	}
	return out
}
