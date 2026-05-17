// INVARIANT: EXPORTED-ERROR-NEW-01
//
// exported_error_new_fixtures_test.go — fixture-based regression tests for
// EXPORTED-ERROR-NEW-01. Each subpackage under
// testdata/exported_error_new_fixtures/ exercises one positive or boundary
// case for the gate. Expected violation counts are declared inline in each
// fixture via spec.Violation() calls (one per expected diagnostic); the test
// calls AssertDiagnosticCount to enforce got==CountViolationMarkers(pass).
//
// ref: docs/plans/202605011500-029-master-roadmap.md G2
package archtest

import (
	"path/filepath"
	"testing"
)

// TestExportedErrorNewFixtures runs the gate scanner over each fixture
// subpackage and asserts the expected violation count via AssertDiagnosticCount.
func TestExportedErrorNewFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "exported_error_new_fixtures")

	cases := []struct {
		pkg string
	}{
		// Positive — must produce 0 violations.
		{"unexported_var_passes"},
		{"func_local_passes"},
		{"errcode_wrap_passes"},
		{"short_err_name_passes"},

		// Negative — expected diagnostic count declared via spec.Violation()
		// in the fixture .go file (one call per expected violation).
		{"exported_var_violates"},
		{"multiple_specs_violates"},
		{"aliased_import_violates"},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, tc.pkg)
			RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
				func(p *Pass) []Diagnostic {
					var got []Diagnostic
					for _, f := range p.Files {
						rel := p.Rel(f)
						got = append(got, scanExportedErrorNewASTDiags(p.Fset, f, rel, p.TypesInfo)...)
					}
					AssertDiagnosticCount(t, "EXPORTED-ERROR-NEW-01", p, got)
					return nil
				})
		})
	}
}
