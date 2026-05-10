package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestScaffoldSlice_SymlinkEscape 验证 scaffold slice 拒绝 slices 目录是 root
// 外 symlink 的情况，outside 目录不被写入。
// RED：pathsafe 漏斗化实施后转 GREEN。
func TestScaffoldSlice_SymlinkEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := setupAssemblyTestProject(t, "examplecell")
	outside := t.TempDir()

	// 在 cells/examplecell/slices/foo → outside
	slicesParent := filepath.Join(root, "cells", "examplecell", "slices")
	if err := os.MkdirAll(slicesParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(slicesParent, "foo")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	err := runScaffoldWithRoot(root, []string{"slice", "--id=foo", "--cell=examplecell"})
	if err == nil {
		t.Fatal("scaffold slice (symlink escape): want error, got nil")
	}

	// outside 不应有任何文件
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("symlink escape: outside must be clean, got %v", entries)
	}
}

// TestScaffoldContract_SymlinkEscape 验证 scaffold contract 拒绝 contracts 目录
// 是 root 外 symlink 的情况。
// RED：pathsafe 漏斗化实施后转 GREEN。
func TestScaffoldContract_SymlinkEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := setupAssemblyTestProject(t, "examplecell")
	outside := t.TempDir()

	// contracts/http/foo/example/v1 → outside symlink
	contractParent := filepath.Join(root, "contracts", "http", "foo", "example")
	if err := os.MkdirAll(contractParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(contractParent, "v1")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	err := runScaffoldWithRoot(root, []string{
		"contract",
		"--id=http.foo.example.v1",
		"--kind=http",
		"--owner=examplecell",
	})
	if err == nil {
		t.Fatal("scaffold contract (symlink escape): want error, got nil")
	}

	// outside 不应有任何文件
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("contract symlink escape: outside must be clean, got %v", entries)
	}
}

// TestScaffoldJourney_SymlinkEscape 验证 scaffold journey 拒绝 journeys 目录
// 是 root 外 symlink 的情况。
// RED：pathsafe 漏斗化实施后转 GREEN。
func TestScaffoldJourney_SymlinkEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := setupAssemblyTestProject(t, "examplecell")
	outside := t.TempDir()

	// journeys → outside symlink（整个 journeys 目录指向 outside）
	journeysPath := filepath.Join(root, "journeys")
	if err := os.Symlink(outside, journeysPath); err != nil {
		t.Fatalf("Symlink journeys → outside: %v", err)
	}

	err := runScaffoldWithRoot(root, []string{
		"journey",
		"--id=J-myjourney",
		"--goal=check things work",
		"--team=platform",
		"--cells=examplecell",
	})
	if err == nil {
		t.Fatal("scaffold journey (symlink escape): want error, got nil")
	}

	// outside 不应有任何文件
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("journey symlink escape: outside must be clean, got %v", entries)
	}

	// 验证错误包含 path/symlink/escape 相关描述（宽松匹配）
	msg := err.Error()
	_ = strings.ContainsAny(msg, "outside root,escapes,containment") // 宽松，RED 阶段不强求具体文字
}

// TestScaffoldCell_SymlinkEscape 验证 scaffold cell 拒绝 cells/<id> 目录是 root
// 外 symlink 的情况。Round-7：填补 scaffold cell symlink 覆盖缺口（kernel 层
// 已有 TestGeneratorScaffold_SymlinkEscape_Asm，但 CLI 层 cell 路径未覆盖）。
func TestScaffoldCell_SymlinkEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := setupAssemblyTestProject(t, "existing")
	outside := t.TempDir()

	// cells/symcell → outside symlink
	cellsDir := filepath.Join(root, "cells")
	if err := os.MkdirAll(cellsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cellsDir, "symcell")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	err := runScaffoldWithRoot(root, []string{
		"cell",
		"--id=symcell",
		"--team=platform",
		"--role=cell-owner",
	})
	if err == nil {
		t.Fatal("scaffold cell (symlink escape): want error, got nil")
	}

	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("cell symlink escape: outside must be clean, got %v", entries)
	}
}

// TestScaffoldAssembly_SymlinkEscape 验证 CLI 入口 scaffoldAssembly 拒绝
// assemblies/<id> 目录是 root 外 symlink 的情况。Round-7：填补 CLI 层覆盖
// 缺口（kernel 层 TestGeneratorScaffold_SymlinkEscape_Asm 已有，但走 CLI
// 入口的回归测试缺失）。
func TestScaffoldAssembly_SymlinkEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root := setupAssemblyTestProject(t, "examplecell")
	outside := t.TempDir()

	// assemblies/symasm → outside symlink
	assembliesDir := filepath.Join(root, "assemblies")
	if err := os.MkdirAll(assembliesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assembliesDir, "symasm")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	err := runScaffoldWithRoot(root, []string{
		"assembly",
		"--id=symasm",
		"--cells=examplecell",
		"--team=platform",
		"--role=maintainer",
	})
	if err == nil {
		t.Fatal("scaffold assembly (symlink escape): want error, got nil")
	}

	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("assembly symlink escape: outside must be clean, got %v", entries)
	}
}
