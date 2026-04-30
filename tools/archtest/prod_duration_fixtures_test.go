// prod_duration_fixtures_test.go — fixture-based regression tests for the
// PROD-DURATION-CONST-01 invariant. Each subpackage under testdata/prod_duration_fixtures/
// exercises one bypass path or boundary condition.
//
// ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6
package archtest

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// runFixtureScan loads the fixture package at fixtureDir and returns the sorted
// slice of "file.go:line" violation strings using the same walk+predicates as
// TestProdDurationConst. Paths outside the fixture module root (stdlib, deps)
// are excluded via the same prodDurationExcludeAbs logic.
func runFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	pkgs, errs, err := typeseval.LoadPackagesWithTags(fixtureDir, []string{"e2e", "integration", "pg"}, "./...")
	require.NoError(t, err, "packages.Load failed for fixture %s", fixtureDir)
	require.Empty(t, errs, "package load errors must fail-closed for %s: %v", fixtureDir, errs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			// Use fixtureDir as root for exclusion: stdlib/dep files have
			// paths outside the fixture dir → "../" prefix → excluded.
			if prodDurationExcludeAbs(fixtureDir, abs) {
				continue
			}

			// Compute path relative to fixture dir for readable violation strings.
			rel, err := filepath.Rel(fixtureDir, abs)
			if err != nil || rel == "" {
				rel = filepath.Base(abs)
			}

			raw := scanProdDurationAST(p.Fset, file, rel, p.TypesInfo)
			violations = append(violations, raw...)
		}
	})

	sort.Strings(violations)
	return violations
}

// TestProdDurationConstFixtures runs the PROD-DURATION-CONST-01 scanner over
// all 22 fixture subpackages (5 positive + 17 negative; package_load_error is
// covered by TestProdDurationConstFailsClosedOnLoadError separately).
func TestProdDurationConstFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "prod_duration_fixtures")

	cases := []struct {
		pkg          string
		wantViolLine []int // expected violation lines; nil = expect 0 violations
	}{
		// Positive — must produce 0 violations
		{"package_const_passes", nil},
		{"package_const_block_passes", nil},
		{"zero_literal_passes", nil},
		{"non_duration_literal_passes", nil},
		{"time_now_add_named_passes", nil},

		// Negative — must produce exact violations
		{"func_local_const_violates", []int{8}},
		{"alias_import_violates", []int{8}},
		{"dot_import_violates", []int{8}},
		{"non_whitelist_sink_violates", []int{11}},
		{"composite_field_violates", []int{12}},
		{"return_violates", []int{8}},
		{"var_init_violates", []int{7}},
		{"var_basicLit_violates", []int{7}},
		{"time_now_add_literal_violates", []int{8}},
		{"switch_case_violates", []int{9}},
		{"for_init_violates", []int{10}},
		{"closure_violates", []int{8}},
		{"type_conversion_violates", []int{9}},
		{"chained_unit_violates", []int{7}},
		{"time_duration_cast_violates", []int{8}},
		{"negative_literal_violates", []int{7}},
		{"addition_violates", []int{8, 8}}, // two violations on same line
		{"build_tag_e2e_violates", []int{10}},
		{"build_tag_integration_violates", []int{10}},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, tc.pkg)
			got := runFixtureScan(t, fixtureDir)

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
				prefix := fmt.Sprintf("usage.go:%d:", line)
				assert.Contains(t, got[i], prefix,
					"fixture %s violation[%d]: expected prefix %q, got %q", tc.pkg, i, prefix, got[i])
			}
		})
	}
}

// TestProdDurationConstFailsClosedOnLoadError verifies that when packages.Load
// encounters a parse/type-check error, the scanner fails closed (require.Empty
// on errs causes test failure) rather than silently passing.
func TestProdDurationConstFailsClosedOnLoadError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "prod_duration_fixtures", "package_load_error")

	_, errs, err := typeseval.LoadPackagesWithTags(fixtureDir, []string{"e2e", "integration", "pg"}, "./...")
	// The load itself should not return a hard error (packages.Load returns
	// partial results with per-package errors for syntax failures), but there
	// must be at least one package error.
	if err != nil {
		// Hard loader error also satisfies fail-closed.
		t.Logf("packages.Load returned hard error (fail-closed): %v", err)
		return
	}
	assert.NotEmpty(t, errs,
		"package_load_error fixture must produce at least one packages.Error to trigger fail-closed")
}
