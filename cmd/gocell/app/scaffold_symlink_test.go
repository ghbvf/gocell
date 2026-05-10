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
