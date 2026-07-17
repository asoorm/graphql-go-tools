package external

import (
	"os"
	"path/filepath"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/differential"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// Differential classification statuses. Exactly these five buckets: every (graph, operation) case
// lands in one and only one.
const (
	// StatusMatch: both planners agree on response shape (differential.CompareResponseShapes == nil).
	StatusMatch = "MATCH"
	// StatusExpectedDivergence: the shapes differ, but the (schema, operation) content pair is a
	// registered entry in differential.KnownDivergences -- an adjudicated, documented divergence
	// (typically v1 known-wrong), not a bug to chase.
	StatusExpectedDivergence = "EXPECTED_DIVERGENCE"
	// StatusUnexplained: the shapes differ and the pair is NOT in the allow-list -- divergence policy
	// (differential package doc) prohibits silence here; this is either a planv2 bug or a new,
	// undocumented divergence that needs adjudication.
	StatusUnexplained = "UNEXPLAINED"
	// StatusPlanV2Error: planv2 failed to build a Configuration or produce a plan for the case.
	// Per the differential harness's own policy, planv2 MUST plan every case -- this is always
	// noteworthy and never allow-listed.
	StatusPlanV2Error = "planv2-error"
	// StatusV1Error: the v1 planner failed to produce a plan for the case (config build succeeded).
	StatusV1Error = "v1-error"
)

// CaseResult is the outcome of running one (graph, operation) pair through BOTH oracles this
// package reuses: the differential package's response-shape oracle (v1 vs planv2 parity) and the
// audit package's plan-level assertions (fetch-document validation against subgraph schemas, and
// root-entry assertion 5), the latter evaluated on the planv2 plan alone.
type CaseResult struct {
	Graph     string // graph directory name
	Operation string // operation file basename

	Status string // one of the Status* constants above
	Detail string // human-readable explanation: divergence description or error text

	// AuditStatus/AuditReason are audit.Outcome's classification (PASS/SKIP/FAIL/GAP) of the planv2
	// plan alone against the plan-level assertions (fetch-doc validity, root-entry honesty). This is
	// informational for the external-corpus report; it does not affect Status above, which is the
	// differential (v1-vs-planv2) classification.
	AuditStatus string
	AuditReason string
}

// RunGraph runs every discovered operation for graph through the differential oracle and the audit
// plan-level assertions, returning one CaseResult per operation (in the graph's OperationPaths
// order).
func RunGraph(graph Graph) []CaseResult {
	results := make([]CaseResult, 0, len(graph.OperationPaths))
	for _, opPath := range graph.OperationPaths {
		results = append(results, runCase(graph, opPath))
	}
	return results
}

func runCase(graph Graph, opPath string) CaseResult {
	res := CaseResult{Graph: graph.Name, Operation: filepath.Base(opPath)}

	opBytes, err := os.ReadFile(opPath)
	if err != nil {
		res.Status, res.Detail = StatusPlanV2Error, "read operation: "+err.Error()
		return res
	}
	opText := string(opBytes)

	// audit.Case is reused verbatim as the common (subgraphs, definition, operation) carrier both
	// oracles need -- audit.BuildDataSources and differential.CaseKey are exported so this package can
	// drive both oracles without duplicating federation-metadata derivation or the divergence
	// allow-list lookup.
	auditCase := audit.Case{
		Name:       res.Operation,
		Suite:      graph.Name,
		Subgraphs:  graph.Subgraphs,
		Definition: graph.SupergraphSDL,
		Operation:  opText,
	}

	// Plan-level assertions (fetch-doc validation, root-entry assertion 5), planv2 only.
	auditOutcome := audit.Run(auditCase)
	res.AuditStatus, res.AuditReason = auditOutcome.Status, auditOutcome.Reason

	dataSources, _, err := audit.BuildDataSources(auditCase)
	if err != nil {
		res.Status, res.Detail = StatusPlanV2Error, "build data sources: "+err.Error()
		return res
	}
	cfg := plan.Configuration{DataSources: dataSources, DisableResolveFieldPositions: true}

	newPlanner, err := planv2.NewPlanner(cfg)
	if err != nil {
		res.Status, res.Detail = StatusPlanV2Error, "planv2 NewPlanner: "+err.Error()
		return res
	}
	oldPlanner, err := plan.NewPlanner(cfg)
	if err != nil {
		res.Status, res.Detail = StatusV1Error, "v1 NewPlanner: "+err.Error()
		return res
	}

	// Each planner mutates its op/def input, so each gets its own parsed copy -- same convention as
	// differential.runBoth and audit.runInner.
	newOp, newDef, newReport := parseAndNormalize(graph.SupergraphSDL, opText)
	var newPlan plan.Plan
	if !newReport.HasErrors() {
		newPlan = newPlanner.Plan(newOp, newDef, "", newReport)
	}
	if newReport.HasErrors() {
		res.Status, res.Detail = StatusPlanV2Error, "planv2 plan: "+newReport.Error()
		return res
	}

	oldOp, oldDef, oldReport := parseAndNormalize(graph.SupergraphSDL, opText)
	var oldPlan plan.Plan
	if !oldReport.HasErrors() {
		oldPlan = oldPlanner.Plan(oldOp, oldDef, "", oldReport)
	}
	if oldReport.HasErrors() {
		res.Status, res.Detail = StatusV1Error, "v1 plan: "+oldReport.Error()
		return res
	}

	div := differential.CompareResponseShapes(oldPlan, newPlan)
	if div == nil {
		res.Status = StatusMatch
		return res
	}
	if reason, allowed := differential.KnownDivergences[differential.CaseKey(graph.SupergraphSDL, opText)]; allowed {
		res.Status, res.Detail = StatusExpectedDivergence, reason
		return res
	}
	res.Status, res.Detail = StatusUnexplained, div.String()
	return res
}

// parseAndNormalize runs the same pinned pipeline differential.parseAndNormalize and
// audit.parseAndNormalize use (both unexported in their own packages -- this is a third, deliberate
// copy of the same small, dependency-free logic rather than a new cross-package export): merge the
// definition with the base schema, then normalize the operation with the v1 option set, so both
// planners see byte-identical input.
func parseAndNormalize(schema, op string) (*ast.Document, *ast.Document, *operationreport.Report) {
	def := unsafeparser.ParseGraphqlDocumentString(schema)
	report := &operationreport.Report{}
	if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
		report.AddInternalError(err)
		return nil, nil, report
	}
	operation := unsafeparser.ParseGraphqlDocumentString(op)
	astnormalization.NewWithOpts(
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
	).NormalizeOperation(&operation, &def, report)
	return &operation, &def, report
}
