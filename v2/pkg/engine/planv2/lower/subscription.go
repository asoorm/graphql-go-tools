package lower

// subscription.go realizes FORMAL_SPEC D11.12 -- subscription lowering, the trigger/response split at
// the root position. A subscription plans through obligation/search exactly like a query (the cover
// spans the FULL selection, root field included); lowering runs the shared obligation-driven parts
// and then re-expresses the single root fetch group as the plan's subscription TRIGGER: its printed
// `subscription` document, its forwarded context variables, and -- when the transport table carries a
// subscription transport for the root subgraph -- the subscription wire envelope and source. Every
// other group stays an ordinary per-event fetch (a `query`, FS-SUB-4) with its fetch id and
// dependencies untouched: the trigger occupies the root group's id slot, so a fetch directly under
// the root depends on the trigger's id -- the id shape v1 emits and postprocess/resolve tolerate.

import (
	"errors"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// ErrSubscriptionSingleRootField is the typed D11.12 precondition error: a subscription operation
// must have exactly one root field (GraphQL spec Section 5.2.3.1, FS-SUB-1). The upstream operation
// validator enforces the rule; lowering re-checks it and fails loudly rather than mis-lowering a
// multi-root subscription (Section 6.3 no silent degrade).
var ErrSubscriptionSingleRootField = errors.New(
	"planv2: subscription operations must have exactly one root field (GraphQL spec Section 5.2.3.1)")

// LowerSubscription maps a searched subscription plan to the v1 subscription output contract
// (plan.SubscriptionResponsePlan wrapping resolve.GraphQLSubscription: Trigger + Response),
// shape-only -- no transport attached, exactly like Lower for the synchronous path.
func LowerSubscription(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document) (*plan.SubscriptionResponsePlan, error) {
	return lowerSubscription(h, o, res, operation, definition, nil, InfoConfig{}, "")
}

// LowerSubscriptionExecutable is LowerSubscription plus transport: per-event entity fetches get the
// ordinary D11.5 HTTP attach from the table, and the trigger gets the root subgraph's SUBSCRIPTION
// transport (D11.12 wire fields + subscription source). A nil table behaves exactly like
// LowerSubscription.
func LowerSubscriptionExecutable(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable) (*plan.SubscriptionResponsePlan, error) {
	return lowerSubscription(h, o, res, operation, definition, transport, InfoConfig{}, "")
}

// LowerSubscriptionExecutableWithInfo is LowerSubscriptionExecutable plus the M4.1 Info emission
// (see LowerExecutableWithInfo). The subscription root field's FieldInfo is load-bearing:
// postprocess builds the trigger fetch node off its Source attribution. operationName selects the
// planned operation in a multi-operation document (for @openfed__subscriptionFilter argument-template
// resolution against the right operation's variable definitions).
func LowerSubscriptionExecutableWithInfo(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable, info InfoConfig, operationName string) (*plan.SubscriptionResponsePlan, error) {
	return lowerSubscription(h, o, res, operation, definition, transport, info, operationName)
}

// lowerSubscription runs the shared obligation-driven parts and performs the trigger/response split.
func lowerSubscription(h *hypergraph.Hypergraph, o *obligation.Tree, res *search.Result,
	operation, definition *ast.Document, transport TransportTable, info InfoConfig, operationName string) (*plan.SubscriptionResponsePlan, error) {

	// D11.12 single-root-field precondition, checked on the obligation tree: exactly one top-level
	// obligation and it is a plain field (the subscription root type is concrete, so a top-level
	// refinement cannot occur; a top-level __typename would make the root count ambiguous and is
	// excluded by the same GraphQL validation rule).
	obs := o.Obligations()
	_, roots := obligationChildren(obs)
	if len(roots) != 1 || obs[roots[0]].Kind != obligation.Field {
		return nil, ErrSubscriptionSingleRootField
	}
	rootOb := obs[roots[0]]

	data, items, rootItems, err := lowerObligationDrivenParts(h, o, res, operation, definition, transport, info)
	if err != nil {
		return nil, err
	}
	// The single root field yields exactly one root fetch group (its selection cannot split across
	// two operation-root documents). Anything else is an internal inconsistency, reported on the same
	// typed error rather than mis-lowered.
	if len(rootItems) != 1 {
		return nil, ErrSubscriptionSingleRootField
	}
	rootIdx := rootItems[0]

	rootFetch, ok := items[rootIdx].Fetch.(*resolve.SingleFetch)
	if !ok {
		return nil, errors.New("planv2: subscription root group did not lower to a single fetch")
	}

	// The trigger transport is the root subgraph's subscription entry, when the table carries one.
	subgraphName := string(rootFetch.DataSourceIdentifier)
	var subTransport *SubscriptionTransport
	if transport != nil {
		subTransport = transport[subgraphName].Subscription
	}

	trigger := resolve.GraphQLSubscriptionTrigger{
		// The root fetch's Input is the body-only envelope (buildFetches skipped the HTTP attach for
		// a subscription root); the subscription wire fields are spliced in front of it. A nil
		// subscription transport leaves the body-only envelope -- the shape-only trigger.
		Input:     []byte(wrapTriggerInput(rootFetch.FetchConfiguration.Input, subTransport)),
		Variables: rootFetch.FetchConfiguration.Variables,
		QueryPlan: rootFetch.FetchConfiguration.QueryPlan,
		// v1's DefaultPostProcessingConfiguration: the trigger payload is a GraphQL response -- data
		// merged per event, errors surfaced.
		PostProcessing: resolve.PostProcessingConfiguration{
			SelectResponseDataPath:   []string{"data"},
			SelectResponseErrorsPath: []string{"errors"},
		},
		SourceName: subgraphName,
	}
	if subTransport != nil {
		trigger.Source = subTransport.Source
		trigger.SourceID = subTransport.SourceID
	}

	// @openfed__subscriptionFilter emission (M4.4): the root field's SubscriptionFilterCondition
	// becomes the resolve.SubscriptionFilter the resolver's SkipEvent consults per event. Set on the
	// GraphQLSubscription (v1 visitor.go configureSubscription: v.subscription.Filter = config.filter),
	// NOT the trigger. A malformed argument template fails lowering loudly rather than silently
	// dropping the filter -- a dropped filter delivers events that MUST be skipped (a data-exposure
	// defect), so this is security-grade parity.
	fieldRef, opDefRef, ok := subscriptionRootFieldRef(operation, operationName)
	if !ok {
		return nil, ErrSubscriptionSingleRootField
	}
	filter, err := buildSubscriptionFilter(operation, definition, info, rootOb.Type, rootOb.Field, fieldRef, opDefRef)
	if err != nil {
		return nil, err
	}

	// Every non-root item stays a per-event fetch, ids and dependencies untouched (the trigger keeps
	// the root group's id slot; FS-SUB-5's dependency shape).
	raw := make([]*resolve.FetchItem, 0, len(items)-1)
	for i, item := range items {
		if i == rootIdx {
			continue
		}
		raw = append(raw, item)
	}

	return &plan.SubscriptionResponsePlan{
		Response: &resolve.GraphQLSubscription{
			Trigger: trigger,
			Filter:  filter,
			Response: &resolve.GraphQLResponse{
				Data:       data,
				RawFetches: raw,
				Info:       &resolve.GraphQLResponseInfo{OperationType: ast.OperationTypeSubscription},
			},
		},
	}, nil
}
