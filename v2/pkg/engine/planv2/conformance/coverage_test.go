package conformance

import (
	"sort"
	"strings"
	"testing"
)

// The frozen untargeted-GEN residual list: GEN propositions no family targets yet. SHRINK-ONLY --
// removing an entry requires a targeting family; adding one requires the same deliberation as an
// audit expect-fail marker.
var untargetedGen = map[string]bool{
	"FS-PLAN-4": true, // CollectFields order oracle (DV-009 executed-truth adjudication stands in)
	"FS-OVR-6":  true, // override x member-possibility calculus
	"FS-ABS-9":  true, // abstract-typed fragment conditions
	"FS-ABS-11": true, // same-key member variants / discriminating-ancestor gates
	"FS-ARG-4":  true, // same-position aliased argument variants
	"FS-ARG-6":  true, // argument-independence needs paired probes
}

// TestCoverage_TableComplete pins the coverage table against FEDERATION_SEMANTICS.md Section 17:
// exactly 104 propositions, the per-construct counts, and the class census.
func TestCoverage_TableComplete(t *testing.T) {
	table := CoverageTable()
	if len(table) != 104 {
		t.Fatalf("coverage table has %d rows; FEDERATION_SEMANTICS.md defines 104 propositions", len(table))
	}

	perConstruct := map[string]int{}
	seen := map[string]bool{}
	census := map[CoverageClass]int{}
	targeted := 0
	for _, row := range table {
		if seen[row.ID] {
			t.Errorf("duplicate coverage row %s", row.ID)
		}
		seen[row.ID] = true
		construct := row.ID[:strings.LastIndex(row.ID, "-")]
		perConstruct[construct]++
		census[row.Class]++
		if row.Class == ClassGen && len(row.Families) > 0 {
			targeted++
		}
	}

	// Section 16's per-construct proposition counts.
	want := map[string]int{
		"FS-PLAN": 7, "FS-KEY": 11, "FS-REQ": 9, "FS-PROV": 4, "FS-EXT": 3,
		"FS-OVR": 6, "FS-SHR": 4, "FS-INACC": 3, "FS-IFO": 7, "FS-ABS": 12,
		"FS-ENT": 8, "FS-ROOT": 6, "FS-ARG": 6, "FS-SUB": 6, "FS-DEF": 7,
		"FS-EDFS": 5,
	}
	for construct, n := range want {
		if perConstruct[construct] != n {
			t.Errorf("construct %s: %d rows, want %d", construct, perConstruct[construct], n)
		}
	}

	// The honest census -- move these numbers DELIBERATELY, with the table.
	if census[ClassGen] != 85 || census[ClassExec] != 6 || census[ClassComp] != 5 || census[ClassFree] != 8 {
		t.Errorf("class census GEN=%d EXEC=%d COMP=%d FREE=%d, want 85/6/5/8 -- reclassifications must update this pin",
			census[ClassGen], census[ClassExec], census[ClassComp], census[ClassFree])
	}
	t.Logf("coverage: 104 propositions -- GEN %d (%d targeted, %d untargeted residuals), EXEC %d, COMP %d, FREE %d",
		census[ClassGen], targeted, census[ClassGen]-targeted, census[ClassExec], census[ClassComp], census[ClassFree])
}

// TestCoverage_GenTargetsExist: every GEN row is either targeted by existing families or on the
// frozen untargeted residual list -- never silently uncovered; and the residual list never names
// a row that IS targeted (shrink discipline).
func TestCoverage_GenTargetsExist(t *testing.T) {
	familyNames := map[string]bool{"*baseline*": true}
	for _, f := range Families() {
		familyNames[f.Name] = true
	}
	for _, row := range CoverageTable() {
		if row.Class != ClassGen {
			if len(row.Families) > 0 {
				t.Errorf("%s: class %s must not list families", row.ID, row.Class)
			}
			continue
		}
		if len(row.Families) == 0 {
			if !untargetedGen[row.ID] {
				t.Errorf("%s: GEN row with no targeting family and no residual-register entry", row.ID)
			}
			continue
		}
		if untargetedGen[row.ID] {
			t.Errorf("%s: targeted AND on the untargeted residual list -- remove the entry", row.ID)
		}
		for _, f := range row.Families {
			if !familyNames[f] {
				t.Errorf("%s: names unknown family %q", row.ID, f)
			}
		}
	}
}

// TestCoverage_CasesCarryKnownPropositions: every generated case's proposition IDs exist in the
// table, and every family's declared propositions are consistent with the table's family lists
// (a family may assert FREE/derived side-propositions; it must never assert an unknown ID).
func TestCoverage_CasesCarryKnownPropositions(t *testing.T) {
	known := map[string]bool{}
	for _, row := range CoverageTable() {
		known[row.ID] = true
	}
	for _, c := range GenerateAll([]uint64{1}) {
		if len(c.Propositions) == 0 {
			t.Errorf("%s: case carries no proposition IDs", c.ID)
		}
		for _, p := range c.Propositions {
			if !known[p] {
				t.Errorf("%s: unknown proposition %q", c.ID, p)
			}
		}
	}
	for _, f := range Families() {
		if len(f.Propositions) == 0 {
			t.Errorf("family %s declares no propositions", f.Name)
		}
		for _, p := range f.Propositions {
			if !known[p] {
				t.Errorf("family %s: unknown proposition %q", f.Name, p)
			}
		}
	}
}

// TestCoverage_TargetedRowsGenerateCases: for every GEN row that names a concrete family, at
// least one generated case carries the proposition -- the table can never claim coverage the
// generator does not produce.
func TestCoverage_TargetedRowsGenerateCases(t *testing.T) {
	byProp := map[string]int{}
	for _, c := range GenerateAll([]uint64{1}) {
		for _, p := range c.Propositions {
			byProp[p]++
		}
	}
	var missing []string
	for _, row := range CoverageTable() {
		if row.Class != ClassGen || len(row.Families) == 0 {
			continue
		}
		onlyBaseline := true
		for _, f := range row.Families {
			if f != "*baseline*" {
				onlyBaseline = false
			}
		}
		if onlyBaseline {
			continue // baseline assertions run on every case; no per-proposition tag required
		}
		if byProp[row.ID] == 0 {
			missing = append(missing, row.ID)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("coverage table claims families target these propositions, but no generated case carries them: %v", missing)
	}
}
