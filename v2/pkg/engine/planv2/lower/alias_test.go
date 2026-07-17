package lower

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// collidingRespKey is the single client response key rho every generated collision resolves under.
const collidingRespKey = "name"

// TestI4_CrossSubgraphAliasingPreservesClientKey discharges the PROOFS T4 aliasing-lemma obligation:
// "generate colliding same-response-key selections across subgraphs/concrete types and assert the
// client key is preserved and fetch documents contain no duplicate keys." Following the planv2 test
// convention (search/property_test.go) it runs DETERMINISTIC, table-driven instances rather than an
// external property-testing dependency (rapid); every instance is reproducible from its member count.
//
// The D11.4 collision predicate is the GraphQL conflict rule (M1 task-8 review I1/M2): same-document
// sibling selections of one rho collide iff their output types differ (SameResponseShape); mutually
// exclusive `... on C { f }` fragments with agreeing types are legal GraphQL and get NO alias.
func TestI4_CrossSubgraphAliasingPreservesClientKey(t *testing.T) {
	t.Run("conflicting output types are aliased, client key preserved end-to-end", func(t *testing.T) {
		for _, n := range []int{2, 3, 4, 5, 8} {
			t.Run(fmt.Sprintf("members=%d", n), func(t *testing.T) {
				h, o, res, op, def := genCollidingCase(t, n, true)
				p, err := Lower(h, o, res, op, def)
				if err != nil {
					t.Fatal(err)
				}
				assertInjectiveDocuments(t, p)
				// Every member fragment must select rho under a DISTINCT alias.
				doc := fetchDocuments(p)[0]
				for i := range n {
					want := fmt.Sprintf("_planv2_%s_%d: %s", collidingRespKey, i, collidingRespKey)
					if !strings.Contains(doc, want) {
						t.Fatalf("fetch document missing aliased selection %q: %s", want, doc)
					}
				}
				// The CLIENT key is rho everywhere; alpha never leaks into a response key.
				assertClientKeyOnly(t, p, collidingRespKey)
				// I1 end-to-end: each aliased response field reads its VALUE at Path=[alpha] -- the key
				// the fetch actually returns the data under -- while its NAME stays rho.
				fields := responseFieldsNamed(p.Response.Data, collidingRespKey)
				if len(fields) != n {
					t.Fatalf("want %d response fields %q, got %d", n, collidingRespKey, len(fields))
				}
				seenPaths := map[string]bool{}
				for _, f := range fields {
					// Leaves are now typed by scalar (String/Int/Float/Boolean/Scalar), so read the
					// Path through the resolve.Node interface, not a concrete *resolve.String assertion.
					vp := f.Value.NodePath()
					if len(vp) != 1 || !strings.HasPrefix(vp[0], "_planv2_"+collidingRespKey+"_") {
						t.Fatalf("aliased field must read at its fetch alias, got Path=%v", vp)
					}
					if seenPaths[vp[0]] {
						t.Fatalf("two response fields read the same alias %q (alpha not injective)", vp[0])
					}
					seenPaths[vp[0]] = true
				}
			})
		}
	})

	t.Run("agreeing output types under exclusive fragments are NOT aliased (legal GraphQL)", func(t *testing.T) {
		h, o, res, op, def := genCollidingCase(t, 3, false)
		p, err := Lower(h, o, res, op, def)
		if err != nil {
			t.Fatal(err)
		}
		assertInjectiveDocuments(t, p)
		doc := fetchDocuments(p)[0]
		if strings.Contains(doc, "_planv2_") {
			t.Fatalf("same-shape sibling fragments are legal GraphQL; no alias may be minted: %s", doc)
		}
		assertClientKeyOnly(t, p, collidingRespKey)
		for _, f := range responseFieldsNamed(p.Response.Data, collidingRespKey) {
			if v := f.Value.(*resolve.String); len(v.Path) != 1 || v.Path[0] != collidingRespKey {
				t.Fatalf("un-aliased field must read at rho, got Path=%v", v.Path)
			}
		}
	})

	t.Run("cross-subgraph same-key selections land in different documents: no alias", func(t *testing.T) {
		h, o, res, op, def := genCrossSubgraphCase(t)
		p, err := Lower(h, o, res, op, def)
		if err != nil {
			t.Fatal(err)
		}
		docs := fetchDocuments(p)
		if len(docs) != 2 {
			t.Fatalf("want 2 fetch documents (one per subgraph), got %d", len(docs))
		}
		for _, doc := range docs {
			if strings.Contains(doc, "_planv2_") {
				t.Fatalf("cross-subgraph selections are separate documents; no alias may be minted: %s", doc)
			}
			if !strings.Contains(doc, collidingRespKey) {
				t.Fatalf("each document must select rho plainly: %s", doc)
			}
		}
		assertInjectiveDocuments(t, p)
		assertClientKeyOnly(t, p, collidingRespKey)
	})
}

// TestAssignAliasesNoCollisionNoAlias pins the negative half of D11.4: when a response key is covered
// by a single edge (no collision predicate fires), no alias is minted -- aliasing is disambiguation,
// not decoration.
func TestAssignAliasesNoCollisionNoAlias(t *testing.T) {
	h, o, res, _, def := planPartialUnion(t)
	_, groupOf := buildGroups(h, res.Cover)
	am := assignAliases(h, o, res.Cover, def, groupOf)
	if len(am.byEdge) != 0 || len(am.byOb) != 0 {
		t.Fatalf("no colliding response key in Section 7.1; want 0 aliases, got %d/%d", len(am.byEdge), len(am.byOb))
	}
}

// genCollidingCase builds an instance where one response key rho ("name") is selected under N distinct
// concrete union members of a single subgraph -- same fetch document, same sibling scope (the D11.4
// collision domain). differentTypes toggles the conflict predicate: true gives each member a distinct
// output type (String, Int, Float, ... -- a SameResponseShape conflict that MUST be aliased); false
// gives every member the same String type (legal GraphQL -- no alias). All members are value types
// local to one subgraph, so D6 narrows none: every member is covered and participates.
func genCollidingCase(t *testing.T, n int, differentTypes bool) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	itemF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "item", Subgraph: 1})
	nodeObj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Node", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "item", Head: itemF, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: nodeObj, Tails: []hypergraph.NodeID{itemF}, Weight: 0})

	scalars := []string{"String", "Int", "Float", "Boolean", "ID"}
	members := make([]string, n)
	types := make([]string, n)
	for i := range members {
		members[i] = fmt.Sprintf("M%d", i)
		switch {
		case !differentTypes:
			types[i] = "String"
		case i < len(scalars):
			types[i] = scalars[i]
		default: // >5 members: list wrappers keep the printed types pairwise distinct
			types[i] = "[" + scalars[i%len(scalars)] + "]"
		}
	}
	for i, m := range members {
		mi := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: m, Subgraph: 1})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: m, Head: mi, Tails: []hypergraph.NodeID{nodeObj}, Weight: 1, Members: members})
		nameF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: m, Field: collidingRespKey, Subgraph: 1})
		// OutputType mirrors what hypergraph.Build derives from the subgraph SDL (printedFieldOutputType):
		// the shipping obligation-driven D11.4 aliaser decides the SameResponseShape conflict on this
		// subgraph-local output type, so the fixture must carry it exactly as Build would.
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: collidingRespKey, Head: nameF, Tails: []hypergraph.NodeID{mi}, Weight: 1, OutputType: types[i]})
	}
	h := b.Build()

	var sb strings.Builder
	sb.WriteString("schema { query: Query } type Query { item: Node } union Node = ")
	sb.WriteString(strings.Join(members, " | "))
	for i, m := range members {
		sb.WriteString(" type " + m + " { " + collidingRespKey + ": " + types[i] + " }")
	}
	var opb strings.Builder
	opb.WriteString("{ item { __typename")
	for _, m := range members {
		opb.WriteString(" ... on " + m + " { " + collidingRespKey + " }")
	}
	opb.WriteString(" } }")

	o, opDoc, defDoc := buildTree(t, h, sb.String(), opb.String())
	res := runSearch(t, h, o)
	// Sanity: every member must be covered (none D6-narrowed) so every edge participates.
	if len(res.Cover.Nulls) != 0 {
		t.Fatalf("single-subgraph members must not be narrowed; got %d nulls", len(res.Cover.Nulls))
	}
	return h, o, res, opDoc, defDoc
}

// genCrossSubgraphCase builds the cross-subgraph axis: rho ("name") selected under two root fields
// resolved by DIFFERENT subgraphs (pa in A, pb in B) with conflicting output types. The selections
// land in two separate fetch documents, so no per-document collision exists and no alias is minted --
// the client key is preserved plainly in both.
func genCrossSubgraphCase(t *testing.T) (*hypergraph.Hypergraph, *obligation.Tree, *search.Result, *ast.Document, *ast.Document) {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "A")
	b.SetSubgraphName(2, "B")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	for _, side := range []struct {
		sg      hypergraph.SubgraphID
		field   string
		typ     string
		nameTyp string
	}{{1, "pa", "PA", "String"}, {2, "pb", "PB", "Int"}} {
		ff := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: side.field, Subgraph: side.sg})
		obj := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: side.typ, Subgraph: side.sg})
		nameF := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: side.typ, Field: collidingRespKey, Subgraph: side.sg})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: side.field, Head: ff, Tails: []hypergraph.NodeID{r}, Weight: 1000})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: obj, Tails: []hypergraph.NodeID{ff}, Weight: 0})
		b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: collidingRespKey, Head: nameF, Tails: []hypergraph.NodeID{obj}, Weight: 1, OutputType: side.nameTyp})
	}
	h := b.Build()

	const schema = `schema { query: Query } type Query { pa: PA pb: PB } type PA { name: String } type PB { name: Int }`
	const op = `{ pa { name } pb { name } }`
	o, opDoc, defDoc := buildTree(t, h, schema, op)
	res := runSearch(t, h, o)
	return h, o, res, opDoc, defDoc
}

// fetchDocuments returns the printed subgraph document of every fetch (QueryPlan.Query keeps the
// document first-class; Input additionally wraps it in the loader's JSON envelope).
func fetchDocuments(p *plan.SynchronousResponsePlan) []string {
	var out []string
	for _, f := range p.Response.RawFetches {
		if sf, ok := f.Fetch.(*resolve.SingleFetch); ok && sf.QueryPlan != nil {
			out = append(out, sf.QueryPlan.Query)
		}
	}
	return out
}

// assertClientKeyOnly asserts the response tree carries the client response key rho and never leaks a
// fetch-side alpha alias into a response KEY (D11.4 / PROOFS T4 clause 2: the client-visible key is rho in
// all cases; alpha appears only in value Paths, mapped back to rho).
func assertClientKeyOnly(t *testing.T, p *plan.SynchronousResponsePlan, rho string) {
	t.Helper()
	rendered := canonResolveFields(p.Response.Data.Fields)
	if !strings.Contains(rendered, rho) {
		t.Fatalf("response tree must contain client key %q: %s", rho, rendered)
	}
	if strings.Contains(rendered, "_planv2_") {
		t.Fatalf("internal alpha alias leaked into a response key: %s", rendered)
	}
}

// responseFieldsNamed collects every response field named key, depth-first.
func responseFieldsNamed(obj *resolve.Object, key string) []*resolve.Field {
	var out []*resolve.Field
	for _, f := range obj.Fields {
		if string(f.Name) == key {
			out = append(out, f)
		}
		if child, ok := f.Value.(*resolve.Object); ok {
			out = append(out, responseFieldsNamed(child, key)...)
		}
	}
	return out
}

// assertInjectiveDocuments asserts, per fetch document, the two halves of alpha-injectivity T4 requires:
// no response key appears twice within one selection-set scope (fragment bodies are their own scope --
// mutually exclusive type conditions make same-key siblings legal), and no `_planv2_` alias is
// reused anywhere in one document.
func assertInjectiveDocuments(t *testing.T, p *plan.SynchronousResponsePlan) {
	t.Helper()
	for _, doc := range fetchDocuments(p) {
		if k, dup := firstDuplicateKey(doc); dup {
			t.Fatalf("fetch document has duplicate key %q in one scope: %s", k, doc)
		}
		seen := map[string]bool{}
		for tok := range strings.FieldsSeq(strings.NewReplacer("{", " ", "}", " ", ":", " ").Replace(doc)) {
			if !strings.HasPrefix(tok, "_planv2_") {
				continue
			}
			if seen[tok] {
				t.Fatalf("alias %q reused within one document (alpha not injective): %s", tok, doc)
			}
			seen[tok] = true
		}
	}
}

// firstDuplicateKey returns the first response key that appears twice among sibling selections of
// the SAME selection-set scope in the printed document. Fragment bodies (`... on T { ... }`) are
// separate scopes: their type conditions are mutually exclusive, so a key repeated across two
// fragments is not by itself a duplicate (the aliaser handles the output-type-conflict case).
// Aliased fields are keyed by their alias. Parenthesised argument lists are stripped first.
func firstDuplicateKey(doc string) (string, bool) {
	toks := tokenizeDoc(stripParens(doc))
	for i, tk := range toks {
		if tk == "{" {
			dup, found, _ := scanKeys(toks, i)
			return dup, found
		}
	}
	return "", false
}

// scanKeys walks the selection set beginning at toks[i]=="{" with a fresh key scope, recursing into
// nested objects and fragment bodies (each its own scope). Returns the first duplicate found.
func scanKeys(toks []string, i int) (string, bool, int) {
	keys := map[string]bool{}
	i++ // past "{"
	for i < len(toks) && toks[i] != "}" {
		if toks[i] == "..." {
			j := i
			for j < len(toks) && toks[j] != "{" {
				j++
			}
			dup, found, ni := scanKeys(toks, j) // fragment body: its own scope
			if found {
				return dup, true, ni
			}
			i = ni
			continue
		}
		key := toks[i]
		i++
		if i < len(toks) && toks[i] == ":" { // alias: realName -> the key is the alias
			i += 2
		}
		if keys[key] {
			return key, true, i
		}
		keys[key] = true
		if i < len(toks) && toks[i] == "{" {
			dup, found, ni := scanKeys(toks, i) // nested object: fresh scope
			if found {
				return dup, true, ni
			}
			i = ni
		}
	}
	return "", false, i + 1
}

func tokenizeDoc(s string) []string {
	for _, sep := range []string{"{", "}", ":"} {
		s = strings.ReplaceAll(s, sep, " "+sep+" ")
	}
	return strings.Fields(s)
}

// stripParens removes every parenthesised group (argument lists) so the key scanner sees only the
// selection structure.
func stripParens(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
