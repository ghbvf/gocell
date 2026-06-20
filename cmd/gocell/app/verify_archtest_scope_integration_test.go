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

// nonEmptyLines splits trimmed stdout into non-empty lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestVerifyArchtest_Integration_ScopeSplit drives the full CLI for both scopes
// and asserts the real execution split end-to-end: framework --list-tests yields
// a non-empty, strictly smaller set than workspace --list-tests.
func TestVerifyArchtest_Integration_ScopeSplit(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off: skipping integration test")
	}
	root := findRootForIntegration(t)
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))

	run := func(scope string) []string {
		exit, stdout, stderr := captureDispatch(t, context.Background(), []string{
			"verify", "archtest", "--list-tests", "--scope=" + scope, "--root=" + root,
		})
		require.Equal(t, ExitOK, exit, "--scope=%s --list-tests must exit 0; stderr=%q", scope, stderr)
		return nonEmptyLines(stdout)
	}

	workspace := run("workspace")
	framework := run("framework")
	require.NotEmpty(t, framework, "framework scope must list at least one test")

	wsSet := make(map[string]bool, len(workspace))
	for _, n := range workspace {
		wsSet[n] = true
	}
	for _, n := range framework {
		assert.True(t, wsSet[n], "framework test %q must be in the workspace suite", n)
	}
	assert.Less(t, len(framework), len(workspace),
		"framework scope must be a strict subset of workspace (real split)")
}
