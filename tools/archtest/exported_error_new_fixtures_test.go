// INVARIANT: EXPORTED-ERROR-NEW-01
//
// exported_error_new_fixtures_test.go — fixture-based regression tests for
// EXPORTED-ERROR-NEW-01. Each subpackage under
// testdata/exported_error_new_fixtures/ exercises one positive or boundary
// case for the gate.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G2
package archtest

import (
	"path/filepath"
	"testing"
)

// runExportedErrorNewFixtureScan loads the fixture package at fixtureDir
// and returns the sorted slice of violation Diagnostics using the same
// walk + predicates as TestExportedErrorNew.
func runExportedErrorNewFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)
				diags = append(diags, scanExportedErrorNewASTDiags(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})
	return diags
}

// TestExportedErrorNewFixtures runs the gate scanner over each fixture
// subpackage and compares against diag.golden.
// GREEN fixtures have an empty golden; RED fixtures capture the real output.
func TestExportedErrorNewFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "exported_error_new_fixtures")

	dirs := []string{
		// GREEN — must produce 0 violations.
		"unexported_var_passes",
		"func_local_passes",
		"errcode_wrap_passes",
		"short_err_name_passes",

		// RED — must produce violations.
		"exported_var_violates",
		"multiple_specs_violates",
		"aliased_import_violates",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, dir)
			diags := runExportedErrorNewFixtureScan(t, fixtureDir)
			AssertGolden(t, filepath.Join(fixtureDir, "diag.golden"), diags)
		})
	}
}
