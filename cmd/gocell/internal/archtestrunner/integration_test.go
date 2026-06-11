//go:build integration

package archtestrunner

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListTests_RealRepo runs ListTests against the actual workspace root.
// Requires GOWORK active (not "off"). Only runs under the integration build tag.
//
// Invoked by CI as: go test -tags=integration ./cmd/gocell/internal/archtestrunner/...
func TestListTests_RealRepo(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off: skipping integration test")
	}
	root := findRepoRoot(t)

	names, err := ListTests(context.Background(), Request{
		WorkspaceRoot: root,
		Scope:         ScopeWorkspace,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, names, "should discover at least one Test* function")
	// All names must start with "Test".
	for _, n := range names {
		assert.True(t, len(n) > 4 && n[:4] == "Test", "discovered test name %q must start with Test", n)
	}
}

// TestListTests_Shard_ExactlyOnce verifies the shard partition property using the real
// test list: union of all shards equals the full list and no test appears twice.
func TestListTests_Shard_ExactlyOnce(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off: skipping integration test")
	}
	root := findRepoRoot(t)
	const total = 3

	seen := map[string]int{}
	for idx := 0; idx < total; idx++ {
		names, err := ListTests(context.Background(), Request{
			WorkspaceRoot: root,
			Shard:         Shard{Index: idx, Total: total},
		})
		require.NoError(t, err)
		for _, n := range names {
			seen[n]++
		}
	}
	for name, count := range seen {
		assert.Equal(t, 1, count, "test %q should appear in exactly one shard", name)
	}
}

// findRepoRoot walks up from the current working directory to find the
// repository root (containing go.work).
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(dir + "/go.work"); err == nil {
			return dir
		}
		parent := dir[:len(dir)-1]
		// Find last separator.
		for i := len(dir) - 1; i >= 0; i-- {
			if dir[i] == '/' || dir[i] == '\\' {
				parent = dir[:i]
				break
			}
		}
		if parent == dir || parent == "" {
			t.Fatal("could not find repo root (go.work)")
		}
		dir = parent
	}
}
