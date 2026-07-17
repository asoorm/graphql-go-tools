package conformance

// oracles.go -- proposition-specific oracles over the observable-plan vocabulary
// (FEDERATION_SEMANTICS.md Section 0.3): fetch documents, target subgraphs, dependency order,
// representation templates, response shape. Each constructor names the FS proposition(s) it
// decides so a failing case reads as a falsified proposition, not a broken helper.

import (
	"fmt"
	"strings"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// singleFetches yields the (index, *SingleFetch) pairs of the artifact fetch list.
func singleFetches(a *Artifacts) []indexedFetch {
	out := make([]indexedFetch, 0, len(a.Fetches))
	for i, item := range a.Fetches {
		if sf, ok := item.Fetch.(*resolve.SingleFetch); ok && sf != nil {
			out = append(out, indexedFetch{i: i, sf: sf, item: item})
		}
	}
	return out
}

type indexedFetch struct {
	i    int
	sf   *resolve.SingleFetch
	item *resolve.FetchItem
}

func (f indexedFetch) subgraph() string { return string(f.sf.DataSourceIdentifier) }
func (f indexedFetch) doc() string      { return fetchDoc(f.sf) }
func (f indexedFetch) isEntity() bool {
	return f.sf.RequiresEntityFetch || f.sf.RequiresEntityBatchFetch
}

// docSelectsField reports whether the document contains `name` as a standalone identifier token
// (never as a substring of a longer identifier -- "name" must not match "__typename").
func docSelectsField(doc, name string) bool {
	isIdent := func(b byte) bool {
		return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	for i := 0; i+len(name) <= len(doc); i++ {
		if doc[i:i+len(name)] != name {
			continue
		}
		if i > 0 && isIdent(doc[i-1]) {
			continue
		}
		if end := i + len(name); end < len(doc) && isIdent(doc[end]) {
			continue
		}
		return true
	}
	return false
}

// OracleFieldServedBy (FS-KEY-1/FS-OVR-1/FS-EXT-1/FS-PROV-2 ...): every fetch document selecting
// field `name` (token-matched) must target one of wantSubgraphs, and at least one fetch does.
func OracleFieldServedBy(name string, wantSubgraphs ...string) Oracle {
	want := map[string]bool{}
	for _, s := range wantSubgraphs {
		want[s] = true
	}
	return func(a *Artifacts) error {
		found := false
		for _, f := range singleFetches(a) {
			if !docSelectsField(f.doc(), name) {
				continue
			}
			if !want[f.subgraph()] {
				return fmt.Errorf("field %q served by subgraph %q, permitted only %v\n  doc: %s",
					name, f.subgraph(), wantSubgraphs, f.doc())
			}
			found = true
		}
		if a.Trigger != nil && docSelectsField(triggerQuery(a.Trigger), name) {
			if !want[a.Trigger.SourceName] {
				return fmt.Errorf("field %q served by trigger subgraph %q, permitted only %v",
					name, a.Trigger.SourceName, wantSubgraphs)
			}
			found = true
		}
		if !found {
			return fmt.Errorf("field %q selected by no fetch document (dropped selection)", name)
		}
		return nil
	}
}

// OracleNoDocContains (FS-ABS-4/6, FS-PROV-2, FS-REQ-2 ...): no fetch document to `subgraph`
// (or to ANY subgraph when subgraph == "") contains needle.
func OracleNoDocContains(subgraph, needle string) Oracle {
	return func(a *Artifacts) error {
		for _, f := range singleFetches(a) {
			if subgraph != "" && f.subgraph() != subgraph {
				continue
			}
			if strings.Contains(f.doc(), needle) {
				return fmt.Errorf("fetch %d (subgraph %q) must not contain %q\n  doc: %s",
					f.i, f.subgraph(), needle, f.doc())
			}
		}
		return nil
	}
}

// OracleRootDocLacks (FS-REQ-3's bypass prohibition, FS-PROV-2's path-scoping): NON-ENTITY
// fetches to `subgraph` must not select needle -- the field may only enter via `_entities`
// re-entry (or not at all in that subgraph).
func OracleRootDocLacks(subgraph, needle string) Oracle {
	return func(a *Artifacts) error {
		for _, f := range singleFetches(a) {
			if f.isEntity() || f.subgraph() != subgraph {
				continue
			}
			if strings.Contains(f.doc(), needle) {
				return fmt.Errorf("root fetch %d to %q must not select %q (input-less resolution / out-of-path grant)\n  doc: %s",
					f.i, subgraph, needle, f.doc())
			}
		}
		return nil
	}
}

// OracleDocContains: some fetch document to `subgraph` ("" = any) contains needle
// (e.g. FS-REQ-4 literal argument rendering, FS-IFO-2 interface-typed entry fragments).
func OracleDocContains(subgraph, needle string) Oracle {
	return func(a *Artifacts) error {
		for _, f := range singleFetches(a) {
			if subgraph != "" && f.subgraph() != subgraph {
				continue
			}
			if strings.Contains(f.doc(), needle) {
				return nil
			}
		}
		if a.Trigger != nil && (subgraph == "" || a.Trigger.SourceName == subgraph) &&
			strings.Contains(triggerQuery(a.Trigger), needle) {
			return nil
		}
		return fmt.Errorf("no fetch document to %q contains %q", subgraph, needle)
	}
}

// OracleEntityFetchCount (FS-ENT/FS-KEY plan-shape assertions): the number of entity
// (`_entities`) fetches is within [min,max].
func OracleEntityFetchCount(min, max int) Oracle {
	return func(a *Artifacts) error {
		n := 0
		for _, f := range singleFetches(a) {
			if f.isEntity() {
				n++
			}
		}
		if n < min || n > max {
			return fmt.Errorf("entity fetch count %d outside obligated [%d,%d]", n, min, max)
		}
		return nil
	}
}

// OracleNoEntityFetchInto (FS-KEY-8): no entity fetch targets the named subgraph.
func OracleNoEntityFetchInto(subgraph string) Oracle {
	return func(a *Artifacts) error {
		for _, f := range singleFetches(a) {
			if f.isEntity() && f.subgraph() == subgraph {
				return fmt.Errorf("FS-KEY-8: entity fetch %d targets %q whose keys are all resolvable:false\n  doc: %s",
					f.i, subgraph, f.doc())
			}
		}
		return nil
	}
}

// OracleFetchBefore (FS-PLAN-2/FS-REQ-1/FS-REQ-6): some fetch selecting `producerNeedle` precedes
// (transitively, via DependsOnFetchIDs) every entity fetch selecting `consumerNeedle`.
func OracleFetchBefore(producerNeedle, consumerNeedle string) Oracle {
	return func(a *Artifacts) error {
		fetches := singleFetches(a)
		producers := map[int]bool{}
		for _, f := range fetches {
			if strings.Contains(f.doc(), producerNeedle) {
				producers[f.i] = true
			}
		}
		if len(producers) == 0 {
			return fmt.Errorf("no fetch selects producer %q", producerNeedle)
		}
		// transitive dependency closure per fetch
		deps := map[int]map[int]bool{}
		var closure func(i int) map[int]bool
		closure = func(i int) map[int]bool {
			if d, ok := deps[i]; ok {
				return d
			}
			d := map[int]bool{}
			deps[i] = d
			for _, f := range fetches {
				if f.i != i {
					continue
				}
				for _, dep := range f.sf.DependsOnFetchIDs {
					d[dep] = true
					for k := range closure(dep) {
						d[k] = true
					}
				}
			}
			return d
		}
		checked := false
		for _, f := range fetches {
			if !strings.Contains(f.doc(), consumerNeedle) {
				continue
			}
			checked = true
			ok := false
			for p := range producers {
				if p == f.i {
					continue // producer and consumer in one fetch: same-document production is fine
				}
				if closure(f.i)[p] {
					ok = true
					break
				}
			}
			// A fetch that selects both needles produces its own input locally -- acceptable.
			if !ok && strings.Contains(f.doc(), producerNeedle) {
				ok = true
			}
			if !ok {
				return fmt.Errorf("fetch %d consumes %q but no producing fetch of %q precedes it (deps=%v)",
					f.i, consumerNeedle, producerNeedle, f.sf.DependsOnFetchIDs)
			}
		}
		if !checked {
			return fmt.Errorf("no fetch selects consumer %q", consumerNeedle)
		}
		return nil
	}
}

// representationPaths collects the nested field paths of every representation template of entity
// fetches into `subgraph` ("" = any): the ResolvableObjectVariable's object tree, rendered as
// dot-joined paths ("__typename", "id", "organization.id", "dimensions.l" ...).
func representationPaths(a *Artifacts, subgraph string) (map[string]bool, int) {
	paths := map[string]bool{}
	entityFetches := 0
	for _, f := range singleFetches(a) {
		if !f.isEntity() {
			continue
		}
		if subgraph != "" && f.subgraph() != subgraph {
			continue
		}
		entityFetches++
		for _, v := range f.sf.Variables {
			rov, ok := v.(*resolve.ResolvableObjectVariable)
			if !ok || rov.Renderer == nil {
				continue
			}
			obj, ok := rov.Renderer.Node.(*resolve.Object)
			if !ok {
				continue
			}
			collectRepPaths(obj, "", paths)
		}
	}
	return paths, entityFetches
}

func collectRepPaths(obj *resolve.Object, prefix string, out map[string]bool) {
	for _, f := range obj.Fields {
		p := string(f.Name)
		if prefix != "" {
			p = prefix + "." + p
		}
		out[p] = true
		switch v := f.Value.(type) {
		case *resolve.Object:
			collectRepPaths(v, p, out)
		case *resolve.Array:
			if inner, ok := v.Item.(*resolve.Object); ok {
				collectRepPaths(inner, p, out)
			}
		}
	}
}

// OracleRepresentationHas (FS-KEY-2/FS-KEY-4/AX-REP-1, FS-REQ-1/AX-REP-2): the representation
// template(s) of entity fetches into subgraph contain every listed dot-path (nested paths assert
// the mirrored-object shape -- never flattened).
func OracleRepresentationHas(subgraph string, dotPaths ...string) Oracle {
	return func(a *Artifacts) error {
		paths, n := representationPaths(a, subgraph)
		if n == 0 {
			return fmt.Errorf("no entity fetch into %q found (representation oracle has nothing to inspect)", subgraph)
		}
		for _, p := range dotPaths {
			if !paths[p] {
				return fmt.Errorf("representation for %q lacks obligated coordinate %q (have %v)", subgraph, p, keys(paths))
			}
		}
		return nil
	}
}

// OracleRepresentationLacks (FS-REQ-2's converse guard, FS-KEY-10 target exclusion probes):
// representation templates into subgraph contain NONE of the listed dot-paths.
func OracleRepresentationLacks(subgraph string, dotPaths ...string) Oracle {
	return func(a *Artifacts) error {
		paths, n := representationPaths(a, subgraph)
		if n == 0 {
			return nil
		}
		for _, p := range dotPaths {
			if paths[p] {
				return fmt.Errorf("representation for %q must not carry %q", subgraph, p)
			}
		}
		return nil
	}
}

// OracleResponseShapeHas (FS-PLAN-3, FS-ABS-4's shape half): the plan's response tree carries a
// field at the dot-path (keys are client response keys; list nesting is transparent).
func OracleResponseShapeHas(dotPath string) Oracle {
	return func(a *Artifacts) error {
		if !responseTreeHas(a.ResponseTree, strings.Split(dotPath, ".")) {
			return fmt.Errorf("response shape lacks obligated key path %q", dotPath)
		}
		return nil
	}
}

// OracleResponseShapeLacks (FS-REQ-2, FS-INACC-1, FS-KEY-3's injection invisibility): the response
// tree does NOT carry a field at the dot-path.
func OracleResponseShapeLacks(dotPath string) Oracle {
	return func(a *Artifacts) error {
		if responseTreeHas(a.ResponseTree, strings.Split(dotPath, ".")) {
			return fmt.Errorf("response shape must not carry %q (plan-internal input leaked into the shape)", dotPath)
		}
		return nil
	}
}

func responseTreeHas(obj *resolve.Object, path []string) bool {
	if obj == nil || len(path) == 0 {
		return len(path) == 0
	}
	for _, f := range obj.Fields {
		if string(f.Name) != path[0] {
			continue
		}
		if len(path) == 1 {
			return true
		}
		node := f.Value
		for {
			if arr, ok := node.(*resolve.Array); ok {
				node = arr.Item
				continue
			}
			break
		}
		if child, ok := node.(*resolve.Object); ok && responseTreeHas(child, path[1:]) {
			return true
		}
	}
	return false
}

// OracleZeroFallbacks (war-story families): the D10 route fallback must not fire.
func OracleZeroFallbacks() Oracle {
	return func(a *Artifacts) error {
		if a.Fallbacks != 0 {
			return fmt.Errorf("plan used %d D10 route fallbacks; the family obligates fallback-free routing", a.Fallbacks)
		}
		return nil
	}
}

// OracleRootFetchKeyword (FS-ROOT-3/FS-ENT-5): every non-entity fetch document starts with the
// given keyword ("query"/"mutation"; planv2 prints root query documents as "query {" or "query(").
func OracleRootFetchKeyword(keyword string) Oracle {
	return func(a *Artifacts) error {
		for _, f := range singleFetches(a) {
			if f.isEntity() {
				// FS-ENT-5: entity fetches are query operations regardless of client kind.
				if !strings.HasPrefix(f.doc(), "query") {
					return fmt.Errorf("FS-ENT-5: entity fetch %d must be a query document: %s", f.i, f.doc())
				}
				continue
			}
			if !strings.HasPrefix(f.doc(), keyword) {
				return fmt.Errorf("root fetch %d must use the %q keyword: %s", f.i, keyword, f.doc())
			}
		}
		return nil
	}
}

// OracleTwinPlanEqual (FS-OVR-3 inert override, FS-DEF-7/FS-SUB erasure comparisons): the case's
// plan is canonically identical to the twin case's plan, modulo an optional string replacement
// applied to the twin's canonical form (oldStr -> newStr; "" = none).
func OracleTwinPlanEqual(twin audit.Case, oldStr, newStr string) Oracle {
	return func(a *Artifacts) error {
		// The base replans with the case's own defer setting (an FS-DEF-7 erasure comparison must
		// keep @defer visible to the planner); the twin is always defer-free by construction.
		base, err := planCanon(a.Case.Case, a.Case.Defer)
		if err != nil {
			return fmt.Errorf("base plan: %w", err)
		}
		other, err := planCanon(twin, false)
		if err != nil {
			return fmt.Errorf("twin plan: %w", err)
		}
		if oldStr != "" {
			other = strings.ReplaceAll(other, oldStr, newStr)
		}
		if base != other {
			return fmt.Errorf("plan differs from obligated twin:\n base: %s\n twin: %s", base, other)
		}
		return nil
	}
}

// OracleFetchIndexOrder (FS-ROOT-3 serial mutations): every fetch selecting field `first`
// appears at a smaller RawFetches index than every fetch selecting field `second`. Plan-encoded
// dependency (DependsOnFetchIDs) is NOT required: mutation seriality is realized by the fetch
// tree's serial execution order, which follows emission order -- the executed-truth harness owns
// the stronger timing claim.
func OracleFetchIndexOrder(first, second string) Oracle {
	return func(a *Artifacts) error {
		maxFirst, minSecond := -1, -1
		for _, f := range singleFetches(a) {
			if docSelectsField(f.doc(), first) && f.i > maxFirst {
				maxFirst = f.i
			}
			if docSelectsField(f.doc(), second) && (minSecond == -1 || f.i < minSecond) {
				minSecond = f.i
			}
		}
		if maxFirst == -1 || minSecond == -1 {
			return fmt.Errorf("serial-order oracle: fields %q/%q not both selected", first, second)
		}
		if maxFirst >= minSecond {
			return fmt.Errorf("FS-ROOT-3: fetch selecting %q (index %d) does not precede fetch selecting %q (index %d)", first, maxFirst, second, minSecond)
		}
		return nil
	}
}

// OracleTriggerSubgraph (FS-SUB-2): the trigger targets one of the declaring subgraphs.
func OracleTriggerSubgraph(declaring ...string) Oracle {
	ok := map[string]bool{}
	for _, s := range declaring {
		ok[s] = true
	}
	return func(a *Artifacts) error {
		if a.Trigger == nil {
			return fmt.Errorf("FS-SUB-1: no trigger")
		}
		if !ok[a.Trigger.SourceName] {
			return fmt.Errorf("FS-SUB-2: trigger targets %q, declared only by %v", a.Trigger.SourceName, declaring)
		}
		return nil
	}
}

// OracleFetchCount pins the total fetch count within [min,max] (worked-example plan shapes).
func OracleFetchCount(min, max int) Oracle {
	return func(a *Artifacts) error {
		n := len(a.Fetches)
		if n < min || n > max {
			return fmt.Errorf("fetch count %d outside obligated [%d,%d]", n, min, max)
		}
		return nil
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
