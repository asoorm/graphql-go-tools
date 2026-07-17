package differential

// exec_defer_test.go is the EXECUTED-TRUTH differential for @defer (D11.13): both planners' defer
// plans are run through the untouched production pipeline -- postprocess (defer partition + tree
// build) and resolve.ResolveGraphQLDeferResponse (incremental delivery) -- against SEMANTIC fake
// subgraphs (tiny servers that actually resolve whatever document they receive from an in-memory
// data model, instead of matching request bytes), and the streamed client frames must be
// JSON-equal frame-by-frame. This is the oracle the byte-keyed v1 engine mocks cannot provide:
// planv2 prints its subgraph documents differently from v1 (an adjudicated print divergence), so
// under the PLANV2=1 seam every v1 defer engine test fails at the mock with "received unexpected
// body" regardless of plan quality; here the subgraph answers BOTH planners' documents and the
// comparison happens where the spec binds it -- the client-visible incremental stream (FS-DEF-2/3/4).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// --- semantic fake subgraph -------------------------------------------------------------------

// sgData is one fake subgraph's in-memory data model. Root values and entity values are JSON-ish
// trees (map[string]any / []any / scalars); every object node carries its own "__typename" value so
// the resolver answers __typename selections without schema bookkeeping.
type sgData struct {
	root     map[string]any                       // root field name -> value tree
	entities map[string]map[string]map[string]any // typename -> id -> entity fields
}

// semanticSubgraph serves a GraphQL-over-HTTP endpoint that RESOLVES the incoming document against
// the data model: plain root queries walk the selection set over root values; `_entities` queries
// look each representation up by (__typename, id) and resolve the matching inline fragment. Field
// aliases are honored (the response is keyed by alias-or-name), so it answers v1's internal-alias
// documents (`__internal_typename: __typename`) and planv2's plain ones alike.
func semanticSubgraph(t *testing.T, data sgData) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		doc := unsafeparser.ParseGraphqlDocumentString(req.Query)
		out := map[string]any{}
		for ref := range doc.OperationDefinitions {
			resolveSelectionSet(&doc, doc.OperationDefinitions[ref].SelectionSet, rootValue(data, req.Variables), out)
			break
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
	}))
}

// rootValue produces the object the operation's top-level selection resolves against: the root
// data, with `_entities` materialized from the request's representations when present.
func rootValue(data sgData, variables json.RawMessage) map[string]any {
	val := map[string]any{}
	for k, v := range data.root {
		val[k] = v
	}
	var vars struct {
		Representations []map[string]any `json:"representations"`
	}
	_ = json.Unmarshal(variables, &vars)
	if vars.Representations != nil {
		ents := make([]any, 0, len(vars.Representations))
		for _, rep := range vars.Representations {
			tn, _ := rep["__typename"].(string)
			id := fmt.Sprintf("%v", rep["id"])
			ent := map[string]any{"__typename": tn}
			for k, v := range data.entities[tn][id] {
				ent[k] = v
			}
			ents = append(ents, ent)
		}
		val["_entities"] = ents
	}
	return val
}

// resolveSelectionSet walks one selection set against a value tree, writing alias-keyed results.
// Inline fragments apply when the value's __typename matches the type condition (or always, when
// the value carries none -- the shape our fixtures never hit).
func resolveSelectionSet(doc *ast.Document, setRef int, value map[string]any, out map[string]any) {
	if setRef < 0 || setRef >= len(doc.SelectionSets) {
		return
	}
	for _, selRef := range doc.SelectionSets[setRef].SelectionRefs {
		sel := doc.Selections[selRef]
		switch sel.Kind {
		case ast.SelectionKindField:
			fRef := sel.Ref
			name := doc.FieldNameString(fRef)
			key := doc.FieldAliasOrNameString(fRef)
			raw, okVal := value[name]
			if name == "__typename" {
				raw, okVal = value["__typename"], true
			}
			if !okVal {
				out[key] = nil
				continue
			}
			if !doc.Fields[fRef].HasSelections {
				out[key] = raw
				continue
			}
			out[key] = resolveComposite(doc, doc.Fields[fRef].SelectionSet, raw)
		case ast.SelectionKindInlineFragment:
			cond := doc.InlineFragmentTypeConditionNameString(sel.Ref)
			if tn, ok := value["__typename"].(string); ok && cond != "" && tn != cond {
				continue
			}
			if doc.InlineFragments[sel.Ref].HasSelections {
				resolveSelectionSet(doc, doc.InlineFragments[sel.Ref].SelectionSet, value, out)
			}
		}
	}
}

// resolveComposite resolves a sub-selection against an object or a list of objects.
func resolveComposite(doc *ast.Document, setRef int, raw any) any {
	switch v := raw.(type) {
	case map[string]any:
		sub := map[string]any{}
		resolveSelectionSet(doc, setRef, v, sub)
		return sub
	case []any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, resolveComposite(doc, setRef, item))
		}
		return items
	default:
		return raw
	}
}

// --- executed-truth fixtures ------------------------------------------------------------------

// execDeferCase is one executed-truth scenario: the composed schema + operation, and per-subgraph
// SDL/metadata/data. wantFrames is the frame count the stream must produce (vacuity guard: a
// flattened plan would produce a single synchronous payload and no increments).
type execDeferCase struct {
	name       string
	schema     string
	op         string
	subgraphs  []subgraph // sdl + metadata; URL filled from the semantic server
	data       map[string]sgData
	wantFrames int
}

func execDeferCases() []execDeferCase {
	const singleUserSchema = `
schema { query: Query }
type Query { user: User }
type User { id: ID! name: String! title: String! description: String! }
`
	singleUser := []subgraph{{
		name: "first",
		sdl: `
type Query { user: User }
type User { id: ID! name: String! title: String! description: String! }
`,
		meta: &plan.DataSourceMetadata{
			RootNodes:  plan.TypeFields{{TypeName: "Query", FieldNames: []string{"user"}}},
			ChildNodes: plan.TypeFields{{TypeName: "User", FieldNames: []string{"id", "name", "title", "description"}}},
		},
	}}
	singleUserData := map[string]sgData{
		"first": {root: map[string]any{
			"user": map[string]any{"__typename": "User", "id": "u1", "name": "Black", "title": "Sabbat", "description": "Paranoid"},
		}},
	}

	const entityUserSchema = `
schema { query: Query }
type Query { user: User }
type User { id: ID! title: String! firstName: String! lastName: String! }
`
	entityUser := []subgraph{
		{
			name: "first",
			sdl: `
type Query { user: User }
type User @key(fields: "id") { id: ID! title: String! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{
					{TypeName: "Query", FieldNames: []string{"user"}},
					{TypeName: "User", FieldNames: []string{"id", "title"}},
				},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
		{
			name: "second",
			sdl: `
type User @key(fields: "id") { id: ID! firstName: String! lastName: String! }
`,
			meta: &plan.DataSourceMetadata{
				RootNodes: plan.TypeFields{{TypeName: "User", FieldNames: []string{"id", "firstName", "lastName"}}},
				FederationMetaData: plan.FederationMetaData{
					Keys: plan.FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
				},
			},
		},
	}
	entityUserData := map[string]sgData{
		"first": {root: map[string]any{
			"user": map[string]any{"__typename": "User", "id": "u1", "title": "Sabbat"},
		}},
		"second": {entities: map[string]map[string]map[string]any{
			"User": {"u1": {"id": "u1", "firstName": "Ozzy", "lastName": "Osbourne"}},
		}},
	}

	return []execDeferCase{
		{
			name:       "root-rewalk",
			schema:     singleUserSchema,
			op:         `query User { user { name ... @defer { title } } }`,
			subgraphs:  singleUser,
			data:       singleUserData,
			wantFrames: 2, // initial + one increment
		},
		{
			name:       "nested",
			schema:     singleUserSchema,
			op:         `query User { user { name ... @defer { title ... @defer { description } } } }`,
			subgraphs:  singleUser,
			data:       singleUserData,
			wantFrames: 3, // initial + parent increment + child increment
		},
		{
			name:       "all-deferred-placeholder",
			schema:     singleUserSchema,
			op:         `query User { user { ... @defer { title } } }`,
			subgraphs:  singleUser,
			data:       singleUserData,
			wantFrames: 2, // initial ({"user":{}}) + one increment
		},
		{
			name:       "entity-jump",
			schema:     entityUserSchema,
			op:         `query User { user { title firstName ... @defer { lastName } } }`,
			subgraphs:  entityUser,
			data:       entityUserData,
			wantFrames: 2, // initial (title + firstName) + one increment (lastName)
		},
	}
}

// --- harness ------------------------------------------------------------------------------------

// frameWriter is the DeferResponseWriter capturing one payload per Flush.
type frameWriter struct {
	mu       sync.Mutex
	buf      []byte
	payloads []string
	complete bool
}

func (w *frameWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *frameWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.payloads = append(w.payloads, string(w.buf))
	w.buf = w.buf[:0]
	return nil
}

func (w *frameWriter) Complete() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.complete = true
}

// executableConfig builds a plan.Configuration whose datasources point at the live semantic
// servers, with a REAL factory (http client + subscription client) so v1's planner emits
// executable fetch sources; planv2 derives its own transport table from the same fetch configs.
func executableConfig(t *testing.T, ctx context.Context, subgraphs []subgraph, urls map[string]string) plan.Configuration {
	t.Helper()
	subClient := graphql_datasource.NewGraphQLSubscriptionClient(ctx)
	factory, err := graphql_datasource.NewFactory(ctx, http.DefaultClient, subClient)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	ds := make([]plan.DataSource, 0, len(subgraphs))
	for _, sg := range subgraphs {
		schemaCfg, err := graphql_datasource.NewSchemaConfiguration(sg.sdl, &graphql_datasource.FederationConfiguration{
			Enabled:    true,
			ServiceSDL: sg.sdl,
		})
		if err != nil {
			t.Fatalf("schema configuration for %s: %v", sg.name, err)
		}
		cfg, err := graphql_datasource.NewConfiguration(graphql_datasource.ConfigurationInput{
			Fetch:               &graphql_datasource.FetchConfiguration{URL: urls[sg.name], Method: http.MethodPost},
			SchemaConfiguration: schemaCfg,
		})
		if err != nil {
			t.Fatalf("configuration for %s: %v", sg.name, err)
		}
		dsCfg, err := plan.NewDataSourceConfiguration[graphql_datasource.Configuration](sg.name, factory, sg.meta, cfg)
		if err != nil {
			t.Fatalf("datasource configuration for %s: %v", sg.name, err)
		}
		ds = append(ds, dsCfg)
	}
	return plan.Configuration{DataSources: ds, DisableResolveFieldPositions: true}
}

// executeDeferPlan postprocesses and resolves one defer plan, returning the streamed frames.
func executeDeferPlan(t *testing.T, ctx context.Context, p plan.Plan, label string) []string {
	t.Helper()
	dp, ok := p.(*plan.DeferResponsePlan)
	if !ok {
		t.Fatalf("%s: want *plan.DeferResponsePlan, got %T", label, p)
	}
	postprocess.NewProcessor().Process(dp)

	resolver := resolve.New(ctx, resolve.ResolverOptions{
		MaxConcurrency:               16,
		PropagateSubgraphErrors:      true,
		PropagateSubgraphStatusCodes: true,
	})
	w := &frameWriter{}
	if _, err := resolver.ResolveGraphQLDeferResponse(resolve.NewContext(context.Background()), dp.Response, w); err != nil {
		t.Fatalf("%s: resolve: %v", label, err)
	}
	if !w.complete {
		t.Fatalf("%s: stream did not complete", label)
	}
	return w.payloads
}

// TestDeferExecutedTruth_FramesMatchV1 is the D11.13 executed-truth gate: for every scenario, the
// planv2-planned execution must stream the SAME client frames (JSON-equal, frame by frame) as the
// v1-planned execution of the same operation against the same semantic subgraphs -- initial payload,
// pending announcements, every incremental payload, completed markers, hasNext sequencing.
func TestDeferExecutedTruth_FramesMatchV1(t *testing.T) {
	for _, c := range execDeferCases() {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			urls := map[string]string{}
			for _, sg := range c.subgraphs {
				srv := semanticSubgraph(t, c.data[sg.name])
				defer srv.Close()
				urls[sg.name] = srv.URL
			}
			cfg := executableConfig(t, ctx, c.subgraphs, urls)

			oldPlanner, err := plan.NewPlanner(cfg)
			if err != nil {
				t.Fatalf("v1 planner: %v", err)
			}
			oldOp, oldDef, oldReport := parseAndNormalizeOpts(t, c.schema, c.op, true)
			oldPlan := oldPlanner.Plan(oldOp, oldDef, "", oldReport)
			if oldReport.HasErrors() {
				t.Fatalf("v1 planning failed: %s", oldReport.Error())
			}
			oldFrames := executeDeferPlan(t, ctx, oldPlan, "v1")

			newPlanner, err := planv2.NewPlanner(cfg)
			if err != nil {
				t.Fatalf("planv2 planner: %v", err)
			}
			newOp, newDef, newReport := parseAndNormalizeOpts(t, c.schema, c.op, true)
			newPlan := newPlanner.Plan(newOp, newDef, "", newReport)
			if newReport.HasErrors() {
				t.Fatalf("planv2 planning failed: %s", newReport.Error())
			}
			newFrames := executeDeferPlan(t, ctx, newPlan, "planv2")

			if len(oldFrames) != c.wantFrames {
				t.Fatalf("v1 produced %d frames, fixture expects %d: %v", len(oldFrames), c.wantFrames, oldFrames)
			}
			if len(newFrames) != len(oldFrames) {
				t.Fatalf("frame count differs: v1=%d planv2=%d\n v1:     %v\n planv2: %v",
					len(oldFrames), len(newFrames), oldFrames, newFrames)
			}
			for i := range oldFrames {
				var oldJSON, newJSON any
				if err := json.Unmarshal([]byte(oldFrames[i]), &oldJSON); err != nil {
					t.Fatalf("v1 frame %d is not JSON: %v (%s)", i, err, oldFrames[i])
				}
				if err := json.Unmarshal([]byte(newFrames[i]), &newJSON); err != nil {
					t.Fatalf("planv2 frame %d is not JSON: %v (%s)", i, err, newFrames[i])
				}
				if !reflect.DeepEqual(oldJSON, newJSON) {
					t.Fatalf("frame %d differs:\n v1:     %s\n planv2: %s", i, oldFrames[i], newFrames[i])
				}
			}
			t.Logf("frames match v1 (%d frames): %v", len(newFrames), newFrames)
		})
	}
}
