package lower_test

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/lower"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// lowerCorpusNewPath drives one audit corpus case through the obligation-driven lowering path (the
// shipping default post THE FLIP) and returns its fetch documents (or an error). It reproduces
// planv2.Plan's stage pipeline with the zero-value LowerConfig. Used on real federation fixtures from
// outside the audit package (audit.LoadCaseForTest breaks the import cycle).
func lowerCorpusNewPath(t *testing.T, suite, name string) ([]string, error) {
	t.Helper()
	mat, err := audit.LoadCaseForTest(suite, name)
	if err != nil {
		return nil, err
	}
	h, err := hypergraph.Build(mat.DataSources, hypergraph.BuildConfig{
		Weights:  hypergraph.DefaultWeights(),
		RootType: map[string]string{"query": "Query", "mutation": "Mutation"},
	})
	if err != nil {
		return nil, err
	}
	o, err := obligation.Build(mat.Operation, mat.Definition, "", h)
	if err != nil {
		return nil, err
	}
	res, err := search.Search(h, o, search.Config{Combine: search.Sum, PreflightCap: 1 << 30, StateCap: 1 << 24})
	if err != nil {
		return nil, err
	}
	p, err := lower.LowerWithConfig(h, o, res, mat.Operation, mat.Definition, lower.LowerConfig{})
	if err != nil {
		return nil, err
	}
	var docs []string
	for _, f := range p.Response.RawFetches {
		docs = append(docs, f.Fetch.(*resolve.SingleFetch).QueryPlan.Query)
	}
	return docs, nil
}

func anyDoc(docs []string, sub string) bool {
	for _, d := range docs {
		if strings.Contains(d, sub) {
			return true
		}
	}
	return false
}

// TestNewPathMultiJumpRequires pins the M1.5 wave-1B multi-jump attribution fix: a same-response-position
// EntityJump CHAIN (@override->@requires provider relay; @requires representation across three subgraphs;
// null-key upc->id->... relay) must lower to a correct producer chain -- each jump depending on the previous,
// each field landing in the subgraph that resolves it, and NO root/parent field re-emitted inside an
// entity fetch. Before the fix walkSpine derailed the up-walk onto @requires tails and ordered same-depth
// jumps non-deterministically, collapsing these to invalid documents (root fields inside `_entities`).
func TestNewPathMultiJumpRequires(t *testing.T) {
	// override-with-requires/case-03: `userInA { cName }`, name @override->b, cName @requires name.
	// Root supplies the key on a; b provides the overridden `name`; c resolves `cName` (reps id+name).
	// D7pp/D11.10 (requires-chain wave) shape: the requires-input pipeline reads c's key from the ROOT
	// fetch's `id` and `name` from b's gather fetch -- both off the merged response object at the same
	// position -- so b's document carries ONLY the input it produces (a fetch DAG). The old linear
	// relay re-selected `__typename id` in b; values are identical, the chain is one link shorter.
	t.Run("override-with-requires/case-03", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "override-with-requires", "case-03")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		for _, want := range []string{
			// D11.6: __typename injected on the concrete entity-key parent (v1 `{userInA {__typename id}}`).
			"query { userInA { __typename id } }",
			"_entities(representations: $representations) { ... on User { name } }",
			"_entities(representations: $representations) { ... on User { cName } }",
		} {
			if !anyDoc(docs, want) {
				t.Fatalf("expected %q among docs; got %v", want, docs)
			}
		}
		// No root field re-emitted inside an entity fetch (the collapse signature).
		if anyDoc(docs, "on User { userInA") {
			t.Fatalf("root field `userInA` re-emitted inside an entity fetch: %v", docs)
		}
	})

	// simple-requires-provides/case-10: `me { reviews { product { inStock } } }`. inStock carries NO
	// @requires (its siblings shippingEstimate/Tag do), so under D7pp demand-driven requires the plan
	// is THREE fetches: accounts key, reviews->product upc, inventory inStock. The old fourth fetch
	// (`... on Product { __typename upc price weight }` into products) was the D7 ride-along
	// gathering inputs for requires fields the client never asked for -- the overfetch D7pp removes.
	// The multi-jump chain still must not collapse (no root field inside an entity fetch).
	t.Run("simple-requires-provides/case-10", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "simple-requires-provides", "case-10")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		for _, want := range []string{
			// D11.6: __typename injected on each entity-key parent (me, and the nested product).
			"query { me { __typename id } }",
			"... on Product { inStock }",
			"... on User { reviews { product { __typename upc } } }",
		} {
			if !anyDoc(docs, want) {
				t.Fatalf("expected %q among docs; got %v", want, docs)
			}
		}
		// D7pp: no ride-along gather for the un-requested requires fields.
		if anyDoc(docs, "price weight") {
			t.Fatalf("ride-along requires gather re-appeared (price/weight fetched for an operation that never needs them): %v", docs)
		}
		if anyDoc(docs, "on Product { me") {
			t.Fatalf("root field `me` re-emitted inside an entity fetch: %v", docs)
		}
	})

	// null-keys/case-01: `bookContainers { book { upc author { name } } }` -- a upc->id->author key relay
	// across three Book subgraphs at the one `book` position. The relay must chain (a upc, b id, c author).
	t.Run("null-keys/case-01", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "null-keys", "case-01")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		for _, want := range []string{
			// D11.6: __typename injected on the nested entity-key parent `book`.
			"query { bookContainers { book { __typename upc } } }",
			"... on Book { __typename id }",
			"... on Book { author { name } }",
		} {
			if !anyDoc(docs, want) {
				t.Fatalf("expected %q among docs; got %v", want, docs)
			}
		}
		if anyDoc(docs, "on Book { bookContainers") {
			t.Fatalf("root field `bookContainers` re-emitted inside an entity fetch: %v", docs)
		}
	})
}

// TestNewPathInterfaceObjectKeys pins the @interfaceObject key-typing fix: a jump departing from a
// subgraph that models the entity as an @interfaceObject (interface-typed object, NO concrete members)
// must inject its key FLAT on the interface-object type -- a `... on Member` fragment would name a type
// the source subgraph does not declare -- and the tail walk-up must stop at the PRE-JUMP entity type
// (obGroup.srcType), not the jump head's member type (which overran to the operation root, re-emitting
// the whole root chain inside the fragment).
func TestNewPathInterfaceObjectKeys(t *testing.T) {
	// non-resolvable-interface-object/case-07: `product { ... on Bread { id } }`; subgraph a models
	// Product @interfaceObject (no Bread). The root doc selects the key flat on Product; the member
	// resolution happens in b's `... on Bread` entity fetch.
	t.Run("non-resolvable-interface-object/case-07", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "non-resolvable-interface-object", "case-07")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		for _, want := range []string{
			"query { product { __typename id } }",
			"_entities(representations: $representations) { ... on Bread { __typename id } }", // __typename: C-disc re-discrimination (deliberate)
		} {
			if !anyDoc(docs, want) {
				t.Fatalf("expected %q among docs; got %v", want, docs)
			}
		}
		if anyDoc(docs, "on Bread { product") {
			t.Fatalf("member fragment/root re-emission regressed against the @interfaceObject subgraph: %v", docs)
		}
	})

	// simple-interface-object/case-13: `accounts { id ... on Admin { isActive } }`; subgraph b models
	// Account @interfaceObject (no Admin). The root doc (b) selects id flat on Account; `isActive`
	// resolves in a's `... on Admin` entity fetch.
	t.Run("simple-interface-object/case-13", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "simple-interface-object", "case-13")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		for _, want := range []string{
			"query { accounts { __typename id } }",
			"_entities(representations: $representations) { ... on Admin { __typename isActive } }", // __typename: C-disc re-discrimination (deliberate)
		} {
			if !anyDoc(docs, want) {
				t.Fatalf("expected %q among docs; got %v", want, docs)
			}
		}
		if anyDoc(docs, "on Admin { accounts") {
			t.Fatalf("root field `accounts` re-emitted inside the member fragment: %v", docs)
		}
	})
}

// TestNewPathDistributedRoots pins the distributed-root / root-resolved-leaf attribution fix: a goal
// whose PATH-CONSISTENT walk resolves in a root group at a DEEP position is recorded against that root
// group, and emitFields materializes its ancestor field chain in that root document -- instead of the
// whole shape collapsing onto one arbitrary root subgraph (shared-root) or a root-resolved leaf being
// dumped into a sibling's jump group (parent-entity-call). Fallback (foreign-root) and member-scoped
// walks keep the inherit behavior -- pinned by the fed*-external and union-intersection witnesses.
func TestNewPathDistributedRoots(t *testing.T) {
	// shared-root/case-01: `product { id name{...} category{...} price{...} }` -- one shared root field,
	// three subgraphs each owning DIFFERENT children. Three root fetches, each selecting only what its
	// subgraph declares.
	t.Run("shared-root/case-01", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "shared-root", "case-01")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		if len(docs) != 3 {
			t.Fatalf("want three per-subgraph root fetches, got %d: %v", len(docs), docs)
		}
		for _, want := range []string{
			"query { product { name { id brand model } } }",
			"query { product { price { id amount currency } } }",
			"category { id name }",
		} {
			if !anyDoc(docs, want) {
				t.Fatalf("expected %q among docs; got %v", want, docs)
			}
		}
		// The collapse signature: one root doc selecting a child its subgraph does not declare.
		if anyDoc(docs, "name { id brand model } category") && anyDoc(docs, "price { id") && len(docs) == 1 {
			t.Fatalf("distributed root collapsed onto one subgraph again: %v", docs)
		}
	})

	// parent-entity-call/case-01: `category.id` resolves in the ROOT subgraph a while `category.details`
	// sits behind the jump to c (whose Category has no id). id must land in a's root doc, materialized
	// under `products { category { ... } }`, and NOT inside c's entity fetch.
	t.Run("parent-entity-call/case-01", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "parent-entity-call", "case-01")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		var root string
		for _, d := range docs {
			if !strings.Contains(d, "_entities") {
				root = d
			}
		}
		for _, want := range []string{"id", "pid", "category { id }"} {
			if !strings.Contains(root, want) {
				t.Fatalf("root doc must select %q, got %q", want, root)
			}
		}
		if !anyDoc(docs, "... on Product { category { details { products } } }") {
			t.Fatalf("entity fetch must carry the details chain without Category.id; docs=%v", docs)
		}
	})
}

// TestNewPathConflationCorpus runs the KEY conflation-family corpus cases through the parallel
// obligation-driven path (via audit.LoadCaseForTest) and pins each case's ACTUAL current behaviour on
// that path -- the flip-readiness measurement the wave-1b brief asked for. Each assertion block states
// FIXED / PARTIAL / BROKEN honestly and trips when the behaviour changes, so this doubles as the
// per-class flip gate. Full findings in docs/planner-v2/m15-flip-report.md.
func TestNewPathConflationCorpus(t *testing.T) {
	// union-intersection/case-04 -- FIXED, re-pinned at the M2 class-D wave (D6pp + D11.7). The KEY
	// criterion holds (correct union members; never the wrong-position aMedia/bMedia siblings), and
	// the DISTRIBUTED member now expands per declaring subgraph: subgraph a's fetch carries the
	// members a can produce (Book/Song -- Movie would be an unknown type there, the class-D 422), and
	// a SECOND root fetch re-enters the shared `viewer` in subgraph b for `... on Movie` at the
	// media/book positions (b's ViewerMedia = {Book, Movie}). Dead members (Movie under `song`,
	// Song under `book`) are D6pp-exempt and appear in NEITHER document.
	t.Run("union-intersection/case-04_fixed", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "union-intersection", "case-04")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		if len(docs) != 2 {
			t.Fatalf("want the a-fetch plus the Movie re-entry via b, got %d: %v", len(docs), docs)
		}
		var aDoc, bDoc string
		for _, d := range docs {
			if strings.Contains(d, "... on Movie") {
				bDoc = d
			} else {
				aDoc = d
			}
		}
		for _, want := range []string{
			"query { viewer { media {",
			"... on Song { title }", "... on Book { title }",
		} {
			if !strings.Contains(aDoc, want) {
				t.Fatalf("expected %q in subgraph a's viewer fetch; doc=%s", want, aDoc)
			}
		}
		if strings.Contains(aDoc, "Movie") {
			t.Fatalf("subgraph a does not declare Movie -- its fragment must not appear there (class-D signature); doc=%s", aDoc)
		}
		for _, want := range []string{
			"query { viewer { media { __typename ... on Movie { title } }",
			"book { __typename ... on Movie { title } }",
		} {
			if !strings.Contains(bDoc, want) {
				t.Fatalf("expected %q in subgraph b's Movie re-entry fetch; doc=%s", want, bDoc)
			}
		}
		if anyDoc(docs, "aMedia") || anyDoc(docs, "bMedia") || anyDoc(docs, "query { media {") {
			t.Fatalf("residual regressed: a wrong-position/mis-rooted selection reappeared; docs=%v", docs)
		}
	})

	// abstract-types/case-04 -- FIXED. `{ products { sku ... on Book { sku } ... on Magazine { sku } } }`
	// lowers to a single clean, valid document: interface-level `sku` plus `__typename`-gated member
	// fragments, no dropped or duplicated selection. A genuine conflation fix on the new path.
	t.Run("abstract-types/case-04_fixed", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "abstract-types", "case-04")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		if len(docs) != 1 {
			t.Fatalf("want one clean fetch, got %d: %v", len(docs), docs)
		}
		want := "query { products { __typename sku ... on Book { sku } ... on Magazine { sku } } }"
		if docs[0] != want {
			t.Fatalf("abstract-types/case-04 drifted:\n got: %s\nwant: %s", docs[0], want)
		}
	})

	// requires-with-fragments/case-01 -- FIXED (D3p typename-terminal coverage goal + __typename fetch
	// selection). The operation selects `data { __typename }` on Entity: `data` is a composite whose only
	// child is a __typename meta-field, so under the plain-leaf goal rule no goal covered it and the new
	// path dropped it (emitting only `{ b { id } bb { id } }` -- a data-loss defect). D3p makes the
	// typename-terminal `data` field a resolution leaf goal, and emitFields now selects its `__typename`
	// into the entity fetch, so `data { __typename }` is resolved via its `_entities` jump. Trips if the
	// coverage regresses.
	t.Run("requires-with-fragments/case-01_fixed-data-covered", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "requires-with-fragments", "case-01")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		if !anyDoc(docs, "data { __typename }") {
			t.Fatalf("typename-terminal `data` must be selected `data { __typename }` in a fetch; docs=%v", docs)
		}
	})

	// typename/case-01 -- FIXED (D3p typename-terminal coverage goal + __typename fetch selection).
	// `{ union { __typename typename: __typename } }` selects a union field whose only children are
	// __typename (aliased). Under the plain-leaf rule no goal covered `union`, so the plan emitted ZERO
	// fetch documents. D3p makes `union` a resolution-leaf goal and emitFields selects `__typename` once
	// into its fetch; the two client response keys (`__typename`, `typename`) both read that value in the
	// response object. Exactly one root fetch, selecting `union { __typename }`.
	t.Run("typename/case-01_fixed-union-typename-covered", func(t *testing.T) {
		docs, err := lowerCorpusNewPath(t, "typename", "case-01")
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		if len(docs) != 1 || docs[0] != "query { union { __typename } }" {
			t.Fatalf("want one root fetch `query { union { __typename } }`; got %d: %v", len(docs), docs)
		}
	})
}
