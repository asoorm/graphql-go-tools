package lower_test

import (
	"strings"
	"testing"
)

// Class-C witnesses (M2 class-C wave): @interfaceObject member flattening (FORMAL_SPEC D3io + D11.9)
// on real corpus fixtures. A member-refined field that lives ONLY on an @interfaceObject subgraph is
// covered by the INTERFACE-typed node and must print at the interface level of the document that
// resolves it -- `... on User { username }` flattens to `username` on the interface-object type the
// subgraph declares (the member fragment would be an invalid document there). The response tree keeps
// the member gate; flattening is a fetch-document concern.

// D3pppp -- abstract-position member expansion. `products { id reviews { id } }` (abstract-types/case-07):
// `products` yields the interface Product from the products subgraph, which has no `reviews`;
// Book.reviews / Magazine.reviews live in the reviews subgraph. The obligation tree is planned in the
// member-exploded form: the root fetch selects the member keys under `... on Book` / `... on Magazine`,
// and ONE entity fetch per member resolves `reviews { id }` in the reviews subgraph.
func TestClassC_MemberExpansion_InterfaceFieldFansOutPerMember(t *testing.T) {
	docs, err := lowerCorpusNewPath(t, "abstract-types", "case-07")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !anyDoc(docs, "_entities(representations: $representations) { ... on Book { reviews { id } } }") {
		t.Fatalf("Book member must resolve reviews via its own entity jump into reviews, got: %v", docs)
	}
	if !anyDoc(docs, "_entities(representations: $representations) { ... on Magazine { reviews { id } } }") {
		t.Fatalf("Magazine member must resolve reviews via its own entity jump into reviews, got: %v", docs)
	}
	// The products root fetch carries the member keys and never selects reviews itself.
	var rootDoc string
	for _, d := range docs {
		if strings.Contains(d, "products") && !strings.Contains(d, "_entities") {
			rootDoc = d
		}
	}
	if rootDoc == "" {
		t.Fatalf("no root document enters products: %v", docs)
	}
	if strings.Contains(rootDoc, "reviews") {
		t.Fatalf("the products root fetch must not select reviews (products does not declare it): %s", rootDoc)
	}
	for _, want := range []string{"... on Book { id", "... on Magazine { id", "__typename"} {
		if !strings.Contains(rootDoc, want) {
			t.Fatalf("the products root fetch must carry %q for the member jumps, got: %s", want, rootDoc)
		}
	}
}

// case-03: aliased root selections (`book: similar(id: "p1")`, `magazine: similar(id: "p2")`) whose
// `delivery` (declared on Product only in the inventory subgraph, @requires dimensions) member-expands
// per position. The two aliases must stay DISTINCT selections in the root document (client aliases
// print), and each member jump carries the @requires dimensions in its representation.
func TestClassC_MemberExpansion_AliasedPositionsAndRequires(t *testing.T) {
	docs, err := lowerCorpusNewPath(t, "abstract-types", "case-03")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var rootDoc string
	for _, d := range docs {
		if strings.Contains(d, "similar") {
			rootDoc = d
		}
	}
	if rootDoc == "" {
		t.Fatalf("no root document enters similar: %v", docs)
	}
	if !strings.Contains(rootDoc, "book: similar(") || !strings.Contains(rootDoc, "magazine: similar(") {
		t.Fatalf("both client aliases must print as distinct root selections: %s", rootDoc)
	}
	if !anyDoc(docs, "... on Book { delivery(") {
		t.Fatalf("delivery must member-expand onto Book in the inventory jump, got: %v", docs)
	}
}

// case-06: `users { ... on User { age id name username } id name }` -- root in subgraph a (the
// interface-declaring subgraph); `username` lives only on b's `NodeWithName @interfaceObject`. The
// flattened goal re-roots into the b entity fetch (D11.9 jump-group placement): username prints flat
// under the interface entry fragment, never under `... on User`.
func TestClassC_InterfaceObjectFlatten_JumpGroup(t *testing.T) {
	docs, err := lowerCorpusNewPath(t, "simple-interface-object", "case-06")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !anyDoc(docs, "_entities(representations: $representations) { ... on NodeWithName { username } }") {
		t.Fatalf("username must flatten onto the NodeWithName entity entry in subgraph b, got: %v", docs)
	}
	if anyDoc(docs, "... on User { username }") {
		t.Fatalf("username must NOT print under a User member fragment (User is undeclared in b), got: %v", docs)
	}
}

// case-05: same selection but rooted at b's `anotherUsers` (the interface-object subgraph itself).
// The flattened goal resolves in the SAME group as its parent position (D11.9 same-document
// placement): username prints as a direct selection of the interface-typed position in the b ROOT
// document, while the concrete-member fields (age) still travel to subgraph a under `... on User`.
func TestClassC_InterfaceObjectFlatten_SameDocument(t *testing.T) {
	docs, err := lowerCorpusNewPath(t, "simple-interface-object", "case-05")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var rootB string
	for _, d := range docs {
		if strings.Contains(d, "anotherUsers") {
			rootB = d
		}
	}
	if rootB == "" {
		t.Fatalf("no root document enters anotherUsers: %v", docs)
	}
	if strings.Contains(rootB, "... on User") {
		t.Fatalf("the b root document must not carry a User member fragment (User is undeclared in b): %s", rootB)
	}
	if !strings.Contains(rootB, "username") {
		t.Fatalf("username must print flat in the b root document, got: %s", rootB)
	}
	if !anyDoc(docs, "... on User { __typename age") { // __typename: C-disc re-discrimination (deliberate)
		t.Fatalf("age must still resolve under `... on User` in the concrete-knowing subgraph a, got: %v", docs)
	}
}

// case-09: `accounts { ... on Admin { name } }` -- `name` lives only on b's `Account @interfaceObject`;
// b also owns the `accounts` root. Same-document flattening: name prints flat on the Account position
// in the b root document (no `... on Admin`, which b does not declare).
func TestClassC_InterfaceObjectFlatten_MemberOnInterfaceObjectRoot(t *testing.T) {
	docs, err := lowerCorpusNewPath(t, "simple-interface-object", "case-09")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var rootB string
	for _, d := range docs {
		if strings.Contains(d, "accounts") {
			rootB = d
		}
	}
	if rootB == "" {
		t.Fatalf("no root document enters accounts: %v", docs)
	}
	if strings.Contains(rootB, "... on Admin") {
		t.Fatalf("the b root document must not carry an Admin member fragment: %s", rootB)
	}
	if !strings.Contains(rootB, "name") {
		t.Fatalf("name must print flat on the accounts position, got: %s", rootB)
	}
}
