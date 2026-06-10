//go:build archtest

// INVARIANT: TEST-TIME-LITERAL-01
//
// test_time_literal_fixtures_test.go — fixture-based regression tests for the
// TEST-TIME-LITERAL-01 invariant. Each subpackage under
// testdata/test_time_literal_fixtures/ exercises one test-specific AST shape
// that the universal `scanProdDurationAST` walk must catch (or correctly leave
// alone) when applied to test code.
//
// PROD-DURATION-CONST-01 already exercises 22 generic AST shapes via its own
// fixtures; here we cover only the patterns specific to test files
// (table-driven test struct fields, testify-Eventually-shaped calls, and
// runtime.Gosched poll-with-deadline barriers).
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6 TEST-TIME-LITERAL-01
package archtest

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

// runTestTimeFixtureScan loads the fixture package at fixtureDir and returns
// the sorted slice of "file.go:line" violation strings using the same
// walk+predicates as TestTestTimeLiteralConst. Fixtures are loaded with
// Tests=true so that *_test.go files participate in the type check.
func runTestTimeFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	var violations []string
	Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: true}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsTestCode(rel) {
					continue
				}

				violations = append(violations,
					scanProdDurationAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	sort.Strings(violations)
	return violations
}

// TestTestTimeLiteralFixtures runs the TEST-TIME-LITERAL-01 scanner over the
// test-specific fixture subpackages and compares against diag.golden.
// GREEN fixtures have an empty golden; RED fixtures capture the real output.
func TestTestTimeLiteralFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "test_time_literal_fixtures")

	dirs := []string{
		// RED — two struct-literal Timeout fields.
		"table_field_violates",
		// GREEN — named const passes.
		"eventually_named_const_passes",
		// GREEN — runtime.Gosched passes.
		"runtime_gosched_passes",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, dir)
			raw := runTestTimeFixtureScan(t, fixtureDir)
			diags := parseDurationDiags(raw)
			AssertGolden(t, filepath.Join(fixtureDir, "diag.golden"), diags)
		})
	}
}
