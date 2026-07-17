package bench

// scale_gen_test.go is the M15 scale-proof benchmark's deterministic synthetic-supergraph
// generator: given (Seed, Subgraphs S, FieldsPerEntity F) it builds S federation subgraphs shaped
// like a real enterprise supergraph at that scale -- entity types with @key (single-field, a
// multi-key sprinkle, a composite/nested-key sprinkle), @requires/@provides sprinkling, a shared
// (non-entity) value type duplicated across every subgraph, and per-entity filler fields (with
// description docstrings) sized to approximate a real SDL byte footprint without proportionally
// inflating the interesting graph structure (entity-jump topology).
//
// TOPOLOGY: the S entities form a RING via a `next: Entity(i+1 mod S)` reference field -- subgraph i
// OWNS Entity_i (id key, filler fields, next/profile/stats/meta) but only STUBS Entity_(i+1) (a
// bare `{ id }` type so `next`'s output type resolves, with id declared @external-but-key so it
// stays locally resolvable -- mirroring hypergraph/testdata/entity_jump.go's dual-key-redeclaration
// pattern). This forces exactly one ENTITY JUMP per ring hop: a query walking `next`
// k times and selecting a filler field at each hop touches exactly k+1 distinct subgraphs (the
// "span" the bench package's scale benchmarks sweep). Reusing the `next` reference for span control
// (rather than one huge flat query) keeps every generated operation's shape uniform across S.
//
// SPRINKLING (deterministic, keyed off `i % 12`): subgraph i, IN ADDITION to owning Entity_i, acts
// as a HELPER for Entity_(i-1) (owned by subgraph i-1) when i%12 lands on one of three reserved
// residues:
//   - i%12==1: multi-key -- Entity_(i-1) gets a SECOND, independent single-field @key(fields:"sku")
//     (its base @key(fields:"id") is unchanged), resolved by subgraph i via an exclusive
//     `bySkuN: String` field.
//   - i%12==4: composite/nested key -- Entity_(i-1) gets an ADDITIONAL
//     @key(fields:"id region { code }") key (entity_jump.go's nested-key shape), resolved by
//     subgraph i via an exclusive `compositeExtraN: String` field.
//   - i%12==7: @requires -- subgraph i resolves an exclusive `derivedN: Float` field on
//     Entity_(i-1) that @requires(fields:"weight") (requiresChainSubgraphs' shape), where `weight`
//     is a plain field the owner (subgraph i-1) already resolves.
//   - every other residue (9 of 12): no helper role -- most subgraphs are plain ring links, so the
//     sprinkling is realistically sparse, not exhaustive.
// Independently, subgraph i @provides(fields:"attr0") on its own `next` field when i%9==0 (a
// provides sprinkle unrelated to the multi-key/composite/requires rotation).
//
// This file builds `[]plan.DataSource` DIRECTLY (BuildConfig-consumable), matching the hypergraph
// testdata + audit deriver patterns (see hypergraph/testdata/entity_jump.go, audit/deriver.go) --
// no v1 composition step is invoked; DataSourceMetadata is constructed by hand from the same
// generator loop that emits the SDL text, so the two stay in sync by construction rather than by a
// separate derivation pass.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
)

// scale role residues (mod scaleRoleCycle) assigned to the sprinkling rotation. Zero is deliberately
// the majority residue (9 of 12) -- "sprinkling", not saturation.
const (
	scaleRoleCycle     = 12
	scaleRoleMultiKey  = 1
	scaleRoleComposite = 4
	scaleRoleRequires  = 7
	scaleProvidesEvery = 9
)

// scaleGenParams parametrizes generateScaleSupergraph. Deterministic: the same params always
// produce byte-identical SDL/metadata (no randomness is actually used -- the generator is a pure
// function of (Subgraphs, FieldsPerEntity); Seed is threaded through and reserved for callers that
// want a documented, explicit determinism knob even though the current generator has no stochastic
// choices to seed).
type scaleGenParams struct {
	Seed            int64
	Subgraphs       int // S: number of federation subgraphs (== number of ring entities)
	FieldsPerEntity int // F: filler scalar fields per entity type (drives SDL byte footprint)
}

// scaleSupergraph is generateScaleSupergraph's result: the built DataSources plus size/shape
// statistics for the BENCHMARKS.md "Scale" section and the verify tests.
type scaleSupergraph struct {
	Params      scaleGenParams
	Subs        []subgraph // SDL + metadata, reusing fixtures_test.go's subgraph type
	DataSources []plan.DataSource
	SDLBytes    int // sum of len(sub.sdl) across all S subgraphs
	TypeCount   int // distinct composed type names (entity + profile + stats + shared value types)
}

// roleOf returns the sprinkling role subgraph i plays as a HELPER for Entity_(i-1): 0 (none),
// scaleRoleMultiKey, scaleRoleComposite, or scaleRoleRequires.
func roleOf(i int) int {
	switch i % scaleRoleCycle {
	case scaleRoleMultiKey:
		return scaleRoleMultiKey
	case scaleRoleComposite:
		return scaleRoleComposite
	case scaleRoleRequires:
		return scaleRoleRequires
	default:
		return 0
	}
}

// fillerDescription is the per-filler-field doc comment. Real enterprise supergraph SDL carries
// substantial description/directive overhead per field; this approximates that byte footprint
// without adding extra hypergraph nodes/edges (a description is parsed but produces no AST content
// beyond the field itself), so SDLBytes and hypergraph structural complexity can be tuned somewhat
// independently via FieldsPerEntity.
func fillerDescription(entityType string, f int) string {
	return fmt.Sprintf(
		`  """Synthetic load-bearing filler field attr%d on %s for the M15 scale-proof benchmark; no generated operation selects it -- it exists to approximate a real enterprise subgraph's field-count and SDL-byte footprint at scale."""`+"\n",
		f, entityType)
}

// generateScaleSupergraph builds p.Subgraphs federation subgraphs in the ring+sprinkle shape
// documented at the top of this file. tb is used only for require-style fatal errors during
// generation (none expected -- the shape is hand-verified -- but plan.NewDataSourceConfiguration in
// dataSource() can fail on a malformed SDL, and a Fatalf there beats a nil-pointer panic).
func generateScaleSupergraph(tb testing.TB, p scaleGenParams) *scaleSupergraph {
	tb.Helper()
	s := p.Subgraphs
	f := p.FieldsPerEntity
	if s < 2 {
		tb.Fatalf("generateScaleSupergraph: Subgraphs must be >= 2, got %d", s)
	}

	subs := make([]subgraph, s)
	sdlBytes := 0
	// distinct composed type names: S entities + S profiles + S stats + Metadata + Query, plus
	// Region if any composite-key role is present at this S.
	typeCount := 3*s + 2
	hasComposite := false
	for i := 0; i < s; i++ {
		if roleOf(i) == scaleRoleComposite {
			hasComposite = true
			break
		}
	}
	if hasComposite {
		typeCount++
	}

	for i := 0; i < s; i++ {
		nextIdx := (i + 1) % s
		prevIdx := (i - 1 + s) % s
		nextRole := roleOf(nextIdx) // what subgraph nextIdx will need FROM Entity_i as ITS helper role
		helperRole := roleOf(i)     // what THIS subgraph contributes to Entity_(i-1) as a helper

		entityType := fmt.Sprintf("Entity%d", i)
		nextType := fmt.Sprintf("Entity%d", nextIdx)
		prevType := fmt.Sprintf("Entity%d", prevIdx)
		profileType := fmt.Sprintf("Profile%d", i)
		statsType := fmt.Sprintf("Stats%d", i)

		var b strings.Builder
		meta := &plan.DataSourceMetadata{}

		if i == 0 {
			b.WriteString("type Query { start: Entity0 }\n")
			meta.RootNodes = append(meta.RootNodes, plan.TypeField{TypeName: "Query", FieldNames: []string{"start"}})
		}

		// --- main entity type: Entity_i, owned by subgraph i ---------------------------------
		keyDirectives := []string{`@key(fields: "id")`}
		switch nextRole {
		case scaleRoleMultiKey:
			keyDirectives = append(keyDirectives, `@key(fields: "sku")`)
		case scaleRoleComposite:
			keyDirectives = append(keyDirectives, `@key(fields: "id region { code }")`)
		}
		fmt.Fprintf(&b, "type %s %s {\n", entityType, strings.Join(keyDirectives, " "))
		b.WriteString("  id: ID!\n")
		for k := 0; k < f; k++ {
			b.WriteString(fillerDescription(entityType, k))
			fmt.Fprintf(&b, "  attr%d: String\n", k)
		}
		fmt.Fprintf(&b, "  next: %s\n", nextType)
		fmt.Fprintf(&b, "  profile: %s\n", profileType)
		fmt.Fprintf(&b, "  stats: %s\n", statsType)
		b.WriteString("  meta: Metadata\n")
		switch nextRole {
		case scaleRoleMultiKey:
			b.WriteString("  sku: String!\n")
		case scaleRoleComposite:
			b.WriteString("  region: Region\n")
		case scaleRoleRequires:
			b.WriteString("  weight: Float\n")
		}
		b.WriteString("}\n")

		fmt.Fprintf(&b, "type %s { bio: String tags: [String] }\n", profileType)
		fmt.Fprintf(&b, "type %s { views: Int rating: Float }\n", statsType)
		b.WriteString("type Metadata { createdAt: String updatedAt: String }\n")
		if nextRole == scaleRoleComposite {
			b.WriteString("type Region { code: String! name: String }\n")
		}

		// --- ring stub: a bare placeholder for Entity_(i+1) so `next`'s output type resolves.
		// `id` is declared @external-but-key (dual-key-redeclaration, entity_jump.go's B pattern):
		// this subgraph does NOT own Entity_(i+1), but redeclaring its base key makes the id field
		// locally resolvable (an @external field that is also a key field of its type stays
		// resolvable), which is what makes the (Entity_(i+1), subgraph i) node reachable so
		// the real owner's entity-jump edge actually has a finite-cost tail to jump FROM. Without
		// this redeclaration the stub's id node would be permanently unreachable (pi=Inf) and no
		// query could ever cross this ring edge.
		fmt.Fprintf(&b, "type %s { id: ID! }\n", nextType)

		attrFields := make([]string, 0, f+5)
		attrFields = append(attrFields, "id")
		for k := 0; k < f; k++ {
			attrFields = append(attrFields, fmt.Sprintf("attr%d", k))
		}
		attrFields = append(attrFields, "next", "profile", "stats", "meta")
		switch nextRole {
		case scaleRoleMultiKey:
			attrFields = append(attrFields, "sku")
		case scaleRoleComposite:
			attrFields = append(attrFields, "region")
		case scaleRoleRequires:
			attrFields = append(attrFields, "weight")
		}
		meta.RootNodes = append(meta.RootNodes, plan.TypeField{TypeName: entityType, FieldNames: attrFields})
		meta.ChildNodes = append(meta.ChildNodes,
			plan.TypeField{TypeName: profileType, FieldNames: []string{"bio", "tags"}},
			plan.TypeField{TypeName: statsType, FieldNames: []string{"views", "rating"}},
			plan.TypeField{TypeName: "Metadata", FieldNames: []string{"createdAt", "updatedAt"}},
			plan.TypeField{TypeName: nextType, ExternalFieldNames: []string{"id"}},
		)
		if nextRole == scaleRoleComposite {
			meta.ChildNodes = append(meta.ChildNodes, plan.TypeField{TypeName: "Region", FieldNames: []string{"code", "name"}})
		}
		meta.Keys = append(meta.Keys,
			plan.FederationFieldConfiguration{TypeName: entityType, SelectionSet: "id"},
			plan.FederationFieldConfiguration{TypeName: nextType, SelectionSet: "id"}, // stub redeclaration
		)
		switch nextRole {
		case scaleRoleMultiKey:
			meta.Keys = append(meta.Keys, plan.FederationFieldConfiguration{TypeName: entityType, SelectionSet: "sku"})
		case scaleRoleComposite:
			meta.Keys = append(meta.Keys, plan.FederationFieldConfiguration{TypeName: entityType, SelectionSet: "id region { code }"})
		}
		if f > 0 && i%scaleProvidesEvery == 0 {
			meta.Provides = append(meta.Provides, plan.FederationFieldConfiguration{
				TypeName: entityType, FieldName: "next", SelectionSet: "attr0",
			})
		}

		// --- helper role: subgraph i resolving an extra key/field on Entity_(i-1) -------------
		switch helperRole {
		case scaleRoleMultiKey:
			extra := fmt.Sprintf("bySku%d", prevIdx)
			fmt.Fprintf(&b, "type %s @key(fields: \"sku\") {\n  sku: String!\n  %s: String\n}\n", prevType, extra)
			meta.RootNodes = append(meta.RootNodes, plan.TypeField{
				TypeName: prevType, FieldNames: []string{extra}, ExternalFieldNames: []string{"sku"},
			})
			meta.Keys = append(meta.Keys, plan.FederationFieldConfiguration{TypeName: prevType, SelectionSet: "sku"})
		case scaleRoleComposite:
			extra := fmt.Sprintf("compositeExtra%d", prevIdx)
			fmt.Fprintf(&b, "type %s @key(fields: \"id region { code }\") {\n  id: ID!\n  region: Region\n  %s: String\n}\n", prevType, extra)
			b.WriteString("type Region { code: String! name: String }\n")
			meta.RootNodes = append(meta.RootNodes, plan.TypeField{
				TypeName: prevType, FieldNames: []string{extra}, ExternalFieldNames: []string{"id", "region"},
			})
			meta.ChildNodes = append(meta.ChildNodes, plan.TypeField{TypeName: "Region", ExternalFieldNames: []string{"code"}})
			meta.Keys = append(meta.Keys, plan.FederationFieldConfiguration{TypeName: prevType, SelectionSet: "id region { code }"})
		case scaleRoleRequires:
			extra := fmt.Sprintf("derived%d", prevIdx)
			fmt.Fprintf(&b, "type %s @key(fields: \"id\") {\n  id: ID!\n  weight: Float\n  %s: Float\n}\n", prevType, extra)
			meta.RootNodes = append(meta.RootNodes, plan.TypeField{
				TypeName: prevType, FieldNames: []string{extra}, ExternalFieldNames: []string{"id", "weight"},
			})
			meta.Keys = append(meta.Keys, plan.FederationFieldConfiguration{TypeName: prevType, SelectionSet: "id"})
			meta.Requires = append(meta.Requires, plan.FederationFieldConfiguration{
				TypeName: prevType, FieldName: extra, SelectionSet: "weight",
			})
		}

		sdl := b.String()
		sdlBytes += len(sdl)
		subs[i] = subgraph{name: fmt.Sprintf("sg%d", i), sdl: sdl, meta: meta}
	}

	dataSources := make([]plan.DataSource, 0, s)
	for _, sg := range subs {
		dataSources = append(dataSources, dataSource(tb, sg))
	}

	return &scaleSupergraph{
		Params:      p,
		Subs:        subs,
		DataSources: dataSources,
		SDLBytes:    sdlBytes,
		TypeCount:   typeCount,
	}
}

// planConfig wraps a scale-generated DataSource list in the plan.Configuration shape both
// planv2.NewPlanner and plan.NewPlanner accept, matching buildCase's construction in
// fixtures_test.go.
func planConfig(ds []plan.DataSource) plan.Configuration {
	return plan.Configuration{DataSources: ds, DisableResolveFieldPositions: true}
}

// spanOperation builds a query that touches exactly `span` distinct subgraphs: `span-1` hops
// through the ring's `next` field, selecting `id`+`attr0` at each hop (attr0 is never available in
// a hop's own subgraph's stub of the NEXT entity, forcing an entity jump into the next subgraph on
// every hop -- see the file doc's TOPOLOGY note). span must be in [1, S].
func spanOperation(span int) string {
	if span < 1 {
		span = 1
	}
	depth := span - 1
	var b strings.Builder
	b.WriteString("{ start { id attr0 ")
	for d := 0; d < depth; d++ {
		b.WriteString("next { id attr0 ")
	}
	for d := 0; d < depth; d++ {
		b.WriteString("} ")
	}
	b.WriteString("} }")
	return b.String()
}

// scaleSchemaSDL re-derives the single composed-schema-shaped SDL string obligation.Build/lower.Lower
// need as `definition` (the same role entityJumpSchema/multiHopSchema etc. play in fixtures_test.go):
// a client-facing schema exposing the UNION of every subgraph's capability for each ring type -- deep
// enough (S entities in the ring) to normalize/plan any span up to S, and rich enough (sku/region/
// derivedN/etc.) to plan the mixed-capability sanity query TestScaleMixedCapabilitiesPlan exercises.
// Unlike the per-subgraph SDL texts (which feed graphql_datasource.NewSchemaConfiguration for
// UpstreamSchema()), this is the COMPOSED view astnormalization/obligation.Build validate the
// operation against -- it must be a superset of every field any subgraph resolves for a type, mirroring
// what real composition produces.
func scaleSchemaSDL(s int) string {
	var b strings.Builder
	b.WriteString("schema { query: Query }\ntype Query { start: Entity0 }\n")
	hasComposite := false
	for i := 0; i < s; i++ {
		next := (i + 1) % s
		prev := (i - 1 + s) % s
		nextRole := roleOf(next)
		helperRole := roleOf(i)

		fmt.Fprintf(&b, "type Entity%d { id: ID!", i)
		for k := 0; k < 2; k++ { // composed schema only needs attr0/attr1 for the sanity/span queries
			fmt.Fprintf(&b, " attr%d: String", k)
		}
		fmt.Fprintf(&b, " next: Entity%d profile: Profile%d stats: Stats%d meta: Metadata", next, i, i)
		switch nextRole {
		case scaleRoleMultiKey:
			b.WriteString(" sku: String")
		case scaleRoleComposite:
			b.WriteString(" region: Region")
			hasComposite = true
		case scaleRoleRequires:
			b.WriteString(" weight: Float")
		}
		switch helperRole {
		case scaleRoleMultiKey:
			fmt.Fprintf(&b, " bySku%d: String", prev)
		case scaleRoleComposite:
			fmt.Fprintf(&b, " compositeExtra%d: String", prev)
			hasComposite = true
		case scaleRoleRequires:
			fmt.Fprintf(&b, " derived%d: Float", prev)
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, "type Profile%d { bio: String tags: [String] }\n", i)
		fmt.Fprintf(&b, "type Stats%d { views: Int rating: Float }\n", i)
	}
	b.WriteString("type Metadata { createdAt: String updatedAt: String }\n")
	if hasComposite {
		b.WriteString("type Region { code: String! name: String }\n")
	}
	return b.String()
}

// pathTo builds `{ id next { id next { ... field } } }` (depth `i` hops through `next`, matching
// spanOperation's nesting shape) ending in a selection of `field` on the type at ring position i.
func pathTo(i int, field string) string {
	var open, close strings.Builder
	open.WriteString("{ id ")
	for d := 0; d < i; d++ {
		open.WriteString("next { id ")
	}
	open.WriteString(field + " ")
	for d := 0; d < i; d++ {
		close.WriteString("} ")
	}
	return open.String() + close.String() + "}"
}
