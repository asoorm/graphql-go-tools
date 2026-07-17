package hypergraph

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// D7pp(4) fragment-conditioned coordinates -- the AX-REQ-COND rendering clause (MG-2 closure).
// filterConditionalRequires prunes, from a raw @requires selection, fragment-conditioned
// coordinates the given resolvability predicate rejects: a conditioned coordinate resolvable
// NOWHERE must render NOWHERE (FS-PLAN-1 dominates the axiom's rendering clause -- the un-pruned
// render was an invalid fetch document; conformance witness
// FS-REQ-9/requires-conditional/probe-unresolvable). Unconditional coordinates are never pruned;
// an unconditional composite left childless renders `{ __typename }` so the document stays valid
// and the enclosing object still rides the representation; a selection with no fragments (or
// nothing to prune) is returned BYTE-IDENTICAL, argument groups included.
func TestFilterConditionalRequires(t *testing.T) {
	schema := unsafeparser.ParseGraphqlDocumentString(`
		type Product { id: ID! media: Media price: Float data: Data }
		interface Media { kind: String }
		type Book implements Media { kind: String title: String subtitle: String }
		type Data { foo: String bars: Bar }
		interface Bar { x: String }
	`)

	none := func(string, string) bool { return false }
	all := func(string, string) bool { return true }
	only := func(allowed map[string]bool) func(string, string) bool {
		return func(typeName, fieldName string) bool { return allowed[typeName+"."+fieldName] }
	}

	tests := []struct {
		name       string
		sel        string
		resolvable func(string, string) bool
		want       string
	}{
		{
			// The probe shape: the conditioned coordinate resolves nowhere -- the fragment renders
			// nowhere, and the emptied unconditional composite renders { __typename }.
			name: "probe-unresolvable prunes branch, composite keeps __typename",
			sel:  "media { ... on Book { title } }", resolvable: none,
			want: "media { __typename }",
		},
		{
			// Resolvable-somewhere branches render exactly as before -- byte-identical.
			name: "resolvable branch untouched",
			sel:  "media { ... on Book { title } }", resolvable: all,
			want: "media { ... on Book { title } }",
		},
		{
			// Per-coordinate pruning inside a fragment: the resolvable sibling survives.
			name:       "partial prune inside fragment",
			sel:        "media { ... on Book { title subtitle } }",
			resolvable: only(map[string]bool{"Book.subtitle": true}),
			want:       "media { ... on Book { subtitle } }",
		},
		{
			// Argument groups on unconditional coordinates survive a rebuild verbatim.
			name: "arguments preserved through rebuild",
			sel:  `price(currency: "USD") media { ... on Book { title } }`, resolvable: none,
			want: `price(currency: "USD") media { __typename }`,
		},
		{
			// No fragments: the fast path returns the original string, whatever the predicate.
			name: "fragment-free selection byte-identical",
			sel:  "comments(limit: 3) { authorId }", resolvable: none,
			want: "comments(limit: 3) { authorId }",
		},
		{
			// A fragment whose every coordinate (recursively) prunes drops whole; unconditional
			// siblings inside the composite keep it non-empty (no __typename injection needed).
			name:       "fragment drops, unconditional sibling keeps composite",
			sel:        "data { foo ... on Bar { x } }",
			resolvable: only(map[string]bool{"Data.foo": true, "Data.bars": true}),
			want:       "data { foo }",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterConditionalRequires(tc.sel, &schema, "Product", tc.resolvable)
			if got != tc.want {
				t.Fatalf("filterConditionalRequires(%q) = %q, want %q", tc.sel, got, tc.want)
			}
		})
	}
}
