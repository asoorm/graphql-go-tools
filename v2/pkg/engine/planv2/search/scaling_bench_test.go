package search

// scaling_bench_test.go is the M1 task-13 "complexity scaling check": a benchmark series over
// generated instances of increasing |E|, logging ns/op growth for the near-linear-scaling claim in
// the M1 report (measurement only -- no assertion on the growth curve itself).
//
// It EXTENDS property_test.go's deterministic instance generator rather than replacing it: this file
// reuses edgeSpec, specWeight (via buildFromPlan), buildFromPlan, identityOrder, numGoals, goalType,
// goalSchema, goalOpQuery, and maxTails verbatim. What's new is planInstanceScaled, a sibling to
// planInstance without the small property-test caps (maxInterm=4, maxEdges=12 -- sized for the
// brute-force oracle in bruteforce_test.go, not for a scaling study) -- it targets an approximate |E|
// directly, trading the small-instance caps for size control.

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/hypergraph"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/obligation"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// planInstanceScaled builds an instancePlan targeting approximately `targetEdges` distinct edges: 3
// guaranteed root->goal edges (reachability, as planInstance's guarantee=true does) plus randomly
// generated edges among `targetEdges` intermediate nodes (one intermediate per target edge gives
// enough (kind, head, tails) identity diversity that dedup collisions stay rare -- see hypergraph's
// Builder.AddEdge A-3 identity-tuple dedup). Acyclic by the same lower-index-tails-only construction
// as planInstance. Deterministic per seed.
func planInstanceScaled(seed int64, targetEdges int) instancePlan {
	rng := rand.New(rand.NewSource(seed))
	m := targetEdges
	if m < numGoals+1 {
		m = numGoals + 1
	}
	numNodes := 1 + m + numGoals
	var p instancePlan
	p.numNodes = numNodes
	for i := 0; i < numGoals; i++ {
		p.goalNode[i] = hypergraph.NodeID(1 + m + i)
	}

	// Guaranteed direct root->goal edges so a cover exists by construction (planInstance's
	// guarantee=true path), same as the property-test generator.
	for i := 0; i < numGoals; i++ {
		p.specs = append(p.specs, edgeSpec{hypergraph.EdgeField, p.goalNode[i], []hypergraph.NodeID{0}})
	}

	seen := make(map[string]bool, targetEdges)
	for _, s := range p.specs {
		seen[specIdentityKey(s)] = true
	}

	// Cap attempts generously above the target so a rare run of dedup collisions near the tail
	// still terminates -- this is a benchmark generator, not a correctness-critical exhaustive
	// search; falling a little short of targetEdges just logs a smaller actual |E|.
	maxAttempts := targetEdges * 20
	for attempts := 0; len(p.specs) < targetEdges+numGoals && attempts < maxAttempts; attempts++ {
		head := hypergraph.NodeID(1 + rng.Intn(numNodes-1)) // never the root
		maxT := int(head)
		if maxT > maxTails {
			maxT = maxTails
		}
		if maxT < 1 {
			continue
		}
		tc := 1 + rng.Intn(maxT)
		seenTail := map[hypergraph.NodeID]bool{}
		var tails []hypergraph.NodeID
		for tailAttempts := 0; len(tails) < tc && tailAttempts < tc*4; tailAttempts++ {
			cand := hypergraph.NodeID(rng.Intn(int(head)))
			if seenTail[cand] {
				continue
			}
			seenTail[cand] = true
			tails = append(tails, cand)
		}
		if len(tails) == 0 {
			continue
		}
		kind := hypergraph.EdgeKind(1 + rng.Intn(4)) // Field/Descent/TypeMove/EntityJump
		spec := edgeSpec{kind, head, tails}
		key := specIdentityKey(spec)
		if seen[key] {
			continue
		}
		seen[key] = true
		p.specs = append(p.specs, spec)
	}
	return p
}

// specIdentityKey mirrors the A-3 identity tuple (kind, head, sorted tails) hypergraph.Builder
// dedups edges on, so the generator can skip a collision before ever calling AddEdge.
func specIdentityKey(s edgeSpec) string {
	key := fmt.Sprintf("%d|%d|", s.kind, s.head)
	// tails are already drawn without replacement per spec above; sort is unnecessary here since
	// planInstanceScaled never emits the same multiset in two different orders for one spec, but
	// guard it anyway for defensive stability against future edits to the tail-generation loop.
	tails := append([]hypergraph.NodeID(nil), s.tails...)
	for i := 1; i < len(tails); i++ {
		for j := i; j > 0 && tails[j-1] > tails[j]; j-- {
			tails[j-1], tails[j] = tails[j], tails[j-1]
		}
	}
	for _, t := range tails {
		key += fmt.Sprintf("%d,", t)
	}
	return key
}

// buildScalingInstance builds the (H, O) pair for one scaling data point: a hypergraph with
// approximately targetEdges edges plus the same `{ root { g0 g1 g2 } }` obligation tree
// property_test.go's small-instance generators use.
func buildScalingInstance(tb testing.TB, seed int64, targetEdges int) (*hypergraph.Hypergraph, *obligation.Tree) {
	tb.Helper()
	p := planInstanceScaled(seed, targetEdges)
	h := buildFromPlan(p, identityOrder(len(p.specs)))
	o := buildObligationsTB(tb, h, goalSchema, goalOpQuery)
	return h, o
}

// buildObligationsTB is buildObligations (search_test.go) generalized to testing.TB so benchmarks
// (*testing.B) can call it -- buildObligations itself is pinned to *testing.T.
func buildObligationsTB(tb testing.TB, h *hypergraph.Hypergraph, schema, op string) *obligation.Tree {
	tb.Helper()
	opDoc := unsafeparser.ParseGraphqlDocumentString(op)
	defDoc := unsafeparser.ParseGraphqlDocumentStringWithBaseSchema(schema)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
	).NormalizeOperation(&opDoc, &defDoc, report)
	if report.HasErrors() {
		tb.Fatalf("normalize operation: %s", report.Error())
	}
	tree, err := obligation.Build(&opDoc, &defDoc, "", h)
	if err != nil {
		tb.Fatalf("build obligation tree: %v", err)
	}
	return tree
}

// BenchmarkSearchComplexityScaling is the M1 near-linear-scaling measurement: |E| in
// {100, 500, 2000, 8000}, ns/op + allocs/op per size via -benchmem. No assertion on the growth
// curve -- this logs the data the M1 report's scaling claim quotes; the curve is read from
// `go test -bench=BenchmarkSearchComplexityScaling -benchmem` output (or benchstat across runs).
func BenchmarkSearchComplexityScaling(b *testing.B) {
	for _, size := range []int{100, 500, 2000, 8000} {
		size := size
		b.Run(fmt.Sprintf("E=%d", size), func(b *testing.B) {
			h, o := buildScalingInstance(b, 42, size)
			b.Logf("target|E|=%d actual|E|=%d |V|=%d", size, h.NumEdges(), h.NumNodes())
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Search(h, o, defaultCfg()); err != nil {
					b.Fatalf("size %d: %v", size, err)
				}
			}
		})
	}
}
