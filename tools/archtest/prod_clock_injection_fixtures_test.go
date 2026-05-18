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
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
package archtest

import (
	"path/filepath"
	"testing"
)

// runProdClockInjectionFixtureScan loads the fixture package at fixtureDir
// and returns the sorted slice of violation Diagnostics using the same predicate
// as TestProdClockInjection (scanProdClockInjectionAST). Files outside the
// fixture module root (stdlib, deps) are excluded via RunTypedDir's Rel filter.
func runProdClockInjectionFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	return RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				d = append(d, scanProdClockInjectionAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// TestProdClockInjectionFixtures runs the PROD-CLOCK-INJECTION-01 scanner
// over each fixture subpackage and compares against diag.golden.
// GREEN fixtures have an empty golden; RED fixtures capture the real output.
func TestProdClockInjectionFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "prod_clock_injection_fixtures")

	dirs := []string{
		// GREEN — must produce 0 violations.
		"injected_clock_passes",

		// RED — must produce violations.
		"after_violates",
		"newticker_violates",
		"afterfunc_violates",
		"tick_violates",
		"sleep_violates",
		"alias_violates",
		"dot_import_violates",
		"func_value_ref_violates",
		"struct_field_assign_violates",

		// Core time symbols — must also be flagged individually.
		"now_violates",
		"since_violates",
		"until_violates",
		"newtimer_violates",

		// Function-level control-plane marker carve-out self-checks
		// (per ai-collab.md §"盲区自检" / PROD-CLOCK-INJECTION-01 godoc).
		"control_plane_marker_passes",
		"control_plane_marker_wrong_path_violates",
		"control_plane_marker_wrong_func_violates",
		"control_plane_no_marker_violates",
		"control_plane_closure_violates",
		"control_plane_exempt_func_closure_violates",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, dir)
			diags := runProdClockInjectionFixtureScan(t, fixtureDir)
			AssertGolden(t, filepath.Join(fixtureDir, "diag.golden"), diags)
		})
	}
}
