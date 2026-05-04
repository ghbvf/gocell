package codegen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/codegen"
)

// fixtureFilePath is the canonical path inside test repos created by initRepo.
const fixtureFilePath = "src/file.txt"

// fixtureInitialContent is the canonical initial commit content used by
// initRepo. Tests that mutate it call os.WriteFile directly inside the
// generateFn closure passed to VerifyInWorktree.
const fixtureInitialContent = "hello\n"

// initRepo creates a fresh git repo with a single committed file at
// fixtureFilePath containing fixtureInitialContent. Returns the absolute repo root.
func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "config", "user.email", "verify@test")
	mustGit(t, root, "config", "user.name", "verify")
	abs := filepath.Join(root, fixtureFilePath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(fixtureInitialContent), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", fixtureFilePath)
	mustGit(t, root, "commit", "-q", "-m", "initial")
	return root
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	// Test-only helper invoking git with t.TempDir-rooted paths; G204
	// false positive because args are constructed inside the test.
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test-only helper, args constructed inside test
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v; output: %s", args, err, out)
	}
}

func TestVerifyInWorktree_NoDriftWhenGenerateNoOp(t *testing.T) {
	root := initRepo(t)

	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		// no-op generator; worktree must remain clean
		return nil
	})
	if err != nil {
		t.Fatalf("VerifyInWorktree returned error: %v", err)
	}
	if len(res.Drifted) != 0 {
		t.Errorf("expected zero drift, got %v", res.Drifted)
	}
}

func TestVerifyInWorktree_DetectsDriftWhenGenerateMutates(t *testing.T) {
	root := initRepo(t)

	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		return os.WriteFile(filepath.Join(workdir, "src", "file.txt"), []byte("changed\n"), 0o644)
	})
	if err != nil {
		t.Fatalf("VerifyInWorktree returned error: %v", err)
	}
	if len(res.Drifted) != 1 {
		t.Fatalf("expected one drifted file, got %v", res.Drifted)
	}
	if res.Drifted[0] != "src/file.txt" {
		t.Errorf("Drifted[0] = %q, want src/file.txt", res.Drifted[0])
	}
	if !strings.Contains(res.DiffSummary, "src/file.txt") {
		t.Errorf("DiffSummary should mention drifted file, got:\n%s", res.DiffSummary)
	}
}

func TestVerifyInWorktree_DetectsNewlyCreatedFile(t *testing.T) {
	root := initRepo(t)

	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		return os.WriteFile(filepath.Join(workdir, "src", "new.txt"), []byte("new\n"), 0o644)
	})
	if err != nil {
		t.Fatalf("VerifyInWorktree returned error: %v", err)
	}
	if len(res.Drifted) != 1 {
		t.Fatalf("expected one drifted entry, got %v", res.Drifted)
	}
	if res.Drifted[0] != "src/new.txt" {
		t.Errorf("Drifted[0] = %q, want src/new.txt", res.Drifted[0])
	}
}

func TestVerifyInWorktree_GenerateFnError(t *testing.T) {
	root := initRepo(t)

	_, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("expected wrapped generateFn error")
	}
	if !strings.Contains(err.Error(), "generateFn") {
		t.Errorf("expected generateFn wrap, got %v", err)
	}
}

func TestVerifyInWorktree_NilGenerateFn(t *testing.T) {
	t.Parallel()
	_, err := codegen.VerifyInWorktree("/tmp/repo", nil)
	if err == nil {
		t.Fatal("expected error for nil generateFn")
	}
}

func TestVerifyInWorktree_EmptyRepoRoot(t *testing.T) {
	t.Parallel()
	_, err := codegen.VerifyInWorktree("", func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error for empty repoRoot")
	}
}

// TestVerifyInWorktree_DetectsRenamedFile verifies that renaming a tracked
// file appears as drift. git status --porcelain reports renames as "R old ->
// new"; parseStatusFiles extracts the new path so Drifted is non-empty.
func TestVerifyInWorktree_DetectsRenamedFile(t *testing.T) {
	root := initRepo(t)

	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		old := filepath.Join(workdir, fixtureFilePath)
		newPath := filepath.Join(workdir, "src", "renamed.txt")
		return os.Rename(old, newPath)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drifted) == 0 {
		t.Error("expected rename to appear as drift")
	}
}

// TestVerifyInWorktree_SpaceInPath verifies that git porcelain quoted paths
// (paths containing spaces are wrapped in double-quotes by git) are correctly
// unquoted so that Drifted contains the real filesystem path, not a
// literal-quoted string. See CORRECT-01.
func TestVerifyInWorktree_SpaceInPath(t *testing.T) {
	root := initRepo(t)

	const spacedPath = "src/with space.txt"
	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		return os.WriteFile(filepath.Join(workdir, spacedPath), []byte("generated\n"), 0o644)
	})
	if err != nil {
		t.Fatalf("VerifyInWorktree returned error: %v", err)
	}
	if len(res.Drifted) != 1 {
		t.Fatalf("expected one drifted file, got %v", res.Drifted)
	}
	if res.Drifted[0] != spacedPath {
		t.Errorf("Drifted[0] = %q, want %q (unquoted path)", res.Drifted[0], spacedPath)
	}
	if strings.Contains(res.Drifted[0], `"`) {
		t.Errorf("Drifted[0] still contains literal quotes: %q", res.Drifted[0])
	}
}
