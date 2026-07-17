package external

// opscoped.go -- the external-corpus (customer sweep) mode-equality pass for the operation-scoped
// search (FORMAL_SPEC Section 6.5). planv2-vs-planv2: each (graph, operation) case is planned with the
// default whole-graph settle and with planv2.Config{OperationScopedSearch: true}, and the two
// plans must be byte-identical on the canonical fetch rendering and equivalent under the
// differential package's semantic oracles. This is the flag the coordinator runs against the
// private corpus (TestExternalCorpusOpScopedModeEquality, gated on PLANNER_V2_EXTERNAL_CORPUS
// exactly like the differential sweep); nothing in-repo ever reads corpus content.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	planlib "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/differential"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// ModeEqualityResult is the outcome of planning one (graph, operation) case under both search
// modes and comparing the plans.
type ModeEqualityResult struct {
	Graph     string
	Operation string
	Equal     bool
	Detail    string // empty when Equal; the first difference (or error divergence) otherwise
}

// RunGraphModeEquality runs the Section 6.5 mode-equality comparison for every discovered operation of
// one graph.
func RunGraphModeEquality(graph Graph) []ModeEqualityResult {
	results := make([]ModeEqualityResult, 0, len(graph.OperationPaths))
	for _, opPath := range graph.OperationPaths {
		results = append(results, runCaseModeEquality(graph, opPath))
	}
	return results
}

func runCaseModeEquality(graph Graph, opPath string) ModeEqualityResult {
	res := ModeEqualityResult{Graph: graph.Name, Operation: filepath.Base(opPath)}
	opBytes, err := os.ReadFile(opPath)
	if err != nil {
		res.Detail = "read operation: " + err.Error()
		return res
	}
	opText := string(opBytes)
	auditCase := audit.Case{
		Name: res.Operation, Suite: graph.Name,
		Subgraphs: graph.Subgraphs, Definition: graph.SupergraphSDL, Operation: opText,
	}
	dataSources, _, err := audit.BuildDataSources(auditCase)
	if err != nil {
		res.Detail = "build data sources: " + err.Error()
		return res
	}
	cfg := planlib.Configuration{DataSources: dataSources, DisableResolveFieldPositions: true}

	planU, reportU, err := planMode(cfg, graph.SupergraphSDL, opText, planv2.Config{})
	if err != nil {
		res.Detail = "unscoped: " + err.Error()
		return res
	}
	planS, reportS, err := planMode(cfg, graph.SupergraphSDL, opText, planv2.Config{OperationScopedSearch: true})
	if err != nil {
		res.Detail = "scoped: " + err.Error()
		return res
	}
	if reportU.HasErrors() != reportS.HasErrors() {
		res.Detail = fmt.Sprintf("mode error divergence: unscoped=%q scoped=%q", reportU.Error(), reportS.Error())
		return res
	}
	if reportU.HasErrors() {
		if reportU.Error() != reportS.Error() {
			res.Detail = fmt.Sprintf("different plan errors: unscoped=%q scoped=%q", reportU.Error(), reportS.Error())
			return res
		}
		res.Equal = true // both modes refuse identically -- mode-equal by the Section 6.5 contract
		return res
	}

	canonU, errU := canonicalFetches(planU)
	canonS, errS := canonicalFetches(planS)
	if errU != nil || errS != nil {
		res.Detail = fmt.Sprintf("canonicalize: unscoped=%v scoped=%v", errU, errS)
		return res
	}
	if canonU != canonS {
		res.Detail = fmt.Sprintf("canonical plans differ:\n--- unscoped ---\n%s\n--- scoped ---\n%s", canonU, canonS)
		return res
	}
	var div *differential.Divergence
	_, uIsDefer := planU.(*planlib.DeferResponsePlan)
	_, sIsDefer := planS.(*planlib.DeferResponsePlan)
	if _, isSub := planS.(*planlib.SubscriptionResponsePlan); isSub {
		div = differential.CompareSubscriptionPlans(planU, planS)
	} else if uIsDefer || sIsDefer {
		div = differential.CompareDeferPlans(planU, planS)
	} else {
		div = differential.CompareResponseShapes(planU, planS)
	}
	if div != nil {
		res.Detail = "semantic divergence: " + div.String()
		return res
	}
	res.Equal = true
	return res
}

// planMode plans one case through the planv2 facade under the given options.
func planMode(cfg planlib.Configuration, schema, op string, opts planv2.Config) (planlib.Plan, *operationreport.Report, error) {
	planner, err := planv2.NewPlannerWithConfig(cfg, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("NewPlanner: %w", err)
	}
	opDoc, defDoc, report := parseAndNormalize(schema, op)
	if report.HasErrors() {
		return nil, report, nil
	}
	p := planner.Plan(opDoc, defDoc, "", report)
	return p, report, nil
}

// canonicalFetches renders a plan's fetch tree canonically (documents, targets, dependencies,
// defer ids, response paths, trigger), mirroring the audit/differential gates' renderer.
func canonicalFetches(p planlib.Plan) (string, error) {
	var trigger *resolve.GraphQLSubscriptionTrigger
	var fetches []*resolve.FetchItem
	switch pl := p.(type) {
	case *planlib.SynchronousResponsePlan:
		if pl == nil || pl.Response == nil {
			return "", fmt.Errorf("nil SynchronousResponsePlan")
		}
		fetches = pl.Response.RawFetches
	case *planlib.SubscriptionResponsePlan:
		if pl == nil || pl.Response == nil || pl.Response.Response == nil {
			return "", fmt.Errorf("nil SubscriptionResponsePlan")
		}
		trigger = &pl.Response.Trigger
		fetches = pl.Response.Response.RawFetches
	case *planlib.DeferResponsePlan:
		if pl == nil || pl.Response == nil || pl.Response.Response == nil {
			return "", fmt.Errorf("nil DeferResponsePlan")
		}
		fetches = pl.Response.Response.RawFetches
	default:
		return "", fmt.Errorf("unexpected plan type %T", p)
	}
	var b strings.Builder
	if trigger != nil {
		doc := ""
		if trigger.QueryPlan != nil {
			doc = trigger.QueryPlan.Query
		}
		fmt.Fprintf(&b, "trigger %s %s\n", trigger.SourceName, doc)
	}
	for i, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok {
			fmt.Fprintf(&b, "fetch %d (%T)\n", i, item.Fetch)
			continue
		}
		doc := ""
		if sf.QueryPlan != nil {
			doc = sf.QueryPlan.Query
		}
		fmt.Fprintf(&b, "fetch %d sg=%s deps=%v defer=%d path=%s doc=%s\n",
			i, sf.DataSourceIdentifier, sf.DependsOnFetchIDs, sf.DeferID, item.ResponsePath, doc)
	}
	return b.String(), nil
}
