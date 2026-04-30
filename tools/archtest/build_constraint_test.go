package archtest

import (
	"bufio"
	"go/build/constraint"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipDirs lists directory names that are always skipped during the walk.
// worktrees is included because each worktree is an independent checkout of
// the same repository; scanning sibling worktrees would produce false positives
// for files that have not yet been fixed in those branches.
var skipDirs = map[string]bool{
	"vendor":       true,
	".git":         true,
	"worktrees":    true,
	"generated":    true,
	"node_modules": true,
	"testdata":     true,
}

// findIntegrationTagViolations walks rootDir and returns the relative paths (from
// rootDir) of every *_integration_test.go file that does NOT carry a //go:build
// constraint expression that evaluates to true when the "integration" tag is set
// and false when no tags are set.
//
// Parse failures are treated as violations (conservative / fail-closed strategy).
func findIntegrationTagViolations(rootDir string) ([]string, error) {
	var violations []string

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		// Only care about *_integration_test.go files.
		if !strings.HasSuffix(d.Name(), "_integration_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			rel = path
		}

		ok, checkErr := fileHasIntegrationTag(path)
		if checkErr != nil || !ok {
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return violations, nil
}

// fileHasIntegrationTag returns true iff the file carries, in its header
// section (before the package clause and following only blank lines and other
// comments — the only zone the Go toolchain recognizes for build constraints),
// a //go:build line whose constraint expression:
//  1. evaluates to true when the "integration" tag is active, AND
//  2. evaluates to false when no tags are active (i.e., the file is not built
//     unconditionally — it must actually be gated on the integration tag).
//
// A //go:build line that appears after the package clause (or after any other
// non-comment, non-blank line) is invisible to the toolchain and therefore
// counted as a violation, matching the semantics of `go build` / `go test`.
//
// Returns (false, nil) when the file lacks a //go:build line in the header.
// Returns (false, err) when the line cannot be parsed.
func fileHasIntegrationTag(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Header zone ends at the first non-blank, non-comment line. The Go
		// toolchain (see go/build/read.go readGoInfo) stops parsing build
		// constraints once it sees the package clause, so any //go:build below
		// that point would be ignored at compile time and must not be accepted
		// by this gate either.
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			if !constraint.IsGoBuild(line) {
				continue
			}
			expr, parseErr := constraint.Parse(line)
			if parseErr != nil {
				return false, parseErr
			}
			withIntegration := expr.Eval(func(tag string) bool { return tag == "integration" })
			withoutAny := expr.Eval(func(_ string) bool { return false })
			return withIntegration && !withoutAny, nil
		}
		// First substantive line (typically `package …`) — stop scanning.
		break
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	// No //go:build line found in the header zone.
	return false, nil
}

// TestArchtest_AllIntegrationTestFiles_HaveIntegrationBuildTag walks the entire
// repository and asserts that every *_integration_test.go file carries a valid
// //go:build integration constraint.  The test fails with a single aggregated
// report listing all violating files so that the full picture is visible at once.
func TestArchtest_AllIntegrationTestFiles_HaveIntegrationBuildTag(t *testing.T) {
	root := findModuleRoot(t)

	violations, err := findIntegrationTagViolations(root)
	require.NoError(t, err, "error walking module root")

	if len(violations) > 0 {
		t.Logf("Found %d *_integration_test.go file(s) missing //go:build integration:", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	assert.Empty(t, violations,
		"all *_integration_test.go files must carry '//go:build integration'; "+
			"add the constraint at the top of each listed file")
}

// TestArchtest_BuildConstraint_Violation_Fixture is the "test the test" meta-test.
// It creates a temporary directory with three synthetic files:
//   - bad_integration_test.go:        no //go:build line at all   → violation
//   - wrong_tag_integration_test.go:  //go:build other_tag        → violation
//   - good_integration_test.go:       //go:build integration      → no violation
//
// The test verifies that findIntegrationTagViolations catches exactly the two
// bad files and ignores the good one.
func TestArchtest_BuildConstraint_Violation_Fixture(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name    string
		content string
		wantBad bool
	}{
		{
			name:    "bad_integration_test.go",
			content: "package fixture\n\nimport \"testing\"\n\nfunc TestBad(t *testing.T) {}\n",
			wantBad: true,
		},
		{
			name:    "wrong_tag_integration_test.go",
			content: "//go:build other_tag\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestWrong(t *testing.T) {}\n",
			wantBad: true,
		},
		{
			name:    "good_integration_test.go",
			content: "//go:build integration\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestGood(t *testing.T) {}\n",
			wantBad: false,
		},
		{
			// Build constraint placed after the package clause is invisible to
			// the Go toolchain and must be flagged by the gate.
			name:    "misplaced_after_package_integration_test.go",
			content: "package fixture\n\n//go:build integration\n\nimport \"testing\"\n\nfunc TestMisplaced(t *testing.T) {}\n",
			wantBad: true,
		},
	}

	root := t.TempDir()
	for _, fx := range fixtures {
		require.NoError(t, os.WriteFile(filepath.Join(root, fx.name), []byte(fx.content), 0o644))
	}

	violations, err := findIntegrationTagViolations(root)
	require.NoError(t, err, "findIntegrationTagViolations must not return an error")

	// Collect violation basenames for easy assertion.
	violationSet := make(map[string]bool, len(violations))
	for _, v := range violations {
		violationSet[filepath.Base(v)] = true
	}

	wantViolations := 0
	for _, fx := range fixtures {
		if fx.wantBad {
			wantViolations++
			assert.True(t, violationSet[fx.name],
				"expected %q to be flagged as a violation", fx.name)
		} else {
			assert.False(t, violationSet[fx.name],
				"expected %q NOT to be flagged as a violation", fx.name)
		}
	}

	assert.Len(t, violations, wantViolations,
		"fixture must produce exactly %d violations", wantViolations)
}
