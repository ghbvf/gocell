package codegen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/codegen"
)

// initRepo creates a fresh git repo with a single committed file at relPath
// containing initialContent. Returns the absolute repo root.
func initRepo(t *testing.T, relPath, initialContent string) string {
	t.Helper()
	root := t.TempDir()
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "config", "user.email", "verify@test")
	mustGit(t, root, "config", "user.name", "verify")
	abs := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(initialContent), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", relPath)
	mustGit(t, root, "commit", "-q", "-m", "initial")
	return root
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v; output: %s", args, err, out)
	}
}

func TestVerifyInWorktree_NoDriftWhenGenerateNoOp(t *testing.T) {
	root := initRepo(t, "src/file.txt", "hello\n")

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
	root := initRepo(t, "src/file.txt", "hello\n")

	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		return os.WriteFile(filepath.Join(workdir, "src/file.txt"), []byte("changed\n"), 0o644)
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
	root := initRepo(t, "src/file.txt", "hello\n")

	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		return os.WriteFile(filepath.Join(workdir, "src/new.txt"), []byte("new\n"), 0o644)
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
	root := initRepo(t, "src/file.txt", "hello\n")

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
