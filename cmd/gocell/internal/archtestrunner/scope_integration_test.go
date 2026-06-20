//go:build integration

package archtestrunner

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListTests_FrameworkScope_RealRepo proves the scope split is real against
// the actual repo: framework scope is a non-empty, strict subset of workspace
// scope. Anti-vacuity — a framework set equal to (or larger than) workspace, or
// empty, would be a regression.
//
// Invoked by CI as: go test -tags=integration ./cmd/gocell/internal/archtestrunner/...
func TestListTests_FrameworkScope_RealRepo(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off: skipping integration test")
	}
	root := findRepoRoot(t)

	workspace, err := ListTests(context.Background(), Request{WorkspaceRoot: root, Scope: ScopeWorkspace})
	require.NoError(t, err)
	require.NotEmpty(t, workspace)

	framework, err := ListTests(context.Background(), Request{WorkspaceRoot: root, Scope: ScopeFramework})
	require.NoError(t, err)
	require.NotEmpty(t, framework, "framework scope must select at least one test")

	wsSet := make(map[string]bool, len(workspace))
	for _, n := range workspace {
		wsSet[n] = true
	}
	for _, n := range framework {
		assert.True(t, wsSet[n], "framework test %q must also be in the workspace suite", n)
	}
	assert.Less(t, len(framework), len(workspace),
		"framework scope must be a strict subset of workspace (real execution split)")
}
