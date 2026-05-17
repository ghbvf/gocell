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
// Expected violation counts are declared inline in each fixture via
// spec.Violation() calls (one per expected diagnostic); the test calls
// AssertDiagnosticCount to enforce got==CountViolationMarkers(pass).
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6 TEST-TIME-LITERAL-01
package archtest

import (
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

// runTestTimeFixtureScan loads the fixture package at fixtureDir and returns
// the collected violation Diagnostics using the same walk+predicates as
// TestTestTimeLiteralConst. Fixtures are loaded with Tests=true so that
// *_test.go files participate in the type check. AssertDiagnosticCount is
// called inside the closure to enforce the spec.Violation() marker count.
func runTestTimeFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	var all []Diagnostic
	RunTypedDir(t, fixtureDir, TypedOpts{Tests: true}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			// Collect diagnostics for this pass only (one pass = one pkg variant).
			var got []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsTestCode(rel) {
					continue
				}
				// Fixtures live in their own ad-hoc module rooted at fixtureDir;
				// passing fixtureDir as modRoot to fileroles.Rel produces clean
				// relative paths that exercise the *_test.go include rule.
				for _, raw := range scanProdDurationAST(p.Fset, f, rel, p.TypesInfo) {
					got = append(got, Diagnostic{Message: raw})
				}
			}
			AssertDiagnosticCount(t, "TEST-TIME-LITERAL-01", p, got)
			all = append(all, got...)
			return nil
		})
	return all
}

// TestTestTimeLiteralFixtures runs the TEST-TIME-LITERAL-01 scanner over the
// test-specific fixture subpackages. Each fixture demonstrates one AST shape
// the gate must (or must not) flag.
func TestTestTimeLiteralFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "test_time_literal_fixtures")

	cases := []struct {
		pkg string
	}{
		// RED cases — expected diagnostic count declared via spec.Violation()
		// in the fixture .go file (one call per expected violation).
		{pkg: "table_field_violates"}, // two struct-literal Timeout fields

		// GREEN cases — expect 0 violations (no spec.Violation() in fixture).
		{pkg: "eventually_named_const_passes"},
		{pkg: "runtime_gosched_passes"},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			fixtureDir := filepath.Join(fixturesBase, tc.pkg)
			runTestTimeFixtureScan(t, fixtureDir)
		})
	}
}
