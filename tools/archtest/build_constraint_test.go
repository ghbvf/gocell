// INVARIANT: BUILD-CONSTRAINT-INTEGRATION-TAG-01: every *_integration_test.go must carry a proper //go:build integration constraint
package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// findIntegrationTagViolations walks rootDir and returns the relative paths (from
// rootDir) of every *_integration_test.go file that does NOT carry a //go:build
// constraint expression that evaluates to true when the "integration" tag is set
// AND false under the default toolchain context (no extra tags).
//
// Delegates to fileHasExclusivelyTag(path, "integration") for the per-file gate
// — the same helper drives CI-INTEGRATION-DISCOVERY-01. PR #472 (PR-BT1) routed
// fileHasExclusivelyTag through typeseval.BuildContextPredicate so toolchain
// defaults (GOOS/GOARCH/cgo/unix/gc/go1.X) are honored; this avoids a latent
// false-negative on compound directives like `//go:build integration && linux`.
//
// Parse failures are treated as violations (conservative / fail-closed strategy).
func findIntegrationTagViolations(rootDir string) ([]string, error) {
	scope := scanner.ModuleScope(rootDir, scanner.IncludeTests())
	files, err := scope.Files()
	if err != nil {
		return nil, err
	}

	var violations []string
	for _, path := range files {
		if !strings.HasSuffix(path, "_integration_test.go") {
			continue
		}
		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			rel = path
		}
		ok, checkErr := fileHasExclusivelyTag(path, "integration")
		if checkErr != nil || !ok {
			violations = append(violations, rel)
		}
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
	scope := scanner.ModuleScope(rootDir, scanner.IncludeTests())
	files, err := scope.Files()
	if err != nil {
		return nil, err
	}

	var violations []string
	for _, path := range files {
		if !strings.HasSuffix(path, "_real_test.go") {
			continue
		}
		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			rel = path
		}
		ok, checkErr := fileHasStricterThanIntegrationTag(path)
		if checkErr != nil || !ok {
			violations = append(violations, rel)
		}
	}
	return violations, nil
}

// fileHasStricterThanIntegrationTag returns true iff the file's //go:build
// expression evaluates to false under three scopes:
//  1. tag set = {} (file would build unconditionally)
//  2. tag set = {integration} + toolchain defaults (GOOS/GOARCH/cgo/go1.x),
//     which CI runners satisfy implicitly even with no -tags flag
//  3. tag set = toolchain defaults alone (no integration), catching files
//     that pass under the default build context regardless of tags
//
// All three evals are required: withoutAny catches edge cases like
// //go:build !linux that union-over-GOOS would misclassify; withDefaultCtx
// catches files that pass on a CI Linux runner without any -tags flag.
//
// Only when all three reject does the file genuinely require an opt-in tag
// such as `integration_cluster`. Files matching `_real_test.go` that pass
// under any of those scopes are violations.
func fileHasStricterThanIntegrationTag(path string) (bool, error) {
	expr, err := typeseval.ParseBuildConstraint(path)
	if err != nil {
		return false, err
	}
	if expr == nil {
		return false, nil
	}
	// Three independent evals — each catches a different class of non-stricter file.
	// All three must be false for the file to genuinely require an opt-in tag beyond
	// integration / toolchain defaults.
	withIntegrationCtx := expr.Eval(typeseval.BuildContextPredicate("integration"))
	withoutAny := expr.Eval(func(_ string) bool { return false })
	withDefaultCtx := expr.Eval(typeseval.BuildContextPredicate())
	return !withIntegrationCtx && !withoutAny && !withDefaultCtx, nil
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
		{
			// CI Linux runners satisfy `//go:build linux` automatically. A
			// _real_test.go with that constraint would be pulled in by every
			// default `go test` run, defeating the opt-in semantics.
			name:    "linux_only_real_test.go",
			content: "//go:build linux\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestLinuxOnly(t *testing.T) {}\n",
			wantBad: true,
		},
		{
			// `cgo` is set whenever CGO_ENABLED=1 (the default). Same risk
			// as `linux`: file would compile under bare `go test` on a
			// default-config runner.
			name:    "cgo_only_real_test.go",
			content: "//go:build cgo\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestCgoOnly(t *testing.T) {}\n",
			wantBad: true,
		},
		{
			// Release tags go1.x are satisfied by every newer compiler. The
			// repo Go floor is 1.25, so this constraint passes on every
			// supported toolchain by definition.
			name:    "go125_only_real_test.go",
			content: "//go:build go1.25\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestGo125Only(t *testing.T) {}\n",
			wantBad: true,
		},
		{
			// Combining a default-context tag with the cluster opt-in is
			// fine — both must be satisfied, and `linux` alone does not
			// satisfy the cluster part.
			name: "linux_and_cluster_real_test.go",
			content: "//go:build linux && integration_cluster\n\n" +
				"package fixture\n\nimport \"testing\"\n\nfunc TestLinuxAndCluster(t *testing.T) {}\n",
			wantBad: false,
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
