// INVARIANT: PROD-CLOCK-INJECTION-01
//
// prod_clock_injection_fixtures_test.go — fixture-based regression tests
// for the PROD-CLOCK-INJECTION-01 invariant. Each subpackage under
// testdata/prod_clock_injection_fixtures/ exercises one bypass path
// (alias / dot-import / function-value reference / struct field assign /
// each forbidden time symbol) or the canonical injected-Clock pass shape.
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
		wantViolLine []int // expected violation lines; nil = expect 0 violations
	}{
		// Positive — must produce 0 violations
		{"injected_clock_passes", nil},

		// Negative — must produce exactly the listed violations
		{"after_violates", []int{7}},
		{"newticker_violates", []int{7}},
		{"afterfunc_violates", []int{7}},
		{"tick_violates", []int{8}},
		{"sleep_violates", []int{7}},
		{"alias_violates", []int{9}},
		{"dot_import_violates", []int{9}},
		{"func_value_ref_violates", []int{9}},
		{"struct_field_assign_violates", []int{14}},

		// Core time symbols — must also be flagged individually.
		{"now_violates", []int{8}},
		{"since_violates", []int{8}},
		{"until_violates", []int{8}},
		{"newtimer_violates", []int{8}},
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

			for i, line := range tc.wantViolLine {
				if i >= len(got) {
					break
				}
				assert.Equal(t, "usage.go", got[i].Rel,
					"fixture %s violation[%d]: expected Rel=usage.go, got %q",
					tc.pkg, i, got[i].Rel)
				assert.Equal(t, line, got[i].Line,
					"fixture %s violation[%d]: expected Line=%d, got %d",
					tc.pkg, i, line, got[i].Line)
			}
		})
	}
}
