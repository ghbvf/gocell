//go:build integration

package archtestrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

// TestChangedRepoFiles_TempRepo exercises the real-git functions in gitdiff.go
// (changedRepoFiles → gitMergeBase → gitDiffNames) against a purpose-built
// temporary git repository.
//
// A temp repo is used instead of the real workspace because CI's PR checkout
// does not have an `origin/develop` ref (git merge-base would exit 128). The
// temp repo creates that ref explicitly via `git update-ref`, making the test
// deterministic in CI and locally — independent of the real branch diff.
//
// It asserts the committed + uncommitted changes are reported regardless of
// kind: changedRepoFiles is the raw "what changed" seam (source narrowing to
// affected rules happens downstream in selectByChangedSource), so the
// non-archtest kernel/x.go change is reported too (covering gitMergeBase,
// gitDiffNames, dedupe, splitLines).
func TestChangedRepoFiles_TempRepo(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		//nolint:gosec // G204: git subcommand args are test-controlled literals, not user input
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	write := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	}

	git("init", "-q")
	git("config", "commit.gpgsign", "false")

	fooRel := archtestPkgDir + "/foo_test.go"
	barRel := archtestPkgDir + "/bar_test.go"
	const kernelRel = "kernel/x.go"     // non-archtest file: must be filtered out
	const newRel = "framework/brand.go" // untracked (never `git add`-ed)

	// Base commit; point origin/develop at it so gitMergeBase resolves.
	write(fooRel, "package archtest\n")
	write(kernelRel, "package kernel\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("update-ref", "refs/remotes/origin/develop", "HEAD")

	// HEAD commit: add an archtest file + modify the non-archtest file.
	write(barRel, "package archtest\n")
	write(kernelRel, "package kernel\n\nvar X = 1\n")
	git("add", "-A")
	git("commit", "-q", "-m", "head")

	// Uncommitted working-tree change to a tracked archtest file → exercises
	// the `git diff --name-only HEAD` (working-tree) arm + dedupe.
	write(fooRel, "package archtest\n\nvar Y = 1\n")

	// Untracked new file (never `git add`-ed) → exercises the
	// `git ls-files --others --exclude-standard` arm (#1877 review F3): a
	// locally-created source file must not silently escape --changed.
	write(newRel, "package framework\n")

	changed, err := changedRepoFiles(context.Background(), dir)
	require.NoError(t, err)

	got := map[string]bool{}
	for _, c := range changed {
		got[filepath.ToSlash(c)] = true
	}
	assert.Truef(t, got[barRel], "committed archtest file %q must be reported; got %v", barRel, changed)
	assert.Truef(t, got[fooRel], "uncommitted archtest file %q must be reported; got %v", fooRel, changed)
	assert.Truef(t, got[newRel], "untracked source file %q must be reported; got %v", newRel, changed)
	assert.Truef(t, got[kernelRel], "non-archtest source change %q must also be reported (raw seam); got %v", kernelRel, changed)
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
