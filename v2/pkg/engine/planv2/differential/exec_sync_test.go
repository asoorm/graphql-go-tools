package differential

// exec_sync_test.go is the EXECUTED-TRUTH harness for synchronous (query/mutation) plans -- the
// in-repo reproduction loop for the runtime-encoding defect classes the real Guild
// federation-gateway-audit exposed (m3-real-audit-report). Both planners' plans run through the
// untouched production pipeline -- postprocess + resolve.ResolveGraphQLResponse -- against SEMANTIC
// fake subgraphs (they resolve whatever document arrives from an in-memory data model, so they
// answer v1's and planv2's differently-printed documents alike), and the client-visible JSON is
// compared three ways: planv2 == expected, v1 == expected (fixture sanity: the fixture reproduces
// a case v1 gets RIGHT), and planv2 == v1 on error PRESENCE (the audit's own error contract --
// message text differs legitimately between engines).
//
// The semantic model here is deliberately richer than exec_defer_test.go's: interface/union
// membership (inline fragments match through `implements`), argument-aware stateful root resolvers
// (`calls`, for the mutation serialization case), per-root-field injected errors (`fieldErrors`,
// for the expected-subgraph-error class), and representation-echo entities -- enough to reproduce
// the audit's interface-object, @requires-gather, aliased-__typename, and serial-mutation shapes.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/wundergraph/astjson"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// --- semantic subgraph (sync flavor) ------------------------------------------------------------

// syncData is one fake subgraph's data model.
type syncData struct {
	root     map[string]any                       // root field name -> value tree
	entities map[string]map[string]map[string]any // representation __typename -> id -> entity fields
	// implements maps a concrete __typename to the abstract type names it satisfies, so an inline
	// fragment `... on I` applies to a value whose __typename is a member/implementer. nil = exact
	// matches only.
	implements map[string][]string
	// calls maps a ROOT field name to a stateful resolver taking the field's (JSON-decoded)
	// arguments -- the mutation-serialization fixtures observe CALL ORDER through shared state.
	calls map[string]func(args map[string]any) any
	// entityCalls maps a representation __typename to a reference resolver taking the FULL
	// representation object -- the @requires fixtures compute their answer from the gathered input,
	// so a representation that arrives without the @requires value produces the observable null.
	// Returning nil yields a null entity item. Takes precedence over `entities` for its typename.
	entityCalls map[string]func(rep map[string]any) map[string]any
	// fieldErrors maps a ROOT field name to an error message: selecting the field yields a null
	// value plus a GraphQL error entry -- the expected-subgraph-error class.
	fieldErrors map[string]string
}

// syncSubgraph serves a GraphQL-over-HTTP endpoint resolving incoming documents against the model.
func syncSubgraph(t *testing.T, data syncData) *httptest.Server {
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
		res := &syncResolver{doc: &doc, data: data, variables: req.Variables}
		out := map[string]any{}
		for ref := range doc.OperationDefinitions {
			res.selectionSet(doc.OperationDefinitions[ref].SelectionSet, res.rootValue(), out, true)
			break
		}
		resp := map[string]any{"data": out}
		if len(res.errors) > 0 {
			resp["errors"] = res.errors
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

type syncResolver struct {
	doc       *ast.Document
	data      syncData
	variables json.RawMessage
	errors    []map[string]any
}

// rootValue is the object the top-level selection resolves against: root data plus `_entities`
// materialized from the representations. An unknown (typename, id) yields a NULL entity item --
// what a real subgraph's __resolveReference returns for a miss.
func (r *syncResolver) rootValue() map[string]any {
	val := map[string]any{}
	for k, v := range r.data.root {
		val[k] = v
	}
	var vars struct {
		Representations []map[string]any `json:"representations"`
	}
	_ = json.Unmarshal(r.variables, &vars)
	if vars.Representations != nil {
		ents := make([]any, 0, len(vars.Representations))
		for _, rep := range vars.Representations {
			tn, _ := rep["__typename"].(string)
			id := fmt.Sprintf("%v", rep["id"])
			var stored map[string]any
			if fn, ok := r.data.entityCalls[tn]; ok {
				stored = fn(rep)
			} else {
				stored = r.data.entities[tn][id]
			}
			if stored == nil {
				ents = append(ents, nil)
				continue
			}
			ent := map[string]any{"__typename": tn}
			for k, v := range stored {
				ent[k] = v // a stored "__typename" overrides the representation echo
			}
			// representation values (@requires inputs) are visible to the entity's field resolvers;
			// merge them in under `_rep_<name>` so fixtures can assert a gather value ARRIVED.
			for k, v := range rep {
				if k == "__typename" || k == "id" {
					continue
				}
				if _, shadowed := ent["_rep_"+k]; !shadowed {
					ent["_rep_"+k] = v
				}
			}
			ents = append(ents, ent)
		}
		val["_entities"] = ents
	}
	return val
}

// args decodes a field's arguments into a JSON-ish map, resolving variable references against the
// request variables.
func (r *syncResolver) args(fRef int) map[string]any {
	refs := r.doc.Fields[fRef].Arguments.Refs
	if len(refs) == 0 {
		return nil
	}
	var reqVars map[string]json.RawMessage
	_ = json.Unmarshal(r.variables, &reqVars)
	out := map[string]any{}
	for _, aRef := range refs {
		name := r.doc.ArgumentNameString(aRef)
		val := r.doc.Arguments[aRef].Value
		if val.Kind == ast.ValueKindVariable {
			vn := r.doc.VariableValueNameString(val.Ref)
			var decoded any
			_ = json.Unmarshal(reqVars[vn], &decoded)
			out[name] = decoded
			continue
		}
		b, err := r.doc.ValueToJSON(val)
		if err != nil {
			continue
		}
		var decoded any
		_ = json.Unmarshal(b, &decoded)
		out[name] = decoded
	}
	return out
}

// selectionSet resolves one selection set against a value, writing alias-keyed results.
func (r *syncResolver) selectionSet(setRef int, value map[string]any, out map[string]any, isRoot bool) {
	if setRef < 0 || setRef >= len(r.doc.SelectionSets) {
		return
	}
	for _, selRef := range r.doc.SelectionSets[setRef].SelectionRefs {
		sel := r.doc.Selections[selRef]
		switch sel.Kind {
		case ast.SelectionKindField:
			fRef := sel.Ref
			name := r.doc.FieldNameString(fRef)
			key := r.doc.FieldAliasOrNameString(fRef)
			if isRoot {
				if msg, boom := r.data.fieldErrors[name]; boom {
					out[key] = nil
					r.errors = append(r.errors, map[string]any{"message": msg, "path": []any{key}})
					continue
				}
				if call, ok := r.data.calls[name]; ok {
					raw := call(r.args(fRef))
					if !r.doc.Fields[fRef].HasSelections {
						out[key] = raw
						continue
					}
					out[key] = r.composite(r.doc.Fields[fRef].SelectionSet, raw)
					continue
				}
			}
			raw, okVal := value[name]
			if name == "__typename" {
				raw, okVal = value["__typename"], true
			}
			if !okVal {
				out[key] = nil
				continue
			}
			if !r.doc.Fields[fRef].HasSelections {
				out[key] = raw
				continue
			}
			out[key] = r.composite(r.doc.Fields[fRef].SelectionSet, raw)
		case ast.SelectionKindInlineFragment:
			cond := r.doc.InlineFragmentTypeConditionNameString(sel.Ref)
			if tn, ok := value["__typename"].(string); ok && cond != "" && tn != cond &&
				!slices.Contains(r.data.implements[tn], cond) {
				continue
			}
			if r.doc.InlineFragments[sel.Ref].HasSelections {
				r.selectionSet(r.doc.InlineFragments[sel.Ref].SelectionSet, value, out, isRoot)
			}
		}
	}
}

func (r *syncResolver) composite(setRef int, raw any) any {
	switch v := raw.(type) {
	case map[string]any:
		sub := map[string]any{}
		r.selectionSet(setRef, v, sub, false)
		return sub
	case []any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, r.composite(setRef, item))
		}
		return items
	default:
		return raw
	}
}

// --- harness ------------------------------------------------------------------------------------

// execSyncCase is one executed-truth scenario. `expected` is the exact client response JSON the
// audit expects -- when the case expects errors, `errors` compares by PRESENCE only (the audit's own
// contract; engine error text is not portable), and `data` byte-exactly.
type execSyncCase struct {
	name      string
	schema    string
	op        string
	subgraphs []subgraph
	data      map[string]syncData
	fields    []plan.FieldConfiguration // v1 argument metadata (planv2 reads args off the operation)
	expected  string
	// skipV1 skips the v1-oracle sanity leg for fixtures v1 itself gets wrong (documented per case).
	skipV1 bool
	// reset runs between the v1-sanity leg and the planv2 leg -- STATEFUL fixtures (consume-on-
	// reference entities, mutation counters) must restore their initial state so each leg observes
	// the same world.
	reset func()
}

// executeSyncPlan postprocesses and resolves one synchronous plan, returning the client JSON.
func executeSyncPlan(t *testing.T, ctx context.Context, p plan.Plan, operation *ast.Document, label string) string {
	t.Helper()
	sp, ok := p.(*plan.SynchronousResponsePlan)
	if !ok {
		t.Fatalf("%s: want *plan.SynchronousResponsePlan, got %T", label, p)
	}
	postprocess.NewProcessor().Process(sp)

	resolver := resolve.New(ctx, resolve.ResolverOptions{
		MaxConcurrency:               16,
		PropagateSubgraphErrors:      true,
		PropagateSubgraphStatusCodes: true,
	})
	rctx := resolve.NewContext(context.Background())
	if vars := operation.Input.Variables; len(vars) > 0 && !bytes.Equal(vars, []byte("null")) {
		v, err := astjson.ParseBytes(vars)
		if err != nil {
			t.Fatalf("%s: parse extracted variables %q: %v", label, vars, err)
		}
		rctx.Variables = v
	}
	buf := &bytes.Buffer{}
	if _, err := resolver.ResolveGraphQLResponse(rctx, sp.Response, nil, buf); err != nil {
		t.Fatalf("%s: resolve: %v", label, err)
	}
	return buf.String()
}

// dataAndErrors splits a client response JSON into its data tree and error PRESENCE.
func dataAndErrors(t *testing.T, label, raw string) (any, bool) {
	t.Helper()
	var resp map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("%s is not JSON: %v (%s)", label, err, raw)
	}
	var data any
	if d, ok := resp["data"]; ok {
		if err := json.Unmarshal(d, &data); err != nil {
			t.Fatalf("%s data is not JSON: %v", label, err)
		}
	}
	errsRaw, ok := resp["errors"]
	if !ok {
		return data, false
	}
	var errs []any
	if err := json.Unmarshal(errsRaw, &errs); err != nil {
		t.Fatalf("%s errors is not JSON: %v", label, err)
	}
	return data, len(errs) > 0
}

func runExecSync(t *testing.T, c execSyncCase) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	urls := map[string]string{}
	for _, sg := range c.subgraphs {
		srv := syncSubgraph(t, c.data[sg.name])
		defer srv.Close()
		urls[sg.name] = srv.URL
	}
	cfg := executableConfig(t, ctx, c.subgraphs, urls)
	cfg.Fields = c.fields

	wantData, wantErrors := dataAndErrors(t, "expected", c.expected)

	if !c.skipV1 {
		oldPlanner, err := plan.NewPlanner(cfg)
		if err != nil {
			t.Fatalf("v1 planner: %v", err)
		}
		oldOp, oldDef, oldReport := parseAndNormalize(t, c.schema, c.op)
		oldPlan := oldPlanner.Plan(oldOp, oldDef, "", oldReport)
		if oldReport.HasErrors() {
			t.Fatalf("v1 planning failed: %s", oldReport.Error())
		}
		v1Raw := executeSyncPlan(t, ctx, oldPlan, oldOp, "v1")
		v1Data, v1Errors := dataAndErrors(t, "v1", v1Raw)
		if !reflect.DeepEqual(v1Data, wantData) || v1Errors != wantErrors {
			t.Fatalf("fixture sanity: v1 output does not match expected\n v1:       %s\n expected: %s", v1Raw, c.expected)
		}
	}
	if c.reset != nil {
		c.reset() // stateful fixture: restore the world the v1 leg consumed
	}

	newPlanner, err := planv2.NewPlanner(cfg)
	if err != nil {
		t.Fatalf("planv2 planner: %v", err)
	}
	newOp, newDef, newReport := parseAndNormalize(t, c.schema, c.op)
	newPlan := newPlanner.Plan(newOp, newDef, "", newReport)
	if newReport.HasErrors() {
		t.Fatalf("planv2 planning failed: %s", newReport.Error())
	}
	// Every planv2 fetch must carry FetchInfo: the router's dev-mode query-plan printer
	// dereferences Fetch.Info unconditionally (fetchtree.go queryPlan) -- nil is a runtime panic.
	if sp, ok := newPlan.(*plan.SynchronousResponsePlan); ok {
		for i, item := range sp.Response.RawFetches {
			if sf, ok := item.Fetch.(*resolve.SingleFetch); ok && sf.Info == nil {
				t.Fatalf("planv2 fetch %d carries nil Info (query-plan printer panic)", i)
			}
		}
	}
	if os.Getenv("EXEC_DUMP") != "" {
		if sp, ok := newPlan.(*plan.SynchronousResponsePlan); ok {
			for i, item := range sp.Response.RawFetches {
				if sf, ok := item.Fetch.(*resolve.SingleFetch); ok {
					t.Logf("fetch %d [%s] path=%q deps=%v input=%s", i, sf.DataSourceIdentifier, item.ResponsePath, sf.DependsOnFetchIDs, sf.Input)
				}
			}
		}
	}
	newRaw := executeSyncPlan(t, ctx, newPlan, newOp, "planv2")
	newData, newErrors := dataAndErrors(t, "planv2", newRaw)

	if !reflect.DeepEqual(newData, wantData) {
		t.Fatalf("planv2 data differs from expected\n planv2:   %s\n expected: %s", newRaw, c.expected)
	}
	if newErrors != wantErrors {
		t.Fatalf("planv2 error presence = %v, expected %v\n planv2: %s", newErrors, wantErrors, newRaw)
	}
}
