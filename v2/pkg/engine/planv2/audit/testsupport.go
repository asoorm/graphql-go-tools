package audit

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
)

// CaseMaterials is everything a lower-level test needs to drive one corpus case through the planv2
// stages by hand (hypergraph.Build -> obligation.Build -> search.Search -> lower.*): the derived
// federation DataSources and the normalized operation + definition documents. It deliberately does NOT
// pre-run search or lowering, so the caller chooses the lowering PATH (e.g. the obligation-driven path
// under LowerConfig) -- the reason this exists.
type CaseMaterials struct {
	Suite       string
	Name        string
	DataSources []plan.DataSource
	Operation   *ast.Document // normalized against Definition
	Definition  *ast.Document // base-merged supergraph SDL
	ExpectFail  string        // the case's expect-fail.txt marker, "" when none
}

// LoadCaseForTest loads a single audit corpus case by suite and case name and returns the materials to
// run it through planv2 directly -- including through the obligation-driven lowering path, which the
// normal planner entry (planv2.Plan -> lower.Lower) does not select. It is exported ONLY for tests: the
// `lower` package's external test (package lower_test) uses it to exercise the parallel path on real
// corpus fixtures without an import cycle (lower cannot import audit through the planner path; an
// external test package can). testdata is located relative to THIS source file, so the caller's working
// directory does not matter.
//
// Errors (case not found, config failure) are returned, not fatal -- the caller decides. A case that
// planv2 cannot even normalize returns an error with the report.
func LoadCaseForTest(suite, name string) (*CaseMaterials, error) {
	// Reuse the package loader, then pick the case (keeps subgraph/definition derivation identical to
	// the corpus run).
	cases, err := loadCorpus(testdataDir())
	if err != nil {
		return nil, err
	}
	var found *Case
	for i := range cases {
		if cases[i].Suite == suite && cases[i].Name == name {
			found = &cases[i]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("audit: case %s/%s not found in corpus", suite, name)
	}

	dataSources, _, err := BuildDataSources(*found)
	if err != nil {
		return nil, fmt.Errorf("audit: build data sources for %s/%s: %w", suite, name, err)
	}
	op, def, report := parseAndNormalize(found.Definition, found.Operation)
	if report.HasErrors() {
		return nil, fmt.Errorf("audit: normalize %s/%s: %s", suite, name, report.Error())
	}
	return &CaseMaterials{
		Suite:       suite,
		Name:        name,
		DataSources: dataSources,
		Operation:   op,
		Definition:  def,
		ExpectFail:  found.ExpectFail,
	}, nil
}

// testdataDir returns the absolute path to this package's testdata directory, independent of the
// caller's working directory (via the compiled-in source path of this file).
func testdataDir() string {
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "testdata")
}
