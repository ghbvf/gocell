package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

// preRenderCell generates all cell scaffolds for the project at root.
// Used by tests to set up a "verify clean" state before running verify commands.
func preRenderCell(t *testing.T, root string) {
	t.Helper()
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("pre-render metadata parse: %v", err)
	}
	if _, err := cellgen.Generate(root, project, cellgen.Options{Verify: false}); err != nil {
		t.Fatalf("pre-render generateAll: %v", err)
	}
}

// minimalCodegenProject creates a fake project at root with one cell that has
// goStructName set (opted into codegen), and writes the matching cell_gen.go
// in advance. Returns once the project is at a "verify clean" state.
func minimalCodegenProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/verifycodegentest\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	cellDir := filepath.Join(root, "cells", "demo")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("mkdir cell: %v", err)
	}
	cellYAML := "id: demo\ntype: core\nconsistencyLevel: L1\n" +
		"owner:\n  team: testteam\n  role: owner\n" +
		"schema:\n  primary: demo_table\n" +
		"verify:\n  smoke: []\ngoStructName: Demo\n"
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o644); err != nil {
		t.Fatalf("write cell.yaml: %v", err)
	}

	sliceDir := filepath.Join(cellDir, "slices", "alpha")
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		t.Fatalf("mkdir slice: %v", err)
	}
	sliceYAML := "id: alpha\nbelongsToCell: demo\ncontractUsages: []\n" +
		"verify:\n  unit: []\n  contract: []\n" +
		"allowedFiles:\n  - cells/demo/slices/alpha/**\n"
	if err := os.WriteFile(filepath.Join(sliceDir, "slice.yaml"), []byte(sliceYAML), 0o644); err != nil {
		t.Fatalf("write slice.yaml: %v", err)
	}

	// Pre-render cell_gen.go so the verify test starts in a clean state.
	preRenderCell(t, root)
	return root
}

// chdirToRoot redirects findRoot to the given directory for the duration of
// the test. Tests using this helper must NOT be t.Parallel — os.Chdir is
// process-global.
func chdirToRoot(t *testing.T, root string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// --- flag-parsing tests (cheap, parallel) -----------------------------------

func TestVerifyCodegenCell_UnknownFlag(t *testing.T) {
	t.Parallel()
	if err := verifyCodegenCell([]string{"--bogus"}); err == nil {
		t.Fatal("expected flag-parse error")
	}
}

// --- in-place mode (--local) ------------------------------------------------

func TestVerifyCodegenCell_LocalNoDrift(t *testing.T) {
	root := minimalCodegenProject(t)
	chdirToRoot(t, root)
	if err := verifyCodegenCell([]string{"--local"}); err != nil {
		t.Fatalf("verifyCodegenCell --local on clean project: %v", err)
	}
}

func TestVerifyCodegenCell_LocalDriftWhenGenFileMissing(t *testing.T) {
	root := minimalCodegenProject(t)
	// Remove cell_gen.go to simulate "yaml changed but generate not run".
	if err := os.Remove(filepath.Join(root, "cells", "demo", "cell_gen.go")); err != nil {
		t.Fatalf("remove cell_gen.go: %v", err)
	}
	chdirToRoot(t, root)
	err := verifyCodegenCell([]string{"--local"})
	if err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("expected drift error, got %v", err)
	}
}

func TestVerifyCodegenCell_LocalNoProjectFails(t *testing.T) {
	// findRoot walks up looking for go.mod; an empty temp dir without
	// go.mod fails before reaching cellgen.
	root := t.TempDir()
	chdirToRoot(t, root)
	if err := verifyCodegenCell([]string{"--local"}); err == nil {
		t.Fatal("expected error when no project root")
	}
}

// --- sandbox mode -----------------------------------------------------------

// TestVerifyCodegenCell_SandboxNoDrift exercises sandbox mode (--local=false)
// against a tmp git repo whose HEAD already contains a clean cell_gen.go.
// The sandbox path requires git; skip if the binary is unavailable.
func TestVerifyCodegenCell_SandboxNoDrift(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := minimalCodegenProject(t)
	gitInit(t, root)
	chdirToRoot(t, root)

	if err := verifyCodegenCell([]string{"--local=false"}); err != nil {
		t.Fatalf("verifyCodegenCell sandbox on clean repo: %v", err)
	}
}

func TestVerifyCodegenCell_SandboxDriftWhenStaleCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := minimalCodegenProject(t)
	gitInit(t, root)

	// Sandbox mode clones HEAD and regenerates against HEAD — to provoke
	// drift we must commit a cell.yaml change WITHOUT regenerating
	// cell_gen.go. The HEAD then has stale cell_gen.go vs new cell.yaml.
	cellPath := filepath.Join(root, "cells", "demo", "cell.yaml")
	content := fileutil.MustReadFile(t, cellPath)
	mutated := strings.Replace(string(content), "goStructName: Demo", "goStructName: DemoX", 1)
	fileutil.MustWriteFile(t, cellPath, []byte(mutated))
	mustGitCmd(t, root, "add", "cells/demo/cell.yaml")
	mustGitCmd(t, root, "commit", "-q", "-m", "stale yaml change without regen")

	chdirToRoot(t, root)
	err := verifyCodegenCell([]string{"--local=false"})
	if err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("expected sandbox drift error, got %v", err)
	}
}

func TestVerifyCodegenCell_SandboxNoProjectFails(t *testing.T) {
	root := t.TempDir()
	chdirToRoot(t, root)
	if err := verifyCodegenCell([]string{"--local=false"}); err == nil {
		t.Fatal("expected error when no project root")
	}
}

// --- helper -----------------------------------------------------------------

// gitInit initializes a git repo at root and commits the entire current
// working tree as a single "initial" commit. The sandbox verify path needs a
// committed HEAD to clone via `git worktree add HEAD`.
func gitInit(t *testing.T, root string) {
	t.Helper()
	mustGitCmd(t, root, "init", "-q")
	mustGitCmd(t, root, "config", "user.email", "verify@test")
	mustGitCmd(t, root, "config", "user.name", "verify")
	mustGitCmd(t, root, "add", ".")
	mustGitCmd(t, root, "commit", "-q", "-m", "initial")
}

func mustGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	// test-only helper invoking git with t.TempDir-rooted paths;
	// args are constructed inside the test, not user input.
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test-only helper, args constructed inside test
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v; output: %s", args, err, out)
	}
}
