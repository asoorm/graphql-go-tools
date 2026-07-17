// Package differential is the head-to-head harness pitting the v1 plan.Planner against the planv2
// facade on identical (schema, operation) inputs, under EVERY datasource ordering, comparing with a
// SEMANTIC response-shape oracle rather than plan-text equality.
//
// Why not datasourcetesting.RunWithPermutations? It hardcodes plan.NewPlanner + assert.Equal against
// a single expected plan -- no seam for a second planner and no semantic comparison. The one genuinely
// reusable piece is permutations.Generate(config.DataSources); this package hand-rolls the
// dual-planner loop around it and diffs response SHAPE (client keys, nesting, aliases, __typename
// gates, leaf node kinds) -- NOT the fetch tree or plan text.
//
// SCOPE OF "MATCH" (read before quoting the counts): a MATCH proves response-shape agreement ONLY --
// the client-visible response tree the two plans would render. Fetch documents, fetch COUNTS, and
// fetch dependency structure are deliberately OUT OF SCOPE here (left to later work); a MATCH must
// never be reported as "planv2 == v1".
//
// Divergence policy (v2/docs/planner-v2/DIVERGENCES.md): a difference is EITHER a bug to fix OR a
// documented, adjudicated register entry. Silent divergence is prohibited. Where v1 is KNOWN-WRONG
// (the partial-union family), the harness records the divergence as EXPECTED via the KnownDivergences
// allow-list instead of failing.
package differential

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astprinter"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/testing/permutations"
)

// Divergence is a single semantic difference between the old and new plans' response shapes.
type Divergence struct {
	Path   string // response path at which the shapes first differ (dotted client keys)
	Reason string // human-readable description of the difference
	Order  []int  // the datasource ordering that diverged (permutations.Permutation.Order)
}

func (d *Divergence) String() string {
	return fmt.Sprintf("path=%q reason=%q order=%v", d.Path, d.Reason, d.Order)
}

// KnownDivergences is the documented allow-list: CaseKey(schema, op) -> adjudication reference. A
// divergence whose case key is present here is EXPECTED (v1 is known wrong) rather than a test
// failure. Seeded in init() from the registered fixtures below.
var KnownDivergences = map[string]string{}

func init() {
	// Partial union -- OBSERVED v1 failure mode: a PLANNING-TIME deadlock, not the runtime
	// strip-fragments->null regression. v1 aborts with "failed to obtain planning paths ... has field
	// waiting for dependency: true" (path_builder_visitor) when BOTH mutually-exclusive members
	// (OnlyA from A + OnlyB from B) are selected together, under BOTH datasource orders.
	//
	// CORROBORATED as a genuine v1 limitation, not a harness-config artifact
	// (TestV1PartialUnionCorroboration): with an independently-built config following the
	// graphql_datasource federation-test conventions (NewSchemaConfiguration with federation
	// ServiceSDL, real Factory, same metadata shape), v1 successfully plans the shared-member-only
	// control, the single-exclusive-member control, and the no-union control on the SAME config --
	// it fails exactly and only when both exclusive members are co-selected. Same partial-union
	// family as v1 Cosmo's audit failures (DIVERGENCES.md "v1 calibration"); this fixture surfaces
	// the planning-deadlock manifestation rather than the runtime-null one. planv2's narrowing
	// plans it correctly (exclusive members become response-only nulls). Apollo passes this family,
	// so this is a v1-only divergence -- no divergence-register entry (that register tracks
	// Apollo/audit divergences).
	KnownDivergences[CaseKey(PartialUnionSchema, PartialUnionOp)] =
		"v1 planning deadlock on co-selected exclusive union members ('failed to obtain planning " +
			"paths ... field waiting for dependency'); corroborated genuine v1 limitation " +
			"(TestV1PartialUnionCorroboration); planv2 narrowing handles it correctly; v1-only divergence " +
			"(DIVERGENCES.md 'v1 calibration')"
}

// subgraph bundles the SDL + capability metadata for one federation subgraph, from which the harness
// builds a real graphql_datasource DataSource -- one the v1 planner can actually plan through AND
// planv2's hypergraph.Build can read (NodesAccess + the federated UpstreamSchema).
type subgraph struct {
	name string
	sdl  string
	meta *plan.DataSourceMetadata
}

// CaseKey is a stable content key for a (schema, operation) pair -- the allow-list key. It hashes the
// raw text so KnownDivergences can be seeded from the fixture constants without importing test code.
//
// Exported so the env-gated external-corpus harness (pkg/engine/planv2/external) can look up
// KnownDivergences for externally-supplied (schema, operation) pairs the same way this package's own
// tests do.
func CaseKey(schema, op string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(schema))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(op))
	return fmt.Sprintf("%016x", h.Sum64())
}

// buildConfig builds a plan.Configuration whose DataSources are real graphql_datasource sources both
// planners consume. The supergraph definition is returned separately (the v1 planner and planv2 both
// take it as the Plan argument, not as a Configuration field). Optional field configurations carry
// v1's argument metadata (planv2 reads arguments off the operation and ignores them).
func buildConfig(t *testing.T, subgraphs []subgraph, fields ...plan.FieldConfiguration) plan.Configuration {
	t.Helper()
	ds := make([]plan.DataSource, 0, len(subgraphs))
	for _, sg := range subgraphs {
		ds = append(ds, dataSource(t, sg))
	}
	return plan.Configuration{
		DataSources:                  ds,
		Fields:                       fields,
		DisableResolveFieldPositions: true,
	}
}

func dataSource(t *testing.T, sg subgraph) plan.DataSource {
	t.Helper()
	schemaCfg, err := graphql_datasource.NewSchemaConfiguration(sg.sdl, &graphql_datasource.FederationConfiguration{
		Enabled:    true,
		ServiceSDL: sg.sdl,
	})
	if err != nil {
		t.Fatalf("schema configuration for %s: %v", sg.name, err)
	}
	cfg, err := graphql_datasource.NewConfiguration(graphql_datasource.ConfigurationInput{
		Fetch: &graphql_datasource.FetchConfiguration{URL: "http://" + sg.name},
		// A subscription transport on every subgraph, so subscription fixtures plan through BOTH
		// planners (v1's ConfigureSubscription errors without one). Query/mutation cases never read it.
		Subscription:        &graphql_datasource.SubscriptionConfiguration{URL: "ws://" + sg.name},
		SchemaConfiguration: schemaCfg,
	})
	if err != nil {
		t.Fatalf("configuration for %s: %v", sg.name, err)
	}
	dsCfg, err := plan.NewDataSourceConfiguration[graphql_datasource.Configuration](
		sg.name, &graphql_datasource.Factory[graphql_datasource.Configuration]{}, sg.meta, cfg)
	if err != nil {
		t.Fatalf("datasource configuration for %s: %v", sg.name, err)
	}
	return dsCfg
}

// parseAndNormalize runs the exact pipeline datasourcetesting.RunTest pins (the facade documents the
// same one), so BOTH planners receive byte-identical input: merge the definition with the base
// schema, then normalize the operation with the v1 option set. A fresh op/def pair is returned per
// call -- the planners mutate their inputs, so each planner run needs its own copy.
func parseAndNormalize(t *testing.T, schema, op string) (*ast.Document, *ast.Document, *operationreport.Report) {
	t.Helper()
	return parseAndNormalizeOpts(t, schema, op, false)
}

// parseAndNormalizeOpts is parseAndNormalize with the engine's defer normalization optionally
// enabled (WithEnableDefer -- the D11.13/defer-design input contract: @defer fragments rewritten to
// per-field @__defer_internal stamps). Without it @defer is stripped to a no-op, so a defer fixture
// MUST pass enableDefer or both planners silently plan the flattened operation.
func parseAndNormalizeOpts(t *testing.T, schema, op string, enableDefer bool) (*ast.Document, *ast.Document, *operationreport.Report) {
	t.Helper()
	def := unsafeparser.ParseGraphqlDocumentString(schema)
	if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
		t.Fatalf("merge base schema: %v", err)
	}
	operation := unsafeparser.ParseGraphqlDocumentString(op)
	report := &operationreport.Report{}
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
	if report.HasErrors() {
		t.Fatalf("normalize: %s", report.Error())
	}
	return &operation, &def, report
}

// runBoth plans (schema, op) with BOTH planners under EVERY datasource ordering and returns the first
// response-shape divergence, or nil if every ordering agrees. planv2 MUST plan every case (agreed or
// known-wrong) -- a planv2 error is a hard failure. An OLD-planner error is itself a divergence (v1
// could not produce a plan), classified against the allow-list by the caller rather than crashing the
// harness.
func runBoth(t *testing.T, schema, op string, subgraphs []subgraph, fields ...plan.FieldConfiguration) *Divergence {
	t.Helper()
	return runBothOpts(t, schema, op, subgraphs, false, fields...)
}

// runBothOpts is runBoth with the defer normalization toggle: a defer fixture normalizes both
// planners' inputs with WithEnableDefer and compares via CompareDeferPlans (descriptors, increment
// partition, response shape with defer stamps); everything else is byte-identical to runBoth.
func runBothOpts(t *testing.T, schema, op string, subgraphs []subgraph, enableDefer bool, fields ...plan.FieldConfiguration) *Divergence {
	t.Helper()
	baseCfg := buildConfig(t, subgraphs, fields...)

	for _, perm := range permutations.Generate(baseCfg.DataSources) {
		cfg := baseCfg
		cfg.DataSources = perm.DataSources

		oldPlanner, err := plan.NewPlanner(cfg)
		if err != nil {
			t.Fatalf("old planner (order %v): %v", perm.Order, err)
		}
		newPlanner, err := planv2.NewPlanner(cfg)
		if err != nil {
			t.Fatalf("planv2 planner (order %v): %v", perm.Order, err)
		}

		oldOp, oldDef, oldReport := parseAndNormalizeOpts(t, schema, op, enableDefer)
		oldPlan := oldPlanner.Plan(oldOp, oldDef, "", oldReport)

		newOp, newDef, newReport := parseAndNormalizeOpts(t, schema, op, enableDefer)
		newPlan := newPlanner.Plan(newOp, newDef, "", newReport)

		if newReport.HasErrors() {
			t.Fatalf("planv2 planning failed (order %v): %s", perm.Order, newReport.Error())
		}
		if oldReport.HasErrors() {
			return &Divergence{Path: "", Reason: "old planner failed: " + oldReport.Error(), Order: perm.Order}
		}

		// A defer case compares descriptors + increment partition + stamped response shape; a
		// subscription case compares the trigger semantics AND the per-event response shape; a
		// sync case compares the response shape alone (the established oracle).
		var div *Divergence
		_, oldIsDefer := oldPlan.(*plan.DeferResponsePlan)
		_, newIsDefer := newPlan.(*plan.DeferResponsePlan)
		if _, isSub := newPlan.(*plan.SubscriptionResponsePlan); isSub {
			div = CompareSubscriptionPlans(oldPlan, newPlan)
		} else if oldIsDefer || newIsDefer {
			div = CompareDeferPlans(oldPlan, newPlan)
		} else {
			div = CompareResponseShapes(oldPlan, newPlan)
		}
		if div != nil {
			div.Order = perm.Order
			return div
		}
	}
	return nil
}

// CompareResponseShapes returns nil if old and new plans are semantically response-shape equivalent
// (same client keys, nesting, aliases resolved to the same client key, __typename OnTypeNames gates,
// and -- for leaves -- the same resolve.NodeKind), else a structured Divergence at the first differing
// path. This is the semantic oracle: it compares the RESPONSE tree (what the client sees), NOT
// the fetch tree or plan text (see the package doc's SCOPE OF "MATCH").
//
// Leaf NodeKind is compared because both planners target the same resolve node kinds
// (String/Integer/Float/Boolean/BigInt/Scalar/Enum), making kinds cross-planner comparable -- and a
// kind mismatch is execution-visible: the resolver dispatches its walkers on NodeKind, so a Float
// rendered as String hard-errors at runtime on numeric JSON (exactly the typed-leaves fix this
// oracle must regression-guard). Path/aliases are deliberately NOT compared -- they are
// planner-internal value plumbing; the client key is the semantic identity.
//
// Sibling order-insensitivity is a deliberate judgment call: the GraphQL spec ties response field
// order to the query, and both planners emit Data.Fields in document order, but v1's sibling
// emission order can shift under datasource-order permutations -- comparing order-insensitively
// avoids those false divergences at the cost of not policing order itself.
//
// Lists: resolve.Array wrappers are peeled by classifyValue -- the list nesting depth is compared as
// part of the shape (a list field vs a scalar field is execution-visible) and the element under the
// wrappers is recursed into like any other value. This closed the former Array-opacity blind spot,
// jointly with lower emitting resolve.Array for list-typed fields.
//
// Known blind spots, to close when fixtures exercising them land:
//   - same-key-same-gate sibling dedup: fieldShapes keys siblings by (client key, gate), so two
//     siblings with identical key AND gate collapse to one map entry (last wins) -- a
//     duplicate-emission bug on one side could hide. Normalized operations merge such duplicates
//     today, so no current fixture can trip it.
//
// Enum + __typename leaf-kind parity (now pinned by the enum-and-typename-leaves agreed case):
// planv2 lowers enum leaves to resolve.Enum, root __typename to resolve.StaticString,
// and nested __typename to resolve.String{IsTypeName} -- matching v1 exactly, so the oracle reports
// MATCH on those leaves rather than a leaf-kind divergence.
func CompareResponseShapes(oldPlan, newPlan plan.Plan) *Divergence {
	oldObj, err := responseData(oldPlan)
	if err != nil {
		return &Divergence{Reason: "old plan: " + err.Error()}
	}
	newObj, err := responseData(newPlan)
	if err != nil {
		return &Divergence{Reason: "new plan: " + err.Error()}
	}
	return diffObject("", oldObj, newObj)
}

func responseData(p plan.Plan) (*resolve.Object, error) {
	switch sp := p.(type) {
	case *plan.SynchronousResponsePlan:
		if sp == nil || sp.Response == nil || sp.Response.Data == nil {
			return nil, fmt.Errorf("plan has no response data")
		}
		return sp.Response.Data, nil
	case *plan.DeferResponsePlan:
		// The FULL client selection tree (deferred fields included, DeferField-stamped) -- the I4
		// object of FS-DEF-4: the union of initial + incremental payloads.
		if sp == nil || sp.Response == nil || sp.Response.Response == nil || sp.Response.Response.Data == nil {
			return nil, fmt.Errorf("defer plan has no response data")
		}
		return sp.Response.Response.Data, nil
	case *plan.SubscriptionResponsePlan:
		// The per-event response tree -- the client-visible shape of every pushed event (FS-SUB-6).
		if sp == nil || sp.Response == nil || sp.Response.Response == nil || sp.Response.Response.Data == nil {
			return nil, fmt.Errorf("subscription plan has no response data")
		}
		return sp.Response.Response.Data, nil
	default:
		return nil, fmt.Errorf("not a response plan (got %T)", p)
	}
}

// CompareSubscriptionPlans is the subscription oracle (D11.12/FS-SUB): both plans must be
// SubscriptionResponsePlans; their TRIGGERS must agree semantically -- same upstream url, same
// subscription document modulo print formatting (both are parsed and re-printed canonically), same
// forwarded-variable count -- and their per-event RESPONSE trees must agree under the established
// response-shape oracle. Trigger input BYTES are deliberately not compared: v1 assembles them with
// sjson (body-first key order, compact document print) and planv2 by string composition (url-first,
// spaced document print); both are read key-wise by the resolver, so byte order carries no semantics.
func CompareSubscriptionPlans(oldPlan, newPlan plan.Plan) *Divergence {
	oldSub, ok := oldPlan.(*plan.SubscriptionResponsePlan)
	if !ok || oldSub == nil || oldSub.Response == nil {
		return &Divergence{Reason: fmt.Sprintf("old plan is not a subscription plan (got %T)", oldPlan)}
	}
	newSub, ok := newPlan.(*plan.SubscriptionResponsePlan)
	if !ok || newSub == nil || newSub.Response == nil {
		return &Divergence{Reason: fmt.Sprintf("new plan is not a subscription plan (got %T)", newPlan)}
	}

	oldTrig, err := canonTrigger(&oldSub.Response.Trigger)
	if err != nil {
		return &Divergence{Path: "@trigger", Reason: "old trigger: " + err.Error()}
	}
	newTrig, err := canonTrigger(&newSub.Response.Trigger)
	if err != nil {
		return &Divergence{Path: "@trigger", Reason: "new trigger: " + err.Error()}
	}
	if oldTrig.url != newTrig.url {
		return &Divergence{Path: "@trigger", Reason: fmt.Sprintf("trigger url differs: old=%q new=%q", oldTrig.url, newTrig.url)}
	}
	if oldTrig.query != newTrig.query {
		return &Divergence{Path: "@trigger", Reason: fmt.Sprintf("trigger document differs:\n old: %s\n new: %s", oldTrig.query, newTrig.query)}
	}
	if oldTrig.vars != newTrig.vars {
		return &Divergence{Path: "@trigger", Reason: fmt.Sprintf("trigger variable count differs: old=%d new=%d", oldTrig.vars, newTrig.vars)}
	}

	// @openfed__subscriptionFilter (M4.4): the resolve.SubscriptionFilter both plans build off the
	// root field's SubscriptionFilterCondition must agree structurally -- FieldPath and each value's
	// rendered segments (kind + variable source path + static data). A filter divergence is
	// SECURITY-VISIBLE: it changes which events SkipEvent skips, so a mismatch silently delivers (or
	// silently drops) events on one planner but not the other.
	if of, nf := canonFilter(oldSub.Response.Filter), canonFilter(newSub.Response.Filter); of != nf {
		return &Divergence{Path: "@filter", Reason: fmt.Sprintf("subscription filter differs:\n old: %s\n new: %s", of, nf)}
	}

	return CompareResponseShapes(oldPlan, newPlan)
}

// canonFilter renders a resolve.SubscriptionFilter into a stable string capturing exactly the
// structure SkipEvent evaluates: the And/Or/Not/In shape, each field filter's FieldPath, and each
// value's ordered segments (static data or a plain context-variable path). Renderer identity and
// other non-semantic plumbing are excluded -- only what decides skip-vs-deliver is compared.
func canonFilter(f *resolve.SubscriptionFilter) string {
	if f == nil {
		return "<nil>"
	}
	var b strings.Builder
	switch {
	case f.And != nil:
		b.WriteString("AND[")
		for i := range f.And {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(canonFilter(&f.And[i]))
		}
		b.WriteByte(']')
	case f.Or != nil:
		b.WriteString("OR[")
		for i := range f.Or {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(canonFilter(&f.Or[i]))
		}
		b.WriteByte(']')
	case f.Not != nil:
		b.WriteString("NOT(")
		b.WriteString(canonFilter(f.Not))
		b.WriteByte(')')
	case f.In != nil:
		fmt.Fprintf(&b, "IN(path=%s;vals=[", strings.Join(f.In.FieldPath, "."))
		for i, v := range f.In.Values {
			if i > 0 {
				b.WriteByte(',')
			}
			for _, seg := range v.Segments {
				switch seg.SegmentType {
				case resolve.StaticSegmentType:
					fmt.Fprintf(&b, "static(%s)", string(seg.Data))
				case resolve.VariableSegmentType:
					fmt.Fprintf(&b, "var(%s)", strings.Join(seg.VariableSourcePath, "."))
				default:
					fmt.Fprintf(&b, "seg(%d)", seg.SegmentType)
				}
			}
		}
		b.WriteString("])")
	default:
		b.WriteString("<empty>")
	}
	return b.String()
}

// CompareDeferPlans is the defer oracle (D11.13/FS-DEF): both plans must be DeferResponsePlans;
// their DEFER DESCRIPTORS must agree exactly (same ids, parent chain, labels, mount paths -- the
// client-visible pending/increment bookkeeping); their INCREMENT PARTITIONS must agree -- the set of
// defer ids that own at least one fetch is identical on both sides (FS-DEF-2/FS-DEF-3: every honored
// fragment is served by its own fetch set; fetch COUNTS within a partition stay out of scope per the
// package doc, matching the sync oracle's scope); and their response trees must agree under the
// established shape oracle EXTENDED with the DeferField stamp (a stamp disagreement flips a field
// between initial and incremental delivery -- execution-visible).
func CompareDeferPlans(oldPlan, newPlan plan.Plan) *Divergence {
	oldDp, ok := oldPlan.(*plan.DeferResponsePlan)
	if !ok || oldDp == nil || oldDp.Response == nil || oldDp.Response.Response == nil {
		return &Divergence{Reason: fmt.Sprintf("old plan is not a defer plan (got %T)", oldPlan)}
	}
	newDp, ok := newPlan.(*plan.DeferResponsePlan)
	if !ok || newDp == nil || newDp.Response == nil || newDp.Response.Response == nil {
		return &Divergence{Reason: fmt.Sprintf("new plan is not a defer plan (got %T)", newPlan)}
	}

	// Descriptors: exact agreement (ids, parents, labels, mount paths).
	oldDesc, newDesc := oldDp.Response.DeferDescriptors, newDp.Response.DeferDescriptors
	if len(oldDesc) != len(newDesc) {
		return &Divergence{Path: "@defer", Reason: fmt.Sprintf("descriptor count differs: old=%d new=%d", len(oldDesc), len(newDesc))}
	}
	for id, od := range oldDesc {
		nd, ok := newDesc[id]
		if !ok {
			return &Divergence{Path: "@defer", Reason: fmt.Sprintf("descriptor %d missing in new plan", id)}
		}
		if od.ID != nd.ID || od.ParentID != nd.ParentID || od.Label != nd.Label ||
			strings.Join(od.Path, ".") != strings.Join(nd.Path, ".") {
			return &Divergence{Path: "@defer", Reason: fmt.Sprintf("descriptor %d differs: old=%+v new=%+v", id, od, nd)}
		}
	}

	// Increment partition: the same set of defer ids owns fetches on both sides.
	oldIDs, newIDs := deferFetchIDs(oldDp.Response.Response), deferFetchIDs(newDp.Response.Response)
	if oldIDs != newIDs {
		return &Divergence{Path: "@defer", Reason: fmt.Sprintf("increment partition differs: old fetch scopes %s, new fetch scopes %s", oldIDs, newIDs)}
	}

	return CompareResponseShapes(oldPlan, newPlan)
}

// deferFetchIDs renders the sorted set of defer scopes that own at least one raw fetch ("0,1,2").
func deferFetchIDs(resp *resolve.GraphQLResponse) string {
	seen := map[int]bool{}
	for _, item := range resp.RawFetches {
		if sf, ok := item.Fetch.(*resolve.SingleFetch); ok {
			seen[sf.DeferID] = true
		}
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, ",")
}

// triggerShape is the canonical view of one subscription trigger for the oracle.
type triggerShape struct {
	url   string
	query string // the subscription document, parsed and re-printed canonically
	vars  int    // forwarded trigger variables
}

// varSegment matches the postprocess `$$N$$` variable segments a trigger input carries inside
// body.variables -- not valid JSON, so they are neutralized before parsing the envelope.
var varSegment = regexp.MustCompile(`\$\$\d+\$\$`)

// canonTrigger parses a trigger's input envelope (url + body.query) into its canonical shape. The
// document is re-printed through the AST printer so v1's compact print and planv2's spaced print
// compare equal when semantically identical.
func canonTrigger(t *resolve.GraphQLSubscriptionTrigger) (triggerShape, error) {
	neutral := varSegment.ReplaceAll(t.Input, []byte("null"))
	var envelope struct {
		URL  string `json:"url"`
		Body struct {
			Query string `json:"query"`
		} `json:"body"`
	}
	if err := json.Unmarshal(neutral, &envelope); err != nil {
		return triggerShape{}, fmt.Errorf("input is not a subscription envelope: %v (%s)", err, t.Input)
	}
	if envelope.Body.Query == "" {
		return triggerShape{}, fmt.Errorf("input carries no body.query: %s", t.Input)
	}
	doc := unsafeparser.ParseGraphqlDocumentString(envelope.Body.Query)
	printed, err := astprinter.PrintString(&doc)
	if err != nil {
		return triggerShape{}, fmt.Errorf("trigger document does not re-print: %v (%s)", err, envelope.Body.Query)
	}
	return triggerShape{url: envelope.URL, query: printed, vars: len(t.Variables)}, nil
}

// shapeField is the canonical view of one response field for the oracle: its client key, its sorted
// __typename gate, its list nesting depth (number of resolve.Array wrappers), whether the ELEMENT
// under those wrappers is a composite (object) or a leaf (with its leaf NodeKind), its object
// children if composite, and its defer stamp (0 = not deferred).
type shapeField struct {
	key       string
	gate      string
	listDepth int              // number of resolve.Array wrappers (0 = not a list)
	isObj     bool             // classifies the element under the Array wrappers
	leafKind  resolve.NodeKind // meaningful only when !isObj
	obj       *resolve.Object  // element object when isObj
	deferID   int              // DeferField stamp (D11.13); execution-visible: decides initial-vs-increment delivery
}

// canonKey identifies a response field by client key + gate -- the identity the oracle matches
// siblings on. Leaf NodeKind is compared as an ATTRIBUTE of the matched pair (diffObject), not
// folded into the identity: a kind mismatch then reports as "leaf kind differs" at the field's path
// rather than as an opaque missing/extra-field pair.
func (f shapeField) canonKey() string { return f.key + f.gate }

// fieldShapes canonicalizes an object's sibling fields by (client key, gate). BLIND SPOT (see
// CompareResponseShapes godoc): two siblings with identical key AND gate collapse to one entry.
func fieldShapes(obj *resolve.Object) map[string]shapeField {
	out := map[string]shapeField{}
	if obj == nil {
		return out
	}
	for _, f := range obj.Fields {
		sf := shapeField{key: string(f.Name), gate: gateString(f.OnTypeNames)}
		if f.Defer != nil {
			sf.deferID = f.Defer.DeferID
		}
		// Unwrap any resolve.Array nesting: the list depth is part of the shape identity (a list
		// field vs a scalar field is execution-visible), and the ELEMENT under the wrappers is
		// what determines composite-vs-leaf and recursion. This closes the former Array-opacity
		// blind spot jointly with lower's list-lowering fix.
		sf.listDepth, sf.isObj, sf.leafKind, sf.obj = classifyValue(f.Value)
		out[sf.canonKey()] = sf
	}
	return out
}

// groupByRespKey buckets canonicalized sibling fields by their response key (ignoring the gate), so
// the gate-subsumption check can see every gate variant of one response key at a position.
func groupByRespKey(shapes map[string]shapeField) map[string][]shapeField {
	out := map[string][]shapeField{}
	for _, f := range shapes {
		out[f.key] = append(out[f.key], f)
	}
	return out
}

// gateMembers splits a gate string ("@A|B" -> {A,B}; "" -> ungated) into its concrete type conditions.
func gateMembers(gate string) (ungated bool, members map[string]bool) {
	if gate == "" {
		return true, nil
	}
	members = map[string]bool{}
	for _, m := range strings.Split(strings.TrimPrefix(gate, "@"), "|") {
		if m != "" {
			members[m] = true
		}
	}
	return false, members
}

// leafGateCovered reports whether a member-gated leaf `of` (a response key carrying an `@M...` gate) is
// covered by the other plan's selections `cands` at the SAME response key: some subset of cands with
// the identical leaf shape (list depth + NodeKind) either selects that key ungated (covers every
// concrete type) or, taken together, covers every type condition in of's gate. An UNGATED `of` (its
// gate covers all types) is never covered by a strictly-gated candidate set -- we cannot know the full
// member set, so that stays a reported divergence (conservative). Same-shape is required so a genuine
// list-ness / leaf-kind change at that key is still caught by the downstream shape checks rather than
// masked here.
func leafGateCovered(of shapeField, cands []shapeField) bool {
	ofUngated, ofMembers := gateMembers(of.gate)
	if ofUngated {
		return false
	}
	covered := map[string]bool{}
	for _, c := range cands {
		if c.isObj || c.listDepth != of.listDepth || c.leafKind != of.leafKind {
			continue
		}
		cUngated, cMembers := gateMembers(c.gate)
		if cUngated {
			return true
		}
		for m := range cMembers {
			covered[m] = true
		}
	}
	for m := range ofMembers {
		if !covered[m] {
			return false
		}
	}
	return true
}

// classifyValue peels resolve.Array wrappers off a response node, returning the list nesting depth
// plus the classification of the element underneath: whether it is a composite object (with its
// child object) or a leaf (with its resolve.NodeKind). A nil value classifies as a leaf of kind 0.
func classifyValue(v resolve.Node) (listDepth int, isObj bool, leafKind resolve.NodeKind, obj *resolve.Object) {
	for {
		arr, ok := v.(*resolve.Array)
		if !ok {
			break
		}
		listDepth++
		v = arr.Item
	}
	if child, ok := v.(*resolve.Object); ok {
		return listDepth, true, 0, child
	}
	if v != nil {
		return listDepth, false, v.NodeKind(), nil
	}
	return listDepth, false, 0, nil
}

// diffObject compares two response objects field-by-field (order-insensitive) and returns the first
// divergence, or nil if the shapes are equivalent.
func diffObject(path string, oldObj, newObj *resolve.Object) *Divergence {
	oldShapes := fieldShapes(oldObj)
	newShapes := fieldShapes(newObj)

	// Missing / extra client keys (or gate mismatch, since the gate is part of the identity).
	//
	// LEAF gate-subsumption: a member-gated LEAF (a response key with an `@M` gate) present in one plan
	// but not the other is NOT a response divergence when the other plan selects the same response key,
	// with the same leaf shape (list depth + NodeKind), under a gate that COVERS M -- either ungated
	// (covers every concrete type) or a union of member-gated selections whose type conditions include
	// M. A concrete runtime object of type M then carries an identical leaf in both responses; the
	// extra copy the other planner emits is a redundant duplicate at one response key (v1 repeats `id`
	// inside `... on Invoice` on top of an interface-level `id`; planv2 dedups it). The client JSON is
	// byte-identical, so this is not a shape divergence. Object fields keep exact (key+gate) identity --
	// only leaves subsume.
	oldByKey := groupByRespKey(oldShapes)
	newByKey := groupByRespKey(newShapes)
	for k, of := range oldShapes {
		if _, ok := newShapes[k]; !ok {
			if !of.isObj && leafGateCovered(of, newByKey[of.key]) {
				continue
			}
			return &Divergence{Path: join(path, of.key), Reason: "field present in old plan, absent in new: " + k}
		}
	}
	for k, nf := range newShapes {
		if _, ok := oldShapes[k]; !ok {
			if !nf.isObj && leafGateCovered(nf, oldByKey[nf.key]) {
				continue
			}
			return &Divergence{Path: join(path, nf.key), Reason: "field present in new plan, absent in old: " + k}
		}
	}

	// Composite-vs-leaf mismatch, leaf-kind mismatch, and recursion, in deterministic key order.
	keys := make([]string, 0, len(oldShapes))
	for k := range oldShapes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nf, ok := newShapes[k]
		if !ok {
			// (key+gate) present in old only but leaf-gate-subsumed above -- no exact pair to shape-check.
			continue
		}
		of := oldShapes[k]
		// Defer stamp (D11.13) is execution-visible: it decides whether the field rides the
		// initial response or an increment, so a stamp disagreement is a real divergence even
		// though the tree SHAPE matches. Zero on both sides for every undeferred operation.
		if of.deferID != nf.deferID {
			return &Divergence{
				Path: join(path, of.key),
				Reason: fmt.Sprintf("defer stamp differs: old=%d new=%d (%s)",
					of.deferID, nf.deferID, k),
			}
		}
		if of.listDepth != nf.listDepth {
			return &Divergence{
				Path: join(path, of.key),
				Reason: fmt.Sprintf("list nesting differs: old=%d new=%d (%s)",
					of.listDepth, nf.listDepth, k),
			}
		}
		if of.isObj != nf.isObj {
			return &Divergence{Path: join(path, of.key), Reason: "field is composite in one plan, leaf in the other: " + k}
		}
		if of.isObj {
			if div := diffObject(join(path, of.key), of.obj, nf.obj); div != nil {
				return div
			}
			continue
		}
		// Leaf NodeKind is execution-visible (walker dispatch): a Float-vs-String disagreement is a
		// real divergence even though the tree SHAPE matches -- the false-MATCH this rules out.
		if of.leafKind != nf.leafKind {
			return &Divergence{
				Path: join(path, of.key),
				Reason: fmt.Sprintf("leaf node kind differs: old=%v new=%v (%s)",
					of.leafKind, nf.leafKind, k),
			}
		}
	}
	return nil
}

// --- registered fixtures (allow-listed: v1 known-wrong) --------------------------------------
//
// These live in non-test code because init() seeds KnownDivergences from them, keeping the allow-list
// key and the fixture text guaranteed-consistent. The test file drives them through runBoth.

// PartialUnionSchema is the composed supergraph for the partial-union case: a partial union whose
// members split across two subgraphs (Common shared; OnlyA in A, OnlyB in B).
const PartialUnionSchema = `
schema { query: Query }
type Query { wrapper: Wrapper }
type Wrapper { id: ID! action: Action }
union Action = Common | OnlyA | OnlyB
type Common { c: String }
type OnlyA { a: String }
type OnlyB { b: String }
`

// PartialUnionOp selects every member under the union, forcing the narrowing decision.
const PartialUnionOp = `{ wrapper { action { __typename ... on Common { c } ... on OnlyA { a } ... on OnlyB { b } } } }`

// partialUnionSubgraphs are the two real federation subgraphs behind the partial-union case.
func partialUnionSubgraphs() []subgraph {
	return []subgraph{
		{
			name: "A",
			sdl: `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyA
type Common { c: String }
type OnlyA { a: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"wrapper"}},
					{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Common", FieldNames: []string{"c"}},
					{TypeName: "OnlyA", FieldNames: []string{"a"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Wrapper", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "B",
			sdl: `
type Query { wrapper: Wrapper }
type Wrapper @key(fields: "id") { id: ID! action: Action }
union Action = Common | OnlyB
type Common { c: String }
type OnlyB { b: String }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"wrapper"}},
					{TypeName: "Wrapper", FieldNames: []string{"id", "action"}},
				},
				ChildNodes: plan.TypeFields{
					{TypeName: "Common", FieldNames: []string{"c"}},
					{TypeName: "OnlyB", FieldNames: []string{"b"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "Wrapper", SelectionSet: "id"}},
				},
			},
		},
	}
}

func gateString(on [][]byte) string {
	if len(on) == 0 {
		return ""
	}
	names := make([]string, len(on))
	for i, n := range on {
		names[i] = string(n)
	}
	sort.Strings(names)
	return "@" + strings.Join(names, "|")
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
