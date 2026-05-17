package pathsafe_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ghbvf/gocell/pkg/pathsafe"
)

// ---------------------------------------------------------------------------
// TestResolveRoot
// ---------------------------------------------------------------------------

func TestResolveRoot(t *testing.T) {
	t.Parallel()

	t.Run("normal_dir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		got, err := pathsafe.ResolveRoot(dir)
		if err != nil {
			t.Fatalf("ResolveRoot(%q): unexpected error: %v", dir, err)
		}
		if got == "" {
			t.Fatalf("ResolveRoot returned empty string for valid dir")
		}
	})

	t.Run("symlinked_dir", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("symlink semantics differ on windows")
		}
		real := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		got, err := pathsafe.ResolveRoot(link)
		if err != nil {
			t.Fatalf("ResolveRoot(symlink): unexpected error: %v", err)
		}
		// 解析后应等于 real（已 evalSymlinks）
		realResolved, _ := filepath.EvalSymlinks(real)
		if got != realResolved {
			t.Errorf("ResolveRoot(symlink) = %q, want %q (realResolved)", got, realResolved)
		}
	})

	t.Run("non_existent_dir", func(t *testing.T) {
		t.Parallel()
		_, err := pathsafe.ResolveRoot("/this/path/does/not/exist/gocell-pathsafe-test")
		if err == nil {
			t.Fatal("ResolveRoot(non-existent): want error, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// TestContainPath
// ---------------------------------------------------------------------------

// resolveRealRoot resolves a fresh TempDir to its real (symlink-free) path.
func resolveRealRoot(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	return root
}

// setupSymlinkInRoot creates an in-root symlink pointing to an inner dir and
// returns the root. The symlink itself stays within the root boundary.
func setupSymlinkInRoot(t *testing.T) (root, relTarget string) {
	t.Helper()
	root = resolveRealRoot(t)
	inner := filepath.Join(root, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(inner, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	return root, filepath.Join("link", "file.yaml")
}

// setupSymlinkOutOfRoot creates a symlink inside root that points outside and
// returns the root and a relative target that would traverse via that symlink.
func setupSymlinkOutOfRoot(t *testing.T) (root, relTarget string) {
	t.Helper()
	outside := t.TempDir()
	root = resolveRealRoot(t)
	link := filepath.Join(root, "cells", "escape")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	return root, filepath.Join("cells", "escape", "cell.yaml")
}

// containPathCase describes a single TestContainPath sub-test. Lifted out of
// the test function (round-7) so the cases table + assertion logic + test
// driver split into three small functions, dropping cognitive complexity
// below the project budget (15).
type containPathCase struct {
	name      string
	setup     func(t *testing.T) (root, relTarget string)
	wantErr   bool
	wantPath  func(root string) string // used only when wantErr=false and non-nil
	skipOnWin bool
}

// containPathCases returns the table of sub-test inputs for TestContainPath.
// Kept as a function (not a package-level var) so each call gets fresh closure
// bindings and the table reads top-down at the call site.
func containPathCases() []containPathCase {
	resolved := func(t *testing.T) string { t.Helper(); return resolveRealRoot(t) }
	return []containPathCase{
		{
			name:    "normal_nested",
			setup:   func(t *testing.T) (string, string) { return resolved(t), filepath.Join("cells", "mycell", "cell.yaml") },
			wantErr: false,
			wantPath: func(root string) string {
				return filepath.Join(root, "cells", "mycell", "cell.yaml")
			},
		},
		{
			name:    "dotdot_traversal",
			setup:   func(t *testing.T) (string, string) { return resolved(t), filepath.Join("..", "escape") },
			wantErr: true,
		},
		{
			name: "abs_path",
			setup: func(t *testing.T) (string, string) {
				abs := "/etc/passwd"
				if runtime.GOOS == "windows" {
					// Windows absolute paths require a volume; POSIX-style
					// "/etc/passwd" is treated as relative by filepath.IsAbs
					// on Windows, so use a volume-rooted path to exercise the
					// same "caller passed an absolute path" rejection branch.
					abs = `C:\Windows\System32\drivers\etc\hosts`
				}
				return resolved(t), abs
			},
			wantErr: true,
		},
		{
			name:      "parent_symlink_in_root",
			setup:     setupSymlinkInRoot,
			wantErr:   false,
			skipOnWin: true,
		},
		{
			name:      "parent_symlink_out_of_root",
			setup:     setupSymlinkOutOfRoot,
			wantErr:   true,
			skipOnWin: true,
		},
		{
			name: "non_existent_parent",
			setup: func(t *testing.T) (string, string) {
				return resolved(t), filepath.Join("cells", "newcell", "cell.yaml")
			},
			wantErr: false,
			wantPath: func(root string) string {
				return filepath.Join(root, "cells", "newcell", "cell.yaml")
			},
		},
		{
			name: "cleaned_redundant",
			setup: func(t *testing.T) (string, string) {
				return resolved(t), filepath.Join("cells", ".", "mycell", "cell.yaml")
			},
			wantErr: false,
			wantPath: func(root string) string {
				return filepath.Join(root, "cells", "mycell", "cell.yaml")
			},
		},
	}
}

// runContainPathCase executes one TestContainPath sub-test: setup → call →
// assertion. Extracted from the original loop body so cyclomatic + cognitive
// complexity stay bounded.
func runContainPathCase(t *testing.T, c containPathCase) {
	t.Helper()
	if c.skipOnWin && runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root, relTarget := c.setup(t)
	got, err := pathsafe.ContainPath(root, relTarget)
	if c.wantErr {
		if err == nil {
			t.Fatalf("ContainPath(%s): want error, got nil", c.name)
		}
		return
	}
	if err != nil {
		t.Fatalf("ContainPath(%s): unexpected error: %v", c.name, err)
	}
	if got == "" {
		t.Fatalf("ContainPath(%s): returned empty string", c.name)
	}
	if c.wantPath == nil {
		return
	}
	if want := c.wantPath(root); got != want {
		t.Errorf("ContainPath(%s) = %q, want %q", c.name, got, want)
	}
}

func TestContainPath(t *testing.T) {
	t.Parallel()

	for _, c := range containPathCases() {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runContainPathCase(t, c)
		})
	}
}

// ---------------------------------------------------------------------------
// TestPlannedPaths
// ---------------------------------------------------------------------------

func TestPlannedPaths(t *testing.T) {
	t.Parallel()

	plan := []pathsafe.PlannedFile{
		{AbsPath: "/root/a/b.go", Content: []byte("a")},
		{AbsPath: "/root/c/d.yaml", Content: []byte("b")},
		{AbsPath: "/root/e/f.go", Content: []byte("c")},
	}
	got := pathsafe.PlannedPaths(plan)
	if len(got) != len(plan) {
		t.Fatalf("PlannedPaths: len=%d, want %d", len(got), len(plan))
	}
	for i, p := range plan {
		if got[i] != p.AbsPath {
			t.Errorf("PlannedPaths[%d] = %q, want %q", i, got[i], p.AbsPath)
		}
	}
}

func TestPlannedPaths_Empty(t *testing.T) {
	t.Parallel()
	got := pathsafe.PlannedPaths(nil)
	if len(got) != 0 {
		t.Fatalf("PlannedPaths(nil): want empty, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// TestWritePlannedFiles
// ---------------------------------------------------------------------------

func TestWritePlannedFiles_EmptyPlan(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	if err := pathsafe.WritePlannedFiles(root, nil, false); err != nil {
		t.Fatalf("WritePlannedFiles(empty): unexpected error: %v", err)
	}
}

func TestWritePlannedFiles_SingleFile(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	abs := filepath.Join(root, "cells", "mycell", "cell.yaml")
	plan := []pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("id: mycell\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles(single): unexpected error: %v", err)
	}
	data, err := os.ReadFile(abs) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "id: mycell\n" {
		t.Errorf("file content = %q, want %q", data, "id: mycell\n")
	}
}

func TestWritePlannedFiles_MultiFile(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	files := []struct{ rel, content string }{
		{"cells/mycell/cell.yaml", "id: mycell\n"},
		{"cells/mycell/cell.go", "package mycell\n"},
		{"contracts/http/mycell/example/v1/contract.yaml", "id: http.mycell.example.v1\n"},
	}
	var plan []pathsafe.PlannedFile
	for _, f := range files {
		plan = append(plan, pathsafe.PlannedFile{
			AbsPath: filepath.Join(root, f.rel),
			Content: []byte(f.content),
		})
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles(multi): unexpected error: %v", err)
	}
	for _, f := range files {
		abs := filepath.Join(root, f.rel)
		if _, err := os.Stat(abs); err != nil {
			t.Errorf("multi: missing %s: %v", f.rel, err)
		}
	}
}

func TestWritePlannedFiles_DryRunNoWrite(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	abs := filepath.Join(root, "cells", "drycell", "cell.yaml")
	plan := []pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("id: drycell\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, true); err != nil {
		t.Fatalf("WritePlannedFiles(dry-run): unexpected error: %v", err)
	}
	// dry-run 不写文件
	if _, err := os.Stat(abs); err == nil {
		t.Fatal("dry-run wrote file to disk — must not write")
	}
}

func TestWritePlannedFiles_ConflictMidPlanRejectsAll(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	// 预置冲突文件 (plan 第二项)
	conflictAbs := filepath.Join(root, "cells", "mycell", "cell.yaml")
	if err := os.MkdirAll(filepath.Dir(conflictAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictAbs, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(root, "cells", "mycell", "cell.go"), Content: []byte("package mycell\n")},
		{AbsPath: conflictAbs, Content: []byte("id: mycell\n")}, // 冲突
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(conflict): want error, got nil")
	}
	// atomic：冲突前的 cell.go 不应存在（whole-plan rejection）
	if _, err := os.Stat(filepath.Join(root, "cells", "mycell", "cell.go")); err == nil {
		t.Error("conflict mid-plan: cell.go must not have been written (atomic rejection)")
	}
}

func TestWritePlannedFiles_ContainmentFailMidPlanRejectsAll(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	outside := t.TempDir()
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	// 创建 symlink -> outside，作为 plan 第二项的父目录
	escapedDir := filepath.Join(root, "cells", "escape")
	if err := os.MkdirAll(filepath.Dir(escapedDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, escapedDir); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(root, "cells", "goodcell", "cell.yaml"), Content: []byte("id: goodcell\n")},
		{AbsPath: filepath.Join(root, "cells", "escape", "evil.yaml"), Content: []byte("pwned")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(containment fail): want error, got nil")
	}
	// atomic：outside 内不应有任何文件
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("containment fail: outside dir must be clean, got %v", entries)
	}
	// goodcell 也不应存在（whole-plan rejection）
	if _, err := os.Stat(filepath.Join(root, "cells", "goodcell")); err == nil {
		t.Error("containment fail: goodcell must not have been written (atomic rejection)")
	}
}

func TestWritePlannedFiles_MkdirFailureRollback(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on windows")
	}
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	// 创建只读父目录，MkdirAll 会失败
	readonlyParent := filepath.Join(root, "readonly")
	if err := os.MkdirAll(readonlyParent, 0o555); err != nil {
		t.Fatal(err)
	}
	// plan[0] 可以成功写入，plan[1] 在只读父目录下 mkdir 失败
	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(root, "cells", "mycell", "cell.yaml"), Content: []byte("id: mycell\n")},
		{AbsPath: filepath.Join(readonlyParent, "sub", "file.yaml"), Content: []byte("data")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(mkdir fail): want error, got nil")
	}
	// rollback：已写的 cell.yaml 应不存在
	if _, err := os.Stat(filepath.Join(root, "cells", "mycell", "cell.yaml")); err == nil {
		t.Error("mkdir failure rollback: cell.yaml must have been removed")
	}
	// rollback：已创建的空目录 cells/mycell 也应被清理
	if _, err := os.Stat(filepath.Join(root, "cells", "mycell")); err == nil {
		t.Error("mkdir failure rollback: cells/mycell dir must have been cleaned up")
	}
}

func TestWritePlannedFiles_WriteFailureRollback(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	root, err := pathsafe.ResolveRoot(tmp)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	// 预置冲突（WriteFile 失败触发器：目标已存在且是目录）
	conflictPath := filepath.Join(root, "cells", "mycell", "cell.yaml")
	if err := os.MkdirAll(conflictPath, 0o755); err != nil { // 创建同名目录
		t.Fatal(err)
	}
	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(root, "contracts", "http", "mycell", "v1", "contract.yaml"), Content: []byte("id: x\n")},
		{AbsPath: conflictPath, Content: []byte("id: mycell\n")}, // 写入会失败（目标是目录）
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(write fail): want error, got nil")
	}
	// rollback：已写的 contract.yaml 应不存在
	if _, err := os.Stat(filepath.Join(root, "contracts", "http", "mycell", "v1", "contract.yaml")); err == nil {
		t.Error("write failure rollback: contract.yaml must have been removed")
	}
}

// ---------------------------------------------------------------------------
// Leaf symlink tests (RED — pathsafe does not yet check the leaf file symlink)
// ---------------------------------------------------------------------------

// TestWritePlannedFiles_RejectLeafSymlinkDangling verifies that WritePlannedFiles
// rejects a plan when the target AbsPath itself is a dangling symlink pointing
// outside root. The outside destination must NOT be written.
//
// RED: conflictPass uses os.Stat which follows symlinks; a dangling symlink
// returns "not found" → conflict pass succeeds → write creates target at symlink
// destination. pathsafe does not yet call O_NOFOLLOW / Lstat on the leaf.
func TestWritePlannedFiles_RejectLeafSymlinkDangling(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := resolveRealRoot(t)
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "evil.yaml")

	// Place a dangling symlink at the plan AbsPath location.
	targetDir := filepath.Join(root, "cells", "leafsymcell")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	leafLink := filepath.Join(targetDir, "cell.yaml")
	if err := os.Symlink(outsideTarget, leafLink); err != nil {
		t.Fatalf("Symlink dangling: %v", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: leafLink, Content: []byte("id: leafsymcell\n")},
	}
	err := pathsafe.WritePlannedFiles(root, plan, false)
	if err == nil {
		t.Fatal("WritePlannedFiles(dangling leaf symlink): want error, got nil")
	}
	// outside target must NOT have been written
	if _, statErr := os.Stat(outsideTarget); statErr == nil {
		t.Error("leaf symlink escape: outside target was written — must not follow leaf symlink")
	}
}

// TestWritePlannedFiles_RejectLeafSymlinkNonDangling verifies that WritePlannedFiles
// rejects a plan when the target AbsPath is a symlink that points to an existing
// file inside root. The symlink-destination file must NOT be overwritten.
//
// RED: conflictPass sees the resolved file exists → returns ErrConflict, which
// coincidentally rejects. However the rejection reason is "file already exists"
// not "leaf is a symlink"; and the check relies on os.Stat following the link.
// This test documents the INTENDED behavior: leaf symlinks must be rejected
// regardless of whether the destination exists — using Lstat not Stat.
func TestWritePlannedFiles_RejectLeafSymlinkNonDangling(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := resolveRealRoot(t)

	// Create a real file inside root that the leaf symlink will point to.
	realFile := filepath.Join(root, "cells", "realcell", "real.yaml")
	if err := os.MkdirAll(filepath.Dir(realFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realFile, []byte("id: realcell\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink at a different AbsPath that points to realFile.
	leafDir := filepath.Join(root, "cells", "symlinkcell")
	if err := os.MkdirAll(leafDir, 0o755); err != nil {
		t.Fatal(err)
	}
	leafLink := filepath.Join(leafDir, "cell.yaml")
	if err := os.Symlink(realFile, leafLink); err != nil {
		t.Fatalf("Symlink non-dangling: %v", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: leafLink, Content: []byte("id: symlinkcell\n")},
	}
	err := pathsafe.WritePlannedFiles(root, plan, false)
	if err == nil {
		t.Fatal("WritePlannedFiles(non-dangling leaf symlink): want error, got nil")
	}
	// realFile content must be unchanged.
	data, _ := os.ReadFile(realFile) //nolint:gosec // test reads its own fixture
	if string(data) != "id: realcell\n" {
		t.Errorf("leaf symlink escape: realFile was overwritten; got %q", data)
	}
}

// TestWritePlannedFiles_RollbackOnPartialWriteFailure_WriteStageFail verifies
// that rollback occurs when the WRITE STAGE fails (not the conflict stage).
//
// Setup: plan[0] can be written normally; plan[1].AbsPath parent dir is
// pre-created as an unwritable file (not a directory), so os.MkdirAll fails.
// After rollback, plan[0]'s written file must be removed.
//
// This replaces/supplements the existing TestWritePlannedFiles_MkdirFailureRollback
// to ensure the failure happens during writePass, not conflictPass.
func TestWritePlannedFiles_RollbackOnPartialWriteFailure_WriteStageFail(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on windows")
	}

	root := resolveRealRoot(t)

	// Plan[0]: normal file under cells/goodcell/ — succeeds.
	goodFile := filepath.Join(root, "cells", "goodcell", "cell.yaml")

	// containmentPass will reject badFile because /dev/null is outside root.
	// Use a path that passes containment but fails mkdir: create a non-dir file
	// at the expected parent location inside root.
	blockedParentDir := filepath.Join(root, "cells", "badcell", "subdir")
	// Create the path component as a regular file instead of a directory.
	if err := os.MkdirAll(filepath.Dir(blockedParentDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a regular file at "subdir" so MkdirAll("subdir/deepdir") fails.
	if err := os.WriteFile(blockedParentDir, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	blockedFile := filepath.Join(blockedParentDir, "deepdir", "cell.yaml")
	plan2 := []pathsafe.PlannedFile{
		{AbsPath: goodFile, Content: []byte("id: goodcell\n")},
		{AbsPath: blockedFile, Content: []byte("id: badcell\n")},
	}

	if err := pathsafe.WritePlannedFiles(root, plan2, false); err == nil {
		t.Fatal("WritePlannedFiles(write-stage failure): want error, got nil")
	}

	// Rollback: the first file (goodFile) must have been removed.
	if _, statErr := os.Stat(goodFile); statErr == nil {
		t.Error("rollback: goodFile must have been removed after write-stage failure")
	}
	// Rollback: the cells/goodcell directory created by secureMkdirAllAndWrite must be removed.
	if _, statErr := os.Stat(filepath.Join(root, "cells", "goodcell")); statErr == nil {
		t.Error("rollback: cells/goodcell dir must have been cleaned up after write-stage failure")
	}
}

// =============================================================================
// Duplicate AbsPath rejection
// =============================================================================

// Two entries with the same AbsPath must be rejected (whole-plan rejection,
// no temporary file created).
func TestWritePlannedFiles_DupAbsPath_Rejects(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)

	abs := filepath.Join(root, "cells", "dup", "cell.yaml")
	plan := []pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("first")},
		{AbsPath: abs, Content: []byte("second")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(dup AbsPath): want error, got nil")
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		t.Error("dup AbsPath: file written despite duplicate rejection")
	}
}

// Dry-run must also catch duplicates: duplicatePass runs before the dry-run
// early return.
func TestWritePlannedFiles_DupAbsPath_RejectsInDryRun(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)

	abs := filepath.Join(root, "cells", "dup", "cell.yaml")
	plan := []pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("a")},
		{AbsPath: abs, Content: []byte("b")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, true); err == nil {
		t.Fatal("WritePlannedFiles(dup, dryRun): want error, got nil")
	}
}

// ForceOverwrite=true: existing regular file must be replaced with new content.
// conflictPass skips ForceOverwrite entries; writePass removes existing file
// then writes fresh.
func TestWritePlannedFiles_ForceOverwrite_OverwritesExistingFile(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)

	abs := filepath.Join(root, "generated", "stamp.go")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("// old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("// regenerated\n"), ForceOverwrite: true},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles(ForceOverwrite=true over existing): unexpected error: %v", err)
	}
	data, err := os.ReadFile(abs) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "// regenerated\n" {
		t.Errorf("file content = %q, want %q", data, "// regenerated\n")
	}
}

// ForceOverwrite=true on a leaf symlink: the symlink must be removed and
// replaced with a real file at the leaf path; the symlink TARGET (outside
// root) must NOT be written to. Aligns with the existing WriteFileForce
// semantics (Remove → O_EXCL|O_NOFOLLOW write).
func TestWritePlannedFiles_ForceOverwrite_ReplacesLeafSymlinkWithoutFollow(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := resolveRealRoot(t)
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "evil.go")

	leafDir := filepath.Join(root, "generated", "cell")
	if err := os.MkdirAll(leafDir, 0o755); err != nil {
		t.Fatal(err)
	}
	leafLink := filepath.Join(leafDir, "stamp.go")
	if err := os.Symlink(outsideTarget, leafLink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: leafLink, Content: []byte("// regenerated\n"), ForceOverwrite: true},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles(ForceOverwrite over leaf symlink): unexpected error: %v", err)
	}
	// Outside target must NOT have been written through the symlink.
	if _, statErr := os.Stat(outsideTarget); statErr == nil {
		t.Error("leaf symlink escape: outside target was written despite O_NOFOLLOW")
	}
	// Leaf path must now be a regular file with the new content.
	info, err := os.Lstat(leafLink)
	if err != nil {
		t.Fatalf("Lstat leafLink after ForceOverwrite: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("leaf path is still a symlink; ForceOverwrite should have replaced it with a real file")
	}
	data, err := os.ReadFile(leafLink) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatalf("ReadFile leafLink: %v", err)
	}
	if string(data) != "// regenerated\n" {
		t.Errorf("leaf content = %q, want regenerated content", data)
	}
}

// =============================================================================
// Parent symlink TOCTOU — fd-anchored walk rejects ALL parent symlinks
// =============================================================================

// Direct parent is an in-root symlink (pointing to a sibling real dir).
// fd-walk rejects any symlink in the parent chain via
// openat(O_NOFOLLOW|O_DIRECTORY) — fail-closed even for in-root targets,
// because a symlink at parse time could be swapped to out-of-root at write
// time (TOCTOU window).
func TestWritePlannedFiles_ParentSymlink_DirectParent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := resolveRealRoot(t)

	realDir := filepath.Join(root, "cells", "realdir")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symDir := filepath.Join(root, "cells", "symdir")
	if err := os.Symlink(realDir, symDir); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(symDir, "cell.yaml"), Content: []byte("id: symdir\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(direct parent is symlink): want error, got nil")
	}
	// File must NOT have been written through the symlink into realDir.
	if _, statErr := os.Stat(filepath.Join(realDir, "cell.yaml")); statErr == nil {
		t.Error("direct parent symlink: file written via symlink to realDir; fd-walk should have rejected")
	}
}

// Intermediate (non-direct) parent is an in-root symlink: `root/cells` is a
// symlink → `root/realcells`. Plan writes to `root/cells/mycell/cell.yaml`.
// fd-walk rejects at the intermediate openat call.
func TestWritePlannedFiles_ParentSymlink_Intermediate(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := resolveRealRoot(t)

	realCells := filepath.Join(root, "realcells")
	if err := os.MkdirAll(realCells, 0o755); err != nil {
		t.Fatal(err)
	}
	symCells := filepath.Join(root, "cells")
	if err := os.Symlink(realCells, symCells); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(symCells, "mycell", "cell.yaml"), Content: []byte("id: mycell\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(intermediate parent is symlink): want error, got nil")
	}
	if _, statErr := os.Stat(filepath.Join(realCells, "mycell")); statErr == nil {
		t.Error("intermediate parent symlink: dir created via realCells; fd-walk should have rejected")
	}
}

// =============================================================================
// EACCES rollback — fd-walk propagates EACCES through writePass → rollback runs
// =============================================================================

func TestWritePlannedFiles_EACCESRollbackCleansCreatedDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 semantics differ on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0o000 ineffective as root")
	}
	// Not Parallel: chmod 0o000 affects every os.Stat / unix.Openat in this
	// test binary. Other tests in package pathsafe_test that use chmod
	// 0o555 (TestWritePlannedFiles_MkdirFailureRollback) tolerate parallel
	// execution because 0o555 still permits Stat traversal; only 0o000
	// serializes against the whole binary. TestCollectMissingDirs_EACCES
	// (in pathsafe_internal_test.go) runs in a different binary (different
	// package) so does not interact.
	root := resolveRealRoot(t)

	goodFile := filepath.Join(root, "good", "ok.yaml")

	blockedRoot := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blockedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blockedRoot, 0o000); err != nil {
		t.Fatalf("Chmod 0o000: %v", err)
	}
	// LIFO: registered AFTER chmod → runs BEFORE t.TempDir cleanup.
	t.Cleanup(func() { _ = os.Chmod(blockedRoot, 0o755) })

	blockedFile := filepath.Join(blockedRoot, "sub", "bad.yaml")

	plan := []pathsafe.PlannedFile{
		{AbsPath: goodFile, Content: []byte("id: good\n")},
		{AbsPath: blockedFile, Content: []byte("id: bad\n")},
	}
	err := pathsafe.WritePlannedFiles(root, plan, false)
	if err == nil {
		t.Fatal("WritePlannedFiles(EACCES intermediate parent): want error, got nil")
	}
	// Error must not be misclassified as not-exist.
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("EACCES misclassified as not-exist: err=%v", err)
	}

	// Whole-plan / rollback invariant: goodFile and its created parent
	// directory must not exist after the failure.
	if _, statErr := os.Stat(goodFile); statErr == nil {
		t.Error("rollback: goodFile not removed after EACCES failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, "good")); statErr == nil {
		t.Error("rollback: root/good/ dir not cleaned after EACCES failure")
	}
}

// =============================================================================
// Post-ContainPath pre-write parent swap (deterministic TOCTOU window)
// =============================================================================

// TestWritePass_TOCTOURaceWindow_PostContainmentPreSwap verifies that even
// when caller-side ContainPath has accepted a path (parent dir was a real
// directory at that moment), if the parent is swapped to a symlink before
// WritePlannedFiles' writePass runs, the fd-anchored
// openat(O_NOFOLLOW|O_DIRECTORY) chain fails closed.
//
// Deterministic post-ContainPath pre-swap injection (no goroutine, no
// time.Sleep): we manually replicate "caller passes ContainPath, then
// attacker swaps parent" as a single-threaded sequence — call ContainPath
// to confirm acceptance, replace the parent real dir with a symlink to
// outside, then call WritePlannedFiles and assert fail-closed.
func TestWritePass_TOCTOURaceWindow_PostContainmentPreSwap(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows; fd-anchored walk not implemented on windows")
	}

	root := resolveRealRoot(t)
	outside := t.TempDir()

	// Create a real parent directory so ContainPath succeeds.
	parentDir := filepath.Join(root, "cells", "racetest")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	targetAbs := filepath.Join(parentDir, "cell.yaml")
	targetRel, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	// Verify ContainPath accepts the path (parent is a real dir at this moment).
	_, err = pathsafe.ContainPath(targetRel, filepath.Join("cells", "racetest", "cell.yaml"))
	if err != nil {
		t.Fatalf("ContainPath: unexpected error before swap: %v", err)
	}

	// Swap: replace the real parent dir with a symlink to outside.
	// This simulates the TOCTOU window: attacker acts after ContainPath but
	// before WritePlannedFiles' writePass resolves the fd chain.
	if err := os.Remove(parentDir); err != nil {
		t.Fatalf("Remove real dir for swap: %v", err)
	}
	if err := os.Symlink(outside, parentDir); err != nil {
		t.Fatalf("Symlink parent to outside: %v", err)
	}

	// WritePlannedFiles must fail closed: the fd-anchored walk hits O_NOFOLLOW
	// on the symlink at cells/racetest and returns ENOTDIR or ELOOP.
	plan := []pathsafe.PlannedFile{
		{AbsPath: targetAbs, Content: []byte("id: racetest\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles post-ContainPath-pre-write swap: want error, got nil")
	}

	// outside must remain empty — nothing was written through the symlink.
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("TOCTOU escape: outside dir has %d entries after swap, want 0: %v", len(entries), entries)
	}
}

// =============================================================================
// Exported single-file APIs — WriteFile / WriteFileForce
// =============================================================================

// TestWriteFile_HappyPath exercises the single-file shorthand: it must funnel
// through WritePlannedFiles, create parent dirs, and write content with
// O_EXCL|O_NOFOLLOW semantics.
func TestWriteFile_HappyPath(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)
	abs := filepath.Join(root, "cells", "wfcell", "cell.yaml")
	if err := pathsafe.WriteFile(root, abs, []byte("id: wfcell\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: unexpected error: %v", err)
	}
	data, err := os.ReadFile(abs) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "id: wfcell\n" {
		t.Errorf("WriteFile content = %q, want %q", data, "id: wfcell\n")
	}
}

// TestWriteFileForce_OverwritesExisting exercises the codegen-regenerate
// variant: an existing file at the target path is replaced (unlinkat at
// parent fd + O_EXCL recreate), preserving root containment.
func TestWriteFileForce_OverwritesExisting(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)
	abs := filepath.Join(root, "generated", "stamp.go")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("// old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pathsafe.WriteFileForce(root, abs, []byte("// regenerated\n"), 0o644); err != nil {
		t.Fatalf("WriteFileForce: unexpected error: %v", err)
	}
	data, err := os.ReadFile(abs) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "// regenerated\n" {
		t.Errorf("WriteFileForce content = %q, want %q", data, "// regenerated\n")
	}
}

// TestWriteFileForce_RejectsEmptyRealRoot verifies the F10 invariant: empty
// realRoot is no longer accepted (fd-walk requires an anchor; the previous
// "caller-responsible" mode is gone).
func TestWriteFileForce_RejectsEmptyRealRoot(t *testing.T) {
	t.Parallel()
	abs := filepath.Join(t.TempDir(), "stamp.go")
	err := pathsafe.WriteFileForce("", abs, []byte("// data\n"), 0o644)
	if err == nil {
		t.Fatal("WriteFileForce(realRoot=\"\"): want error, got nil")
	}
}

// TestWriteFileForce_EscapesRoot verifies containment is enforced: an absPath
// outside realRoot is rejected before any write happens.
func TestWriteFileForce_EscapesRoot(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)
	outside := t.TempDir()
	escapeAbs := filepath.Join(outside, "escape.go")
	err := pathsafe.WriteFileForce(root, escapeAbs, []byte("// data\n"), 0o644)
	if err == nil {
		t.Fatal("WriteFileForce(absPath outside root): want error, got nil")
	}
	if _, statErr := os.Stat(escapeAbs); statErr == nil {
		t.Error("escape: file written to outside root")
	}
}

// =============================================================================
// ForceOverwrite rollback atomicity — original inode (regular file / symlink)
// must be restored when a subsequent plan entry fails to write.
// =============================================================================

// TestWritePlannedFiles_ForceOverwrite_RollbackRestoresOriginalRegular:
// plan[0] ForceOverwrite=true over an existing regular file F0; plan[1]
// fails mid-write. After rollback, F0 MUST exist with its original content
// and mode — not "no such file or directory".
func TestWritePlannedFiles_ForceOverwrite_RollbackRestoresOriginalRegular(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 semantics differ on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0o000 ineffective as root")
	}
	// Not Parallel: chmod 0o000 affects process-wide os.Stat / unix.Openat.
	root := resolveRealRoot(t)

	f0 := filepath.Join(root, "gen", "stamp.go")
	if err := os.MkdirAll(filepath.Dir(f0), 0o755); err != nil {
		t.Fatal(err)
	}
	originalContent := []byte("// original content\n")
	if err := os.WriteFile(f0, originalContent, 0o644); err != nil {
		t.Fatal(err)
	}

	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("Chmod 0o000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	plan := []pathsafe.PlannedFile{
		{AbsPath: f0, Content: []byte("// new content\n"), ForceOverwrite: true},
		{AbsPath: filepath.Join(blocked, "sub", "fail.go"), Content: []byte("// fail\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(P1 fails): want error, got nil")
	}

	// Atomicity contract: F0 MUST still exist with the original content.
	data, err := os.ReadFile(f0) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatalf("rollback: F0 lost after ForceOverwrite + mid-plan failure: %v", err)
	}
	if !bytes.Equal(data, originalContent) {
		t.Errorf("rollback: F0 content = %q, want original %q", data, originalContent)
	}
	info, err := os.Lstat(f0)
	if err != nil {
		t.Fatalf("rollback: Lstat F0 after restore: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("rollback: F0 mode = %o, want 0o644", info.Mode().Perm())
	}
}

// TestWritePlannedFiles_ForceOverwrite_RollbackRestoresOriginalSymlink:
// plan[0] ForceOverwrite=true over an existing in-root symlink S0 → T0;
// plan[1] fails. After rollback, S0 MUST be restored as a symlink pointing
// to T0 (not gone, not a regular file).
func TestWritePlannedFiles_ForceOverwrite_RollbackRestoresOriginalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0o000 ineffective as root")
	}
	root := resolveRealRoot(t)

	target := filepath.Join(root, "gen", "real-target.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("// target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s0 := filepath.Join(root, "gen", "alias.go")
	if err := os.Symlink(target, s0); err != nil {
		t.Fatal(err)
	}

	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("Chmod 0o000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	plan := []pathsafe.PlannedFile{
		{AbsPath: s0, Content: []byte("// overwrite\n"), ForceOverwrite: true},
		{AbsPath: filepath.Join(blocked, "sub", "fail.go"), Content: []byte("// fail\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(P1 fails): want error, got nil")
	}

	// S0 must be restored as a symlink → target.
	info, err := os.Lstat(s0)
	if err != nil {
		t.Fatalf("rollback: Lstat S0 after restore: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("rollback: S0 should be a symlink after restore, got mode=%v", info.Mode())
	}
	gotTarget, err := os.Readlink(s0)
	if err != nil {
		t.Fatalf("rollback: Readlink S0: %v", err)
	}
	if gotTarget != target {
		t.Errorf("rollback: S0 target = %q, want %q", gotTarget, target)
	}
}

// TestWritePlannedFiles_ForceOverwrite_RejectsUnsupportedOriginalKind:
// when the existing entry at AbsPath is neither a regular file nor a
// symlink (e.g., a directory), ForceOverwrite is rejected pre-write
// because rollback cannot restore it.
func TestWritePlannedFiles_ForceOverwrite_RejectsUnsupportedOriginalKind(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink/mode semantics differ on windows")
	}
	root := resolveRealRoot(t)

	// Pre-place a directory at the leaf path.
	dirAtLeaf := filepath.Join(root, "gen", "occupied")
	if err := os.MkdirAll(dirAtLeaf, 0o755); err != nil {
		t.Fatal(err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: dirAtLeaf, Content: []byte("// data\n"), ForceOverwrite: true},
	}
	err := pathsafe.WritePlannedFiles(root, plan, false)
	if err == nil {
		t.Fatal("WritePlannedFiles(ForceOverwrite over directory): want error, got nil")
	}
	// Directory must still exist (no destructive operation happened).
	info, statErr := os.Lstat(dirAtLeaf)
	if statErr != nil {
		t.Fatalf("dir was destroyed by failed ForceOverwrite: %v", statErr)
	}
	if !info.IsDir() {
		t.Errorf("dir at leaf was replaced by non-dir: mode=%v", info.Mode())
	}
}

// TestWritePlannedFiles_PlanContainmentPass_EscapesRoot exercises the
// lexical escape branch of planContainmentPass (separate from the existing
// "absolute path" / "dotdot" cases handled via ContainPath).
func TestWritePlannedFiles_PlanContainmentPass_EscapesRoot(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)
	// An AbsPath that lies outside root → planContainmentPass returns
	// "target escapes root" before the write funnel runs.
	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(t.TempDir(), "outside.yaml"), Content: []byte("id: x\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(AbsPath escapes root): want error, got nil")
	}
}

// =============================================================================
// ForceOverwrite dry-run / live parity (F2 conformance lock)
// =============================================================================

type forceOverwriteParityCase struct {
	name       string
	setup      func(t *testing.T, abs string)
	wantReject bool
}

// parity-case helpers — prefix disambiguates from setupSymlinkInRoot /
// setupSymlinkOutOfRoot (TestContainPath fixtures) which have a different
// signature and a different semantic domain.

func parityCaseSetupAbsent(t *testing.T, _ string) { t.Helper() }

func parityCaseSetupRegularFile(t *testing.T, abs string) {
	t.Helper()
	if err := os.WriteFile(abs, []byte("// old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func parityCaseSetupSymlink(t *testing.T, abs string) {
	t.Helper()
	if err := os.Symlink(filepath.Join(t.TempDir(), "x"), abs); err != nil {
		t.Fatal(err)
	}
}

func parityCaseSetupDirectory(t *testing.T, abs string) {
	t.Helper()
	if err := os.Mkdir(abs, 0o755); err != nil {
		t.Fatal(err)
	}
}

// runForceOverwriteParityCase exercises one ForceOverwrite kind in dry and
// live modes and asserts the two outcomes agree. Extracted from
// TestWritePlannedFiles_ForceOverwrite_DryRunLiveParity to keep cognitive
// complexity below the project limit. Parallelism is controlled by the
// caller (`t.Parallel()` in the t.Run inline func) — this helper only owns
// the per-case execution logic.
func runForceOverwriteParityCase(t *testing.T, tc forceOverwriteParityCase) {
	t.Helper()

	// Two independent roots so the live write of one case cannot perturb
	// the dry-run of another.
	dryRoot := resolveRealRoot(t)
	liveRoot := resolveRealRoot(t)

	mk := func(root string) []pathsafe.PlannedFile {
		abs := filepath.Join(root, "generated", "stamp.go")
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		tc.setup(t, abs)
		return []pathsafe.PlannedFile{
			{AbsPath: abs, Content: []byte("// regenerated\n"), ForceOverwrite: true},
		}
	}

	dryErr := pathsafe.WritePlannedFiles(dryRoot, mk(dryRoot), true)
	liveErr := pathsafe.WritePlannedFiles(liveRoot, mk(liveRoot), false)

	if (dryErr != nil) != (liveErr != nil) {
		t.Fatalf("dry-run/live parity broken for %s: dryErr=%v liveErr=%v",
			tc.name, dryErr, liveErr)
	}
	if (dryErr != nil) != tc.wantReject {
		t.Fatalf("%s: wantReject=%v but dryErr=%v", tc.name, tc.wantReject, dryErr)
	}
}

// TestWritePlannedFiles_ForceOverwrite_DryRunLiveParity is the F2
// conformance lock: for every ForceOverwrite target inode kind, dry-run and
// live must agree on accept/reject. Before the forceOverwritePreflightPass
// single-source gate, dry-run returned after conflictPass (which skips
// ForceOverwrite entries) and never reached the captureOriginal kind check,
// so a directory/device squatting a generated path passed dry-run but failed
// live. The shared forceOverwriteRestorable predicate makes drift
// unrepresentable; this test pins the observable contract.
func TestWritePlannedFiles_ForceOverwrite_DryRunLiveParity(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("inode-kind / symlink semantics differ on windows")
	}
	cases := []forceOverwriteParityCase{
		{name: "absent", setup: parityCaseSetupAbsent, wantReject: false},
		{name: "regular_file", setup: parityCaseSetupRegularFile, wantReject: false},
		{name: "symlink", setup: parityCaseSetupSymlink, wantReject: false},
		{name: "directory", setup: parityCaseSetupDirectory, wantReject: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runForceOverwriteParityCase(t, tc)
		})
	}
}
