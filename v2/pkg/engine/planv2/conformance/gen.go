package conformance

// gen.go -- the deterministic seed machinery and the family registry. Every case's randomness is a
// pure function of (family, scenario, base seed): the case ID is hashed (FNV-1a) into a splitmix64
// stream, so a case regenerates byte-identically from its ID alone -- no time, no global PRNG state,
// no ordering sensitivity. This is what lets the corpus be committed as CODE (the generator), not
// as golden files.

import (
	"fmt"
	"sort"
)

// rng is a splitmix64 stream -- tiny, stable across Go versions, and good enough for name/shape
// variation (this is a case generator, not a statistics engine).
type rng struct{ state uint64 }

func (r *rng) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// intn returns a value in [0,n).
func (r *rng) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// pick returns one of the choices.
func (r *rng) pick(choices []string) string { return choices[r.intn(len(choices))] }

// fnv1a hashes a string (the case-ID -> seed map).
func fnv1a(s string) uint64 {
	var h uint64 = 0xcbf29ce484222325
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 0x100000001b3
	}
	return h
}

// newRNG derives the per-case stream: ID hash mixed with the base seed.
func newRNG(caseID string, baseSeed uint64) *rng {
	return &rng{state: fnv1a(caseID) ^ (baseSeed * 0x2545f4914f6cdd1d)}
}

// caseID builds the stable identifier "<FS-ID>/<family>/<scenario>-s<seed>".
func caseID(proposition, family, scenario string, seed uint64) string {
	return fmt.Sprintf("%s/%s/%s-s%d", proposition, family, scenario, seed)
}

// fieldNamePool provides seeded-but-plausible field names so generated schemas vary across seeds
// without colliding: each name is suffixed by a stream-derived tag.
type namer struct{ r *rng }

func (n namer) field(base string) string {
	return fmt.Sprintf("%s%c%c", base, 'a'+rune(n.r.intn(26)), 'a'+rune(n.r.intn(26)))
}

// Family is one scenario family: a named generator that expands into cases for a base seed.
type Family struct {
	Name string
	// Propositions the family targets (for the coverage cross-check).
	Propositions []string
	// Generate returns the family's cases for one base seed. MUST be a pure function of seed.
	Generate func(seed uint64) []GeneratedCase
}

// families is the registry, populated by the family files' init functions.
var families []Family

func register(f Family) { families = append(families, f) }

// Families returns the registered families sorted by name (registration order is init order,
// which is file-name order -- sorting removes that coupling).
func Families() []Family {
	out := append([]Family(nil), families...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GenerateAll expands every family for every base seed. Case IDs are guaranteed unique -- a
// duplicate is a generator bug and panics here (a generator defect must never masquerade as a
// planner result).
func GenerateAll(seeds []uint64) []GeneratedCase {
	var out []GeneratedCase
	seen := map[string]bool{}
	for _, f := range Families() {
		for _, seed := range seeds {
			for _, c := range f.Generate(seed) {
				if seen[c.ID] {
					panic("conformance: duplicate generated case ID " + c.ID)
				}
				seen[c.ID] = true
				if c.Family == "" {
					c.Family = f.Name
				}
				out = append(out, c)
			}
		}
	}
	return out
}

// DefaultSeeds is the CI seed set: two base seeds keep the default run broad but budgeted;
// deeper sweeps pass more via the CONFORMANCE_SEEDS knob (conformance_test.go).
var DefaultSeeds = []uint64{1, 2}
