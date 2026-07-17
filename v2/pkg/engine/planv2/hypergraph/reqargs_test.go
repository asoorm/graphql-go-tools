package hypergraph

import (
	"reflect"
	"testing"
)

// TestParseSelectionArgumentAware pins the D7 tokenizer's argument-blindness (A-3): a key/@requires
// selection carrying field arguments (`price(currency: "USD")`, `comments(limit: 3) { authorId }`)
// must parse to the bare field NAMES and nesting -- the argument group is skipped whole, so edge
// identity does not depend on argument values. Before the fix strings.Fields split
// `price(currency: "USD")` into three bogus tokens, making the requires selection unresolvable.
func TestParseSelectionArgumentAware(t *testing.T) {
	names := func(fs []keyField) []string {
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.Name
		}
		return out
	}
	tests := []struct {
		in       string
		wantTop  []string
		checkSub func(t *testing.T, fs []keyField)
	}{
		{in: `upc`, wantTop: []string{"upc"}},
		{in: `price(currency: "USD") weight`, wantTop: []string{"price", "weight"}},
		{
			in:      `category { averagePrice(currency: "USD") }`,
			wantTop: []string{"category"},
			checkSub: func(t *testing.T, fs []keyField) {
				if got := names(fs[0].Sub); !reflect.DeepEqual(got, []string{"averagePrice"}) {
					t.Errorf("nested: got %v, want [averagePrice]", got)
				}
			},
		},
		{
			in:      `comments(limit: 3) { authorId }`,
			wantTop: []string{"comments"},
			checkSub: func(t *testing.T, fs []keyField) {
				if got := names(fs[0].Sub); !reflect.DeepEqual(got, []string{"authorId"}) {
					t.Errorf("nested: got %v, want [authorId]", got)
				}
			},
		},
		// A `)` inside a string literal must not terminate the argument group early.
		{in: `price(currency: "US)D") weight`, wantTop: []string{"price", "weight"}},
	}
	for _, tc := range tests {
		fs := parseSelection(tc.in)
		if got := names(fs); !reflect.DeepEqual(got, tc.wantTop) {
			t.Errorf("parseSelection(%q) top-level = %v, want %v", tc.in, got, tc.wantTop)
		}
		if tc.checkSub != nil {
			tc.checkSub(t, fs)
		}
	}
}
