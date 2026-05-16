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

	"github.com/stretchr/testify/assert"
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
// over each fixture subpackage and asserts the expected violation lines.
func TestProdClockInjectionFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "prod_clock_injection_fixtures")

	cases := []struct {
		pkg          string
		wantViolLine []int  // expected violation lines; nil = expect 0 violations
		wantRel      string // expected Diagnostic.Rel; "" defaults to "usage.go"
	}{
		// Positive — must produce 0 violations
		{pkg: "injected_clock_passes"},

		// Negative — must produce exactly the listed violations
		{pkg: "after_violates", wantViolLine: []int{7}},
		{pkg: "newticker_violates", wantViolLine: []int{7}},
		{pkg: "afterfunc_violates", wantViolLine: []int{7}},
		{pkg: "tick_violates", wantViolLine: []int{8}},
		{pkg: "sleep_violates", wantViolLine: []int{7}},
		{pkg: "alias_violates", wantViolLine: []int{9}},
		{pkg: "dot_import_violates", wantViolLine: []int{9}},
		{pkg: "func_value_ref_violates", wantViolLine: []int{9}},
		{pkg: "struct_field_assign_violates", wantViolLine: []int{14}},

		// Core time symbols — must also be flagged individually.
		{pkg: "now_violates", wantViolLine: []int{8}},
		{pkg: "since_violates", wantViolLine: []int{8}},
		{pkg: "until_violates", wantViolLine: []int{8}},
		{pkg: "newtimer_violates", wantViolLine: []int{8}},

		// Function-level control-plane marker carve-out self-checks
		// (per ai-collab.md §"盲区自检" / PROD-CLOCK-INJECTION-01 godoc).
		//
		// GREEN: marker doc comment AND (rel, name) ∈ controlPlaneClockCarveOut.
		// The fixture file lives at runtime/command/lifecycle.go (mirroring the
		// real allowlisted path) with the two allowlisted func names → 0 viol.
		{pkg: "control_plane_marker_passes"},
		// RED (P1-3): right name + valid marker but WRONG path (usage.go ∉
		// allowlist) — must still be flagged. Proves the marker alone never
		// exempts.
		{pkg: "control_plane_marker_wrong_path_violates", wantViolLine: []int{15}, wantRel: "usage.go"},
		// RED (P1-3): a THIRD marked function on the allowlisted path
		// runtime/command/lifecycle.go — name not in allowlist, must still be
		// flagged. Proves the allowlist is name-exhaustive, not path-blanket.
		{pkg: "control_plane_marker_wrong_func_violates", wantViolLine: []int{15}, wantRel: "runtime/command/lifecycle.go"},
		// RED: inline body comment (not doc comment group) is NOT recognized;
		// time.NewTicker is still flagged.
		{pkg: "control_plane_no_marker_violates", wantViolLine: []int{16}},
		// RED: non-exempt function with closure calling time.NewTicker is flagged.
		{pkg: "control_plane_closure_violates", wantViolLine: []int{22}},
		// RED: blind-spot-A self-check — time.* inside a FuncLit (closure) within
		// an exempt (marked) FuncDecl is NOT exempt; must still be flagged.
		{pkg: "control_plane_exempt_func_closure_violates", wantViolLine: []int{21}},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, tc.pkg)
			got := runProdClockInjectionFixtureScan(t, fixtureDir)

			if len(tc.wantViolLine) == 0 {
				assert.Empty(t, got,
					"fixture %s: expected 0 violations, got %v", tc.pkg, got)
				return
			}

			assert.Equal(t, len(tc.wantViolLine), len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.pkg, len(tc.wantViolLine), len(got), got)

			wantRel := tc.wantRel
			if wantRel == "" {
				wantRel = "usage.go"
			}
			for i, line := range tc.wantViolLine {
				if i >= len(got) {
					break
				}
				assert.Equal(t, wantRel, got[i].Rel,
					"fixture %s violation[%d]: expected Rel=%s, got %q",
					tc.pkg, i, wantRel, got[i].Rel)
				assert.Equal(t, line, got[i].Line,
					"fixture %s violation[%d]: expected Line=%d, got %d",
					tc.pkg, i, line, got[i].Line)
			}
		})
	}
}
