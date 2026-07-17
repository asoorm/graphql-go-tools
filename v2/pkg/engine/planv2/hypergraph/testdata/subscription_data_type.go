package testdata

// subscription_data_type.go -- fixtures for the "data type literally named Subscription" family
// (customer re-sweep regression): billing/commerce schemas carry an ENTITY named `Subscription`
// (a customer's product subscription: endsAt/region/holder), which is ordinary data. A type
// named Subscription that is NOT a declared subscription operation root in a given subgraph must
// participate in query/mutation routing exactly as any entity -- never be re-tailed onto the
// subscription operation root.

import "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"

// BillingSubscriptionConfig builds the billing shape across two subgraphs. typeName selects the
// entity's name: "Subscription" (the colliding name) or any neutral name ("BillingSubscription") --
// the twin the regression compares against. Both subgraphs cover the two evidence paths for "this
// is data, not an operation root":
//   - billing (owner): EXPLICIT `schema { query: Query }` block WITHOUT a subscription binding --
//     block evidence frees the name.
//   - holders (extender): no schema block, but the type is a KEYED ENTITY there -- entity
//     evidence (an operation root cannot be an entity).
func BillingSubscriptionConfig(typeName string) []plan.DataSource {
	schemaBilling := `
schema { query: Query }
type Query { currentPlan: ` + typeName + ` }
type Mutation { planCancel(id: ID!): ` + typeName + ` }
type ` + typeName + ` @key(fields: "id") {
  id: ID!
  endsAt: String
  region: String
}
`
	schemaHolders := `
type ` + typeName + ` @key(fields: "id") {
  id: ID!
  holder: Holder
}
type Holder { id: ID! name: String }
`
	billing := newDataSource("billing", schemaBilling, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"currentPlan"}},
			{TypeName: "Mutation", FieldNames: []string{"planCancel"}},
			{TypeName: typeName, FieldNames: []string{"id", "endsAt", "region"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: typeName, SelectionSet: "id"}},
		},
	})
	holders := newDataSource("holders", schemaHolders, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: typeName, FieldNames: []string{"id", "holder"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Holder", FieldNames: []string{"id", "name"}},
		},
		FederationMetaData: plan.FederationMetaData{
			Keys: plan.FederationFieldConfigurations{{TypeName: typeName, SelectionSet: "id"}},
		},
	})
	return []plan.DataSource{billing, holders}
}

// BillingSubscriptionWithRealtimeConfig is BillingSubscriptionConfig("Subscription") plus a THIRD
// subgraph declaring a GENUINE subscription operation root -- necessarily under a renamed root type
// (`schema { subscription: RealtimeSubscription }`), since the composed name "Subscription" is
// taken by the billing entity. The sharp sub-case: per subgraph, the same-named type is plain data
// in billing/holders while realtime has an actual subscription root.
func BillingSubscriptionWithRealtimeConfig() []plan.DataSource {
	const schemaRealtime = `
schema { query: Query subscription: RealtimeSubscription }
type Query { ping: String }
type RealtimeSubscription { tick: Tick }
type Tick { id: ID! value: Float }
`
	realtime := newDataSource("realtime", schemaRealtime, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"ping"}},
			{TypeName: "RealtimeSubscription", FieldNames: []string{"tick"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Tick", FieldNames: []string{"id", "value"}},
		},
	})
	return append(BillingSubscriptionConfig("Subscription"), realtime)
}

// BillingRootNodesOnlyConfig reproduces the VERIFIED customer-config shape that defeats keys/child
// evidence (the re-sweep's 39-op case): the billing entity-ish type is listed in the subgraph's
// rootNodes WITH its full field list, has ZERO entries in the federation keys metadata, appears
// NOWHERE in childNodes, and the SDL has NO schema block -- Cosmo's config generator classifies
// types named like operation roots into rootNodes BY NAME, so rootNodes membership is not evidence
// of operation-root identity. The remaining per-subgraph evidence is the SDL itself: the type is
// REFERENCED AS AN OUTPUT TYPE by other types' fields (Query.currentPlan,
// PlanCancelPayload.subscription) -- data usage no genuine operation root exhibits.
// typeName selects "Subscription" (the colliding name) or the neutral twin name.
func BillingRootNodesOnlyConfig(typeName string) []plan.DataSource {
	schemaBilling := `
type Query { currentPlan: ` + typeName + ` }
type Mutation { planCancel(id: ID!): PlanCancelPayload }
type PlanCancelPayload { subscription: ` + typeName + ` }
type ` + typeName + ` {
  id: ID!
  availablePlans: [String]
  queueSize: Int
  endsAt: String
  region: String
  holder: Holder
}
type Holder { id: ID! name: String }
`
	const schemaOther = `
type Query { other: String }
`
	billing := newDataSource("billing", schemaBilling, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"currentPlan"}},
			{TypeName: "Mutation", FieldNames: []string{"planCancel"}},
			// The mislabeled pattern, verbatim: the data type's FULL field list under rootNodes.
			{TypeName: typeName, FieldNames: []string{"id", "availablePlans", "queueSize", "endsAt", "region", "holder"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "PlanCancelPayload", FieldNames: []string{"subscription"}},
			{TypeName: "Holder", FieldNames: []string{"id", "name"}},
		},
		// ZERO federation keys -- the type is not an entity in the config.
	})
	other := newDataSource("other", schemaOther, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"other"}},
		},
	})
	return []plan.DataSource{billing, other}
}

// MergedDualRoleConfig reproduces the GROUND-TRUTH customer shape (verified from the failing
// config's stringStorage SDL): ONE composed type named `Subscription` that is SIMULTANEOUSLY the
// subscription operation root AND a data object.
//   - billing (subgraph id-4 analog): its SDL declares `schema { ... subscription: Subscription }`
//     at the top AND uses the same type as data (`PlanCancelPayload.subscription:
//     Subscription`, `Query.currentPlan: Subscription`); its rootNodes list the type with the
//     billing entity field set.
//   - realtime (subgraph id-5 analog): contributes one genuine realtime subscription root field to
//     the same type name.
//
// Composition merges billing entity fields and the realtime root field into one composed
// `Subscription` type. Queries/mutations must traverse the billing fields as DATA (object-tailed,
// unconditionally) while a subscription op anchors on the realtime field -- the dual role.
func MergedDualRoleConfig() []plan.DataSource {
	const schemaBilling = `
schema { query: Query mutation: Mutation subscription: Subscription }
type Query { currentPlan: Subscription }
type Mutation { planCancel(id: ID!): PlanCancelPayload }
type PlanCancelPayload { subscription: Subscription }
type Subscription {
  id: ID!
  availablePlans: [String]
  queueSize: Int
  endsAt: String
  region: String
  holder: Holder
}
type Holder { id: ID! name: String }
`
	const schemaRealtime = `
type Subscription { realtimeTick: Tick }
type Tick { id: ID! value: Float }
`
	billing := newDataSource("billing", schemaBilling, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Query", FieldNames: []string{"currentPlan"}},
			{TypeName: "Mutation", FieldNames: []string{"planCancel"}},
			{TypeName: "Subscription", FieldNames: []string{"id", "availablePlans", "queueSize", "endsAt", "region", "holder"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "PlanCancelPayload", FieldNames: []string{"subscription"}},
			{TypeName: "Holder", FieldNames: []string{"id", "name"}},
		},
	})
	realtime := newDataSource("realtime", schemaRealtime, &plan.DataSourceMetadata{
		RootNodes: plan.TypeFields{
			{TypeName: "Subscription", FieldNames: []string{"realtimeTick"}},
		},
		ChildNodes: plan.TypeFields{
			{TypeName: "Tick", FieldNames: []string{"id", "value"}},
		},
	})
	return []plan.DataSource{billing, realtime}
}
