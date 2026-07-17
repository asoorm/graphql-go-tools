// Package conformance is the property-based conformance-case GENERATOR for planner-v2: it turns
// the propositions of docs/planner-v2/FEDERATION_SEMANTICS.md (and the formal dispositions of
// FEDERATION_SEMANTICS_FORMAL.md) into executable, seeded, deterministic test cases -- synthesized
// subgraph SDLs + operation + plan-property assertions derived from each proposition.
//
// Design:
//   - Every generated case is a pure function of its ID and seed (splitmix64; NO time or global
//     randomness), so the corpus is committed as CODE, regenerable by seed, never as golden files.
//   - The audit runner's seven plan-level assertions (planv2/audit) are the baseline oracle for
//     every query/mutation case: a generated case IS an audit.Case, run through audit.Run.
//   - Proposition-specific oracles (oracles.go) assert what the FS text demands beyond the seven:
//     which subgraph serves a field, representation contents (keys mirrored per AX-REP-1, the
//     requires closure per AX-REP-2), member-fragment placement, interface-typed entry (AX-IFO-1),
//     dependency chains, determinism (FS-KEY-7 permutation testing), plan-equality twins
//     (FS-OVR-3, FS-DEF erasure), and typed planning errors (FS-PLAN-6/FS-KEY-9).
//   - Subscription cases cannot go through audit.Run (it expects a SynchronousResponsePlan); the
//     runner plans them directly and applies the FS-SUB oracles to the trigger/response split.
//
// The generated corpus is planv2's own systematic conformance surface -- first-party, publishable,
// free of third-party corpus content. Self-test failures are TRIAGED, not hidden: a genuine planv2
// gap is registered in triage.go (and DIVERGENCES.md) with the generated case as witness, exactly
// like the audit corpus's expect-fail register.
package conformance

import (
	"fmt"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astvalidation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// GeneratedCase is one generated conformance case: an audit-format fixture plus the
// proposition-specific oracles its FS proposition demands. ID format:
// "<FS-ID>/<family>/<scenario>" -- stable across runs, never renumbered.
type GeneratedCase struct {
	ID           string
	Propositions []string // every FS proposition the case asserts (first = primary)
	Family       string
	Seed         uint64
	Case         audit.Case
	Oracles      []Oracle
	// ExpectPlanError: the case's obligated outcome is a TYPED planning refusal (FS-PLAN-6 class);
	// baseline audit assertions are replaced by "planning must error".
	ExpectPlanError bool
	// Subscription: the operation is a subscription -- planned directly (trigger/response split
	// oracles) instead of through audit.Run.
	Subscription bool
	// Defer: the operation carries @defer and is planned through the defer-enabled normalization
	// (WithEnableDefer). The baseline audit assertions then run against BaselineOperation (the
	// erased twin), because the audit pipeline is defer-erasing by construction.
	Defer bool
	// BaselineOperation, when non-empty, replaces Case.Operation for the baseline audit.Run
	// (defer families: the erased twin).
	BaselineOperation string
	// CheckDeterminism: additionally re-plan with permuted datasource order and require canonically
	// identical plans (FS-KEY-7 / C.4).
	CheckDeterminism bool
}

// Outcome mirrors the audit outcome vocabulary: PASS, FAIL (assertion violated -- unexpected),
// GAP (violated but registered in the triage register: a known finding, not a test error).
type Outcome struct {
	ID     string
	Family string
	Status string
	Reason string
}

// Oracle is one proposition-specific plan-property assertion.
type Oracle func(a *Artifacts) error

// Artifacts is everything an oracle can observe about a planned case -- the observable-plan
// vocabulary of FEDERATION_SEMANTICS.md Section 0.3, nothing planner-internal.
type Artifacts struct {
	Case *GeneratedCase
	// Fetches is the flat fetch list (query/mutation plans). For subscriptions it is the
	// PER-EVENT (response) fetch list; Trigger carries the trigger fetch.
	Fetches      []*resolve.FetchItem
	ResponseTree *resolve.Object
	Trigger      *resolve.GraphQLSubscriptionTrigger
	// Defer is set for DeferResponsePlan cases: descriptors + the full defer response object.
	Defer     *resolve.GraphQLDeferResponse
	Fallbacks int
	// Upstream maps subgraph name -> its federated upstream schema (for doc validation).
	Upstream map[string]*ast.Document
}

// Run executes one generated case: baseline assertions (audit.Run's seven for query/mutation;
// the subscription split otherwise; typed-error expectation for ExpectPlanError cases), then the
// case's proposition oracles, then optional determinism probing. Never panics or t.Fatals -- the
// caller aggregates outcomes against the triage register.
func Run(c GeneratedCase) Outcome {
	return RunWithPlannerConfig(c, planv2.Config{})
}

// RunWithPlannerConfig is Run with planv2 planner-construction options threaded through every
// plan the case makes (the case plan, the audit baseline, the determinism probe) -- the
// operation-scoped dual-mode gate (opscoped_equality_test.go) runs the generated suite under both
// modes with it. Run passes the zero Config, so the default suite run is unchanged. The twin
// self-equality oracles (planCanon pairs inside c.Oracles) stay default-mode: they compare a case
// against its own twin within one mode, so they are mode-independent by construction.
func RunWithPlannerConfig(c GeneratedCase, opts planv2.Config) Outcome {
	out := Outcome{ID: c.ID, Family: c.Family, Status: "PASS"}
	fail := func(format string, args ...any) Outcome {
		out.Status, out.Reason = "FAIL", fmt.Sprintf(format, args...)
		return out
	}

	arts, planErr := planCaseCfg(c.Case, c.Defer, opts)
	arts.Case = &c

	if c.ExpectPlanError {
		if planErr == nil {
			return fail("expected a typed planning error (FS-PLAN-6), got a plan with %d fetches", len(arts.Fetches))
		}
		if !strings.Contains(planErr.Error(), "no valid plan") &&
			!strings.Contains(planErr.Error(), "external: field") &&
			!strings.Contains(planErr.Error(), "InvalidPlan") {
			// Any typed refusal is acceptable; record what fired so wording drift is visible.
			out.Reason = "planning refused (accepted): " + planErr.Error()
		}
		return runOracles(c, arts, out)
	}

	if planErr != nil {
		return fail("planning failed: %v", planErr)
	}

	if c.Subscription {
		if err := subscriptionBaseline(arts); err != nil {
			return fail("%v", err)
		}
	} else {
		// Baseline: the audit runner's seven plan-level assertions. Defer cases run the baseline
		// against the erased twin operation (the audit pipeline cannot see @defer).
		baseCase := c.Case
		if c.BaselineOperation != "" {
			baseCase.Operation = c.BaselineOperation
		}
		auditOut := audit.RunWithPlannerConfig(baseCase, opts)
		switch auditOut.Status {
		case "PASS":
		case "SKIP":
			return fail("baseline audit runner SKIPPED (planv2 cannot plan the generated case): %s", auditOut.Reason)
		default:
			return fail("baseline (audit assertions): %s", auditOut.Reason)
		}
	}

	if c.CheckDeterminism {
		if err := determinismProbe(c.Case, c.Defer, opts); err != nil {
			return fail("determinism (FS-KEY-7/C.4): %v", err)
		}
	}

	return runOracles(c, arts, out)
}

func runOracles(c GeneratedCase, arts *Artifacts, out Outcome) Outcome {
	for _, o := range c.Oracles {
		if err := o(arts); err != nil {
			out.Status, out.Reason = "FAIL", err.Error()
			return out
		}
	}
	out.Status = "PASS"
	return out
}

// planCase plans an audit.Case through the planv2 facade and extracts oracle-observable artifacts.
// A nil error with empty artifacts is impossible: either the plan materializes or err is set.
func planCase(c audit.Case, enableDefer bool) (*Artifacts, error) {
	return planCaseCfg(c, enableDefer, planv2.Config{})
}

// planCaseCfg is planCase with planner-construction options (the operation-scoped toggle).
func planCaseCfg(c audit.Case, enableDefer bool, opts planv2.Config) (*Artifacts, error) {
	arts := &Artifacts{}

	dataSources, upstream, err := audit.BuildDataSources(c)
	if err != nil {
		return arts, fmt.Errorf("config: %w", err)
	}
	arts.Upstream = upstream

	planner, err := planv2.NewPlannerWithConfig(plan.Configuration{DataSources: dataSources, DisableResolveFieldPositions: true}, opts)
	if err != nil {
		return arts, fmt.Errorf("NewPlanner: %w", err)
	}

	op, def, report := parseAndNormalizeOp(c.Definition, c.Operation, enableDefer)
	if report.HasErrors() {
		return arts, fmt.Errorf("normalize: %s", report.Error())
	}

	p, diags := planner.PlanWithDiagnostics(op, def, "", report)
	arts.Fallbacks = len(diags.RouteFallbacks)
	if report.HasErrors() {
		return arts, fmt.Errorf("plan: %s", report.Error())
	}

	switch pl := p.(type) {
	case *plan.SynchronousResponsePlan:
		if pl == nil || pl.Response == nil {
			return arts, fmt.Errorf("nil SynchronousResponsePlan")
		}
		arts.Fetches = pl.Response.RawFetches
		arts.ResponseTree = pl.Response.Data
	case *plan.SubscriptionResponsePlan:
		if pl == nil || pl.Response == nil || pl.Response.Response == nil {
			return arts, fmt.Errorf("nil SubscriptionResponsePlan")
		}
		arts.Trigger = &pl.Response.Trigger
		arts.Fetches = pl.Response.Response.RawFetches
		arts.ResponseTree = pl.Response.Response.Data
	case *plan.DeferResponsePlan:
		if pl == nil || pl.Response == nil || pl.Response.Response == nil {
			return arts, fmt.Errorf("nil DeferResponsePlan")
		}
		arts.Defer = pl.Response
		arts.Fetches = pl.Response.Response.RawFetches
		arts.ResponseTree = pl.Response.Response.Data
	default:
		return arts, fmt.Errorf("unexpected plan type %T", p)
	}
	return arts, nil
}

// subscriptionBaseline applies the FS-SUB baseline to a subscription plan (the audit runner's
// analogue for the trigger/response split):
//
//	FS-SUB-1/2 -- exactly one trigger, a `subscription` document, against one declaring subgraph;
//	FS-SUB-4   -- every non-trigger fetch is a `query` document;
//	FS-PLAN-1  -- trigger and per-event fetch documents validate against their subgraph schemas;
//	FS-PLAN-2  -- per-event fetch dependencies reference earlier fetches only.
func subscriptionBaseline(a *Artifacts) error {
	if a.Trigger == nil {
		return fmt.Errorf("FS-SUB-1: subscription plan has no trigger fetch")
	}
	tdoc := triggerQuery(a.Trigger)
	if !strings.HasPrefix(tdoc, "subscription") {
		return fmt.Errorf("FS-SUB-1: trigger document must be a subscription operation, got: %s", tdoc)
	}
	name := a.Trigger.SourceName
	def, ok := a.Upstream[name]
	if !ok {
		return fmt.Errorf("FS-SUB-2: trigger targets unknown subgraph %q", name)
	}
	if err := validateDocAgainst(tdoc, def); err != nil {
		return fmt.Errorf("FS-PLAN-1: trigger document invalid against %q: %w\n  doc: %s", name, err, tdoc)
	}
	for i, item := range a.Fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok || sf == nil {
			return fmt.Errorf("per-event fetch %d is not a SingleFetch (%T)", i, item.Fetch)
		}
		doc := fetchDoc(sf)
		if doc == "" {
			return fmt.Errorf("per-event fetch %d has no document", i)
		}
		if strings.HasPrefix(doc, "subscription") || strings.HasPrefix(doc, "mutation") {
			return fmt.Errorf("FS-SUB-4: per-event fetch %d must be a query document, got: %s", i, doc)
		}
		// FS-SUB-5/FS-PLAN-2: dependencies reference the TRIGGER (fetch ID 0) or an earlier
		// per-event fetch's FetchID -- never a later one. (Per-event fetch IDs are plan-global:
		// the trigger holds ID 0, so slice-index comparison would misread trigger dependence.)
		earlier := map[int]bool{0: true}
		for j := 0; j < i; j++ {
			if prev, ok := a.Fetches[j].Fetch.(*resolve.SingleFetch); ok && prev != nil {
				earlier[prev.FetchID] = true
			}
		}
		for _, dep := range sf.DependsOnFetchIDs {
			if dep != sf.FetchID && !earlier[dep] {
				return fmt.Errorf("FS-PLAN-2: per-event fetch %d (id %d) depends on %d which is neither the trigger nor an earlier per-event fetch", i, sf.FetchID, dep)
			}
			if dep == sf.FetchID {
				return fmt.Errorf("FS-PLAN-2: per-event fetch %d depends on itself (id %d)", i, dep)
			}
		}
		sub := string(sf.DataSourceIdentifier)
		sdef, ok := a.Upstream[sub]
		if !ok {
			return fmt.Errorf("per-event fetch %d targets unknown subgraph %q", i, sub)
		}
		if err := validateDocAgainst(doc, sdef); err != nil {
			return fmt.Errorf("FS-PLAN-1: per-event fetch %d invalid against %q: %w\n  doc: %s", i, sub, err, doc)
		}
	}
	return nil
}

// determinismProbe re-plans the case with the datasource list REVERSED and requires the
// canonicalized plans to be identical -- FS-KEY-7's falsifiable form (semantically equal inputs,
// identical plans; C.4 tie order is input-order-free).
func determinismProbe(c audit.Case, enableDefer bool, opts planv2.Config) error {
	base, err := planCanonCfg(c, enableDefer, opts)
	if err != nil {
		return err
	}
	permuted := c
	permuted.Subgraphs = make([]audit.Subgraph, len(c.Subgraphs))
	for i, sg := range c.Subgraphs {
		permuted.Subgraphs[len(c.Subgraphs)-1-i] = sg
	}
	other, err := planCanonCfg(permuted, enableDefer, opts)
	if err != nil {
		return fmt.Errorf("permuted input failed to plan: %w", err)
	}
	if base != other {
		return fmt.Errorf("plan differs under datasource-order permutation:\n base:     %.400s\n permuted: %.400s", base, other)
	}
	return nil
}

// planCanon plans a case and renders the plan canonically (for twin-equality oracles).
func planCanon(c audit.Case, enableDefer bool) (string, error) {
	return planCanonCfg(c, enableDefer, planv2.Config{})
}

// planCanonCfg is planCanon with planner-construction options -- the dual-mode equality gate
// compares its output byte-for-byte across modes.
func planCanonCfg(c audit.Case, enableDefer bool, opts planv2.Config) (string, error) {
	arts, err := planCaseCfg(c, enableDefer, opts)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if arts.Trigger != nil {
		b.WriteString("trigger " + arts.Trigger.SourceName + " " + triggerQuery(arts.Trigger) + "\n")
	}
	for i, item := range arts.Fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok {
			b.WriteString(fmt.Sprintf("fetch %d (%T)\n", i, item.Fetch))
			continue
		}
		fmt.Fprintf(&b, "fetch %d sg=%s deps=%v defer=%d path=%s doc=%s\n",
			i, sf.DataSourceIdentifier, sf.DependsOnFetchIDs, sf.DeferID, item.ResponsePath, fetchDoc(sf))
	}
	return b.String(), nil
}

// fetchDoc returns a fetch's printed GraphQL document.
func fetchDoc(sf *resolve.SingleFetch) string {
	if sf.QueryPlan != nil && sf.QueryPlan.Query != "" {
		return sf.QueryPlan.Query
	}
	return ""
}

func triggerQuery(t *resolve.GraphQLSubscriptionTrigger) string {
	if t.QueryPlan != nil {
		return t.QueryPlan.Query
	}
	return ""
}

// parseAndNormalizeOp mirrors the facade's pinned pipeline (the audit runner's private helper),
// with the defer-enabled variant for @defer-bearing cases (WithEnableDefer keeps the directive
// visible to the planner; the default pipeline erases it).
func parseAndNormalizeOp(schema, op string, enableDefer bool) (*ast.Document, *ast.Document, *operationreport.Report) {
	def := unsafeparser.ParseGraphqlDocumentString(schema)
	report := &operationreport.Report{}
	if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
		report.AddInternalError(err)
		return nil, nil, report
	}
	operation := unsafeparser.ParseGraphqlDocumentString(op)
	opts := []astnormalization.Option{
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
	}
	if enableDefer {
		opts = append(opts, astnormalization.WithEnableDefer())
	}
	astnormalization.NewWithOpts(opts...).NormalizeOperation(&operation, &def, report)
	return &operation, &def, report
}

// validateDocAgainst parses and validates a fetch document against a subgraph upstream schema
// (the audit runner's validateAgainst, restated here for the subscription path).
func validateDocAgainst(doc string, subgraphDef *ast.Document) error {
	opDoc := unsafeparser.ParseGraphqlDocumentString(doc)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
	).NormalizeOperation(&opDoc, subgraphDef, report)
	if report.HasErrors() {
		return fmt.Errorf("normalize: %s", report.Error())
	}
	astvalidation.DefaultOperationValidator().Validate(&opDoc, subgraphDef, report)
	if report.HasErrors() {
		return fmt.Errorf("%s", report.Error())
	}
	return nil
}
