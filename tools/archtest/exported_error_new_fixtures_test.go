// exported_error_new_fixtures_test.go — fixture-based regression tests for
// EXPORTED-ERROR-NEW-01. Each subpackage under
// testdata/exported_error_new_fixtures/ exercises one positive or boundary
// case for the gate.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G2
package archtest

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

// runExportedErrorNewFixtureScan loads the fixture package at fixtureDir
// and returns the sorted slice of "file.go:line: ..." violation strings
// using the same walk + predicates as TestExportedErrorNew.
func runExportedErrorNewFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	pkgs, errs, err := typeseval.LoadPackages(fixtureDir, false, nil, "./...")
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

			rel, ok := fileroles.Rel(fixtureDir, abs)
			if !ok {
				continue
			}
			violations = append(violations, scanExportedErrorNewAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	return violations
}

// TestExportedErrorNewFixtures runs the gate scanner over each fixture
// subpackage and asserts the expected violation lines.
func TestExportedErrorNewFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "exported_error_new_fixtures")

	cases := []struct {
		pkg          string
		wantViolLine []int // expected violation lines; nil = expect 0 violations
	}{
		// Positive — must produce 0 violations.
		{"unexported_var_passes", nil},
		{"func_local_passes", nil},
		{"errcode_wrap_passes", nil},
		{"short_err_name_passes", nil},

		// Negative — must produce exact violations.
		{"exported_var_violates", []int{7}},
		{"multiple_specs_violates", []int{9, 10}},
		{"aliased_import_violates", []int{8}},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, tc.pkg)
			got := runExportedErrorNewFixtureScan(t, fixtureDir)

			if len(tc.wantViolLine) == 0 {
				assert.Empty(t, got,
					"fixture %s: expected 0 violations, got %v", tc.pkg, got)
				return
			}

			gotLines := violationLines(t, got)
			wantLines := append([]int(nil), tc.wantViolLine...)
			sort.Ints(gotLines)
			sort.Ints(wantLines)
			assert.Equal(t, wantLines, gotLines,
				"fixture %s: expected violations on lines %v, got %v (raw: %v)",
				tc.pkg, wantLines, gotLines, got)
		})
	}
}

// violationLines extracts the line numbers from "usage.go:<line>: ..." entries.
var fixtureLineRe = regexp.MustCompile(`^usage\.go:(\d+):`)

func violationLines(t *testing.T, raw []string) []int {
	t.Helper()
	out := make([]int, 0, len(raw))
	for _, s := range raw {
		m := fixtureLineRe.FindStringSubmatch(s)
		require.Lenf(t, m, 2, "violation line did not match expected prefix: %q", s)
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		out = append(out, n)
	}
	return out
}
