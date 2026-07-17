package planv2

// refuse.go is the M4.1 typed-loud sweep (PARITY.md Section 2/Section 3/Section 5): every plan.Configuration
// surface planv2 accepts but cannot honor is REFUSED at NewPlanner with a typed sentinel error --
// the no-silent-degrade bar forbids accepting a config whose semantics would be silently dropped.
// Each sentinel names the capability so the router seam (or any embedder) can errors.Is-match the
// refusal and fall back to v1; when a capability lands, its check is deleted and its PARITY.md row
// flips MISSING-LOUD -> PARITY.
//
// The sweep distinguishes two kinds of unhonored surface, and only the first refuses:
//   - SEMANTIC divergence: honoring the config would change the RESPONSE. Ignoring it would make
//     planv2 silently produce WRONG results (a dropped subscription filter delivers events that must
//     be skipped; a missing type rename yields the wrong type name). These MUST refuse -- until the
//     capability lands, at which point honoring it replaces the refusal. (IBM cost -- ComputeCosts --
//     was such a case: an unenforced cost limit lets a rejected query through; M4.5 honors it by
//     attaching a v1-equivalent CostCalculator. @openfed__subscriptionFilter was another: a dropped
//     SubscriptionFilterCondition silently delivered events that must be skipped; M4.4 honors it by
//     building the resolve.SubscriptionFilter in subscription lowering, so it no longer refuses.)
//   - NON-SEMANTIC divergence: the response is byte-identical either way; the config only requests an
//     optimization or an observability nicety planv2 skips (a smaller subgraph query string, a name
//     on the subgraph operation). These MUST NOT refuse -- a refusal here rejects a config planv2
//     could serve identically, and if the flag is a router default it disables planv2 wholesale.
//
// Deliberate NON-refusals, each adjudicated in PARITY.md:
//   - MinifySubgraphOperations: a non-semantic subgraph-query-string size optimization. planv2 emits
//     an unminified but semantically identical subgraph document, so responses are byte-identical.
//     This flag is envDefault:"true" in the Cosmo router (cosmo/router/pkg/config/config.go) -- ON in
//     EVERY default deployment -- so refusing it disabled planv2 for 100% of real operations (the
//     router seam fell back to v1 for every config). Proven results-identical at b7b8e1f0: planv2
//     PLANNED these suites WITH MinifySubgraphOperations set and scored 188; adding the refusal at
//     e1a86364 collapsed the audit to the stock-v1 result (185/199, every op v1-fallback). Accept and
//     plan without minifying.
//   - EnableOperationNamePropagation: sets only the NAME of the subgraph operation (observability/
//     tracing). The response is identical whether or not the subgraph operation is named, so this is
//     non-semantic. Default-off, but refusing it would be wrong-in-kind (a name is not a result), so
//     it is a NON-refusal alongside MinifySubgraphOperations. Accept and plan with unnamed documents.
//   - FieldConfiguration.Arguments with SourceType == ObjectFieldSource: real router execution
//     configs carry OBJECT_FIELD argument mappings (the exec-config adapter decodes them), so a
//     constructor refusal could reject production supergraphs wholesale. planv2 derives
//     FIELD_ARGUMENT routing from the operation itself; OBJECT_FIELD sourcing stays a registered
//     MISSING row (M4.4) flagged for a corpus-compat decision rather than a blind refusal.
//   - DisableIncludeFieldDependencies / DisableCalculateFieldDependencies: inverted-default flags.
//     Their zero value (false) REQUESTS FetchInfo.CoordinateDependencies, which planv2 does not
//     build yet; refusing on the zero value would refuse every default config, breaking every
//     harness and the corpus sweep. Registered MISSING (rides the Info follow-up wave).
//   - plan.Opts: the options type is an opaque closure over the v1 planner's unexported _opts
//     struct -- planv2 cannot invoke or inspect one. The only constructible option is
//     IncludeQueryPlanInResponse, whose data planv2 attaches unconditionally (a strict superset),
//     so acceptance is sound by construction. See PARITY.md Section 5.
//   - Logger / Debug / MaxDataSourceCollectorsConcurrency / EntityInterfaceNames /
//     RelaxSubgraphOperationFieldSelectionMergingNullability: N-A rows (justified in PARITY.md) --
//     v1-pipeline-internal or redundant inputs with nothing to honor or refuse.

import (
	"errors"
	"fmt"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
)

var (
	// ErrTypeRenamesNotSupported: plan.Configuration.Types (TypeConfiguration.RenameTo, the
	// stitched/contract-schema type-renaming table) is not consulted by planv2 (PARITY.md Section 2).
	ErrTypeRenamesNotSupported = errors.New(
		"planv2: plan.Configuration.Types (type renaming) is not supported")
	// ErrCustomResolveMapNotSupported: resolve.CustomResolve custom-scalar resolvers are never
	// attached to planv2 plans (PARITY.md Section 2).
	ErrCustomResolveMapNotSupported = errors.New(
		"planv2: plan.Configuration.CustomResolveMap (custom scalar resolvers) is not supported")
	// ErrFetchReasonsNotSupported: FetchInfo.FetchReasons/PropagatedFetchReasons are never built, so
	// BuildFetchReasons and the ValidateRequiredExternalFields mode depending on them cannot be
	// honored (PARITY.md Section 2; @openfed__requireFetchReasons register row).
	ErrFetchReasonsNotSupported = errors.New(
		"planv2: plan.Configuration.BuildFetchReasons / ValidateRequiredExternalFields (fetch reasons) are not supported")
	// ErrFieldPathMappingNotSupported: FieldConfiguration.Path response-path mapping is never
	// applied; planv2 always emits default mapping (PARITY.md Section 3; M4.4).
	ErrFieldPathMappingNotSupported = errors.New(
		"planv2: plan.FieldConfiguration.Path (response path mapping) is not supported")
	// ErrUnescapeResponseJSONNotSupported: escaped-JSON-string unwrapping is never set on planv2
	// fields (PARITY.md Section 3; M4.4).
	ErrUnescapeResponseJSONNotSupported = errors.New(
		"planv2: plan.FieldConfiguration.UnescapeResponseJson is not supported")
	// ErrArgumentRenderingNotSupported: non-default ArgumentConfiguration.RenderConfig variants and
	// RenameTypeTo are never consulted (PARITY.md Section 3; M4.4).
	ErrArgumentRenderingNotSupported = errors.New(
		"planv2: plan.ArgumentConfiguration.RenderConfig / RenameTypeTo are not supported")
	// ErrDirectiveRenamesNotSupported: per-datasource DirectiveConfigurations (directive renaming)
	// is never consulted (PARITY.md Section 4).
	ErrDirectiveRenamesNotSupported = errors.New(
		"planv2: DataSourceMetadata.Directives (directive renaming) is not supported")
)

// gRPC/ConnectRPC datasources are ACCEPTED as of M4.3: buildTransportTable builds a per-fetch executable
// grpc_datasource.DataSource (reusing grpc_datasource, mirroring the HTTP transport table), so a
// gRPC-configured datasource no longer lowers to shape-only, non-executable output. The former
// ErrGRPCDatasourceNotSupported refusal (and its NewPlanner check) is therefore removed; the live RPC
// transport, unreachable through plan.Configuration, is supplied by the embedder via
// Config.GRPCTransports. See transport.go and PARITY.md Section 1.

// refuseUnsupportedConfig returns the typed refusal for the first unsupported capability the given
// configuration carries, or nil when every carried capability is honored. Config-carried surfaces
// are checked here (constructor time) per the M4.1 contract; there are no per-operation surfaces
// left to check at Plan time (see the package doc's plan.Opts note).
func refuseUnsupportedConfig(config plan.Configuration) error {
	if len(config.Types) > 0 {
		return fmt.Errorf("%w (%d type rename(s) configured)", ErrTypeRenamesNotSupported, len(config.Types))
	}
	if len(config.CustomResolveMap) > 0 {
		return fmt.Errorf("%w (%d custom resolver(s) configured)", ErrCustomResolveMapNotSupported, len(config.CustomResolveMap))
	}
	if config.BuildFetchReasons || config.ValidateRequiredExternalFields {
		return ErrFetchReasonsNotSupported
	}
	for i := range config.Fields {
		fc := &config.Fields[i]
		coord := fc.TypeName + "." + fc.FieldName
		if len(fc.Path) > 0 {
			return fmt.Errorf("%w (field %s)", ErrFieldPathMappingNotSupported, coord)
		}
		if fc.UnescapeResponseJson {
			return fmt.Errorf("%w (field %s)", ErrUnescapeResponseJSONNotSupported, coord)
		}
		for j := range fc.Arguments {
			a := &fc.Arguments[j]
			if a.RenderConfig != plan.RenderArgumentDefault || a.RenameTypeTo != "" {
				return fmt.Errorf("%w (field %s, argument %s)", ErrArgumentRenderingNotSupported, coord, a.Name)
			}
		}
	}
	for _, ds := range config.DataSources {
		if dc := ds.DirectiveConfigurations(); dc != nil && len(*dc) > 0 {
			return fmt.Errorf("%w (datasource %s)", ErrDirectiveRenamesNotSupported, ds.Id())
		}
	}
	return nil
}
