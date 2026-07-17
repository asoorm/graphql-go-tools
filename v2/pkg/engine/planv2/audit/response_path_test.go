package audit

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
)

// a7def is a tiny composed schema with a sibling-entity shape (order.buyer / order.seller both User).
func a7def(t *testing.T) *ast.Document {
	t.Helper()
	d := unsafeparser.ParseGraphqlDocumentString(
		"schema { query: Query } type Query { order: Order } type Order { buyer: User seller: User } type User { id: ID rating: Int }")
	if err := asttransform.MergeDefinitionWithBaseSchema(&d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func a7entityFetch(respPath string, fp []resolve.FetchItemPathElement, doc string) *resolve.FetchItem {
	sf := &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{
		RequiresEntityFetch: true,
		QueryPlan:           &resolve.QueryPlan{Query: doc},
	}}
	return resolve.FetchItemWithPath(sf, respPath, fp...)
}

// TestAssertion7_ResponsePathOracle pins assertion 7's three checks: a legitimate per-position entity
// fetch passes; a fetch mis-attributed to a non-requested position fails (A); a FetchPath that disagrees
// with the ResponsePath fails (B); an unrelated concrete member type at the position fails (C).
func TestAssertion7_ResponsePathOracle(t *testing.T) {
	def := a7def(t)
	const op = "{ order { buyer { rating } seller { rating } } }"
	const userDoc = "query($representations: [_Any!]!) { _entities(representations: $representations) { ... on User { rating } } }"

	objHop := func(field, typ string) resolve.FetchItemPathElement {
		return resolve.FetchItemPathElement{Kind: resolve.FetchItemPathElementKindObject, Path: []string{field}, TypeNames: []string{typ}}
	}

	t.Run("legitimate per-position entity fetch at order.seller passes", func(t *testing.T) {
		fp := []resolve.FetchItemPathElement{objHop("order", "Query"), objHop("seller", "Order")}
		if err := validateResponsePaths([]*resolve.FetchItem{a7entityFetch("order.seller", fp, userDoc)}, op, def); err != nil {
			t.Fatalf("legitimate order.seller fetch must pass assertion 7: %v", err)
		}
	})

	t.Run("(A) fetch attached at a non-requested position fails", func(t *testing.T) {
		fp := []resolve.FetchItemPathElement{objHop("order", "Query"), objHop("shipper", "Order")}
		if err := validateResponsePaths([]*resolve.FetchItem{a7entityFetch("order.shipper", fp, userDoc)}, op, def); err == nil {
			t.Fatal("fetch at unrequested position order.shipper must FAIL assertion 7 (A)")
		}
	})

	t.Run("(B) FetchPath disagreeing with ResponsePath fails", func(t *testing.T) {
		fp := []resolve.FetchItemPathElement{objHop("order", "Query"), objHop("buyer", "Order")}
		if err := validateResponsePaths([]*resolve.FetchItem{a7entityFetch("order.seller", fp, userDoc)}, op, def); err == nil {
			t.Fatal("FetchPath [order,buyer] with ResponsePath order.seller must FAIL assertion 7 (B)")
		}
	})

	t.Run("(C) unrelated concrete member type at the position fails", func(t *testing.T) {
		const productDoc = "query($representations: [_Any!]!) { _entities(representations: $representations) { ... on Product { rating } } }"
		defWithProduct := unsafeparser.ParseGraphqlDocumentString(
			"schema { query: Query } type Query { order: Order } type Order { buyer: User seller: User } type User { id: ID rating: Int } type Product { rating: Int }")
		if err := asttransform.MergeDefinitionWithBaseSchema(&defWithProduct); err != nil {
			t.Fatal(err)
		}
		fp := []resolve.FetchItemPathElement{objHop("order", "Query"), objHop("seller", "Order")}
		if err := validateResponsePaths([]*resolve.FetchItem{a7entityFetch("order.seller", fp, productDoc)}, op, &defWithProduct); err == nil {
			t.Fatal("entity fetch resolving unrelated Product at User position must FAIL assertion 7 (C)")
		}
	})

	t.Run("empty ResponsePath on an entity fetch fails", func(t *testing.T) {
		if err := validateResponsePaths([]*resolve.FetchItem{a7entityFetch("", nil, userDoc)}, op, def); err == nil {
			t.Fatal("entity fetch with empty ResponsePath must FAIL assertion 7")
		}
	})
}
