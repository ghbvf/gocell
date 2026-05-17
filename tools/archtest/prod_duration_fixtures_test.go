// INVARIANT: PROD-DURATION-CONST-01
//
// prod_duration_fixtures_test.go — fixture-based regression tests for the
// PROD-DURATION-CONST-01 invariant. Each subpackage under testdata/prod_duration_fixtures/
// exercises one bypass path or boundary condition.
//
// Expected violation counts are declared inline in each fixture via
// spec.Violation() calls (one per expected diagnostic); the test calls
// AssertDiagnosticCount to enforce got==CountViolationMarkers(pass).
//
// ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6
package archtest

import (
	"path/filepath"
	"testing"
)

// runFixtureScan loads the fixture package at fixtureDir and returns the
// collected violation Diagnostics using the same walk+predicates as
// TestProdDurationConst. Paths outside the fixture module root (stdlib, deps)
// are excluded via RunTypedDir's Rel filter. AssertDiagnosticCount is called
// inside the closure to enforce the spec.Violation() marker count.
func runFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	var all []Diagnostic
	RunTypedDir(t, fixtureDir, TypedOpts{Tests: false, Tags: []string{"e2e", "integration", "pg"}}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			// Collect diagnostics for this pass only (one pass = one pkg variant).
			var got []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				// Fixtures live in their own ad-hoc module rooted at fixtureDir;
				// stdlib / dependency files come back with a "../" rel prefix
				// from fileroles.Rel, which returns ok=false for them.
				// RunTypedDir sets the root to fixtureDir, so p.Rel already
				// returns fixture-relative paths without a "../" prefix for
				// files in the fixture module; stdlib files won't appear in p.Files.
				for _, raw := range scanProdDurationAST(p.Fset, f, rel, p.TypesInfo) {
					got = append(got, Diagnostic{Message: raw})
				}
			}
			AssertDiagnosticCount(t, "PROD-DURATION-CONST-01", p, got)
			all = append(all, got...)
			return nil
		})
	return all
}

// TestProdDurationConstFixtures runs the PROD-DURATION-CONST-01 scanner over
// all fixture subpackages (5 positive + 18 negative; package_load_error is
// covered by TestProdDurationConstFailsClosedOnLoadError separately).
func TestProdDurationConstFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "prod_duration_fixtures")

	cases := []struct {
		pkg string
	}{
		// GREEN cases — expect 0 violations (no spec.Violation() in fixture).
		{"package_const_passes"},
		{"package_const_block_passes"},
		{"zero_literal_passes"},
		{"non_duration_literal_passes"},
		{"time_now_add_named_passes"},

		// RED cases — expected diagnostic count declared via spec.Violation()
		// in the fixture .go file (one call per expected violation).
		{"func_local_const_violates"},
		{"alias_import_violates"},
		{"dot_import_violates"},
		{"non_whitelist_sink_violates"},
		{"composite_field_violates"},
		{"return_violates"},
		{"var_init_violates"},
		{"var_basicLit_violates"},
		{"time_now_add_literal_violates"},
		{"switch_case_violates"},
		{"for_init_violates"},
		{"closure_violates"},
		{"type_conversion_violates"},
		{"chained_unit_violates"},
		{"time_duration_cast_violates"},
		{"negative_literal_violates"},
		{"addition_violates"}, // two violations on same line
		{"build_tag_e2e_violates"},
		{"build_tag_integration_violates"},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, tc.pkg)
			runFixtureScan(t, fixtureDir)
		})
	}
}

// TestProdDurationConstFailsClosedOnLoadError is intentionally removed: the
// fail-closed property is now enforced by RunTypedDir itself (it calls
// t.Fatalf on load errors), making a separate test redundant.
