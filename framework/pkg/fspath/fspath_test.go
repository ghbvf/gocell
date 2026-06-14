package fspath_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/fspath"
)

func TestIsWithinRoot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		root   string
		target string
		want   bool
	}{
		{"inside root", "/project/src", "/project/src/cmd/main.go", true},
		{"equals root", "/project/src", "/project/src", true},
		{"escapes root", "/project/src", "/project/etc/passwd", false},
		{"dot-dot escapes", "/project/src", "/project/src/../etc/passwd", false},
		{"different tree", "/project/src", "/other/place", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := fspath.IsWithinRoot(tt.root, tt.target); got != tt.want {
				t.Errorf("IsWithinRoot(%q, %q) = %v, want %v", tt.root, tt.target, got, tt.want)
			}
		})
	}
}

// TestIsWithinRootRelativeRoot covers a relative root ("."): IsWithinRoot must
// absolutize both sides before comparing. The target is constructed under the
// current working directory so it is genuinely contained.
func TestIsWithinRootRelativeRoot(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	target := filepath.Join(cwd, "nested", "artifact.yaml")
	if !fspath.IsWithinRoot(".", target) {
		t.Errorf("IsWithinRoot(%q, %q) = false, want true (relative root must absolutize)", ".", target)
	}
}

// TestIsWithinRootMissingLeaf: a not-yet-existing leaf under an existing root is
// still within root — the longest existing ancestor resolves and the missing
// suffix is appended. This is the common "checking whether a file exists" path.
func TestIsWithinRootMissingLeaf(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "missing", "schema.json")
	if !fspath.IsWithinRoot(root, target) {
		t.Errorf("IsWithinRoot(%q, %q) = false, want true (missing leaf under root)", root, target)
	}
}

// TestIsWithinRootSymlinkEscape: a symlink inside root that points outside root
// must be rejected — symlink resolution defeats the prefix check.
func TestIsWithinRootSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()

	outsideFile := filepath.Join(outside, "secret.yaml")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	symlink := filepath.Join(root, "escape")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	target := filepath.Join(symlink, "secret.yaml")
	if fspath.IsWithinRoot(root, target) {
		t.Errorf("IsWithinRoot(%q, %q) = true, want false (symlink target outside root)", root, target)
	}
}

// TestEvalExistingPrefixResolvesAncestorSymlink: for a non-existent path whose
// parent is a symlink, EvalExistingPrefix resolves the existing-ancestor symlink
// and appends the missing suffix.
func TestEvalExistingPrefixResolvesAncestorSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	realDir := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	resolvedReal, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(realDir): %v", err)
	}
	got := fspath.EvalExistingPrefix(filepath.Join(link, "missing.json"))
	want := filepath.Join(resolvedReal, "missing.json")
	if got != want {
		t.Errorf("EvalExistingPrefix = %q, want %q (ancestor symlink should resolve)", got, want)
	}
}

// TestEvalExistingPrefixExistingPath: a path that fully exists resolves through
// EvalSymlinks.
func TestEvalExistingPrefixExistingPath(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got := fspath.EvalExistingPrefix(dir); got != resolved {
		t.Errorf("EvalExistingPrefix(%q) = %q, want %q", dir, got, resolved)
	}
}
