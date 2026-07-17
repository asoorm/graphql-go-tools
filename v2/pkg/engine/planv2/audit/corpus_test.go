package audit

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update SCOREBOARD.md from the corpus run")

// TestAudit_PriorityFamilies pins the abstract-type + entity-jump families (the classes that
// motivated the planner-v2 rebuild: D6 narrowing, member aliasing, entity jumps) plus the DV-005
// suite at FULL PASS -- every case, no skips, no gaps. A regression in any of these suites fails
// here even though the corpus test would only reclassify it.
func TestAudit_PriorityFamilies(t *testing.T) {
	cases, err := loadCorpus("testdata")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	priority := map[string]bool{
		"simple-entity-call":    true,
		"partial-union":         true,
		"partial-union-complex": true,
		"union-intersection":    true,
		"provides-on-union":     true,
		"provides-on-interface": true,
		"child-type-mismatch":   true,
		"keys-mashup":           true, // DV-005: registered divergence; see DIVERGENCES.md
	}
	// Characterized exceptions to the FULL-PASS bar: cases in a priority suite whose non-PASS is a
	// REGISTERED finding (expect-fail-marked GAP), not a regression. union-intersection/case-09 was
	// "passing" only while the oracle could not see its foreign-root fetch (the plan answers
	// `{ aMedia ... }` by fetching Query.bMedia); the root-entry assertion (assertion 5) exposes it
	// as a foreign-root model-gap residual (class B, see its expect-fail.txt). The planner behavior
	// is unchanged -- the oracle got stricter. Remove the entry when the missing jump is modelled.
	exempt := map[string]string{
		// union-intersection/case-09 flipped GAP -> genuine PASS (M2 class-D wave, D6pp dead-member
		// exemption: the member goal that needed the missing jump is dead at its position, so the
		// plan enters only requested roots); marker removed.
		// M1.5 wave-1b THE FLIP: the obligation-driven per-position lowering is now the shipping
		// default and CLOSED the bulk of the sibling / path-conflation class in the priority families --
		// partial-union-complex/case-02, provides-on-interface/case-02, and union-intersection/case-05,
		// 06, 07 flipped GAP -> genuine PASS and were removed from this map (their expect-fail markers are
		// gone). Each remaining entry keeps a per-case expect-fail.txt with its post-flip observed
		// reason; remove an entry when its gap closes.
		// partial-union/case-02 flipped GAP -> genuine PASS (M1.5 IR+D6 wave: D6p route-scoping un-narrows
		// the resolvable Beta/Gamma members whose only reachable producer supplies them); marker removed.
		// partial-union-complex/case-03 flipped GAP -> PASS (D3pp exempt-terminal composite coverage: the
		// all-narrowed `actions` composite gets a `{ __typename }` coverage goal); marker removed.
		// union-intersection/case-04, 08, 11, 12 flipped GAP -> genuine PASS (M2 class-D wave:
		// D11.7 member-qualified placement expands distributed members per declaring subgraph --
		// the `... on Movie` selections re-enter the shared viewer via subgraph b -- and D6pp
		// exempts the dead members); markers removed.
		// partial-union-complex/case-05 flipped GAP -> genuine PASS (M2 class-C wave: the D10
		// chain-layered consistent trace routes the member-scoped leaf through its own position's
		// member chain instead of the sneak/shortcut route); marker removed -- the priority families
		// are back to FULL PASS with no characterized exceptions.
	}
	var ran int
	for _, c := range cases {
		if !priority[c.Suite] {
			continue
		}
		out := Run(c)
		if want, ok := exempt[c.Suite+"/"+c.Name]; ok {
			if out.Status != want {
				t.Errorf("%s %s/%s -- characterized exception must be %s (see exempt map): %s",
					out.Status, c.Suite, c.Name, want, out.Reason)
			}
			continue
		}
		if out.Status != "PASS" {
			t.Errorf("%s %s/%s -- priority families must FULLY pass: %s", out.Status, c.Suite, c.Name, out.Reason)
			continue
		}
		ran++
	}
	if ran == 0 {
		t.Fatal("no priority-family cases ran -- fixtures missing?")
	}
	t.Logf("priority families: %d cases, all PASS", ran)
}

// TestAudit_Corpus runs the whole transcribed corpus, emits a per-suite scoreboard, and (with
// -update) writes SCOREBOARD.md. It fails only on FAIL outcomes (a plan-level assertion violated) --
// SKIP outcomes (M1 feature gaps planv2 cannot yet plan) are reported, not failed, per the M1 bar
// counting the in-scope set. The headline is in-scope passed/total plus the skipped count.
func TestAudit_Corpus(t *testing.T) {
	cases, err := loadCorpus("testdata")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("empty corpus")
	}

	outcomes := make([]Outcome, 0, len(cases))
	for _, c := range cases {
		outcomes = append(outcomes, Run(c))
	}
	sortOutcomes(outcomes)

	var pass, fail, gap, skip int
	for _, o := range outcomes {
		switch o.Status {
		case "PASS":
			pass++
		case "FAIL":
			fail++
			t.Errorf("FAIL %s/%s -- %s", o.Suite, o.Case, o.Reason)
		case "GAP":
			gap++
			t.Logf("GAP  %s/%s -- %s", o.Suite, o.Case, o.Reason)
		case "SKIP":
			skip++
		}
	}
	// GAP cases are IN SCOPE (known planv2 defects on supported features, expect-fail-marked and
	// reported as findings) and count against the bar; SKIP cases (cannot plan: M1 feature gap) do not.
	inScope := pass + fail + gap
	board := renderScoreboard(outcomes, pass, fail, gap, skip)
	t.Log("\n" + board)
	selfAuthored := 0
	for _, c := range cases {
		if c.Suite == "sibling-conflation" {
			selfAuthored++
		}
	}
	t.Logf("AUDIT PLAN-LEVEL HEADLINE: in-scope %d/%d passed (%d known gaps), %d skipped, %d total (%d Guild-transcribed + %d self-authored sibling-conflation witnesses)",
		pass, inScope, gap, skip, len(cases), len(cases)-selfAuthored, selfAuthored)

	if *update {
		path := filepath.Join("..", "..", "..", "..", "docs", "planner-v2", "SCOREBOARD.md")
		existing, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatalf("read scoreboard: %v", readErr)
		}
		out := preserveAppendix(board, string(existing))
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatalf("write scoreboard: %v", err)
		}
		t.Logf("wrote %s", path)
	}
}

// scoreboardAppendixMarker separates the generated per-suite scoreboard from the hand-appended
// executed-truth appendix (the M1.5 router-level results sections). Everything from the first
// occurrence of this marker onward is NOT generated by renderScoreboard and must survive -update.
const scoreboardAppendixMarker = "\n---\n"

// preserveAppendix combines a freshly generated scoreboard with the appendix of the existing file.
// The appendix is everything from the first "\n---\n" separator onward -- the executed-truth sections
// appended by the M1.5/M2 harness waves. A prior version of -update wrote the generated board over
// the whole file, silently deleting that appendix (a measured, hand-written record); this preserves
// it. A file without the marker (or no existing file) yields the generated board unchanged.
func preserveAppendix(board, existing string) string {
	i := strings.Index(existing, scoreboardAppendixMarker)
	if i < 0 {
		return board
	}
	return strings.TrimRight(board, "\n") + "\n" + existing[i:]
}

// renderScoreboard builds the committed SCOREBOARD.md content: a per-suite table plus the headline.
func renderScoreboard(outcomes []Outcome, pass, fail, gap, skip int) string {
	type agg struct{ pass, fail, gap, skip int }
	suites := map[string]*agg{}
	var order []string
	for _, o := range outcomes {
		a := suites[o.Suite]
		if a == nil {
			a = &agg{}
			suites[o.Suite] = a
			order = append(order, o.Suite)
		}
		switch o.Status {
		case "PASS":
			a.pass++
		case "FAIL":
			a.fail++
		case "GAP":
			a.gap++
		case "SKIP":
			a.skip++
		}
	}
	sort.Strings(order)

	var b strings.Builder
	b.WriteString("# Audit corpus -- planner-v2 plan-level scoreboard\n\n")
	b.WriteString("Generated by `go test ./pkg/engine/planv2/audit/ -run TestAudit_Corpus -update`.\n")
	b.WriteString("Source corpus: The Guild `graphql-federation-gateway-audit` (MIT); see ")
	b.WriteString("`pkg/engine/planv2/audit/testdata/README.md` for attribution and scope.\n\n")
	b.WriteString("Plan-level PASS = planv2 plans the operation; every fetch document validates against ")
	b.WriteString("its subgraph schema; fetch dependencies form a valid order; the plan's response ")
	b.WriteString("shape can produce the audit's expected response (keys/nesting/list-ness, not values); ")
	b.WriteString("every ROOT fetch enters only root fields the client requested (root-entry honesty, ")
	b.WriteString("assertion 5 -- `_entities` fetches exempt); and every requested field is selected by some ")
	b.WriteString("fetch document along its obligation path while no fetch selects an un-requested / ")
	b.WriteString("non-mechanism field (leaf-coverage / path-correspondence, assertion 6, added M1.5 wave 1); ")
	b.WriteString("every requested field is additionally covered AT ITS RESPONSE POSITION -- a drop at one ")
	b.WriteString("position cannot hide behind the same Type.field coordinate elsewhere, and a member-refined ")
	b.WriteString("leaf is exempt only while no fetch materializes its member at that position ")
	b.WriteString("(position-aware forward coverage, assertion 6p, M3 oracle-hardening wave); ")
	b.WriteString("and every entity (`_entities`) fetch's ResponsePath / FetchPath denotes the requested ")
	b.WriteString("response position it serves (response-path oracle, assertion 7, added M1.5 wave-1b); ")
	b.WriteString("and every entity fetch's representation requirement (its Key/Requires fragment fields) is ")
	b.WriteString("selected by the fetches it depends on (key-supply completeness, assertion 8, M3 ")
	b.WriteString("oracle-hardening wave). An error-expectation case (`errors: true`) PASSes only when the ")
	b.WriteString("planner's outcome matches its PINNED reason class (expected-error-class.txt: a typed ")
	b.WriteString("ErrNoValidPlan reject, a normalize reject, or plan-success for runtime-error expectations) -- ")
	b.WriteString("an unrelated crash cannot masquerade as a correct rejection. ")
	b.WriteString("GAP = a KNOWN planv2 defect (expect-fail-marked; a reported finding) -- in scope and ")
	b.WriteString("counted against the bar. SKIP = an M1 feature gap planv2 cannot yet plan (out of the ")
	b.WriteString("in-scope bar). Router-level execution is the separate M1.5 harness. Oracle soundness is ")
	b.WriteString("itself CI-checked: the committed mutation suite (mutation_oracle_test.go) corrupts fresh ")
	b.WriteString("plans across the anti-cheat audit's ten mutation classes and requires the assertion chain ")
	b.WriteString("to kill every representative (documented intentional survivors excepted).\n\n")

	// Corpus composition, stated honestly (anti-cheat audit V2 footnote): the corpus is the Guild
	// transcription PLUS the in-house sibling-conflation defect witnesses -- never conflated into a
	// single "transcribed" figure.
	selfAuthored := 0
	for _, o := range outcomes {
		if o.Suite == "sibling-conflation" {
			selfAuthored++
		}
	}
	fmt.Fprintf(&b, "**Headline (re-audited, M3 oracle-hardening wave -- assertions 6p/8 + error-class pins + committed mutation suite): in-scope %d/%d passed (%d known gaps), %d skipped (%d total: %d Guild-transcribed + %d self-authored `sibling-conflation` defect witnesses).**\n\n",
		pass, pass+fail+gap, gap, skip, len(outcomes), len(outcomes)-selfAuthored, selfAuthored)

	b.WriteString("> **The M2 distributed-key wave landed D7ppp distributed @key + the D11.11 key-input pipeline** -- ")
	b.WriteString("the LAST non-PASS case in the corpus: a composite @key no single source supplies ")
	b.WriteString("(`complex-entity-call`: price's `products{id pid category{id tag}} selected{id}` -- pid lives only in ")
	b.WriteString("link/list, category only in products/price, selected only in list) now compiles to per-assignment ")
	b.WriteString("entity jumps: every key coordinate -- composite paths included -- contributes an explicit tail in its ")
	b.WriteString("assigned subgraph (path-coherent, target-excluded, bounded), GATED to keys with no full foreign ")
	b.WriteString("supplier so every single-source key keeps the byte-identical base-D7 edges. The chain-layered ")
	b.WriteString("consistent trace gained the tail-reachability retry tier (a distributed jump's tails legitimately ")
	b.WriteString("live on sibling coordinates the goal's kappa mask removes -- the retry reads tail reachability off the ")
	b.WriteString("unmasked table while the mask still constrains the spine), and lowering places each key coordinate ")
	b.WriteString("in the group that PRODUCES it (D11.11: Key fragment rendered from Edge.KeySelection; branch parents ")
	b.WriteString("resolved at the position the key structure denotes -- the pre-jump anchor path). ")
	b.WriteString("complex-entity-call/case-01, the ONE remaining SKIP, un-skipped and PASSes with the 5-fetch ")
	b.WriteString("products->link->price(Product)->list(gather)->price(ProductList) plan -- the corpus is COMPLETE: every ")
	b.WriteString("transcribed case is in scope and PASSES. D10 register byte-identical (2 goals, the frozen witnesses). ")
	b.WriteString("Registered residual: list-valued key-path representation VALUE (executed-truth class, with the ")
	b.WriteString("requires-fragment representation residual); see the DIVERGENCES register.\n\n")

	b.WriteString("> **The M2 D10-narrow mini-wave landed the provable-non-resolvability narrowing** (FORMAL_SPEC D10 ")
	b.WriteString("amendment): the D10 completeness fall-back no longer fires for a goal whose every candidate is ")
	b.WriteString("PROVABLY non-resolvable -- every heading @key of the candidate's type in its subgraph declares ")
	b.WriteString("`resolvable: false` (so no entity jump into it may ever be modelled) AND the candidate is reachable ")
	b.WriteString("only through its own subgraph's operation roots. Such a goal now fails loud with ErrNoValidPlan -- ")
	b.WriteString("the honest reject the audit expects -- instead of being rescued into a wrong foreign-root plan. ")
	b.WriteString("non-resolvable-interface-object/case-02 (the last GAP; an expected-errors case) flipped GAP -> PASS; ")
	b.WriteString("the D10 fallback register dropped 3 -> 2 distinct goals (exactly the two frozen benign witnesses). ")
	b.WriteString("The narrowing is CONSERVATIVE (suppression only on schema-level proof over ALL candidates; a ")
	b.WriteString("resolvable key, a keyless type, or any non-root entry keeps the fall-back byte-identical), so the ")
	b.WriteString("measured customer-corpus fall-back reliance is untouched -- full fall-back retirement stays gated on ")
	b.WriteString("BOTH register counts reaching zero (see the DIVERGENCES.md D10 register entry).\n\n")

	b.WriteString("> **The M2 requires-chain wave landed D7pp requires-scoped resolution + the D11.10 requires-input ")
	b.WriteString("pipeline** -- the whole @requires-chain family: a @requires field resolves ONLY behind a jump ")
	b.WriteString("carrying its own inputs (per-field requires-scope nodes; the requires-bypass is gone model-wide), ")
	b.WriteString("plain jumps carry keys only (no ride-along -- demand-driven requires), requirement coordinates the ")
	b.WriteString("source cannot resolve are assigned to a resolving subgraph (distributed tails; nested chains settle ")
	b.WriteString("by AND-relaxation order alone -- L6 preserved), and lowering places every requires coordinate in the ")
	b.WriteString("group that PRODUCES it, materializing gather pipelines (v1's b->a->b relay; argument-conflict gathers ")
	b.WriteString("aliased with the representation reading the aliased position back). Assertion 7 gained the ")
	b.WriteString("requires-mechanism position rule. The headline moved from **187/191** to the live number above: ")
	b.WriteString("requires-requires 5 SKIPs -> PASS, requires-with-argument/02-05 4 SKIPs -> PASS, requires-circular ")
	b.WriteString("1 SKIP -> PASS (and case-02's silent bypass PASS became the honest relay), requires-interface/case-02 ")
	b.WriteString("+ requires-with-fragments/case-02,03 GAP -> PASS (three of the four class-A/B fall-back users), and ")
	b.WriteString("the requires-fragment cases (requires-interface/case-03, requires-with-fragments/04-06) now plan the ")
	b.WriteString("relay instead of the bypass. The ONE remaining SKIP was complex-entity-call/case-01 -- DISTRIBUTED ")
	b.WriteString("@key (since CLOSED -- M2 distributed-key wave, D7ppp) -- and the ONE remaining ")
	b.WriteString("GAP was non-resolvable-interface-object/case-02 (the D10 fall-back RETIREMENT policy case; since ")
	b.WriteString("CLOSED -- M2 D10-narrow mini-wave). ")
	b.WriteString("Registered residuals: requires-fragment representation value; scoped/unscoped twin co-location; ")
	b.WriteString("see the DIVERGENCES register.\n\n")

	b.WriteString("> **The M2 class-C wave landed the @interfaceObject / provider-split family** (FORMAL_SPEC ")
	b.WriteString("D7p/D3io/D3pppp/D11.9 + the D10 chain-layered consistent trace): entity-interface keys now head the ")
	b.WriteString("interface node itself (an @interfaceObject source jumps at interface level, `... on NodeWithName ")
	b.WriteString("{ name }`), a member-refined field served only by an interface-object subgraph is covered by the ")
	b.WriteString("interface-flattened candidate and PRINTS at the interface level (never `... on User` against a ")
	b.WriteString("subgraph that lacks `User`), and a bare interface field locally unresolvable at its position is ")
	b.WriteString("planned in the MEMBER-EXPLODED form -- one `... on Member` branch per position-possible member, each ")
	b.WriteString("travelling to its declaring subgraph via its own entity jump (Apollo's type explosion / v1's ")
	b.WriteString("abstract-selection rewrite, realized on O(Q)). Nested expanded positions of a repeated abstract type ")
	b.WriteString("route through the chain-layered consistent trace (the walk's spine must spell the obligation chain; ")
	b.WriteString("Cover.Spines carries the depth structure to lowering). The headline moved from **161/184** to the ")
	b.WriteString("live number above: abstract-types 14 GAPs -> PASS, simple-interface-object 13/13 and ")
	b.WriteString("interface-object-with-requires 7/7 (7 SKIPs closed), plus bonus closes ")
	b.WriteString("partial-union-complex/case-05, simple-inaccessible/case-01, simple-requires-provides/case-08,09. ")
	b.WriteString("Registered residuals: C-disc (interface-object runtime discriminator/representation rewrite -- ")
	b.WriteString("executed-truth only) and the interface-object-with-requires/case-05 requires-bypass (owner D7pp -- ")
	b.WriteString("since CLOSED by the requires-chain wave); see the DIVERGENCES register.\n\n")

	b.WriteString("> **The M2 class-D wave landed distributed abstract-member expansion** (FORMAL_SPEC D6pp/D11.7/D11.8) ")
	b.WriteString("on top of wave-2 argument lowering: member narrowing is judged POSITIONALLY (position-possible member ")
	b.WriteString("sets from each subgraph's own output type; dead members exempt regardless of entity-ness; interface ")
	b.WriteString("refinements un-narrowed via implementer possibility), lowering keys grouping by MEMBER-QUALIFIED ")
	b.WriteString("position keys (member fragments materialize only in fetches whose subgraph declares the member, with ")
	b.WriteString("`... on Member` wrappers and member-scoped key injection), and same-key member response variants stay ")
	b.WriteString("sibling fields (v1 parity; postprocess owns the depth-correct fold). The headline moved from ")
	b.WriteString("**152/181** to the live number above; all 7 class-D gaps PASS, `abstract-types/case-17,18` entered ")
	b.WriteString("the bar as newly-plannable class-C GAPs (SKIP-reveals-GAP), and the executed-truth count moved ")
	b.WriteString("**20/27 -> 24/27** (the 3 remaining are the DV-009 field-ORDER divergence -- values byte-identical, ")
	b.WriteString("spec CollectFields order vs v1's rewriter artifact).\n\n")

	b.WriteString("> **M1.5 wave 2 landed field-argument lowering** on top of wave-1b THE FLIP. Field arguments ")
	b.WriteString("are now carried through O(Q) into fetch documents (D2/D3 argument carry, D11 rendering): each ")
	b.WriteString("fetch prints its selections' arguments, declares the variables it references in a ")
	b.WriteString("`query($v: T)` / `mutation($v: T)` header, and forwards their values via ContextVariable ")
	b.WriteString("Input segments (the v1 graphql_datasource contract). `@include`/`@skip` are resolved by ")
	b.WriteString("normalization (literal / defaulted conditions), and mutation operations lower with the ")
	b.WriteString("`mutation` root keyword. The old blanket argument-SKIP gate is gone: the 48-case argument ")
	b.WriteString("bucket now PLANS. The headline moved from **98/135** to the live number above; the SKIP count ")
	b.WriteString("dropped as those cases entered the bar. Several argument cases that ALSO carry a routing ")
	b.WriteString("(foreign-root / missing-jump) or distributed-member gap surfaced that gap as a registered GAP ")
	b.WriteString("once they could be planned at all -- exactly the \"SKIP reveals GAP\" dynamic the eval doc ")
	b.WriteString("predicted. `@requires`-with-argument (DV-006) and the same-coordinate argument-CONFLICT split ")
	b.WriteString("(DV-007) are now BOTH VERIFIED at plan level: the D7 tokenizer is argument-aware, the literal ")
	b.WriteString("requires argument values ride on `Edge.Requires`/`Edge.RequiresBy`, and lowering renders them ")
	b.WriteString("into the source document + `Requires` fragment -- splitting conflicting bindings across separate ")
	b.WriteString("`_entities` fetches (v1 HasArgumentConflictWith) with source-document aliasing. What REMAINS SKIP ")
	b.WriteString("is `requires-with-argument` cases 02-05 only: a distributed `@requires` spanning two subgraphs ")
	b.WriteString("(a multi-jump-requires model gap, NOT argument work) -- see the reqargs report. (Since CLOSED -- ")
	b.WriteString("M2 requires-chain wave.)\n\n")

	b.WriteString("**Sibling / path-conflation class (assertion 6, demand 1) -- RESOLVED by the flip; member-scoped ")
	b.WriteString("residual RESOLVED by the class-D wave.** The prior root cause (D4 keys object/field nodes `(T,s)` ")
	b.WriteString("path-blind; D10 covers are node-reachability, so ")
	b.WriteString("two response positions of one type collapsed onto one node with one back-derivation) is closed ")
	b.WriteString("for the general case: the shipping obligation-driven lowering assigns each response position its ")
	b.WriteString("own fetch and derives documents from O(Q), so `order { buyer { rating } seller { rating } }` and ")
	b.WriteString("the self-referential `order { buyer { friends { friends { id } } } }` now lower correctly (both ")
	b.WriteString("`sibling-conflation` witnesses PASS, markers removed). The formerly-residual **member-scoped** ")
	b.WriteString("subset -- a distributed position's different concrete members resolving in different root groups -- ")
	b.WriteString("is owned by the D11.7 member-qualified position key (the posGroup member-qualification the ")
	b.WriteString("multi-jump report asked for): distinct members of one position are distinct positions for ")
	b.WriteString("grouping, so member leaves re-root to their declaring subgraphs without stealing sibling ")
	b.WriteString("positions.\n\n")

	b.WriteString("**Foreign-root / missing-jump residual (explicit scope note).** The remaining GAPs that are not ")
	b.WriteString("member-scoped leaf-coverage are the D10 fall-back's registered model-gap class: the model lacks ")
	b.WriteString("the entity jump a path-consistent route needs, so a fetch document selects a field on a subgraph ")
	b.WriteString("(a Query/Viewer root or an entity type) that does not declare it. ")
	b.WriteString("Classes (per-case expect-fail.txt): (A) @external extension-key non-resolution, (B) missing entity ")
	b.WriteString("jump to the base-field subgraph. Class (C), @requires/@provides provider-split and @interfaceObject ")
	b.WriteString("member-flattening, is CLOSED (M2 class-C wave: D7p interface-node jump heads, D3io member-flattening ")
	b.WriteString("candidates + D11.9 flattened placement, D3pppp abstract-position member expansion, D10 chain-layered ")
	b.WriteString("consistent trace). Class (D), distributed abstract-member expansion, is CLOSED (M2 ")
	b.WriteString("class-D wave: D6pp position-possible member sets -- dead-member exemption + abstract-C possibility; ")
	b.WriteString("D11.7 member-qualified position keys -- member fragments materialize only in declaring subgraphs; ")
	b.WriteString("D11.8 sibling member response variants with the postprocess-owned depth-correct fold) -- all seven ")
	b.WriteString("class-D gaps (union-interface-distributed/02,05,08; union-intersection/04,08,11,12) plus ")
	b.WriteString("union-intersection/case-09 (class B, dead-member ride-along) PASS. The remaining classes flip to genuine ")
	b.WriteString("PASSes as the missing jumps are modelled (M2); the fall-back then becomes dead code. ")
	b.WriteString("The fall-back is TYPED AND LOUD (M2 D10 disposition): every firing is a search.Result.RouteFallbacks ")
	b.WriteString("record surfaced as a Warn diagnostic via PlanWithDiagnostics, and a committed register test ")
	b.WriteString("(fallback_register_test.go) pins the corpus figure -- 2 distinct fallback-using goals after the ")
	b.WriteString("D10-narrow mini-wave (28 at the disposition landing; 14 after class-D; 12 after class-C's D7p ")
	b.WriteString("interface-node jump heads; 6 after the same wave's D3pppp member expansion + chain-layered consistent ")
	b.WriteString("trace; 3 after the requires-chain wave) -- and freezes the ")
	b.WriteString("two benign PASS witnesses whose search-level fallback does NOT reach the wire (a fallback is a ")
	b.WriteString("search-level observation, not a plan defect; see the DIVERGENCES.md D10 register entry). NO ")
	b.WriteString("fallback-using GAP case remains: non-resolvable-interface-object/case-02 ")
	b.WriteString("(the expected-errors case the root-pin fall-back rescued into a wrong plan) now fails loud under ")
	b.WriteString("the provable-non-resolvability narrowing and PASSes; requires-interface/case-02 and ")
	b.WriteString("requires-with-fragments/case-02,03 flipped ")
	b.WriteString("GAP -> PASS at the requires-chain wave (per-requires scoped jumps gave them path-consistent routes).\n\n")

	b.WriteString("**Field-argument lowering (M1.5 wave 2) -- LANDED.** The fetch printer now renders each ")
	b.WriteString("selection's arguments, declares the referenced variables in the document's operation header, ")
	b.WriteString("and forwards their values as ContextVariable Input segments; literal argument values are ")
	b.WriteString("normalized into variables (WithExtractVariables) and JSON-escaped at Input assembly if a ")
	b.WriteString("literal ever survives (the ledgered Task-8/10 escaping obligation). A shared root field whose ")
	b.WriteString("children split across subgraphs prints its arguments in every root document it materializes ")
	b.WriteString("in. Argument-bearing `@requires` (DV-006) and the same-coordinate argument-CONFLICT split ")
	b.WriteString("(DV-007) have since LANDED: the D7 tokenizer is argument-aware, `Edge.Requires`/`Edge.RequiresBy` ")
	b.WriteString("carry the literal values + requiring-field association (outside the A-3 identity tuple), and ")
	b.WriteString("lowering renders `price(currency: \"USD\")` into the representation while partitioning ")
	b.WriteString("`shippingEstimate` vs `shippingEstimateEUR` into separate `_entities` fetches (source-document ")
	b.WriteString("aliasing + representation value-path indirection). The formerly-SKIP distributed-`@requires` cases ")
	b.WriteString("(`requires-with-argument` 02-05) are CLOSED by the M2 requires-chain wave (D7pp distributed tails + ")
	b.WriteString("the D11.10 gather pipeline).\n\n")

	b.WriteString("| Suite | Pass | Fail | Gap | Skip |\n|---|---|---|---|---|\n")
	for _, s := range order {
		a := suites[s]
		fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d |\n", s, a.pass, a.fail, a.gap, a.skip)
	}
	fmt.Fprintf(&b, "| **total** | **%d** | **%d** | **%d** | **%d** |\n", pass, fail, gap, skip)
	return b.String()
}
