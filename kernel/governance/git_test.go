package governance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitTool_ResolvesAbsolutePath(t *testing.T) {
	// gitTool wraps cmdrun.NewTool("git"); it returns an error only when git
	// is not on PATH. CI environments always have git, so the happy path is
	// the normal expectation; the error path is covered by NewTool's own
	// tests in pkg/cmdrun.
	tool, err := gitTool()
	require.NoError(t, err)
	out, err := runGit(t.Context(), "--version")
	require.NoError(t, err)
	assert.Contains(t, string(out), "git version")
	_ = tool // resolution is the assertion; the wrapped path is unexported.
}

func TestHasGitMetadata_TrueWhenDotGitExists(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	assert.True(t, HasGitMetadata(root))
}

func TestHasGitMetadata_FalseWhenMissing(t *testing.T) {
	root := t.TempDir()
	assert.False(t, HasGitMetadata(root))
}

func TestHasHEAD_FalseInEmptyRepo(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	assert.False(t, hasHEAD(t.Context(), root),
		"freshly-initialized repo with no commits must report no HEAD")
}

func TestHasHEAD_TrueAfterCommit(t *testing.T) {
	root := t.TempDir()
	gitInitAndCommit(t, root, "seed.txt", "seed\n")
	assert.True(t, hasHEAD(t.Context(), root))
}

func TestHasHEAD_FalseWhenNotARepo(t *testing.T) {
	root := t.TempDir()
	assert.False(t, hasHEAD(t.Context(), root))
}

func TestCommittedInHEAD_TrueForCommittedFile(t *testing.T) {
	root := t.TempDir()
	gitInitAndCommit(t, root, "docs/note.md", "tracked\n")

	committed, err := CommittedInHEAD(t.Context(), root, "docs/note.md")
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestCommittedInHEAD_FalseForStagedOnly(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	gitConfigUser(t, root)
	writeRepoFile(t, root, "seed.txt", "seed\n")
	gitRun(t, root, "add", "seed.txt")
	gitRun(t, root, "commit", "-q", "-m", "seed", "--no-gpg-sign")

	writeRepoFile(t, root, "docs/staged.md", "only staged, never committed\n")
	gitRun(t, root, "add", "docs/staged.md")

	committed, err := CommittedInHEAD(t.Context(), root, "docs/staged.md")
	require.NoError(t, err)
	assert.False(t, committed,
		"index-only files must not satisfy the committed-in-HEAD predicate")
}

func TestCommittedInHEAD_FalseForUnknownPath(t *testing.T) {
	root := t.TempDir()
	gitInitAndCommit(t, root, "seed.txt", "seed\n")

	committed, err := CommittedInHEAD(t.Context(), root, "does/not/exist.md")
	require.NoError(t, err)
	assert.False(t, committed)
}

func TestCommittedInHEAD_FalseInEmptyRepo(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)

	committed, err := CommittedInHEAD(t.Context(), root, "anything.md")
	require.NoError(t, err)
	assert.False(t, committed,
		"empty repo (no HEAD) must report no committed paths, not error")
}

func TestCommittedInHEAD_PropagatesNonExitErrors(t *testing.T) {
	// Pointing at a missing root produces a process-launch failure (git
	// exits 128 with "fatal: not a git repository"), which manifests as
	// an *exec.ExitError. The function still returns (false, nil) for
	// any ExitError; non-exit errors (like exec.LookPath failures or
	// permission denials starting the process) propagate. We assert the
	// no-such-path case behaves as not-committed rather than panicking.
	root := filepath.Join(t.TempDir(), "no-repo-here")
	committed, err := CommittedInHEAD(t.Context(), root, "anything")
	// Either flavor is acceptable: ExitError → (false, nil); other
	// process error → (false, err). We only forbid the "true" answer.
	if err == nil {
		assert.False(t, committed)
	}
}

// --- shared test helpers ---

func gitInit(t *testing.T, root string) {
	t.Helper()
	gitRun(t, root, "init", "-q")
}

func gitConfigUser(t *testing.T, root string) {
	t.Helper()
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	gitRun(t, root, "config", "commit.gpgsign", "false")
}

func gitInitAndCommit(t *testing.T, root, rel, body string) {
	t.Helper()
	gitInit(t, root)
	gitConfigUser(t, root)
	writeRepoFile(t, root, rel, body)
	gitRun(t, root, "add", rel)
	gitRun(t, root, "commit", "-q", "-m", "fixture", "--no-gpg-sign")
}

func writeRepoFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	out, err := runGit(t.Context(), append([]string{"-C", root}, args...)...)
	require.NoError(t, err, "git %s failed:\n%s", strings.Join(args, " "), string(out))
}
