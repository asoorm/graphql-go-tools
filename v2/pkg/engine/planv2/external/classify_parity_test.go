package external

// classify_parity_test.go -- the diagnostic-parity pin for the reachability classifier
// (reachability_classify_test.go -- a NOT-FOR-COMMIT, gitignored local diagnostic; this pin is
// committed and corpus-free, and deliberately references nothing defined there).
//
// THE INCIDENT (2026-07-16 classifier report, "7-op [v1-OK] orphan value-type class"): the
// classifier replicated the planv2 pipeline with RootType {query, mutation} -- omitting the
// subscription root the shipping facade (planv2.NewPlanner) registers. A subscription operation
// then fails its own root goal: the un-rooted Subscription type's object node has no producing
// edge, so every type reachable only through the subscription payload walks up to an orphan, and
// the classifier reported ErrNoValidPlan with root-blocker shape
// FIELD->ORPHAN:valuetype-unreachable-everywhere (plain payload chain: the `ticks.box.x`
// operation below) or FIELD->TYPEMOVE:ORPHAN:valuetype-unreachable-everywhere (a union member
// leaf: the `... on Add` operation below) -- verified against the classifier's own classifyGoal on
// this exact fixture -- while the REAL pipeline plans both operations. The classifier now builds
// through classifyBuildConfig (defined HERE, facade-verbatim, so the committed tree owns it).
//
// The moral: a diagnostic that replays the pipeline must replay the pipeline's CONFIG -- a
// diagnostic-only divergence manufactures phantom gap classes.

import (
	"errors"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
)

// classifyBuildConfig is the BuildConfig every local pipeline diagnostic MUST build through: the
// shipping facade's (planv2.NewPlanner) config verbatim -- root types included. See the package
// comment above for the incident a divergent diagnostic config caused. Referenced by the
// gitignored classifier so the config cannot silently fork again.
var classifyBuildConfig = hypergraph.BuildConfig{
	Weights:  hypergraph.DefaultWeights(),
	RootType: map[string]string{"query": "Query", "mutation": "Mutation", "subscription": "Subscription"},
}

// paritySearchConfig mirrors the facade's fixed search budget (planv2.searchConfig).
var paritySearchConfig = search.Config{Combine: search.Sum, PreflightCap: 1 << 30, StateCap: 1 << 24}

const parityComposed = `
schema { query: Query subscription: Subscription }
type Query { noop: String }
type Subscription { ticks: Tick }
type Tick { value: Float box: Box event: Event }
type Box { x: Float }
union Event = Add | Remove
type Add { n: Int }
type Remove { m: Int }
`

const paritySubgraph = `
type Query { noop: String }
type Subscription { ticks: Tick }
type Tick { value: Float box: Box event: Event }
type Box { x: Float }
union Event = Add | Remove
type Add { n: Int }
type Remove { m: Int }
`

// parityOps are the two artifact witnesses: under the {query,mutation}-only config the first
// classifies FIELD->ORPHAN:valuetype-unreachable-everywhere, the second
// FIELD->TYPEMOVE:ORPHAN:valuetype-unreachable-everywhere (the report's two [v1-OK] buckets).
var parityOps = []string{
	`subscription { ticks { box { x } } }`,
	`subscription { ticks { event { ... on Add { n } } } }`,
}

func TestClassifierPipelineParity_SubscriptionRoots(t *testing.T) {
	ds, _, err := audit.BuildDataSources(audit.Case{
		Name: "classify-parity", Suite: "classify-parity",
		Subgraphs:  []audit.Subgraph{{Name: "realtime", SDL: paritySubgraph}},
		Definition: parityComposed,
	})
	if err != nil {
		t.Fatalf("datasources: %v", err)
	}

	// (1) Artifact reproduction: the subscription-root-less config turns both operations into
	// ErrNoValidPlan (their payload subtrees orphan under the un-rooted Subscription type).
	oldCfg := hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation"},
	}
	hOld, err := hypergraph.Build(ds, oldCfg)
	if err != nil {
		t.Fatalf("build (artifact config): %v", err)
	}
	for _, opText := range parityOps {
		op, def, report := parseAndNormalize(parityComposed, opText)
		if report.HasErrors() {
			t.Fatalf("normalize %q: %s", opText, report.Error())
		}
		o, err := obligation.Build(op, def, "", hOld)
		if err != nil {
			t.Fatalf("obligation %q: %v", opText, err)
		}
		_, err = search.Search(hOld, o, paritySearchConfig)
		var nvp *search.ErrNoValidPlan
		if !errors.As(err, &nvp) {
			t.Fatalf("artifact config: expected ErrNoValidPlan for %q, got %v", opText, err)
		}
	}

	// (2) Parity: the shared diagnostic config plans both operations at pipeline level...
	hNew, err := hypergraph.Build(ds, classifyBuildConfig)
	if err != nil {
		t.Fatalf("build (classifier config): %v", err)
	}
	for _, opText := range parityOps {
		op, def, report := parseAndNormalize(parityComposed, opText)
		if report.HasErrors() {
			t.Fatalf("normalize %q: %s", opText, report.Error())
		}
		o, err := obligation.Build(op, def, "", hNew)
		if err != nil {
			t.Fatalf("obligation %q: %v", opText, err)
		}
		if _, err := search.Search(hNew, o, paritySearchConfig); err != nil {
			t.Fatalf("classifier config must plan %q (the facade does): %v", opText, err)
		}
	}

	// ...and the facade plans them end to end (the ground truth the classifier must match).
	pl, err := planv2.NewPlanner(plan.Configuration{DataSources: ds, DisableResolveFieldPositions: true})
	if err != nil {
		t.Fatalf("facade NewPlanner: %v", err)
	}
	for _, opText := range parityOps {
		op, def, report := parseAndNormalize(parityComposed, opText)
		if report.HasErrors() {
			t.Fatalf("normalize %q: %s", opText, report.Error())
		}
		if p := pl.Plan(op, def, "", report); report.HasErrors() || p == nil {
			t.Fatalf("facade must plan %q: %v", opText, report.Error())
		}
	}
}
