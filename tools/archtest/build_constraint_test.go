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

// findRealTagViolations walks rootDir and returns paths of every
// *_real_test.go file that does NOT carry a //go:build constraint requiring
// strictly more than just the "integration" tag.
//
// Why a separate gate from *_integration_test.go: real-cluster /
// real-broker / real-vault style tests need stricter isolation than the
// default `integration` opt-in (e.g. cluster tests demand a pre-launched
// 6-node cluster which a developer running `go test -tags=integration` has
// not booted). The convention is to use a more specific tag like
// `integration_cluster`. This gate enforces that every *_real_test.go file
// uses such a tag and crucially does NOT pass under `-tags=integration`
// alone — otherwise the file is just an integration test by another name
// and would be better off as *_integration_test.go.
func findRealTagViolations(rootDir string) ([]string, error) {
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
		if !strings.HasSuffix(d.Name(), "_real_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			rel = path
		}

		ok, checkErr := fileHasStricterThanIntegrationTag(path)
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

// fileHasStricterThanIntegrationTag returns true iff the file's //go:build
// expression evaluates to false when ONLY the "integration" tag is active
// (i.e. it requires something more specific) AND evaluates to true under
// some specific tag set. Files matching `_real_test.go` without any
// //go:build constraint, or with one that admits `integration` alone, are
// violations.
func fileHasStricterThanIntegrationTag(path string) (bool, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			if !constraint.IsGoBuild(line) {
				continue
			}
			expr, parseErr := constraint.Parse(line)
			if parseErr != nil {
				return false, parseErr
			}
			withIntegrationOnly := expr.Eval(func(tag string) bool { return tag == "integration" })
			withoutAny := expr.Eval(func(_ string) bool { return false })
			// Strict: must NOT pass under integration alone, must NOT pass under empty tag set.
			return !withIntegrationOnly && !withoutAny, nil
		}
		break
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
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
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

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

// TestArchtest_AllRealTestFiles_HaveStricterTag walks the repository and
// asserts every *_real_test.go file uses a //go:build constraint that does
// NOT pass under `-tags=integration` alone (i.e. requires a strictly more
// specific tag like `integration_cluster`). Without this gate, naming a file
// `*_real_test.go` would be a silent escape hatch around the
// *_integration_test.go gate, blurring the convention.
func TestArchtest_AllRealTestFiles_HaveStricterTag(t *testing.T) {
	root := findModuleRoot(t)

	violations, err := findRealTagViolations(root)
	require.NoError(t, err, "error walking module root")

	if len(violations) > 0 {
		t.Logf("Found %d *_real_test.go file(s) missing a stricter-than-integration build tag:", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	assert.Empty(t, violations,
		"all *_real_test.go files must carry a '//go:build' constraint that does NOT pass "+
			"under '-tags=integration' alone (e.g. 'integration_cluster'); add or tighten "+
			"the constraint at the top of each listed file")
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

// TestArchtest_RealBuildConstraint_Violation_Fixture is the meta-test for
// findRealTagViolations. Mirror of TestArchtest_BuildConstraint_Violation_Fixture
// but for the *_real_test.go gate: only build tags strictly more specific than
// `integration` are accepted.
func TestArchtest_RealBuildConstraint_Violation_Fixture(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name    string
		content string
		wantBad bool
	}{
		{
			// No //go:build line at all — file would compile under any tag,
			// including the bare `integration` runs we want to keep cluster
			// tests out of.
			name:    "bad_no_tag_real_test.go",
			content: "package fixture\n\nimport \"testing\"\n\nfunc TestBad(t *testing.T) {}\n",
			wantBad: true,
		},
		{
			// //go:build integration alone defeats the purpose of using
			// _real_test.go as a stricter tier — it would behave identically
			// to a *_integration_test.go file, so reject.
			name:    "integration_only_real_test.go",
			content: "//go:build integration\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestIntegrationOnly(t *testing.T) {}\n",
			wantBad: true,
		},
		{
			// //go:build integration_cluster — strictly more specific than
			// integration, the canonical pattern for this gate.
			name:    "good_cluster_real_test.go",
			content: "//go:build integration_cluster\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestCluster(t *testing.T) {}\n",
			wantBad: false,
		},
		{
			// `integration && integration_cluster` is also strictly more
			// specific (requires both tags). Accepted.
			name: "good_compound_real_test.go",
			content: "//go:build integration && integration_cluster\n\n" +
				"package fixture\n\nimport \"testing\"\n\nfunc TestCompound(t *testing.T) {}\n",
			wantBad: false,
		},
		{
			// `integration || integration_cluster` accepts the file under
			// `-tags=integration` alone, defeating the gate. Reject.
			name:    "or_relaxed_real_test.go",
			content: "//go:build integration || integration_cluster\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestRelaxed(t *testing.T) {}\n",
			wantBad: true,
		},
	}

	root := t.TempDir()
	for _, fx := range fixtures {
		require.NoError(t, os.WriteFile(filepath.Join(root, fx.name), []byte(fx.content), 0o644))
	}

	violations, err := findRealTagViolations(root)
	require.NoError(t, err, "findRealTagViolations must not return an error")

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
