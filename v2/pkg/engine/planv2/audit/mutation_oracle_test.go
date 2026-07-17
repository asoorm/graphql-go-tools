package audit

// Committed anti-cheat mutation suite -- the M3 adversarial audit's oracle-soundness harness,
// rebuilt lean as a permanent fixture so the plan-level oracle's kill-power is CI-checked forever.
//
// Method (mirrors the audit's worktree harness): plan a corpus case through EXACTLY the runner's
// pipeline, verify the fresh plan passes the assertion chain (baseline), apply ONE deliberate
// corruption, and require the SAME chain (assertPlan -- the code the corpus run executes) to catch
// it. The table below is deterministic (no randomness -- the "seed" is the fixed spec list) and
// covers the audit's ten mutation classes:
//
//	M1  drop a whole fetch                     M6  corrupt a representation Key/Requires fragment
//	M2  re-root a fetch to another subgraph    M7  bogus ResponsePath on an entity fetch
//	M3  drop a `... on T` member fragment      M8  FetchPath/ResponsePath disagreement
//	M4  forward/dangling dependency            M9  drop a field from the response tree
//	M5  drop one leaf from a fetch document    M10 swap `... on T` to an unrelated concrete type
//
// M3 has no committed spec: corpus entity documents carry a single member fragment, so removing it
// empties the document -- that is M1/M10 territory (the audit measured 0 applicable M3 mutations).
//
// INTENTIONAL SURVIVORS. A spec with a non-empty `survivor` reason documents a corruption the
// plan-level oracle deliberately does NOT catch; the test asserts it (still) survives, so an oracle
// change that starts catching it must upgrade the spec in the same commit (the inverse is loud by
// construction). The surviving classes at this suite's landing:
//   - M2 partial: a re-rooted fetch whose document happens to validate against BOTH subgraphs'
//     overlapping schemas -- subgraph identity beyond schema validation is out of plan-level reach
//     (audit blind spot 4; executed truth is the net).
//   - M1/M5 member-branch: dropping a WHOLE member's coverage of an UNREFINED abstract position
//     (or the fetch carrying it) removes the member's materialization, so the assertion-6p
//     narrowing legitimately exempts it -- per-member completeness of member-exploded positions is
//     adjudicated as executed-truth's business (assertion 6 leniency, audit V1 item 2).
//   - M5 argument-binding: assertion 8 is coordinate-level, so dropping ONE argument-binding of a
//     field supplied under several bindings (`_planv2req_price_1: price(currency:"EUR")` vs
//     `price(currency:"USD")`) is invisible -- bindings share the Type.field coordinate.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astprinter"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// mutArtifacts is one freshly-planned corpus case, ready for corruption.
type mutArtifacts struct {
	c            Case
	fetches      []*resolve.FetchItem
	data         *resolve.Object
	normalizedOp string
	def          *ast.Document
	upstream     map[string]*ast.Document
}

// assert runs the runner's assertion chain over the (possibly corrupted) artifacts. A panic counts
// as a kill: a corruption that leaves a fetch document unparseable dies in the oracle's parser
// (assertion-2 territory -- the audit's "parser-panic" kill mechanism).
func (m *mutArtifacts) assert() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("oracle machinery rejected the plan (panic): %v", r)
		}
	}()
	return assertPlan(m.c.Expected, m.fetches, m.data, m.normalizedOp, m.def, m.upstream)
}

// planForMutation replicates runInner's pipeline (BuildDataSources -> NewPlanner -> parseAndNormalize
// -> PlanWithDiagnostics) for one corpus case and returns the artifacts the assertion chain consumes.
func planForMutation(t *testing.T, corpus []Case, suite, name string) *mutArtifacts {
	t.Helper()
	var found *Case
	for i := range corpus {
		if corpus[i].Suite == suite && corpus[i].Name == name {
			found = &corpus[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("case %s/%s not in corpus", suite, name)
	}
	dataSources, upstream, err := BuildDataSources(*found)
	if err != nil {
		t.Fatalf("%s/%s: BuildDataSources: %v", suite, name, err)
	}
	planner, err := planv2.NewPlanner(plan.Configuration{DataSources: dataSources, DisableResolveFieldPositions: true})
	if err != nil {
		t.Fatalf("%s/%s: NewPlanner: %v", suite, name, err)
	}
	op, def, report := parseAndNormalize(found.Definition, found.Operation)
	if report.HasErrors() {
		t.Fatalf("%s/%s: normalize: %s", suite, name, report.Error())
	}
	normalizedOp, printErr := astprinter.PrintString(op)
	if printErr != nil {
		t.Fatalf("%s/%s: print: %v", suite, name, printErr)
	}
	p, _ := planner.PlanWithDiagnostics(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("%s/%s: plan: %s", suite, name, report.Error())
	}
	sp, ok := p.(*plan.SynchronousResponsePlan)
	if !ok || sp == nil || sp.Response == nil {
		t.Fatalf("%s/%s: not a SynchronousResponsePlan (%T)", suite, name, p)
	}
	return &mutArtifacts{
		c:            *found,
		fetches:      sp.Response.RawFetches,
		data:         sp.Response.Data,
		normalizedOp: normalizedOp,
		def:          def,
		upstream:     upstream,
	}
}

// --- mutation helpers ---------------------------------------------------------------------------

func (m *mutArtifacts) single(t *testing.T, i int) *resolve.SingleFetch {
	t.Helper()
	if i < 0 || i >= len(m.fetches) {
		t.Fatalf("fetch %d out of range (%d fetches)", i, len(m.fetches))
	}
	sf, ok := m.fetches[i].Fetch.(*resolve.SingleFetch)
	if !ok || sf == nil {
		t.Fatalf("fetch %d is not a SingleFetch", i)
	}
	return sf
}

// dropFetch removes fetch i and remaps the remaining dependency indices, so the kill must come from
// a real oracle, not index bookkeeping.
func (m *mutArtifacts) dropFetch(t *testing.T, i int) {
	t.Helper()
	m.single(t, i) // bounds check
	m.fetches = append(append([]*resolve.FetchItem{}, m.fetches[:i]...), m.fetches[i+1:]...)
	for j := range m.fetches {
		sf, ok := m.fetches[j].Fetch.(*resolve.SingleFetch)
		if !ok || sf == nil {
			continue
		}
		var deps []int
		for _, dep := range sf.DependsOnFetchIDs {
			switch {
			case dep == i:
				// dropped
			case dep > i:
				deps = append(deps, dep-1)
			default:
				deps = append(deps, dep)
			}
		}
		sf.DependsOnFetchIDs = deps
	}
}

// reroot points fetch i at another subgraph without touching its document.
func (m *mutArtifacts) reroot(t *testing.T, i int, subgraph string) {
	t.Helper()
	m.single(t, i).DataSourceIdentifier = []byte(subgraph)
}

// setDoc replaces fetch i's oracle-visible document.
func (m *mutArtifacts) setDoc(t *testing.T, i int, doc string) {
	t.Helper()
	sf := m.single(t, i)
	if sf.QueryPlan == nil {
		t.Fatalf("fetch %d has no QueryPlan", i)
	}
	sf.QueryPlan.Query = doc
}

func (m *mutArtifacts) doc(t *testing.T, i int) string {
	t.Helper()
	return fetchQuery(m.single(t, i))
}

// dropDocField removes the first selection of field `field` from doc -- everywhere when parent==""
// and cond=="", else only inside the selection set of field `parent` / inside the inline fragment
// conditioned on `cond`. Fails the test when nothing was removed (the spec no longer applies).
func dropDocField(t *testing.T, doc, parent, cond, field string) string {
	t.Helper()
	parsed := unsafeparser.ParseGraphqlDocumentString(doc)
	removed := false
	var walk func(setRef int, inParent, inCond bool, depth int)
	walk = func(setRef int, inParent, inCond bool, depth int) {
		if setRef < 0 || removed || depth > 32 {
			return
		}
		refs := parsed.SelectionSets[setRef].SelectionRefs
		for k, selRef := range refs {
			sel := parsed.Selections[selRef]
			switch sel.Kind {
			case ast.SelectionKindField:
				name := parsed.FieldNameString(sel.Ref)
				if name == field && (parent == "" || inParent) && (cond == "" || inCond) {
					parsed.SelectionSets[setRef].SelectionRefs = append(append([]int{}, refs[:k]...), refs[k+1:]...)
					removed = true
					return
				}
				if ss, ok := parsed.FieldSelectionSet(sel.Ref); ok {
					walk(ss, parent != "" && name == parent, false, depth+1)
				}
			case ast.SelectionKindInlineFragment:
				tc := parsed.InlineFragmentTypeConditionNameString(sel.Ref)
				walk(parsed.InlineFragments[sel.Ref].SelectionSet, inParent, inCond || (cond != "" && tc == cond), depth+1)
			}
			if removed {
				return
			}
		}
	}
	for ref := range parsed.OperationDefinitions {
		walk(parsed.OperationDefinitions[ref].SelectionSet, false, false, 0)
		break
	}
	if !removed {
		t.Fatalf("dropDocField: %q (parent %q, cond %q) not found in %s", field, parent, cond, doc)
	}
	out, err := astprinter.PrintString(&parsed)
	if err != nil {
		t.Fatalf("dropDocField: reprint: %v", err)
	}
	return out
}

// dropDocAlias removes the first selection whose ALIAS is `alias`.
func dropDocAlias(t *testing.T, doc, alias string) string {
	t.Helper()
	parsed := unsafeparser.ParseGraphqlDocumentString(doc)
	removed := false
	var walk func(setRef, depth int)
	walk = func(setRef, depth int) {
		if setRef < 0 || removed || depth > 32 {
			return
		}
		refs := parsed.SelectionSets[setRef].SelectionRefs
		for k, selRef := range refs {
			sel := parsed.Selections[selRef]
			switch sel.Kind {
			case ast.SelectionKindField:
				if parsed.FieldAliasOrNameString(sel.Ref) == alias {
					parsed.SelectionSets[setRef].SelectionRefs = append(append([]int{}, refs[:k]...), refs[k+1:]...)
					removed = true
					return
				}
				if ss, ok := parsed.FieldSelectionSet(sel.Ref); ok {
					walk(ss, depth+1)
				}
			case ast.SelectionKindInlineFragment:
				walk(parsed.InlineFragments[sel.Ref].SelectionSet, depth+1)
			}
			if removed {
				return
			}
		}
	}
	for ref := range parsed.OperationDefinitions {
		walk(parsed.OperationDefinitions[ref].SelectionSet, 0)
		break
	}
	if !removed {
		t.Fatalf("dropDocAlias: alias %q not found in %s", alias, doc)
	}
	out, err := astprinter.PrintString(&parsed)
	if err != nil {
		t.Fatalf("dropDocAlias: reprint: %v", err)
	}
	return out
}

// corruptRepFragment rewrites fetch i's representation fragment `old`->`new` (first occurrence).
func (m *mutArtifacts) corruptRepFragment(t *testing.T, i int, old, new string) {
	t.Helper()
	sf := m.single(t, i)
	if sf.QueryPlan == nil || len(sf.QueryPlan.DependsOnFields) == 0 {
		t.Fatalf("fetch %d carries no representation fragments", i)
	}
	for j := range sf.QueryPlan.DependsOnFields {
		f := sf.QueryPlan.DependsOnFields[j].Fragment
		if strings.Contains(f, old) {
			sf.QueryPlan.DependsOnFields[j].Fragment = strings.Replace(f, old, new, 1)
			return
		}
	}
	t.Fatalf("corruptRepFragment: %q not found in fetch %d's fragments", old, i)
}

// swapDocFragment swaps a `... on` type condition in fetch i's document (string-level; the printed
// form is unambiguous).
func (m *mutArtifacts) swapDocFragment(t *testing.T, i int, from, to string) {
	t.Helper()
	doc := m.doc(t, i)
	if !strings.Contains(doc, "... on "+from) {
		t.Fatalf("swapDocFragment: `... on %s` not in fetch %d doc: %s", from, i, doc)
	}
	m.setDoc(t, i, strings.Replace(doc, "... on "+from, "... on "+to, 1))
}

// dropResponseField removes the first response-tree field named key (DFS).
func (m *mutArtifacts) dropResponseField(t *testing.T, key string) {
	t.Helper()
	if !dropFieldFromObject(m.data, key) {
		t.Fatalf("dropResponseField: %q not in response tree", key)
	}
}

func dropFieldFromObject(node resolve.Node, key string) bool {
	switch n := node.(type) {
	case *resolve.Object:
		if n == nil {
			return false
		}
		for i, f := range n.Fields {
			if string(f.Name) == key {
				n.Fields = append(n.Fields[:i], n.Fields[i+1:]...)
				return true
			}
		}
		for _, f := range n.Fields {
			if dropFieldFromObject(f.Value, key) {
				return true
			}
		}
	case *resolve.Array:
		if n != nil {
			return dropFieldFromObject(n.Item, key)
		}
	}
	return false
}

// --- the spec table -------------------------------------------------------------------------------

type mutationSpec struct {
	class    string
	name     string
	suite    string
	caseName string
	// survivor documents an INTENTIONAL survivor (see the package comment); empty = must be killed.
	survivor string
	apply    func(t *testing.T, m *mutArtifacts)
}

func mutationSpecs() []mutationSpec {
	return []mutationSpec{
		// M1 -- drop a whole fetch.
		{class: "M1", name: "drop-entity-fetch", suite: "simple-entity-call", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.dropFetch(t, 1) }},
		{class: "M1", name: "drop-coordinate-masked-fetch", suite: "child-type-mismatch", caseName: "case-01",
			// The audit's M1 survivor: f1 serves users.name, but User.name is also selected under
			// accounts -- coordinate-masked. Assertion 6p (position-aware) must kill it.
			apply: func(t *testing.T, m *mutArtifacts) { m.dropFetch(t, 1) }},
		{class: "M1", name: "drop-key-gather-fetch", suite: "complex-entity-call", caseName: "case-01",
			// f3 gathers the distributed ProductList key f4's representation reads.
			apply: func(t *testing.T, m *mutArtifacts) { m.dropFetch(t, 3) }},
		{class: "M1", name: "drop-member-branch-fetch", suite: "union-intersection", caseName: "case-04",
			survivor: "dropping f1 removes ALL `... on Movie` coverage AND its materialization, so the assertion-6p narrowing exempts the Movie-refined leaves; per-member completeness of member-exploded positions is executed-truth's business (adjudicated assertion-6 leniency)",
			apply:    func(t *testing.T, m *mutArtifacts) { m.dropFetch(t, 1) }},

		// M2 -- re-root a fetch to another subgraph.
		{class: "M2", name: "reroot-source-fetch", suite: "simple-entity-call", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.reroot(t, 0, "nickname") }},
		{class: "M2", name: "reroot-overlapping-schema", suite: "complex-entity-call", caseName: "case-01",
			survivor: "the `... on Product { pid }` entity document validates against BOTH link's and list's schemas -- subgraph identity beyond schema validation is out of plan-level reach (audit blind spot 4); executed truth is the net",
			apply:    func(t *testing.T, m *mutArtifacts) { m.reroot(t, 1, "list") }},
		{class: "M2", name: "reroot-overlapping-schema-2", suite: "keys-mashup", caseName: "case-01",
			survivor: "the entity document validates against both a's and b's overlapping schemas (audit blind spot 4)",
			apply:    func(t *testing.T, m *mutArtifacts) { m.reroot(t, 1, "b") }},

		// M4 -- forward/dangling dependency.
		{class: "M4", name: "self-dependency", suite: "simple-entity-call", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.single(t, 1).DependsOnFetchIDs = []int{1} }},
		{class: "M4", name: "forward-dependency", suite: "requires-requires", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.single(t, 1).DependsOnFetchIDs = []int{2} }},

		// M5 -- drop one leaf from a fetch document.
		{class: "M5", name: "drop-requested-leaf", suite: "requires-requires", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.setDoc(t, 1, dropDocField(t, m.doc(t, 1), "", "", "price")) }},
		{class: "M5", name: "drop-key-leaf-email", suite: "simple-entity-call", caseName: "case-01",
			// The audit's headline M5 survivor: `email` is f1's KEY, selected by f0 only as
			// mechanism. Assertion 8 (key supply) must kill it.
			apply: func(t *testing.T, m *mutArtifacts) { m.setDoc(t, 0, dropDocField(t, m.doc(t, 0), "", "", "email")) }},
		{class: "M5", name: "drop-composite-key-leaf", suite: "keys-mashup", caseName: "case-01",
			// `two` is part of f2's composite key `compositeId { two three }` (audit M5 survivor).
			apply: func(t *testing.T, m *mutArtifacts) { m.setDoc(t, 0, dropDocField(t, m.doc(t, 0), "", "", "two")) }},
		{class: "M5", name: "drop-key-leaf-pid", suite: "parent-entity-call", caseName: "case-01",
			// `pid` is mechanism-only key supply for f1 (audit M5 survivor class).
			apply: func(t *testing.T, m *mutArtifacts) { m.setDoc(t, 0, dropDocField(t, m.doc(t, 0), "", "", "pid")) }},
		{class: "M5", name: "drop-position-masked-leaf", suite: "child-type-mismatch", caseName: "case-01",
			// `users.id` dropped from f0 while `accounts > ... on User > id` keeps the User.id
			// COORDINATE alive in f2 (audit blind spot 2 -- its exact child-type-mismatch witness).
			// Assertion 6p (position-aware) must kill it.
			apply: func(t *testing.T, m *mutArtifacts) {
				m.setDoc(t, 0, dropDocField(t, m.doc(t, 0), "users", "", "id"))
			}},
		{class: "M5", name: "drop-member-refined-leaf", suite: "child-type-mismatch", caseName: "case-01",
			// `name` inside `... on User` dropped while the User member still materializes and
			// `... on Admin { name }` keeps the name coordinate alive (audit blind spot 3).
			// Assertion 6p's narrowed member exemption must kill it.
			apply: func(t *testing.T, m *mutArtifacts) {
				m.setDoc(t, 2, dropDocField(t, m.doc(t, 2), "", "User", "name"))
			}},
		{class: "M5", name: "drop-requires-supply-leaf", suite: "mutations", caseName: "case-01",
			// `price` supplies f1's @requires; also client-requested.
			apply: func(t *testing.T, m *mutArtifacts) { m.setDoc(t, 0, dropDocField(t, m.doc(t, 0), "", "", "price")) }},
		{class: "M5", name: "drop-one-argument-binding", suite: "requires-with-argument-conflict", caseName: "case-01",
			survivor: "assertion 8 is coordinate-level: dropping the EUR binding (`_planv2req_price_1: price(currency:\"EUR\")`) leaves the Product.price coordinate supplied by the USD binding -- argument-binding identity is invisible at plan level",
			apply: func(t *testing.T, m *mutArtifacts) {
				m.setDoc(t, 0, dropDocAlias(t, m.doc(t, 0), "_planv2req_price_1"))
			}},

		// M6 -- corrupt a representation fragment (the plan's own declaration of its runtime inputs;
		// the audit measured these NON-load-bearing at runtime -- the wire representation renders from
		// the same tries -- but the fragment is assertions 6/7/8's admissibility input, so a lying
		// fragment must fail: assertion 8 reads it as a requirement nobody supplies).
		{class: "M6", name: "corrupt-key-fragment-field", suite: "simple-entity-call", caseName: "case-01",
			// `email` is mechanism-only: the rename also orphans f0's `email` selection, so assertion
			// 6's BACKWARD direction already kills this variant.
			apply: func(t *testing.T, m *mutArtifacts) { m.corruptRepFragment(t, 1, "email", "phantomField") }},
		{class: "M6", name: "corrupt-requires-fragment-field", suite: "requires-requires", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.corruptRepFragment(t, 2, "price", "phantomField") }},
		{class: "M6", name: "corrupt-key-fragment-requested-field", suite: "child-type-mismatch", caseName: "case-01",
			// The audit's true M6 survivor class: `id` is ALSO client-requested, so assertion 6
			// backward stays satisfied and only assertion 8 (the fragment declares a requirement
			// nobody supplies) can kill the corrupted fragment.
			apply: func(t *testing.T, m *mutArtifacts) { m.corruptRepFragment(t, 1, "id", "phantomField") }},
		{class: "M6", name: "corrupt-requires-fragment-requested-field", suite: "keys-mashup", caseName: "case-01",
			// Same shape for a Requires fragment: `name` is client-requested (f1 selects it for the
			// client), so only assertion 8 sees the corruption.
			apply: func(t *testing.T, m *mutArtifacts) { m.corruptRepFragment(t, 2, "name", "phantomField") }},

		// M7 -- bogus ResponsePath on an entity fetch.
		{class: "M7", name: "bogus-response-path", suite: "simple-entity-call", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.fetches[1].ResponsePath = "phantom" }},
		{class: "M7", name: "bogus-response-path-nested", suite: "keys-mashup", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.fetches[1].ResponsePath = "b.phantom" }},
		{class: "M7", name: "empty-response-path", suite: "mutations", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.fetches[1].ResponsePath = "" }},

		// M8 -- FetchPath/ResponsePath disagreement.
		{class: "M8", name: "fetchpath-truncated", suite: "simple-entity-call", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) {
				fp := m.fetches[1].FetchPath
				if len(fp) == 0 {
					t.Fatal("fetch 1 has empty FetchPath")
				}
				m.fetches[1].FetchPath = fp[:len(fp)-1]
			}},
		{class: "M8", name: "fetchpath-renamed", suite: "requires-requires", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) {
				fp := m.fetches[1].FetchPath
				if len(fp) == 0 || len(fp[0].Path) == 0 {
					t.Fatal("fetch 1 has no FetchPath element to rename")
				}
				fp[0].Path[0] = "phantom"
			}},

		// M9 -- drop a field from the response tree.
		{class: "M9", name: "drop-response-leaf", suite: "simple-entity-call", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.dropResponseField(t, "nickname") }},
		{class: "M9", name: "drop-response-object", suite: "complex-entity-call", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.dropResponseField(t, "selected") }},

		// M10 -- swap `... on T` to an unrelated concrete type.
		{class: "M10", name: "swap-member-unrelated", suite: "child-type-mismatch", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.swapDocFragment(t, 1, "User", "Admin") }},
		{class: "M10", name: "swap-member-unrelated-2", suite: "parent-entity-call", caseName: "case-01",
			apply: func(t *testing.T, m *mutArtifacts) { m.swapDocFragment(t, 1, "Product", "Category") }},
	}
}

// TestAudit_MutationOracle is the permanent oracle-soundness gate: every mutation class keeps at
// least one KILLED representative, every spec's outcome is pinned (killed, or a documented
// intentional survivor), and the baseline plan passes the chain before each corruption.
func TestAudit_MutationOracle(t *testing.T) {
	corpus, err := loadCorpus("testdata")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}

	specs := mutationSpecs()
	killedByClass := map[string]int{}
	var killed, survived int
	for _, spec := range specs {
		spec := spec
		t.Run(spec.class+"/"+spec.suite+"/"+spec.name, func(t *testing.T) {
			m := planForMutation(t, corpus, spec.suite, spec.caseName)
			if err := m.assert(); err != nil {
				t.Fatalf("baseline plan fails the assertion chain before mutation: %v", err)
			}
			spec.apply(t, m)
			err := m.assert()
			if spec.survivor == "" {
				if err == nil {
					t.Errorf("MUTATION SURVIVED: %s %s on %s/%s -- the oracle no longer catches this corruption class",
						spec.class, spec.name, spec.suite, spec.caseName)
					return
				}
				killed++
				killedByClass[spec.class]++
				t.Logf("killed by: %v", err)
				return
			}
			if err != nil {
				t.Errorf("documented survivor now KILLED (%v) -- the oracle improved; upgrade this spec to killed and update its survivor note: %s",
					err, spec.survivor)
				return
			}
			survived++
			t.Logf("intentional survivor: %s", spec.survivor)
		})
	}

	for _, class := range []string{"M1", "M2", "M4", "M5", "M6", "M7", "M8", "M9", "M10"} {
		if killedByClass[class] == 0 {
			t.Errorf("mutation class %s has no killed representative", class)
		}
	}
	t.Logf("mutation suite: %d specs, %d killed, %d documented survivors", len(specs), killed, survived)
}
