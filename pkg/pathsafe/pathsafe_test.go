package pathsafe_test

import (
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

func TestContainPath(t *testing.T) {
	t.Parallel()

	type tc struct {
		name      string
		setup     func(t *testing.T) (root, relTarget string)
		wantErr   bool
		wantPath  func(root string) string // used only when wantErr=false and non-nil
		skipOnWin bool
	}

	cases := []tc{
		{
			name: "normal_nested",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				return resolveRealRoot(t), filepath.Join("cells", "mycell", "cell.yaml")
			},
			wantErr: false,
			wantPath: func(root string) string {
				return filepath.Join(root, "cells", "mycell", "cell.yaml")
			},
		},
		{
			name: "dotdot_traversal",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				return resolveRealRoot(t), filepath.Join("..", "escape")
			},
			wantErr: true,
		},
		{
			name: "abs_path",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				return resolveRealRoot(t), "/etc/passwd"
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
				t.Helper()
				return resolveRealRoot(t), filepath.Join("cells", "newcell", "cell.yaml")
			},
			wantErr: false,
			wantPath: func(root string) string {
				return filepath.Join(root, "cells", "newcell", "cell.yaml")
			},
		},
		{
			name: "cleaned_redundant",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				return resolveRealRoot(t), filepath.Join("cells", ".", "mycell", "cell.yaml")
			},
			wantErr: false,
			wantPath: func(root string) string {
				return filepath.Join(root, "cells", "mycell", "cell.yaml")
			},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
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
			if c.wantPath != nil {
				if want := c.wantPath(root); got != want {
					t.Errorf("ContainPath(%s) = %q, want %q", c.name, got, want)
				}
			}
			if got == "" {
				t.Fatalf("ContainPath(%s): returned empty string", c.name)
			}
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
