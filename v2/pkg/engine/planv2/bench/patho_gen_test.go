package bench

// patho_gen_test.go is the M15 PATHOLOGICAL-SHAPE generator: a deterministic synthetic supergraph
// family targeting a query-shape family known to stress federation planners' combinatorial
// search -- deep ABSTRACT-TYPE nesting with multiplicative plan-option growth (see
// docs/planner-v2/RESEARCH.md: type explosion -- one DownCast branch per interface implementation --
// times per-leaf multi-subgraph resolution alternatives, cartesian-multiplied across leaves).
//
// SHAPE (pure function of the params; Seed is a documented determinism knob, no randomness used):
//   - D = Levels interface types PIface0..PIface{D-1}, each with M = Impls implementations
//     PImpl{d}_0..PImpl{d}_{M-1}. Every interface declares `common: String` and (below the last
//     level) `child: PIface{d+1}` -- an interface field RETURNING the next interface, so selecting
//     through the nesting compounds one type-explosion per level.
//   - Every implementation is an ENTITY (@key "id") OWNED by R = Replicas subgraphs (owner r of
//     impl (d,j) is subgraph ((d*M+j)*R + r) mod S). All R owners resolve the impl's `common`,
//     `child` and its EXCLUSIVE scalar `only{d}_{j}` (@shareable overlapping resolution -- every
//     resolution step has R same-cost subgraph alternatives, the per-leaf option multiplier).
//   - A subgraph that resolves a field returning PIface{d} (Query.root for level 0; any owned
//     level-(d-1) impl's `child` otherwise) must be able to name every possible concrete result, so
//     it also carries pure REFERENCE STUBS for the level-d impls it does not own: key declared
//     resolvable:false, the interface-mandated fields @external (the Fed2 idiom for "may reference,
//     must jump to an owner to resolve"). Stubs keep the base key locally producible (the same
//     external-but-key D5p pattern scale_gen_test.go's ring stubs use) so entity jumps out of the
//     stub have a finite-cost tail.
//   - Subgraph 0 owns `Query { root: PIface0 }`. Subgraphs owning nothing (only possible when
//     S > D*M*R, which the benchmark grids never hit) get a `_pad{s}` Query field so no subgraph is
//     empty.
//
// WHY THIS IS THE PATHOLOGICAL CLASS: an operation walking `child` D levels deep and selecting
// interface fields forces, in a type-explosion planner, M downcast branches per level (M^D leaf
// branches at the bottom) with R resolution choices each -- a plan-option space that grows
// multiplicatively in M, D and R while the OPERATION TEXT stays linear in D (walk/leaffan) or
// linear in D with constant fragments per level (frag). planv2's settle-per-node model predicts NO
// such explosion (options never cartesian-multiply); this generator exists to measure both claims
// on the same graph.
//
// Unlike scale_gen_test.go (which hand-builds DataSourceMetadata in parallel with its SDL), this
// generator has ONE source of truth for all four benchmark legs: the self-describing Fed2 subgraph
// SDL emitted by fed2PathoSubgraphSDL (patho_export_test.go). The in-process legs derive their
// []plan.DataSource from that SDL via audit.BuildDataSources -- the SAME derivation the audit corpus
// pins as "what a composition step would produce" (interface fields claimed per declaring subgraph,
// @external partitioning, resolvable:false -> DisableEntityResolver) -- and the external legs compose
// the identical files with @apollo/composition. A topology change in the SDL emitter is therefore
// visible to every leg at once, by construction.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
)

// pathoGenParams parametrizes generatePathoSupergraph. All fields are load-bearing except Seed
// (reserved determinism knob, mirroring scaleGenParams.Seed).
type pathoGenParams struct {
	Seed      int64
	Levels    int // D: nesting depth = number of interface types (interface field returning interface)
	Impls     int // M: implementations per interface
	Subgraphs int // S: number of federation subgraphs
	Replicas  int // R: subgraphs fully owning each implementation (@shareable overlap factor)
}

// pathoSupergraph is generatePathoSupergraph's result.
type pathoSupergraph struct {
	Params      pathoGenParams
	SDLs        []string // fed2PathoSubgraphSDL per subgraph (the single source of truth)
	DataSources []plan.DataSource
	SDLBytes    int // sum of len(SDLs[i]) across all S subgraphs
	TypeCount   int // distinct composed type names (interfaces + impls + Query)
}

// pathoOwner reports whether subgraph s is one of the R owners of impl (d, j).
func (p pathoGenParams) pathoOwner(s, d, j int) bool {
	base := ((d*p.Impls + j) * p.Replicas) % p.Subgraphs
	for r := 0; r < p.Replicas; r++ {
		if (base+r)%p.Subgraphs == s {
			return true
		}
	}
	return false
}

// pathoIfaceName / pathoImplName / pathoOnlyField are the generated type/field naming scheme,
// shared with patho_export_test.go and the op builders below.
func pathoIfaceName(d int) string      { return fmt.Sprintf("PIface%d", d) }
func pathoImplName(d, j int) string    { return fmt.Sprintf("PImpl%d_%d", d, j) }
func pathoOnlyField(d, j int) string   { return fmt.Sprintf("only%d_%d", d, j) }
func pathoHasChild(d, levels int) bool { return d < levels-1 }
func pathoPadField(s int) string       { return fmt.Sprintf("_pad%d", s) }

// pathoOwnsAnyAtLevel reports whether subgraph s owns at least one impl at level d.
func (p pathoGenParams) pathoOwnsAnyAtLevel(s, d int) bool {
	for j := 0; j < p.Impls; j++ {
		if p.pathoOwner(s, d, j) {
			return true
		}
	}
	return false
}

// pathoNeedsStubs reports whether subgraph s must declare ALL level-d impls (stubs for the ones it
// does not own): true when s resolves a field returning PIface{d} -- Query.root (d==0, s==0) or an
// owned level-(d-1) impl's `child`.
func (p pathoGenParams) pathoNeedsStubs(s, d int) bool {
	if d == 0 {
		return s == 0
	}
	return p.pathoOwnsAnyAtLevel(s, d-1)
}

// generatePathoSupergraph builds the pathological-shape supergraph described in the file doc: it
// emits the per-subgraph Fed2 SDL (fed2PathoSubgraphSDL) and derives the in-process
// []plan.DataSource from it via audit.BuildDataSources (the pinned composition-faithful metadata
// derivation -- see the file doc's single-source-of-truth note).
func generatePathoSupergraph(tb testing.TB, p pathoGenParams) *pathoSupergraph {
	tb.Helper()
	if p.Levels < 1 || p.Impls < 2 || p.Subgraphs < 2 || p.Replicas < 1 || p.Replicas > p.Subgraphs {
		tb.Fatalf("generatePathoSupergraph: invalid params %+v (need Levels>=1, Impls>=2, Subgraphs>=2, 1<=Replicas<=Subgraphs)", p)
	}

	c := audit.Case{Name: pathoCaseName(p), Definition: pathoSchemaSDL(p)}
	sdls := make([]string, p.Subgraphs)
	sdlBytes := 0
	for s := 0; s < p.Subgraphs; s++ {
		sdls[s] = fed2PathoSubgraphSDL(s, p)
		sdlBytes += len(sdls[s])
		c.Subgraphs = append(c.Subgraphs, audit.Subgraph{Name: fmt.Sprintf("sg%d", s), SDL: sdls[s]})
	}
	dataSources, _, err := audit.BuildDataSources(c)
	if err != nil {
		tb.Fatalf("generatePathoSupergraph: audit.BuildDataSources: %v", err)
	}

	return &pathoSupergraph{
		Params:      p,
		SDLs:        sdls,
		DataSources: dataSources,
		SDLBytes:    sdlBytes,
		TypeCount:   p.Levels + p.Levels*p.Impls + 1, // interfaces + impls + Query
	}
}

// pathoCaseName is the human-readable shape label used in benchmark sub-names and reports.
func pathoCaseName(p pathoGenParams) string {
	return fmt.Sprintf("patho-M%d-D%d-S%d-R%d", p.Impls, p.Levels, p.Subgraphs, p.Replicas)
}

// pathoSchemaSDL is the composed client-facing schema (the `definition` document the normalization
// + obligation pipeline validates operations against): the union of every subgraph's capability --
// all interfaces, all impls with every field.
func pathoSchemaSDL(p pathoGenParams) string {
	var b strings.Builder
	b.WriteString("schema { query: Query }\ntype Query { root: PIface0 }\n")
	for d := 0; d < p.Levels; d++ {
		fmt.Fprintf(&b, "interface %s { common: String", pathoIfaceName(d))
		if pathoHasChild(d, p.Levels) {
			fmt.Fprintf(&b, " child: %s", pathoIfaceName(d+1))
		}
		b.WriteString(" }\n")
		for j := 0; j < p.Impls; j++ {
			fmt.Fprintf(&b, "type %s implements %s { id: ID! common: String", pathoImplName(d, j), pathoIfaceName(d))
			if pathoHasChild(d, p.Levels) {
				fmt.Fprintf(&b, " child: %s", pathoIfaceName(d+1))
			}
			fmt.Fprintf(&b, " %s: String }\n", pathoOnlyField(d, j))
		}
	}
	return b.String()
}

// pathoWalkOp selects ONLY interface fields at every level -- no fragments; the operation text is
// linear in D while a type-explosion planner branches M ways per level:
//
//	{ root { __typename common child { __typename common child { ... } } } }
func pathoWalkOp(p pathoGenParams) string {
	var b strings.Builder
	b.WriteString("{ root { __typename common ")
	depth := 0
	for d := 0; d < p.Levels-1; d++ {
		b.WriteString("child { __typename common ")
		depth++
	}
	for i := 0; i < depth; i++ {
		b.WriteString("} ")
	}
	b.WriteString("} }")
	return b.String()
}

// pathoFragOp adds TWO inline fragments per level (constant per level, so still linear in D):
// each selects that impl's EXCLUSIVE field (forcing a downcast plus a jump to one of its R owners),
// and the first fragment carries the recursion into the next level:
//
//	{ root { __typename common
//	  ... on PImpl0_0 { only0_0 child { <next level> } }
//	  ... on PImpl0_1 { only0_1 } } }
func pathoFragOp(p pathoGenParams) string {
	var level func(d int) string
	level = func(d int) string {
		var b strings.Builder
		b.WriteString("__typename common ")
		if pathoHasChild(d, p.Levels) {
			fmt.Fprintf(&b, "... on %s { %s child { %s } } ", pathoImplName(d, 0), pathoOnlyField(d, 0), level(d+1))
		} else {
			fmt.Fprintf(&b, "... on %s { %s } ", pathoImplName(d, 0), pathoOnlyField(d, 0))
		}
		fmt.Fprintf(&b, "... on %s { %s } ", pathoImplName(d, 1), pathoOnlyField(d, 1))
		return b.String()
	}
	return "{ root { " + level(0) + "} }"
}

// pathoLeafFanOp walks pure interface fields to the LAST level, then fans out one fragment per
// implementation, each selecting that impl's exclusive field -- M leaf branches x R owner choices
// each on top of the walk's per-level explosion; operation text is O(D + M):
//
//	{ root { __typename child { __typename child { ... { __typename
//	  ... on PImpl{D-1}_0 { only } ... on PImpl{D-1}_1 { only } ... } } } } }
func pathoLeafFanOp(p pathoGenParams) string {
	last := p.Levels - 1
	var b strings.Builder
	b.WriteString("{ root { __typename ")
	depth := 0
	for d := 0; d < last; d++ {
		b.WriteString("child { __typename ")
		depth++
	}
	for j := 0; j < p.Impls; j++ {
		fmt.Fprintf(&b, "... on %s { %s } ", pathoImplName(last, j), pathoOnlyField(last, j))
	}
	for i := 0; i < depth; i++ {
		b.WriteString("} ")
	}
	b.WriteString("} }")
	return b.String()
}

// pathoOps enumerates the three operation families by name, in the order the sweep reports them.
func pathoOps(p pathoGenParams) []struct{ Name, Op string } {
	return []struct{ Name, Op string }{
		{"walk", pathoWalkOp(p)},
		{"frag", pathoFragOp(p)},
		{"leaffan", pathoLeafFanOp(p)},
	}
}
