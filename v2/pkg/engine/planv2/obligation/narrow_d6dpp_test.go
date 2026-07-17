package obligation

// narrow_d6dpp_test.go -- D6pp position-possible member sets (FORMAL_SPEC D6pp, M2 class-D wave).
//
// Four verdict witnesses, mirroring the audit's class-D fixtures:
//   1. DEAD entity member (union-interface-distributed/case-02 shape): a member impossible at the
//      position in EVERY capable subgraph is exempt even though it is an entity -- the old entity
//      gate wrongly kept it a cover requirement, and it was then served through a D10 fallback
//      route that emitted `... on Oven` against a subgraph whose Node can never be an Oven.
//   2. Abstract-C possibility (union-interface-distributed/case-05 shape): `... on WithWarranty`
//      under Node was narrowed out because the NAME "WithWarranty" is not a member of Node -- but a
//      possible member (Toaster) implements it, so it must NOT be exempt.
//   3. Concrete-position deadness (union-intersection child-type-mismatch shape): a subgraph whose
//      own output type for the position is the CONCRETE Book contributes {Book}, so Song is dead
//      at the position even though Song is an entity elsewhere; Movie stays coverable (possible
//      via the other subgraph -- the distributed member).
//   4. Unknown member set (TOP, the @interfaceObject-like shape): a subgraph that models the
//      position's abstract type without any local TypeMove member knowledge must count every
//      member possible -- no exemption may be derived from ignorance.

import (
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
)

const uidSchema = `
schema { query: Query }
type Query { nodes: [Node] products: [Product] }
interface Node { id: ID! }
interface WithWarranty { warranty: Int }
union Product = Oven | Toaster
type Oven implements Node & WithWarranty { id: ID! warranty: Int }
type Toaster implements Node & WithWarranty { id: ID! warranty: Int }
`

// buildUIDH mirrors union-interface-distributed: subgraph a resolves Query.nodes with
// Mem_a(Node) = {Toaster}; Oven exists in a only as a Product member (with an id key), and
// implements Node only in b (reached by an EntityJump a->b). Oven is therefore an ENTITY that is
// nonetheless impossible at the `nodes` position.
func buildUIDH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "a")
	b.SetSubgraphName(2, "b")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})

	qNodes := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "nodes", Subgraph: 1})
	nodeA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Node", Subgraph: 1})
	toasterA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Toaster", Subgraph: 1})
	toasterWarranty := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Toaster", Field: "warranty", Subgraph: 1})
	wwA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "WithWarranty", Subgraph: 1})
	wwWarrantyA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "WithWarranty", Field: "warranty", Subgraph: 1})

	qProducts := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "products", Subgraph: 1})
	productA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Product", Subgraph: 1})
	ovenA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Oven", Subgraph: 1})
	ovenAID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Oven", Field: "id", Subgraph: 1})

	ovenB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Oven", Subgraph: 2})
	ovenBWarranty := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Oven", Field: "warranty", Subgraph: 2})
	nodeB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Node", Subgraph: 2})
	wwB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "WithWarranty", Subgraph: 2})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "nodes", Head: qNodes, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: nodeA, Tails: []hypergraph.NodeID{qNodes}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Toaster", Head: toasterA, Tails: []hypergraph.NodeID{nodeA}, Weight: 1, Members: []string{"Toaster"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Toaster", Head: toasterA, Tails: []hypergraph.NodeID{wwA}, Weight: 1, Members: []string{"Toaster"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "warranty", Head: toasterWarranty, Tails: []hypergraph.NodeID{toasterA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "warranty", Head: wwWarrantyA, Tails: []hypergraph.NodeID{wwA}, Weight: 1})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "products", Head: qProducts, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: productA, Tails: []hypergraph.NodeID{qProducts}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Oven", Head: ovenA, Tails: []hypergraph.NodeID{productA}, Weight: 1, Members: []string{"Oven", "Toaster"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Toaster", Head: toasterA, Tails: []hypergraph.NodeID{productA}, Weight: 1, Members: []string{"Oven", "Toaster"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: ovenAID, Tails: []hypergraph.NodeID{ovenA}, Weight: 1})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: ovenB, Tails: []hypergraph.NodeID{ovenAID}, KeyTails: []hypergraph.NodeID{ovenAID}, Weight: 1010})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "warranty", Head: ovenBWarranty, Tails: []hypergraph.NodeID{ovenB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Oven", Head: ovenB, Tails: []hypergraph.NodeID{nodeB}, Weight: 1, Members: []string{"Oven"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Oven", Head: ovenB, Tails: []hypergraph.NodeID{wwB}, Weight: 1, Members: []string{"Oven"}})
	return b.Build()
}

// TestClassifyNarrowing_DeadEntityMemberExempt -- D6pp verdict 1.
func TestClassifyNarrowing_DeadEntityMemberExempt(t *testing.T) {
	h := buildUIDH(t)
	op, def := parseOp(t, uidSchema, `{ nodes { ... on Toaster { warranty } ... on Oven { id } } }`)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}
	if !tree.Exempt(goalFor(t, tree, "Oven", "id")) {
		t.Fatal("Oven.id under the nodes position must be exempt: the only capable subgraph's Node is {Toaster}, so the position can never produce an Oven (dead member, D6pp verdict 1) -- entity-ness does not resurrect it")
	}
	if tree.Exempt(goalFor(t, tree, "Toaster", "warranty")) {
		t.Fatal("Toaster.warranty is possible at the position and must NOT be exempt")
	}
}

// TestClassifyNarrowing_AbstractRefinementPossible -- D6pp possibility for an abstract C.
func TestClassifyNarrowing_AbstractRefinementPossible(t *testing.T) {
	h := buildUIDH(t)
	op, def := parseOp(t, uidSchema, `{ nodes { ... on WithWarranty { warranty } } }`)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Exempt(goalFor(t, tree, "WithWarranty", "warranty")) {
		t.Fatal("`... on WithWarranty` under nodes must NOT be exempt: the possible member Toaster implements WithWarranty (abstract-C possibility, D6pp) -- the name-membership test wrongly nulled it")
	}
}

const ctmSchema = `
schema { query: Query }
type Query { viewer: Viewer }
type Viewer { book: ViewerMedia }
union ViewerMedia = Book | Song | Movie
type Book { id: ID! title: String }
type Song { id: ID! title: String }
type Movie { id: ID! title: String }
`

// buildCTMH mirrors the union-intersection child-type-mismatch: Viewer.book is the CONCRETE Book
// in subgraph a but the union ViewerMedia = {Book, Movie} in subgraph b. Book is an entity
// (jumps both ways). Song appears in the composed union only -- impossible at the position via
// both subgraphs.
func buildCTMH(t *testing.T) *hypergraph.Hypergraph {
	t.Helper()
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "a")
	b.SetSubgraphName(2, "b")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})

	qViewerA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "viewer", Subgraph: 1})
	viewerA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Viewer", Subgraph: 1})
	bookFieldA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Viewer", Field: "book", Subgraph: 1})
	bookA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Book", Subgraph: 1})
	bookATitle := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Book", Field: "title", Subgraph: 1})
	bookAID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Book", Field: "id", Subgraph: 1})

	qViewerB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "viewer", Subgraph: 2})
	viewerB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Viewer", Subgraph: 2})
	bookFieldB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Viewer", Field: "book", Subgraph: 2})
	vmB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "ViewerMedia", Subgraph: 2})
	bookB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Book", Subgraph: 2})
	movieB := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Movie", Subgraph: 2})
	bookBTitle := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Book", Field: "title", Subgraph: 2})
	bookBID := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Book", Field: "id", Subgraph: 2})
	movieBTitle := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Movie", Field: "title", Subgraph: 2})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "viewer", Head: qViewerA, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: viewerA, Tails: []hypergraph.NodeID{qViewerA}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "book", Head: bookFieldA, Tails: []hypergraph.NodeID{viewerA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: bookA, Tails: []hypergraph.NodeID{bookFieldA}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "title", Head: bookATitle, Tails: []hypergraph.NodeID{bookA}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: bookAID, Tails: []hypergraph.NodeID{bookA}, Weight: 1})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "viewer", Head: qViewerB, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: viewerB, Tails: []hypergraph.NodeID{qViewerB}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "book", Head: bookFieldB, Tails: []hypergraph.NodeID{viewerB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: vmB, Tails: []hypergraph.NodeID{bookFieldB}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Book", Head: bookB, Tails: []hypergraph.NodeID{vmB}, Weight: 1, Members: []string{"Book", "Movie"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeTypeMove, Label: "Movie", Head: movieB, Tails: []hypergraph.NodeID{vmB}, Weight: 1, Members: []string{"Book", "Movie"}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "title", Head: bookBTitle, Tails: []hypergraph.NodeID{bookB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "id", Head: bookBID, Tails: []hypergraph.NodeID{bookB}, Weight: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "title", Head: movieBTitle, Tails: []hypergraph.NodeID{movieB}, Weight: 1})

	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: bookB, Tails: []hypergraph.NodeID{bookAID}, KeyTails: []hypergraph.NodeID{bookAID}, Weight: 1010})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeEntityJump, Head: bookA, Tails: []hypergraph.NodeID{bookBID}, KeyTails: []hypergraph.NodeID{bookBID}, Weight: 1010})
	return b.Build()
}

// TestClassifyNarrowing_ConcretePositionDeadness -- D6pp position sets from the subgraph-LOCAL output
// type: pos_a(Viewer.book) = {Book} (concrete), pos_b = {Book, Movie}.
func TestClassifyNarrowing_ConcretePositionDeadness(t *testing.T) {
	h := buildCTMH(t)
	op, def := parseOp(t, ctmSchema, `{ viewer { book { ... on Song { title } ... on Movie { title } ... on Book { title } } } }`)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}
	if !tree.Exempt(goalFor(t, tree, "Song", "title")) {
		t.Fatal("Song.title under the book position must be exempt: pos_a={Book} (concrete local output type), pos_b={Book,Movie} -- Song is dead at the position (D6pp verdict 1)")
	}
	if tree.Exempt(goalFor(t, tree, "Movie", "title")) {
		t.Fatal("Movie.title is possible via subgraph b (distributed member) and must NOT be exempt")
	}
	if tree.Exempt(goalFor(t, tree, "Book", "title")) {
		t.Fatal("Book.title is possible via both subgraphs and must NOT be exempt")
	}
}

const unknownMemberSchema = `
schema { query: Query }
type Query { item: Face }
interface Face { x: String }
type Impl implements Face { x: String }
`

// TestClassifyNarrowing_UnknownMemberSetIsTop -- D6pp TOP: a subgraph that models the abstract position
// with NO local TypeMove member knowledge (the @interfaceObject shape) must count every member
// possible -- the old name-membership intersection wrongly exempted the member.
func TestClassifyNarrowing_UnknownMemberSetIsTop(t *testing.T) {
	b := hypergraph.NewBuilder()
	b.SetSubgraphName(1, "a")
	r := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeRoot, Field: "query"})
	qItem := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Query", Field: "item", Subgraph: 1})
	faceA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Face", Subgraph: 1})
	faceX := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Face", Field: "x", Subgraph: 1})
	implX := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeField, Type: "Impl", Field: "x", Subgraph: 1})
	implA := b.AddNode(hypergraph.Node{Kind: hypergraph.NodeObject, Type: "Impl", Subgraph: 1})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "item", Head: qItem, Tails: []hypergraph.NodeID{r}, Weight: 1000})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeDescent, Head: faceA, Tails: []hypergraph.NodeID{qItem}})
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "x", Head: faceX, Tails: []hypergraph.NodeID{faceA}, Weight: 1})
	// Impl exists as a type with a field but there is NO TypeMove (Face,a) -> (Impl,a): the member
	// set of Face in a is UNKNOWN, not empty-and-closed.
	b.AddEdge(hypergraph.Edge{Kind: hypergraph.EdgeField, Label: "x", Head: implX, Tails: []hypergraph.NodeID{implA}, Weight: 1})
	h := b.Build()

	op, def := parseOp(t, unknownMemberSchema, `{ item { ... on Impl { x } } }`)
	tree, err := Build(op, def, "", h)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Exempt(goalFor(t, tree, "Impl", "x")) {
		t.Fatal("Impl.x must NOT be exempt: subgraph a's member knowledge for Face is unknown (TOP), and no exemption may be derived from ignorance (D6pp)")
	}
}
