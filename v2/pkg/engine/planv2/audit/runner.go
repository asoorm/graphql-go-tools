// Package audit encodes The Guild's federation-gateway-audit scenarios as in-repo, PLAN-LEVEL
// planner-v2 tests. Each case is transcribed from the audit corpus (MIT-licensed test DATA -- schemas,
// operations, expected responses; see testdata/README.md for attribution) into the fixture format
// this package loads: per-subgraph federation SDLs, a client-facing supergraph SDL, an operation, and
// the audit's expected response.
//
// SCOPE -- what "plan-level" checks (the audit lets us assert this WITHOUT executing subgraphs):
//  1. planv2 plans the operation without error (a case it cannot yet plan -- a feature gap -- is
//     SKIPPED with a reason, not failed; only the in-scope set is counted).
//  2. Each emitted fetch document is a VALID GraphQL operation against its target subgraph's schema
//     (astvalidation against the federated upstream schema).
//  3. The fetch tree targets the right subgraphs in a valid dependency order (a fetch's dependencies
//     are earlier fetches).
//  4. The plan's response shape can PRODUCE the audit's expected response: every key in the expected
//     response is present at the right nesting, with the right list/object/leaf kind, in planv2's
//     response tree (presence/nesting/list-ness -- NOT scalar values). Abstract-type plans legitimately
//     carry MORE (gated) members than any single concrete runtime response, so the oracle checks that
//     the expected keys are a subset of the plan's rather than requiring exact key-equality (the
//     differential harness covers exact parity).
//
// Router-level end-to-end execution (real subgraph servers, executed fetches, value equality) is the
// separate end-to-end harness, out of this package.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astprinter"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astvalidation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/lower"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// Subgraph is one federation subgraph of an audit case: its name and its raw federation SDL (the
// `typeDefs` transcribed verbatim from the audit's *.subgraph.ts).
type Subgraph struct {
	Name string
	SDL  string
}

// Case is one transcribed audit test case. Definition is the client-facing supergraph SDL (what the
// operation is validated/normalized against). Expected is the audit's expected response JSON (used
// for its key STRUCTURE only). Skip, when non-empty, marks a case as a feature planv2 can't yet plan;
// it is reported as SKIP rather than run.
type Case struct {
	Name       string
	Suite      string
	Subgraphs  []Subgraph
	Definition string
	Operation  string
	Expected   json.RawMessage
	Skip       string
	// ExpectFail, when non-empty, marks a KNOWN planv2 defect (a precise, reported finding): the
	// case runs and its FAIL is reported as GAP (in-scope, known-broken) rather than a test error.
	// If such a case PASSES, that is itself a failure -- the expect-fail marker must be removed so
	// the register tracks reality.
	ExpectFail string
	// ErrorClass pins the plan-level REASON CLASS of an error-expectation case (`errors: true` in
	// expected.json), loaded from expected-error-class.txt (first line; see classifyRejection for
	// the vocabulary). Without a pin, ANY normalize/plan failure scored such a case PASS -- an
	// unrelated crash could masquerade as the correct rejection. Every error-expectation case MUST
	// carry a pin: a missing pin, a wrong-class rejection, or a plan where the pin demands a typed
	// rejection are all FAIL.
	ErrorClass string
}

// Outcome is the plan-level classification of running one case.
//
// Statuses: PASS (all plan-level assertions hold), FAIL (an assertion violated -- unexpected, fails
// the suite), GAP (an assertion violated on a case marked expect-fail: a KNOWN, reported planv2
// defect; in-scope but not a test error), SKIP (planv2 cannot yet plan the case -- a feature gap,
// outside the in-scope set).
type Outcome struct {
	Case    string
	Suite   string
	Status  string // "PASS", "SKIP", "FAIL", "GAP"
	Reason  string
	Fetches int
	// RouteFallbacks are the D10 route-fallback events the planner surfaced for this case
	// (planv2.PlanWithDiagnostics; FORMAL_SPEC D10 amendment -- typed-loud fall-back). Recorded for
	// every case that reached the search (including GAP cases and lowering-failure SKIPs) so the
	// corpus-wide figure is measurable. A fallback is a SEARCH-LEVEL observation, not a plan-level
	// verdict -- post-flip lowering can emit a correct plan over a fallen-back walk -- so it does NOT
	// reclassify the outcome here; the committed register test (fallback_register_test.go) freezes
	// the exact set of PASS cases that fire one and pins the corpus distinct-goal count.
	RouteFallbacks []search.RouteFallback
}

// federationLink is prepended to any subgraph SDL that does not already declare the federation @link,
// so hand-transcribed fixtures that omit the boilerplate still parse the federation directives.
const federationLink = `extend schema @link(url: "https://specs.apollo.dev/federation/v2.5", import: ["@key", "@external", "@requires", "@provides", "@shareable", "@inaccessible", "@override", "@interfaceObject", "@tag"])` + "\n"

// Run plans one case at plan level and returns its Outcome. It never calls t.Fatal -- a case that
// planv2 cannot plan is a SKIP (feature gap), a plan that violates a plan-level assertion is a FAIL
// (or GAP when the case is expect-fail-marked), and a plan satisfying all assertions is a PASS. The
// caller aggregates.
func Run(c Case) Outcome {
	return RunWithPlannerConfig(c, planv2.Config{})
}

// RunWithPlannerConfig is Run with planv2 planner-construction options threaded through. The
// operation-scoped dual-mode gate (opscoped_equality_test.go) runs the corpus under both modes
// with it; Run itself always passes the zero Config, so the default corpus run is unchanged.
func RunWithPlannerConfig(c Case, opts planv2.Config) Outcome {
	out := runInner(c, opts)
	if c.ExpectFail == "" {
		return out
	}
	switch out.Status {
	case "FAIL":
		out.Status = "GAP"
		out.Reason = c.ExpectFail + " | observed: " + out.Reason
	case "PASS":
		out.Status = "FAIL"
		out.Reason = "case marked expect-fail now PASSES -- remove expect-fail.txt (" + c.ExpectFail + ")"
	}
	return out
}

func runInner(c Case, opts planv2.Config) Outcome {
	out := Outcome{Case: c.Name, Suite: c.Suite}
	if c.Skip != "" {
		out.Status, out.Reason = "SKIP", c.Skip
		return out
	}

	// Error-expectation cases: the audit expects the gateway to produce ERRORS (data null/absent).
	// At plan level, a planv2 rejection can be the correct outcome -- but only a rejection of the
	// RIGHT REASON CLASS: each error-expectation case pins its class (Case.ErrorClass, loaded from
	// expected-error-class.txt), and classifyRejection matches the observed rejection against it,
	// so an unrelated crash cannot masquerade as the correct reject. A successful plan is acceptable
	// only when the pin says so ("plan-success": the expected error is a RUNTIME one -- e.g.
	// unresolvable representations -- which plan level cannot adjudicate); assertions 2-8 still run
	// on the produced plan.
	expectsErrors := expectsErrorsOnly(c.Expected)
	if !expectsErrors && c.ErrorClass != "" && c.ErrorClass != errorClassPlanSuccess {
		out.Status, out.Reason = "FAIL", "expected-error-class.txt pins a rejection class on a case whose expected.json is not an error-only expectation -- remove or fix the pin"
		return out
	}

	dataSources, upstream, err := BuildDataSources(c)
	if err != nil {
		out.Status, out.Reason = "SKIP", "config: "+err.Error()
		return out
	}

	planner, err := planv2.NewPlannerWithConfig(plan.Configuration{DataSources: dataSources, DisableResolveFieldPositions: true}, opts)
	if err != nil {
		out.Status, out.Reason = "SKIP", "NewPlanner: "+err.Error()
		return out
	}

	op, def, report := parseAndNormalize(c.Definition, c.Operation)
	if report.HasErrors() {
		if expectsErrors {
			out.Status, out.Reason = classifyRejection(c.ErrorClass, rejectionStageNormalize, report)
			return out
		}
		if c.ErrorClass == errorClassPlanSuccess {
			out.Status, out.Reason = "FAIL", "pinned plan-success (runtime-error expectation) but rejected at normalize: "+report.Error()
			return out
		}
		out.Status, out.Reason = "SKIP", "normalize: "+report.Error()
		return out
	}
	// The structural oracles (assertions 5-7) measure against the NORMALIZED operation, not the raw
	// string: normalization is the authority on what planv2 actually planned -- it inlines fragment
	// spreads and RESOLVES @include/@skip against variable defaults (a field under @include(if:false)
	// is legitimately absent from every fetch, so the raw-operation comparand would false-FAIL
	// leaf-coverage). Printed once here, before Plan, so a later planner mutation cannot perturb it.
	normalizedOp, printErr := astprinter.PrintString(op)
	if printErr != nil {
		normalizedOp = c.Operation // defensive: fall back to raw (no @include/@skip corpus case needs it)
	}

	// Field-argument lowering has landed (client arguments, @include/@skip resolution, mutations):
	// arguments are now carried into fetch documents with their variables forwarded, so the old
	// blanket "skip anything with arguments" gate is gone. Cases that still cannot be planned (e.g.
	// @requires with an argument, which the hypergraph does not yet handle) SKIP via a suite/case
	// skip.txt or surface a typed plan error below; they are no longer pre-filtered here.
	p, diags := planner.PlanWithDiagnostics(op, def, "", report)
	// Record the typed D10 route-fallback events no matter how the case classifies below: the
	// corpus-wide figure (fallback_register_test.go) is the drift instrument the D10 retirement
	// gate reads.
	out.RouteFallbacks = diags.RouteFallbacks
	if report.HasErrors() {
		if expectsErrors {
			out.Status, out.Reason = classifyRejection(c.ErrorClass, rejectionStagePlan, report)
			return out
		}
		if c.ErrorClass == errorClassPlanSuccess {
			out.Status, out.Reason = "FAIL", "pinned plan-success (runtime-error expectation) but rejected at plan: "+report.Error()
			return out
		}
		out.Status, out.Reason = "SKIP", "plan: "+report.Error()
		return out
	}
	// An error-expectation case pinned to a TYPED REJECTION must not plan: if the planner produces a
	// plan where the pin demands (say) ErrNoValidPlan, the honest-reject regressed into a plan -- the
	// exact regression the pin exists to make loud (assertions passing on that plan would mask it).
	if expectsErrors && c.ErrorClass != "" && c.ErrorClass != errorClassPlanSuccess {
		out.Status, out.Reason = "FAIL", fmt.Sprintf("pinned rejection class %q but the planner produced a plan -- the typed reject regressed", c.ErrorClass)
		return out
	}
	sp, ok := p.(*plan.SynchronousResponsePlan)
	if !ok || sp == nil || sp.Response == nil {
		out.Status, out.Reason = "FAIL", fmt.Sprintf("not a SynchronousResponsePlan (%T)", p)
		return out
	}

	fetches := sp.Response.RawFetches
	out.Fetches = len(fetches)

	if err := assertPlan(c.Expected, fetches, sp.Response.Data, normalizedOp, def, upstream); err != nil {
		out.Status, out.Reason = "FAIL", err.Error()
		return out
	}

	out.Status = "PASS"
	return out
}

// assertPlan runs the plan-level assertion chain (assertions 2-8, in the runner's order) over a
// successfully-produced plan. Extracted from runInner so the committed mutation suite
// (mutation_oracle_test.go) exercises EXACTLY the oracle the corpus run uses -- corrupting a fresh
// plan and asserting this chain catches it. Returns the first violated assertion's error, nil when
// the plan satisfies all of them.
func assertPlan(expected json.RawMessage, fetches []*resolve.FetchItem, data *resolve.Object,
	normalizedOp string, def *ast.Document, upstream map[string]*ast.Document) error {

	// Assertion 2 + 3: every fetch document valid against its subgraph; dependencies are earlier.
	if err := validateFetches(fetches, upstream); err != nil {
		return err
	}

	// Assertion 4: the plan can produce the audit's expected response shape.
	if err := shapeSatisfies(expected, data); err != nil {
		return fmt.Errorf("response shape: %w", err)
	}

	// Assertion 5 (root-entry honesty): every ROOT fetch must enter only root fields the client's
	// operation actually requested. A plan that answers `{ b { city } }` by fetching
	// `query { a { ... } }` passed assertions 1-4 (the document is valid and the shape check only
	// looks at key structure) while returning data from the WRONG root. This assertion makes such
	// plans FAIL (and, expect-fail-marked, GAP) instead of silently passing. `_entities` fetches are
	// exempt: they enter via a key
	// representation, not a root field. No other mechanism is blanket-exempted -- in the current
	// corpus every legitimate plan (incl. every @interfaceObject co-resolution case, which enters
	// via `_entities` after a requested-root first fetch) satisfies this check as-is.
	if err := validateRootEntries(fetches, normalizedOp); err != nil {
		return err
	}

	// Assertion 6 (leaf coverage / path correspondence): recurses the root-entry check BELOW the top
	// level. Every unconditionally-requested field must be selected by some fetch document (forward:
	// catches a dropped sibling), and every field a fetch selects must correspond to a client request
	// or a key/requires/__typename mechanism (backward: catches an un-requested sibling). This is what
	// exposes the wrong-data class that assertions 1-5 pass. See leaf_coverage.go.
	if err := validateLeafCoverage(fetches, normalizedOp, def); err != nil {
		return err
	}

	// Assertion 6p (position-aware forward coverage): tightens assertion 6's coordinate-level forward
	// check to response POSITIONS -- a drop at one position can no longer hide behind the same
	// Type.field coordinate selected elsewhere, and a member-refined leaf is exempt only while the
	// plan does not materialize its member at that position. See position_coverage.go.
	if err := validatePositionCoverage(fetches, normalizedOp, def); err != nil {
		return err
	}

	// Assertion 7 (response-path check): every entity fetch's ResponsePath and FetchPath must match
	// the position it serves. Closes assertion 6's path-blind shared-instance gap now that lowering
	// assigns each fetch a response path. See response_path.go.
	if err := validateResponsePaths(fetches, normalizedOp, def); err != nil {
		return err
	}

	// Assertion 8 (key-supply completeness): every entity fetch's representation requirement (its
	// Key/Requires fragment fields) must be selected by the fetches it depends on -- a source document
	// that drops a key field would break the runtime entity jump while every earlier assertion still
	// passes. See key_supply.go.
	if err := validateKeySupply(fetches, normalizedOp, def); err != nil {
		return err
	}

	return nil
}

// validateRootEntries implements assertion 5: for each non-entity fetch, every top-level field of
// its query document must be a top-level field the client operation requested (by field NAME on the
// operation root -- planv2 prints fetch documents with client field names). __typename is always
// admissible. requested is computed from the RAW operation so aliases/fragments cannot hide a root:
// normalization inlines fragment spreads into the top level, and planv2 prints the FIELD name (not
// the alias) into fetch documents, so the raw top-level field-name set is the right comparand.
func validateRootEntries(fetches []*resolve.FetchItem, operation string) error {
	requested := topLevelFieldNames(operation)
	for i, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		// Both single (RequiresEntityFetch) and BATCH (RequiresEntityBatchFetch) entity fetches enter
		// via a key representation on `_entities`, not a client-requested root field -- exempt both.
		if !ok || sf == nil || sf.RequiresEntityFetch || sf.RequiresEntityBatchFetch {
			continue
		}
		doc := fetchQuery(sf)
		if doc == "" {
			continue // absence already failed assertion 2
		}
		for f := range topLevelFieldNames(doc) {
			if f == "__typename" {
				continue
			}
			if _, ok := requested[f]; !ok {
				return fmt.Errorf(
					"foreign root entry: fetch %d (subgraph %q) selects root field %q the operation never requested -- the plan is answering from a root the client did not ask for\n  doc: %s",
					i, sf.DataSourceIdentifier, f, doc)
			}
		}
	}
	return nil
}

// topLevelFieldNames parses a GraphQL document and returns the set (and list) of field NAMES selected
// at the top level of its first operation, descending through top-level inline fragments and fragment
// spreads (a root selection wrapped in `... { a }` still enters root field a).
func topLevelFieldNames(doc string) map[string]struct{} {
	parsed := unsafeparser.ParseGraphqlDocumentString(doc)
	out := map[string]struct{}{}
	fragments := map[string]int{} // fragment name -> selection set ref
	for ref := range parsed.FragmentDefinitions {
		fragments[parsed.FragmentDefinitionNameString(ref)] = parsed.FragmentDefinitions[ref].SelectionSet
	}
	var collect func(setRef int, depth int)
	collect = func(setRef int, depth int) {
		if setRef == -1 || depth > 16 {
			return
		}
		for _, sel := range parsed.SelectionSets[setRef].SelectionRefs {
			s := parsed.Selections[sel]
			switch s.Kind {
			case ast.SelectionKindField:
				out[parsed.FieldNameString(s.Ref)] = struct{}{}
			case ast.SelectionKindInlineFragment:
				collect(parsed.InlineFragments[s.Ref].SelectionSet, depth+1)
			case ast.SelectionKindFragmentSpread:
				name := parsed.FragmentSpreadNameString(s.Ref)
				if fs, ok := fragments[name]; ok {
					collect(fs, depth+1)
				}
			}
		}
	}
	for ref := range parsed.OperationDefinitions {
		collect(parsed.OperationDefinitions[ref].SelectionSet, 0)
		break // fetch documents and audit operations are single-operation
	}
	return out
}

// BuildDataSources derives a plan.DataSource per subgraph (real graphql_datasource, federation
// enabled) and returns them alongside a map from subgraph NAME to its federated upstream schema
// (for fetch-document validation). Metadata derivation is two-phase: per-subgraph derive (with the
// COMPOSED schema's interface->implementers map, needed by @interfaceObject), then the cross-subgraph
// @override post-pass (an override removes the field from the `from` subgraph's capability lists).
//
// Exported so the env-gated external-corpus harness (pkg/engine/planv2/external) can build a
// plan.Configuration from a Case's Subgraphs/Definition without duplicating federation-metadata
// derivation.
func BuildDataSources(c Case) ([]plan.DataSource, map[string]*ast.Document, error) {
	implementers := interfaceImplementers(c.Definition)

	metas := map[string]*plan.DataSourceMetadata{}
	var allOverrides []overrideDecl
	for _, sg := range c.Subgraphs {
		meta, overrides, err := deriveMetadata(sg.SDL, implementers)
		if err != nil {
			return nil, nil, fmt.Errorf("derive metadata for %s: %w", sg.Name, err)
		}
		metas[sg.Name] = meta
		allOverrides = append(allOverrides, overrides...)
	}
	applyOverrides(metas, allOverrides)

	ds := make([]plan.DataSource, 0, len(c.Subgraphs))
	upstream := map[string]*ast.Document{}
	for _, sg := range c.Subgraphs {
		sdl := sg.SDL
		if !strings.Contains(sdl, "@link") {
			sdl = federationLink + sdl
		}
		meta := metas[sg.Name]
		// The UPSTREAM schema (used for planv2's type lookups AND for fetch-document
		// validation) must have fed-v1 style `extend type X` blocks folded into definitions --
		// a subgraph server (buildSubgraphSchema) does exactly that merge, so an orphan extension
		// IS a queryable type at runtime. ServiceSDL stays RAW: the federation schema builder
		// scans it (incl. extends) for @key entities to emit the _Entity union.
		schemaCfg, err := graphql_datasource.NewSchemaConfiguration(mergeExtensions(sdl), &graphql_datasource.FederationConfiguration{
			Enabled:    true,
			ServiceSDL: sdl,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("schema config for %s: %w", sg.Name, err)
		}
		cfg, err := graphql_datasource.NewConfiguration(graphql_datasource.ConfigurationInput{
			Fetch:               &graphql_datasource.FetchConfiguration{URL: "http://" + sg.Name},
			SchemaConfiguration: schemaCfg,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("config for %s: %w", sg.Name, err)
		}
		dsCfg, err := plan.NewDataSourceConfiguration[graphql_datasource.Configuration](
			sg.Name, &graphql_datasource.Factory[graphql_datasource.Configuration]{}, meta, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("datasource for %s: %w", sg.Name, err)
		}
		if us, ok := dsCfg.UpstreamSchema(); ok {
			upstream[sg.Name] = us
		}
		ds = append(ds, dsCfg)
	}
	return ds, upstream, nil
}

// mergeExtensions folds type extensions (incl. fed-v1 orphan `extend type X` with no local base
// definition) into plain definitions, via the repo's definition normalizer -- the same effective
// schema a subgraph server builds. Returns the input unchanged if nothing needs merging or the
// merge fails (defensive: the raw SDL is still a parseable schema).
func mergeExtensions(sdl string) string {
	if !strings.Contains(sdl, "extend type") && !strings.Contains(sdl, "extend interface") {
		return sdl
	}
	doc := unsafeparser.ParseGraphqlDocumentString(sdl)
	report := &operationreport.Report{}
	astnormalization.NormalizeDefinition(&doc, report)
	if report.HasErrors() {
		return sdl
	}
	out, err := astprinter.PrintStringIndent(&doc, "  ")
	if err != nil {
		return sdl
	}
	return out
}

// operationUsesFieldArguments reports whether the operation passes any field arguments -- a lowering
// gap: planv2's fetch-document printer emits field NAMES only, so a required argument would be
// dropped (invalid document) and an optional one silently lost (wrong semantics). Such cases are
// SKIPPED rather than risk a false PASS.
func operationUsesFieldArguments(op string) bool {
	doc := unsafeparser.ParseGraphqlDocumentString(op)
	for ref := range doc.Fields {
		if len(doc.Fields[ref].Arguments.Refs) > 0 {
			return true
		}
	}
	// @skip/@include with variable conditions are runtime-conditional shapes lowering does not handle.
	for ref := range doc.Directives {
		name := doc.DirectiveNameString(ref)
		if name == "skip" || name == "include" {
			return true
		}
	}
	return false
}

// interfaceImplementers parses the composed client schema and maps every interface to the object
// types implementing it -- the composition knowledge @interfaceObject derivation needs.
func interfaceImplementers(definition string) map[string][]string {
	doc := unsafeparser.ParseGraphqlDocumentString(definition)
	out := map[string][]string{}
	for ref := range doc.ObjectTypeDefinitions {
		objName := doc.ObjectTypeDefinitionNameString(ref)
		node, ok := doc.Index.FirstNodeByNameStr(objName)
		if !ok {
			continue
		}
		for ifaceRef := range doc.InterfaceTypeDefinitions {
			iface := doc.InterfaceTypeDefinitionNameString(ifaceRef)
			if doc.NodeImplementsInterface(node, ast.ByteSlice(iface)) {
				out[iface] = append(out[iface], objName)
			}
		}
	}
	return out
}

// expectsErrorsOnly reports whether the audit case's expected response is an ERROR expectation:
// `errors: true` with data null or absent. (A response with concrete data AND errors:true would be a
// partial response -- none exist in the corpus; data presence wins if both appear.)
func expectsErrorsOnly(expected json.RawMessage) bool {
	if len(expected) == 0 {
		return false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(expected, &root); err != nil {
		return false
	}
	errs, ok := root["errors"]
	if !ok || strings.TrimSpace(string(errs)) != "true" {
		return false
	}
	data, ok := root["data"]
	return !ok || isJSONNull(data)
}

// --- error-expectation reason-class pinning ---------------------------------------------------
//
// The vocabulary of expected-error-class.txt (one token on the first line; later lines are free
// commentary). Each error-expectation case (`errors: true`, data null/absent) pins exactly one:
//
//	plan-success                        the expected error is a RUNTIME one (unresolvable
//	                                    representations, invalid enum value, ...): plan level must
//	                                    PLAN cleanly and pass assertions 2-8; any rejection FAILs.
//	normalize                           the operation must be rejected at validation/normalization
//	                                    (an ExternalError on the composed schema).
//	no-valid-plan:unreachable           the planner must reject with search.ErrNoValidPlan, reason
//	                                    "unreachable" (a required field no candidate can reach).
//	no-valid-plan:provably-non-resolvable
//	                                    search.ErrNoValidPlan with the provable-non-resolvability
//	                                    reason (every candidate behind @key(resolvable: false),
//	                                    root-only entry -- the D10-narrow honest reject).
//	lowering:subscription-single-root   the typed D11.12 precondition reject
//	                                    (lower.ErrSubscriptionSingleRootField).
const (
	errorClassPlanSuccess            = "plan-success"
	errorClassNormalize              = "normalize"
	errorClassNoValidPlanUnreachable = "no-valid-plan:unreachable"
	errorClassNoValidPlanNonResolv   = "no-valid-plan:provably-non-resolvable"
	errorClassSubscriptionSingleRoot = "lowering:subscription-single-root"
)

type rejectionStage string

const (
	rejectionStageNormalize rejectionStage = "normalize"
	rejectionStagePlan      rejectionStage = "plan"
)

// classifyRejection scores an error-expectation case's observed rejection against its pinned reason
// class. Only a rejection of the pinned class is PASS; a missing pin or a wrong-class rejection is
// FAIL -- an unrelated crash must never masquerade as the correct reject (anti-cheat must-fix 3).
func classifyRejection(pinned string, stage rejectionStage, report *operationreport.Report) (status, reason string) {
	observed := describeRejection(stage, report)
	if pinned == "" {
		return "FAIL", "error-expectation case has no expected-error-class.txt pin -- add one so a wrong-reason reject cannot PASS; observed: " + observed
	}
	if rejectionMatchesClass(pinned, stage, report) {
		return "PASS", "errors expected; rejected with pinned class " + pinned + ": " + observed
	}
	return "FAIL", fmt.Sprintf("errors expected with pinned class %q but the rejection does not match: %s", pinned, observed)
}

// rejectionMatchesClass reports whether the observed rejection (stage + report) is of the pinned
// reason class.
func rejectionMatchesClass(pinned string, stage rejectionStage, report *operationreport.Report) bool {
	switch pinned {
	case errorClassNormalize:
		return stage == rejectionStageNormalize
	case errorClassNoValidPlanUnreachable, errorClassNoValidPlanNonResolv:
		if stage != rejectionStagePlan {
			return false
		}
		for _, e := range report.InternalErrors {
			var nvp *search.ErrNoValidPlan
			if !errors.As(e, &nvp) {
				continue
			}
			if pinned == errorClassNoValidPlanUnreachable && nvp.Reason == "unreachable" {
				return true
			}
			if pinned == errorClassNoValidPlanNonResolv && strings.HasPrefix(nvp.Reason, "provably non-resolvable") {
				return true
			}
		}
		return false
	case errorClassSubscriptionSingleRoot:
		if stage != rejectionStagePlan {
			return false
		}
		for _, e := range report.InternalErrors {
			if errors.Is(e, lower.ErrSubscriptionSingleRootField) {
				return true
			}
		}
		return false
	default: // plan-success never matches a rejection; unknown tokens fail loud via classifyRejection
		return false
	}
}

// describeRejection renders the observed rejection for reasons/diagnostics.
func describeRejection(stage rejectionStage, report *operationreport.Report) string {
	return fmt.Sprintf("%s reject (%d internal, %d external): %s",
		stage, len(report.InternalErrors), len(report.ExternalErrors), report.Error())
}

// parseAndNormalize runs the facade's pinned pipeline: merge the client definition with the base
// schema, then normalize the operation with the v1 option set.
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

// validateFetches checks each fetch document parses + validates against its subgraph's upstream
// schema, and that fetch dependencies reference only earlier fetches (a valid topological emission
// order).
func validateFetches(fetches []*resolve.FetchItem, upstream map[string]*ast.Document) error {
	for i, item := range fetches {
		sf, ok := item.Fetch.(*resolve.SingleFetch)
		if !ok || sf == nil {
			return fmt.Errorf("fetch %d is not a *resolve.SingleFetch (%T)", i, item.Fetch)
		}
		for _, dep := range sf.DependsOnFetchIDs {
			if dep >= i {
				return fmt.Errorf("fetch %d depends on fetch %d which is not earlier (invalid order)", i, dep)
			}
		}
		name := string(sf.DataSourceIdentifier)
		def, ok := upstream[name]
		if !ok {
			return fmt.Errorf("fetch %d targets unknown subgraph %q", i, name)
		}
		doc := fetchQuery(sf)
		if doc == "" {
			return fmt.Errorf("fetch %d has no query document", i)
		}
		if err := validateAgainst(doc, def); err != nil {
			return fmt.Errorf("fetch %d document invalid against subgraph %q: %w\n  doc: %s", i, name, err, doc)
		}
	}
	return nil
}

// fetchQuery returns the printed GraphQL document a fetch will send (the QueryPlan query).
func fetchQuery(sf *resolve.SingleFetch) string {
	if sf.QueryPlan != nil && sf.QueryPlan.Query != "" {
		return sf.QueryPlan.Query
	}
	return ""
}

// validateAgainst parses doc and runs the default operation validator against the (already
// base-merged) subgraph upstream schema.
func validateAgainst(doc string, subgraphDef *ast.Document) error {
	opDoc := unsafeparser.ParseGraphqlDocumentString(doc)
	report := &operationreport.Report{}
	// The upstream schema from BuildFederationSchema already includes the base + federation meta
	// types (_Any, _Entity, _entities); normalize then validate.
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

// --- response-shape oracle (assertion 4) -----------------------------------------------------

// shapeSatisfies checks that planTree can PRODUCE the expected response's key structure: every key
// in expected is present at the right nesting with a compatible kind (object / list / leaf). It does
// NOT require the plan to be a subset of expected (abstract-type plans carry extra gated members).
// Values are ignored.
func shapeSatisfies(expected json.RawMessage, planTree *resolve.Object) error {
	if len(expected) == 0 {
		return nil // no expected response captured -- presence-only cases rely on assertions 1-3
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(expected, &root); err != nil {
		return fmt.Errorf("expected JSON not an object: %w", err)
	}
	data, ok := root["data"]
	if !ok {
		return nil // e.g. error-only expectations -- not a shape assertion
	}
	if isJSONNull(data) {
		return nil
	}
	var dataObj map[string]json.RawMessage
	if err := json.Unmarshal(data, &dataObj); err != nil {
		return fmt.Errorf("expected data not an object: %w", err)
	}
	return matchObject("data", dataObj, planTree)
}

// matchObject checks every expected key exists in the plan object (possibly gated) with a compatible
// value kind, recursing. Same-key sibling fields (one client field's ungated + member-gated variants,
// D11.8) are PRESENCE-UNIONED: the expected subkeys may be split across the variants (`someObject{a}`
// ungated plus `... on T1 { someObject { b } }` gated), so the comparand is the union of their
// sub-selections, not any single variant.
func matchObject(path string, expected map[string]json.RawMessage, planObj *resolve.Object) error {
	if planObj == nil {
		return fmt.Errorf("%s: plan has no object where response expects one", path)
	}
	byKey := planFieldsByKey(planObj)
	for key, ev := range expected {
		variants, ok := byKey[key]
		if !ok {
			return fmt.Errorf("%s.%s: expected key not present in plan response tree", path, key)
		}
		if err := matchValue(path+"."+key, ev, unionVariantsNode(variants)); err != nil {
			return err
		}
	}
	return nil
}

// unionVariantsNode returns the node to match an expected value against: the single field's value,
// or -- for same-key sibling variants whose values are objects (possibly under equal list nesting) --
// a synthetic object unioning their sub-selections at the first variant's list depth. Variants that
// are not composite (or of unequal kinds) fall back to the first variant's value: kind mismatches
// then surface through matchValue exactly as before.
func unionVariantsNode(variants []*resolve.Field) resolve.Node {
	if len(variants) == 1 {
		return variants[0].Value
	}
	depth0, isObj0, _ := peel(variants[0].Value)
	merged := &resolve.Object{}
	for _, v := range variants {
		d, isObj, obj := peel(v.Value)
		if !isObj || !isObj0 || d != depth0 {
			return variants[0].Value
		}
		merged.Fields = append(merged.Fields, obj.Fields...)
	}
	var out resolve.Node = merged
	for i := 0; i < depth0; i++ {
		out = &resolve.Array{Item: out}
	}
	return out
}

// matchValue checks one expected JSON value against a plan node's kind (object / array / leaf).
func matchValue(path string, ev json.RawMessage, node resolve.Node) error {
	depth, isObj, obj := peel(node)
	switch {
	case isJSONNull(ev):
		return nil // null value: presence already established; nullability not asserted further
	case isJSONArray(ev):
		if depth == 0 {
			return fmt.Errorf("%s: expected a list, plan node is not a resolve.Array", path)
		}
		// recurse into the first element's shape, if any (a nested list vs object element).
		var items []json.RawMessage
		_ = json.Unmarshal(ev, &items)
		if len(items) == 0 {
			return nil
		}
		return matchValue(path+"[]", items[0], arrayItem(node))
	case isJSONObject(ev):
		if !isObj {
			return fmt.Errorf("%s: expected an object, plan node is a leaf", path)
		}
		var sub map[string]json.RawMessage
		_ = json.Unmarshal(ev, &sub)
		return matchObject(path, sub, obj)
	default: // scalar leaf
		if isObj {
			return fmt.Errorf("%s: expected a scalar leaf, plan node is an object", path)
		}
		return nil
	}
}

// planFieldsByKey indexes an object's response fields by client key, keeping ALL same-key variants
// (one client field's ungated + member-gated siblings, D11.8) so the oracle can presence-union them.
func planFieldsByKey(obj *resolve.Object) map[string][]*resolve.Field {
	out := map[string][]*resolve.Field{}
	for _, f := range obj.Fields {
		out[string(f.Name)] = append(out[string(f.Name)], f)
	}
	return out
}

// peel unwraps resolve.Array nesting and reports the list depth plus whether the element is an
// object (and that object).
func peel(node resolve.Node) (depth int, isObj bool, obj *resolve.Object) {
	for {
		arr, ok := node.(*resolve.Array)
		if !ok {
			break
		}
		depth++
		node = arr.Item
	}
	if o, ok := node.(*resolve.Object); ok {
		return depth, true, o
	}
	return depth, false, nil
}

// arrayItem returns the element node under the first Array wrapper of node (node itself if not an
// Array).
func arrayItem(node resolve.Node) resolve.Node {
	if arr, ok := node.(*resolve.Array); ok {
		return arr.Item
	}
	return node
}

func isJSONNull(m json.RawMessage) bool   { return strings.TrimSpace(string(m)) == "null" }
func isJSONArray(m json.RawMessage) bool  { return firstNonSpace(m) == '[' }
func isJSONObject(m json.RawMessage) bool { return firstNonSpace(m) == '{' }

func firstNonSpace(m json.RawMessage) byte {
	for _, b := range m {
		if b != ' ' && b != '\n' && b != '\t' && b != '\r' {
			return b
		}
	}
	return 0
}

// sortOutcomes orders outcomes by suite then case for a stable scoreboard.
func sortOutcomes(o []Outcome) {
	sort.Slice(o, func(i, j int) bool {
		if o[i].Suite != o[j].Suite {
			return o[i].Suite < o[j].Suite
		}
		return o[i].Case < o[j].Case
	})
}
