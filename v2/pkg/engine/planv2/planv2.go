// Package planv2 is the public entry point for planner-v2. It has the same shape as the v1
// plan.Planner (NewPlanner + Plan), so callers can swap one for the other without changing anything
// else. It just wires up the pipeline: build the graph once (at NewPlanner), then per query turn the
// query into a tree of obligations, search for the cheapest plan, and lower that plan back into a
// GraphQL fetch tree.
//
// Along with lower, this is the only planv2 package allowed to import the v1 `plan` package, and only
// for its public types (Configuration, Plan, Opts). All the actual routing lives in the subpackages;
// this file is just orchestration.
//
// Input contract (identical to v1): Plan expects `operation` already normalized against `definition`,
// and `definition` already merged with the base schema. The exact pipeline is:
//
//	def  := parse(schemaSDL); asttransform.MergeDefinitionWithBaseSchema(&def)
//	op   := parse(operationSDL)
//	astnormalization.NewWithOpts(
//	    astnormalization.WithExtractVariables(),
//	    astnormalization.WithInlineFragmentSpreads(),
//	    astnormalization.WithRemoveFragmentDefinitions(), // obligation.Build tolerates leftover frag
//	    astnormalization.WithRemoveUnusedVariables(),      // defs, but we remove them to match v1
//	).NormalizeOperation(&op, &def, &report)
//	// (optional) astvalidation.DefaultOperationValidator().Validate(&op, &def, &report)
//
// This mirrors datasourcetesting.RunTest exactly; the differential harness uses the same pipeline so
// both planners get byte-identical input.
package planv2

import (
	"fmt"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	grpcdatasource "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/grpc_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/cost"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/lower"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// searchConfig is the fixed budget every plan runs under. PreflightCap rejects a query whose size
// estimate is too big before any allocation; StateCap stops a runaway search. Both are generous
// ceilings that only trip on pathological input, and both surface as typed errors, never a wrong
// plan.
var searchConfig = search.Config{Combine: search.Sum, PreflightCap: 1 << 30, StateCap: 1 << 24}

// Config are planner-construction options -- planv2's own knobs, separate from the v1
// plan.Configuration NewPlanner also takes. The zero value is the shipping default.
type Config struct {
	// OperationScopedSearch selects the operation-scoped settle domain (FORMAL_SPEC Section 6.5,
	// search.Config.OperationScoped): per plan, the search settles only the backward closure of
	// the operation's candidate nodes instead of the whole graph, making per-plan cost
	// proportional to the query rather than the schema. Emitted plans are mode-identical -- the
	// dual-mode oracle, the audit-corpus and conformance plan-equality gates, and the differential
	// mode-equality pass assert byte-identical plans -- and only resource-guard behavior may differ
	// (Section 6.5). Default false: the whole-graph settle remains the default pending an owner-approved
	// soak.
	OperationScopedSearch bool

	// GRPCTransports supplies the live RPC transport (gRPC client connection or Connect transport) for
	// each gRPC/ConnectRPC-configured subgraph, keyed by subgraph name (ds.Name(), matching
	// hypergraph.Build). It is a NewPlanner input, not part of plan.Configuration, because the transport
	// is a Factory-level runtime dependency unreachable through the plan.DataSource interface (the same
	// reason planv2 rebuilds HTTP sources on http.DefaultClient -- see transport.go). When a gRPC
	// subgraph has no entry here, planv2 still emits an executable gRPC fetch (the RPC plan is compiled),
	// but its Load returns the typed "requires an rpc transport" error until a transport is supplied --
	// never a silent shape-only stub. HTTP-only supergraphs leave this nil.
	GRPCTransports map[string]grpcdatasource.RPCTransport
}

// Planner holds the compiled graph (built once at NewPlanner), the per-subgraph transport table
// (the HTTP + subscription details each fetch/trigger needs) derived from the same config, and the
// config's default subscription flush interval. Everything else in the config is consumed at build
// time and not kept. No field carries per-query state, so one Planner is safe for concurrent Plan
// calls -- matching v1, and for the same reason: the HTTP sources are stateless and shared across
// plans.
type Planner struct {
	h                   *hypergraph.Hypergraph
	transport           lower.TransportTable
	flushIntervalMillis int64
	searchCfg           search.Config // the fixed per-plan budget + the Section 6.5 mode toggle
	// info is the M4.1 Info-emission configuration lowering consumes: the config's field table
	// (HasAuthorizationRule lookups) and IncludeInfo = !DisableIncludeInfo. See lower.InfoConfig.
	info lower.InfoConfig
	// cost is the M4.5 cost-support state, nil unless config.ComputeCosts is set. When present, Plan
	// attaches a plan.CostCalculator (built by the cost package, mirroring v1) to every emitted plan
	// so the router's cost-limit enforcement sees a working calculator rather than nil.
	cost *costSupport
}

// costSupport carries what the facade needs to build a per-plan CostCalculator: the plan.Configuration
// (plan.NewCostCalculator reads its per-DS CostConfig, StaticCostDefaultListSize, and
// IgnoreImplementingTypeWeights) and the subgraph-name -> datasource-hash map that translates the
// search's chosen route into the cost tree's datasource attribution.
type costSupport struct {
	config       plan.Configuration
	dsHashByName map[string]plan.DSHash
}

// NewPlanner compiles the supergraph Configuration into the graph once, up front, matching v1's
// "build the planner, then Plan many operations" lifecycle. It maps the query, mutation, and
// subscription operation kinds to their root type names; a root type with no resolvable fields is
// pruned during the build, so naming "mutation"/"subscription" here is harmless for a query-only
// supergraph.
func NewPlanner(config plan.Configuration) (*Planner, error) {
	return NewPlannerWithConfig(config, Config{})
}

// NewPlannerWithConfig is NewPlanner plus planv2's own options (see Config). NewPlanner is
// NewPlannerWithConfig with the zero Config, so the default construction path is unchanged.
func NewPlannerWithConfig(config plan.Configuration, opts Config) (*Planner, error) {
	// M4.1 typed-loud sweep (refuse.go): a configuration carrying a capability planv2 would
	// silently drop is refused here, typed, before any graph work -- the router seam catches the
	// sentinel and falls back to v1 (the MISSING-LOUD adoption contract).
	if err := refuseUnsupportedConfig(config); err != nil {
		return nil, err
	}
	h, err := hypergraph.Build(config.DataSources, hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation", "subscription": "Subscription"},
	})
	if err != nil {
		return nil, err
	}
	searchCfg := searchConfig
	searchCfg.OperationScoped = opts.OperationScopedSearch
	// Derive the per-subgraph transport (HTTP source + url/method/header; subscription source + wire
	// options) from the same config, so Plan can emit fetches and triggers that are actually
	// executable. Built once here. A supergraph whose datasources expose no fetch/subscription config
	// yields a nil table, in which case lowering produces shape-only output, unchanged.
	return &Planner{
		h:                   h,
		transport:           buildTransportTable(config.DataSources, opts.GRPCTransports),
		flushIntervalMillis: config.DefaultFlushIntervalMillis,
		searchCfg:           searchCfg,
		// v1 parity: FieldInfo/RootFields emission is on unless DisableIncludeInfo; the field
		// table supplies the HasAuthorizationRule lookups (postprocess derives the pre-fetch
		// authorization coordinates from these surfaces -- PARITY.md Section 6).
		info: lower.InfoConfig{Fields: config.Fields, IncludeInfo: !config.DisableIncludeInfo},
		// M4.5: when the router configures IBM cost, attach a v1-equivalent CostCalculator to every
		// plan (built lazily per Plan call from the chosen route). Nil otherwise -- byte-identical to
		// the pre-cost output.
		cost: newCostSupport(config),
	}, nil
}

// newCostSupport returns the cost-support state when config.ComputeCosts is set, else nil. It
// precomputes the subgraph-name -> datasource-hash map once (the names match hypergraph.Build, which
// keys subgraphs by DataSource.Name).
func newCostSupport(config plan.Configuration) *costSupport {
	if !config.ComputeCosts {
		return nil
	}
	byName := make(map[string]plan.DSHash, len(config.DataSources))
	for _, ds := range config.DataSources {
		byName[ds.Name()] = ds.Hash()
	}
	return &costSupport{config: config, dsHashByName: byName}
}

// attachCost builds and attaches the CostCalculator to plan p when cost support is active. It mirrors
// v1 (plan/planner.go), which attaches a calculator to every plan kind. A nil calculator (nothing to
// price) is attached as nil, exactly as the router tolerates.
func (p *Planner) attachCost(out plan.Plan, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, operationName string) {
	if p.cost == nil || out == nil {
		return
	}
	calc := cost.BuildCalculator(p.cost.config, p.cost.dsHashByName, p.h, o, res, operation, definition, operationName)
	out.SetCostCalculator(calc)
}

// Diagnostic is one non-fatal planner finding attached to a plan by PlanWithDiagnostics. It is a
// value, not a log line: the facade has no logging dependency, so "loud by default" means every
// finding is always populated on the returned Diagnostics for the caller to log or assert on.
type Diagnostic struct {
	Severity string // SeverityWarn -- the only severity emitted today
	Code     string // stable machine-readable code (e.g. DiagRouteFallback)
	Message  string // human-readable, names the goal/branch/subgraph for triage
}

// SeverityWarn marks a diagnostic that does not invalidate the plan but must not pass silently.
const SeverityWarn = "WARN"

// DiagRouteFallback is the code for a D10 route-fallback event (FORMAL_SPEC D10 amendment --
// typed-loud fall-back): a goal was served through a path-inconsistent route because the graph
// lacks the edges a path-consistent route needs. The plan is still sound and complete (I1/I2);
// the event marks a registered model-gap class, tracked in DIVERGENCES.md.
const DiagRouteFallback = "D10_ROUTE_FALLBACK"

// Diagnostics is the facade's non-fatal output channel, returned by PlanWithDiagnostics alongside
// the plan. RouteFallbacks is the typed record straight from the search layer (no re-encoding);
// Warnings carries one Warn-level rendering per event, populated unconditionally (the loud
// default). Both are empty on a fully path-consistent plan. When search succeeds but lowering
// fails, the diagnostics still describe the searched plan -- the audit and corpus counters read
// them even for cases that never produce an executable plan.
type Diagnostics struct {
	RouteFallbacks []search.RouteFallback
	Warnings       []Diagnostic
}

// routeFallbackDiagnostics renders the typed search record into the facade's Diagnostics: the
// record itself, plus one Warn diagnostic per event.
func routeFallbackDiagnostics(fallbacks []search.RouteFallback) Diagnostics {
	if len(fallbacks) == 0 {
		return Diagnostics{}
	}
	d := Diagnostics{RouteFallbacks: fallbacks, Warnings: make([]Diagnostic, 0, len(fallbacks))}
	for _, f := range fallbacks {
		d.Warnings = append(d.Warnings, Diagnostic{
			Severity: SeverityWarn,
			Code:     DiagRouteFallback,
			Message: fmt.Sprintf(
				"route fallback (D10): goal %s under root field %q served through a path-inconsistent %s route via subgraph %q -- the graph lacks the edges a path-consistent route needs (registered model-gap class; plan still sound/complete)",
				f.Coordinate, f.RootField, f.Kind, f.Subgraph),
		})
	}
	return d
}

// Plan matches plan.Planner.Plan's signature exactly (including the variadic plan.Opts) so planv2 is
// a drop-in at the planner boundary. It expects `operation` already normalized against `definition`
// (see the package doc for the pinned pipeline) and returns a plan.Plan, or nil with a typed error
// recorded on report. The v1 signature has no diagnostics channel, so this wrapper drops them --
// callers that must see D10 route-fallback events (the audit runner, corpus sweeps, anything
// enforcing the no-silent-degrade bar) call PlanWithDiagnostics instead.
//
// plan.Opts handling (M1): the options are accepted for signature compatibility but none are honored
// yet -- notably plan.IncludeQueryPlanInResponse (lowering already attaches the query-plan fragments
// to each fetch, so the data is there; embedding it in the response is deferred). Stated here rather
// than silently ignored. New options are likewise accepted and ignored until a later milestone wires
// them.
func (p *Planner) Plan(operation, definition *ast.Document, operationName string,
	report *operationreport.Report, options ...plan.Opts) plan.Plan {
	out, _ := p.PlanWithDiagnostics(operation, definition, operationName, report, options...)
	return out
}

// PlanWithDiagnostics is Plan plus the facade's non-fatal findings (see Diagnostics). Today the
// only diagnostic source is the D10 route fallback; a plan produced via fallback is still sound
// and complete but flagged, never silent (FORMAL_SPEC D10 amendment -- typed-loud fall-back).
func (p *Planner) PlanWithDiagnostics(operation, definition *ast.Document, operationName string,
	report *operationreport.Report, options ...plan.Opts) (plan.Plan, Diagnostics) {

	_ = options // accepted for drop-in compatibility; see Plan's godoc -- M1 honors none yet.

	// Turn the normalized operation into the obligation tree. Build also decides which fields narrow
	// away to null; the facade must not redo that, as it would mutate the tree and break search's
	// purity.
	o, err := obligation.Build(operation, definition, operationName, p.h)
	if err != nil {
		report.AddInternalError(err)
		return nil, Diagnostics{}
	}

	// Search for the cheapest plan. On failure it returns one of the typed errors: no valid plan,
	// plan too large, or search state cap exceeded.
	res, err := search.Search(p.h, o, p.searchCfg)
	if err != nil {
		report.AddInternalError(err)
		return nil, Diagnostics{}
	}

	// The searched plan exists -- surface its D10 route-fallback events now, so they are visible
	// even if lowering fails below (corpus counters classify such cases too).
	diags := routeFallbackDiagnostics(res.RouteFallbacks)

	// A subscription lowers to the trigger/response split (D11.12): the single root fetch group
	// becomes the resolve.GraphQLSubscription trigger, everything below stays per-event fetches.
	// Obligation and search above are operation-type agnostic -- the root goals simply resolved
	// against the Subscription root registered at NewPlanner.
	if isSubscription(operation, operationName) {
		out, err := lower.LowerSubscriptionExecutableWithInfo(p.h, o, res, operation, definition, p.transport, p.info, operationName)
		if err != nil {
			report.AddInternalError(err)
			return nil, diags
		}
		out.FlushInterval = p.flushIntervalMillis
		p.attachCost(out, o, res, operation, definition, operationName)
		return out, diags
	}

	// A query carrying @defer records lowers to the primary + increments encoding (D11.13): the
	// same obligation-driven lowering, fetches partitioned into defer scope variants, wrapped in
	// v1's DeferResponsePlan so the existing postprocess/resolve incremental-delivery machinery
	// executes it unchanged. Defers() is non-empty only for query operations (FS-DEF-7 gate in
	// obligation.Build), so mutations/subscriptions with stray @defer stamps flatten (FS-DEF-1).
	if len(o.Defers()) > 0 {
		out, err := lower.LowerDeferExecutableWithInfo(p.h, o, res, operation, definition, p.transport, p.info)
		if err != nil {
			report.AddInternalError(err)
			return nil, diags
		}
		p.attachCost(out, o, res, operation, definition, operationName)
		return out, diags
	}

	// Lower the plan into the v1 output contract: fetch groups, response shape, cross-subgraph
	// aliasing, typed scalar leaves. The definition types each leaf so the resolver walks it correctly.
	out, err := lower.LowerExecutableWithInfo(p.h, o, res, operation, definition, p.transport, p.info)
	if err != nil {
		report.AddInternalError(err)
		return nil, diags
	}
	p.attachCost(out, o, res, operation, definition, operationName)
	return out, diags
}

// isSubscription reports whether the operation the caller asked to plan is a subscription.
//
// The loop ranges over a slice in document order, so the scan is deterministic. With a name given,
// the matching operation decides. An empty name is only valid for single-operation documents, so the
// empty-name branch looks at the first operation and stops: for a valid single-op document that is
// the operation; for an invalid multi-op document the answer doesn't matter, because obligation.Build
// will immediately report the missing-operation-name error. If no operation matches the name, this
// returns false and obligation.Build reports the not-found error the same way.
func isSubscription(operation *ast.Document, operationName string) bool {
	for ref := range operation.OperationDefinitions {
		name := operation.OperationDefinitionNameString(ref)
		if operationName == "" || name == operationName {
			if operation.OperationDefinitions[ref].OperationType == ast.OperationTypeSubscription {
				return true
			}
			if operationName == "" {
				return false // empty name: only the first (single) operation matters
			}
		}
	}
	return false
}
