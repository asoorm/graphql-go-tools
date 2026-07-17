package planv2_test

// diagnostics_external_test.go (external test package: it needs the audit package's federation
// datasource derivation, which imports planv2) proves the D10 typed-loud fall-back END TO END at
// the facade: a real two-subgraph config with a genuine foreign-root model gap -- the requested
// field is reachable only through a root field the client never selected, and no entity key exists
// to jump with -- plans successfully (completeness, honest scope 1) AND surfaces the fall-back as a
// typed RouteFallback plus a Warn diagnostic on PlanWithDiagnostics.

import (
	"strings"
	"testing"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/audit"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/planv2/search"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

// TestPlanWithDiagnostics_SurfacesRouteFallback: subgraph a owns Query.aMedia: Media (id only);
// subgraph b owns Query.bMedia: Media and is the ONLY producer of Media.title. Media is a value
// type (no @key), so there is no entity jump from a to b -- `{ aMedia { title } }` can serve
// `title` only by entering through the foreign root Query.bMedia. That is the registered D10
// model-gap class: the plan is still produced, and the fall-back must now be typed and loud.
func TestPlanWithDiagnostics_SurfacesRouteFallback(t *testing.T) {
	c := audit.Case{
		Name:  "facade-route-fallback",
		Suite: "facade",
		Subgraphs: []audit.Subgraph{
			{Name: "a", SDL: `type Query { aMedia: Media } type Media @shareable { id: ID }`},
			{Name: "b", SDL: `type Query { bMedia: Media } type Media @shareable { id: ID title: String }`},
		},
		Definition: `schema { query: Query } type Query { aMedia: Media bMedia: Media } type Media { id: ID title: String }`,
		Operation:  `{ aMedia { title } }`,
	}
	dataSources, _, err := audit.BuildDataSources(c)
	if err != nil {
		t.Fatalf("build datasources: %v", err)
	}
	p, err := planv2.NewPlanner(plan.Configuration{DataSources: dataSources, DisableResolveFieldPositions: true})
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}

	op, def, report := parseAndNormalizeExt(t, c.Definition, c.Operation)
	result, diags := p.PlanWithDiagnostics(op, def, "", report)
	if report.HasErrors() {
		t.Fatalf("the fall-back preserves completeness -- Plan must succeed, got: %s", report.Error())
	}
	if result == nil {
		t.Fatal("expected a plan")
	}

	if len(diags.RouteFallbacks) == 0 {
		t.Fatal("foreign-root model gap must surface typed RouteFallbacks on the facade result")
	}
	if len(diags.Warnings) != len(diags.RouteFallbacks) {
		t.Fatalf("loud default: one Warn diagnostic per fallback event, got %d warnings for %d events",
			len(diags.Warnings), len(diags.RouteFallbacks))
	}
	var sawTitle bool
	for _, f := range diags.RouteFallbacks {
		if f.Coordinate == "Media.title" && f.RootField == "aMedia" && f.Subgraph == "b" {
			sawTitle = true
		}
	}
	if !sawTitle {
		t.Fatalf("want a fallback record for Media.title anchored to aMedia served via subgraph b, got %+v",
			diags.RouteFallbacks)
	}
	for _, w := range diags.Warnings {
		if w.Severity != planv2.SeverityWarn || w.Code != planv2.DiagRouteFallback {
			t.Fatalf("warnings must be Warn-level %s diagnostics, got %+v", planv2.DiagRouteFallback, w)
		}
		if !strings.Contains(w.Message, "D10") {
			t.Fatalf("warning must cite the D10 clause for triage, got %q", w.Message)
		}
	}
	// The typed record is the search type itself -- no lossy re-encoding at the facade boundary.
	var _ []search.RouteFallback = diags.RouteFallbacks
}

// parseAndNormalizeExt is the facade's pinned input pipeline (see the planv2 package doc), local
// to this external test package.
func parseAndNormalizeExt(t *testing.T, schema, operation string) (*ast.Document, *ast.Document, *operationreport.Report) {
	t.Helper()
	def := unsafeparser.ParseGraphqlDocumentString(schema)
	if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
		t.Fatalf("merge base schema: %v", err)
	}
	op := unsafeparser.ParseGraphqlDocumentString(operation)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
	).NormalizeOperation(&op, &def, report)
	if report.HasErrors() {
		t.Fatalf("normalize: %s", report.Error())
	}
	return &op, &def, report
}
