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
// ref: docs/plans/202605011500-029-master-roadmap.md G6 TEST-TIME-LITERAL-01
package archtest

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// runTestTimeFixtureScan loads the fixture package at fixtureDir and returns
// the sorted slice of "file.go:line" violation strings using the same
// walk+predicates as TestTestTimeLiteralConst. Fixtures are loaded with
// Tests=true so that *_test.go files participate in the type check.
func runTestTimeFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	pkgs, errs, err := typeseval.LoadPackages(fixtureDir, true, nil, "./...")
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

			// Fixtures live in their own ad-hoc module rooted at fixtureDir;
			// passing fixtureDir as modRoot to fileroles.Rel produces clean
			// relative paths that exercise the *_test.go include rule.
			rel, ok := fileroles.Rel(fixtureDir, abs)
			if !ok || !fileroles.IsTestCode(rel) {
				continue
			}

			violations = append(violations,
				scanProdDurationAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	return violations
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
		pkg          string
		wantViolLine []int // expected violation lines; nil = expect 0 violations
	}{
		{
			pkg:          "table_field_violates",
			wantViolLine: []int{18, 19}, // two struct-literal Timeout fields
		},
		{pkg: "eventually_named_const_passes"},
		{pkg: "runtime_gosched_passes"},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			fixtureDir := filepath.Join(fixturesBase, tc.pkg)
			got := runTestTimeFixtureScan(t, fixtureDir)
			if len(tc.wantViolLine) == 0 {
				assert.Empty(t, got, "fixture %s: expected 0 violations, got: %v", tc.pkg, got)
				return
			}
			require.Len(t, got, len(tc.wantViolLine), "fixture %s: violation count mismatch (got: %v)", tc.pkg, got)
			for i, want := range tc.wantViolLine {
				assert.Contains(t, got[i], formatLine(want),
					"fixture %s: violation %d expected at line %d (got: %s)", tc.pkg, i, want, got[i])
			}
		})
	}
}

// formatLine returns ":<n>:" — the substring used to anchor a violation
// report at a specific source line, matching the "file.go:<n>: <expr>"
// format produced by scanProdDurationAST.
func formatLine(n int) string {
	return ":" + itoa(n) + ":"
}

// itoa is a minimal int → string helper that avoids importing strconv just to
// format a small line number. Mirrors the style of nearby helpers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
