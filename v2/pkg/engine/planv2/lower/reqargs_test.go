package lower_test

import (
	"strings"
	"testing"
)

// TestNewPathRequiresWithArgument pins the M1.5 argument-in-@requires lowering (DV-006): the source
// document that supplies an argument-bearing @requires must render the field WITH its literal argument
// (`price(currency: "USD")`, `averagePrice(currency: "USD")`) -- the fetch document is invalid against
// the subgraph schema otherwise (the required `currency` argument would be missing). The audit corpus
// only checks document validity and response shape; this test pins the literal argument value itself,
// the datum the argument-blind Tails cannot carry.
func TestNewPathRequiresWithArgument(t *testing.T) {
	docs, err := lowerCorpusNewPath(t, "requires-with-argument", "case-01")
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	// The source subgraph (b) must select the @requires prerequisites with their arguments so the values
	// ride in the entity representation to subgraph a.
	for _, want := range []string{
		`price(currency: "USD")`,
		`weight`,
		`averagePrice(currency: "USD")`,
	} {
		if !anyDoc(docs, want) {
			t.Errorf("expected some fetch document to contain %q; docs:\n%s", want, strings.Join(docs, "\n"))
		}
	}
	// The entity fetch computes the @requires-bearing fields.
	if !anyDoc(docs, "shippingEstimate") || !anyDoc(docs, "isExpensiveCategory") {
		t.Errorf("expected an entity fetch computing shippingEstimate + isExpensiveCategory; docs:\n%s", strings.Join(docs, "\n"))
	}
}

// TestNewPathRequiresArgumentConflict pins the DV-007 representation SPLIT: two @requires selecting the
// same coordinate (Product.price) with DIFFERENT argument values (USD vs EUR) cannot be served by one
// entity representation, so -- mirroring v1's FederationFieldConfigurations.HasArgumentConflictWith --
// lowering partitions the requiring fields across SEPARATE `_entities` fetches. The shared source
// document (subgraph b) must select BOTH price variants, aliasing the second so they coexist in one
// valid document; each entity fetch computes only its own consistent-binding field(s).
func TestNewPathRequiresArgumentConflict(t *testing.T) {
	docs, err := lowerCorpusNewPath(t, "requires-with-argument-conflict", "case-01")
	if err != nil {
		t.Fatalf("lower: %v (DV-007 split should plan, not fail)", err)
	}
	joined := strings.Join(docs, "\n")

	// The source subgraph must supply BOTH argument variants of price. One renders under its real name,
	// the other under an injective `_planv2req_price_*` alias (source-document aliasing).
	if !anyDoc(docs, `price(currency: "USD")`) || !anyDoc(docs, `price(currency: "EUR")`) {
		t.Fatalf("source document must select both price(USD) and price(EUR); docs:\n%s", joined)
	}
	if !strings.Contains(joined, `_planv2req_price_`) {
		t.Fatalf("the conflicting second price selection must be aliased in the source document; docs:\n%s", joined)
	}

	// The two conflicting requiring fields must be computed by SEPARATE entity fetches (not one), so
	// neither representation carries a colliding price binding.
	var seWithEUR, seWithoutEUR bool
	for _, d := range docs {
		if !strings.Contains(d, "_entities") {
			continue
		}
		if strings.Contains(d, "shippingEstimateEUR") {
			seWithEUR = true
			if strings.Contains(d, "shippingEstimate ") || strings.Contains(d, "shippingEstimate}") ||
				strings.Contains(d, "{ shippingEstimate ") {
				// shippingEstimate (USD) must NOT share the EUR entity fetch.
				t.Fatalf("shippingEstimate and shippingEstimateEUR must not share one entity fetch; doc: %s", d)
			}
		} else if strings.Contains(d, "shippingEstimate") {
			seWithoutEUR = true
		}
	}
	if !seWithEUR || !seWithoutEUR {
		t.Fatalf("expected separate entity fetches for shippingEstimate and shippingEstimateEUR; docs:\n%s", joined)
	}
	// isExpensiveCategory (no conflict) must still be computed somewhere.
	if !anyDoc(docs, "isExpensiveCategory") {
		t.Fatalf("isExpensiveCategory must be computed by some entity fetch; docs:\n%s", joined)
	}
}
