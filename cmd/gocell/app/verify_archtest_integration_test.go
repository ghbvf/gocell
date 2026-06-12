//go:build integration

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findRootForIntegration walks up from cwd to find the repo root (go.work).
// Mirrors graph_test.go's repoRoot but is local to this file so the
// integration-tagged file compiles standalone without a cross-file dependency
// on the untagged repoRoot helper.
func findRootForIntegration(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err, "getwd")
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("findRootForIntegration: no go.work found walking up from %s", dir)
		}
		dir = parent
	}
}

// TestVerifyArchtest_Integration_ListTests exercises the full CLI dispatch
// path for `gocell verify archtest --list-tests` against the real repo.
// Covers: parseArchtestFlags, parseShard, runArchtestListTests, dispatch wiring.
//
// Guard: GOWORK=off means the workspace is not available; skip in that case
// (same pattern as archtestrunner/integration_test.go).
func TestVerifyArchtest_Integration_ListTests(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off: skipping integration test")
	}
	root := findRootForIntegration(t)
	// Point GOWORK at the real go.work so the workspace is active regardless of
	// ambient GOWORK setting (mirrors graph_test.go's pattern).
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))

	exit, stdout, stderr := captureDispatch(t, context.Background(), []string{
		"verify", "archtest", "--list-tests", "--root=" + root,
	})
	assert.Equal(t, ExitOK, exit,
		"verify archtest --list-tests must exit 0; stderr=%q", stderr)
	assert.NotEmpty(t, strings.TrimSpace(stdout),
		"--list-tests must print at least one test name to stdout")
	// Every line must look like a test function name.
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		assert.True(t, strings.HasPrefix(line, "Test"),
			"list-tests output line %q must start with 'Test'", line)
	}
}

// TestVerifyArchtest_Integration_AliasListTests verifies that the top-level
// `gocell archtest --list-tests` alias delegates to the same handler as
// `gocell verify archtest --list-tests` and produces identical non-empty output.
func TestVerifyArchtest_Integration_AliasListTests(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off: skipping integration test")
	}
	root := findRootForIntegration(t)
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))

	exit, stdout, stderr := captureDispatch(t, context.Background(), []string{
		"archtest", "--list-tests", "--root=" + root,
	})
	assert.Equal(t, ExitOK, exit,
		"archtest alias --list-tests must exit 0; stderr=%q", stderr)
	assert.NotEmpty(t, strings.TrimSpace(stdout),
		"alias --list-tests must print test names")
}

// TestVerifyArchtest_Integration_Report exercises the report path
// (runArchtestReport + mapReportToResults + printer.Print).  A high-K shard
// (0/500) selects very few tests, keeping wall-clock time small while still
// running through the real subprocess and JSON-parse pipeline.
//
// The selected shard may contain zero tests (completely valid if the hash
// partition maps no test to index 0 in a 500-shard split), or it may
// contain a few tests that all pass.  We assert:
//   - exit 0  (pass: no failures in selected subset), OR
//   - exit 1  (acceptable: a subset test legitimately failed — the report path
//     still ran successfully end-to-end).
//
// We do NOT assert exit 2 (usage error) — that would indicate a flag/dispatch
// regression, not a test failure.
func TestVerifyArchtest_Integration_Report(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off: skipping integration test")
	}
	root := findRootForIntegration(t)
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))

	exit, stdout, stderr := captureDispatch(t, context.Background(), []string{
		"verify", "archtest",
		"--shard=0/500",
		"--format=json",
		"--root=" + root,
	})

	// ExitOK (0): selected shard is empty or all tests passed.
	// ExitRuntime (1): report path ran but some tests in shard failed.
	// ExitUsage (2) is the only bad outcome — it means dispatch/flag parsing broke.
	require.NotEqual(t, ExitUsage, exit,
		"report path must not return ExitUsage; stdout=%q stderr=%q", stdout, stderr)

	// The report ran: either stdout has JSON output or is empty (zero-test shard).
	trimmed := strings.TrimSpace(stdout)
	if trimmed != "" {
		assert.True(t,
			strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "["),
			"non-empty --format=json output must be JSON; got: %q", trimmed)
	}
}
