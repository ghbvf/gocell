// INVARIANT: PROD-CLOCK-INJECTION-01
//
// prod_clock_injection_fixtures_test.go — fixture-based regression tests
// for the PROD-CLOCK-INJECTION-01 invariant. Each subpackage under
// testdata/prod_clock_injection_fixtures/ exercises one bypass path
// (alias / dot-import / function-value reference / struct field assign /
// each forbidden time symbol), the canonical injected-Clock pass shape, or
// the function-level control-plane marker carve-out.
//
// Control-plane marker self-checks (per ai-collab.md §"盲区自检"):
//   - control_plane_marker_passes: GREEN — FuncDecls with doc-comment marker
//     produce 0 violations.
//   - control_plane_no_marker_violates: RED — inline body comment (not doc)
//     is NOT recognized; time.NewTicker is still flagged (1 violation).
//   - control_plane_closure_violates: RED — a non-exempt function containing
//     a closure that calls time.NewTicker is still flagged (1 violation).
//   - control_plane_exempt_func_closure_violates: RED — blind-spot-A closure
//     self-check: time.* inside a FuncLit within an exempt (marked) FuncDecl
//     is NOT exempt; still flagged (1 violation).
//
// Expected violation counts are declared inline in each fixture via
// spec.Violation() calls (one per expected diagnostic); the test calls
// AssertDiagnosticCount to enforce got==CountViolationMarkers(pass).
//
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
package archtest

import (
	"path/filepath"
	"testing"
)

// runProdClockInjectionFixtureScan loads the fixture package at fixtureDir,
// runs the PROD-CLOCK-INJECTION-01 scanner, asserts the diagnostic count
// matches the spec.Violation() markers in the fixture, and returns the
// collected diagnostics.
func runProdClockInjectionFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	var all []Diagnostic
	RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			// Collect diagnostics for this pass only (one pass = one pkg variant).
			var got []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				got = append(got, scanProdClockInjectionAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			AssertDiagnosticCount(t, "PROD-CLOCK-INJECTION-01", p, got)
			all = append(all, got...)
			return nil
		})
	return all
}

// TestProdClockInjectionFixtures runs the PROD-CLOCK-INJECTION-01 scanner
// over each fixture subpackage and asserts the expected violation count via
// spec.Violation() markers declared in the fixture .go files.
func TestProdClockInjectionFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "prod_clock_injection_fixtures")

	cases := []struct {
		pkg string
	}{
		// GREEN cases — expect 0 violations (no spec.Violation() in fixture).
		{pkg: "injected_clock_passes"},
		{pkg: "control_plane_marker_passes"},

		// RED cases — expected diagnostic count declared via spec.Violation()
		// in the fixture .go file (one call per expected violation).
		{pkg: "after_violates"},
		{pkg: "newticker_violates"},
		{pkg: "afterfunc_violates"},
		{pkg: "tick_violates"},
		{pkg: "sleep_violates"},
		{pkg: "alias_violates"},
		{pkg: "dot_import_violates"},
		{pkg: "func_value_ref_violates"},
		{pkg: "struct_field_assign_violates"},

		// Core time symbols — must also be flagged individually.
		{pkg: "now_violates"},
		{pkg: "since_violates"},
		{pkg: "until_violates"},
		{pkg: "newtimer_violates"},

		// Function-level control-plane marker carve-out self-checks
		// (per ai-collab.md §"盲区自检" / PROD-CLOCK-INJECTION-01 godoc).
		{pkg: "control_plane_marker_wrong_path_violates"},
		{pkg: "control_plane_marker_wrong_func_violates"},
		{pkg: "control_plane_no_marker_violates"},
		{pkg: "control_plane_closure_violates"},
		{pkg: "control_plane_exempt_func_closure_violates"},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, tc.pkg)
			runProdClockInjectionFixtureScan(t, fixtureDir)
		})
	}
}
