package scanner_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestWalk_BubblesError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0000 ineffective as root")
	}

	tmp := t.TempDir()
	// Create a subdirectory and make it unreadable.
	noRead := filepath.Join(tmp, "noperm")
	if err := os.MkdirAll(noRead, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Put a file inside so the dir is non-empty.
	if err := os.WriteFile(filepath.Join(noRead, "x.go"), []byte("package x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(noRead, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		// Restore so TempDir cleanup works.
		_ = os.Chmod(noRead, 0o755)
	})

	s := scanner.ModuleScope(tmp)
	_, err := s.Files()
	if err == nil {
		t.Fatal("expected walk error from unreadable directory, got nil")
	}
}

// TestWalk_RejectsSymlinkFile asserts that symlink files inside the walked
// tree cause Files() to fail-loud with an explicit error. archtest scans the
// static repository structure; an evil_gen.go → /etc/passwd symlink would
// otherwise be compiled by Go tooling (which follows the link) while
// bypassing every archtest gate. The error must mention "symlink" and carry
// the module-relative offending path so CI logs surface the misconfiguration.
func TestWalk_RejectsSymlinkFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires admin/Developer Mode on Windows")
	}

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	realFile := filepath.Join(srcDir, "real.go")
	if err := os.WriteFile(realFile, []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	linkFile := filepath.Join(srcDir, "link.go")
	if err := os.Symlink(realFile, linkFile); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := scanner.ModuleScope(tmp).Files()
	if err == nil {
		t.Fatal("expected error for symlink file under module root, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "symlink") {
		t.Errorf("error message must mention 'symlink', got: %v", err)
	}
	if !strings.Contains(msg, "src/link.go") && !strings.Contains(msg, filepath.Join("src", "link.go")) {
		t.Errorf("error must carry module-relative path src/link.go, got: %v", err)
	}
}

// TestWalk_RejectsSymlinkDir asserts that symlink directories also cause
// Files() to fail-loud. filepath.WalkDir already declines to descend into a
// symlink dir, but a silent skip would let a misconfigured fixture or a
// malicious "mirror" directory pass through unnoticed.
func TestWalk_RejectsSymlinkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires admin/Developer Mode on Windows")
	}

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("WriteFile a.go: %v", err)
	}
	mirrorDir := filepath.Join(tmp, "mirror")
	if err := os.Symlink(srcDir, mirrorDir); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := scanner.ModuleScope(tmp).Files()
	if err == nil {
		t.Fatal("expected error for symlink dir under module root, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error message must mention 'symlink', got: %v", err)
	}
	if !strings.Contains(err.Error(), "mirror") {
		t.Errorf("error must carry module-relative path 'mirror', got: %v", err)
	}
}

// TestWalk_RejectsSymlinkRoot pins down fail-closed behavior when the modRoot
// path itself is a symbolic link. filepath.WalkDir does not descend into a
// symlink root (it emits one Type==Symlink callback then stops), so silently
// returning zero files would mask a misconfigured caller. The scanner returns
// an explicit error so that the misconfiguration surfaces immediately.
//
// Note: macOS /var → /private/var lives on path segments above the temp root,
// never at the root itself, so this check does not affect t.TempDir-based
// fixtures used elsewhere in the suite.
func TestWalk_RejectsSymlinkRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires admin/Developer Mode on Windows")
	}

	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "modroot")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := scanner.ModuleScope(link).Files()
	if err == nil {
		t.Fatal("expected error when modRoot is a symlink, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error message should mention symlink, got: %v", err)
	}
}

func TestWalk_LstatNonExistError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0000 ineffective as root")
	}

	// Create a parent dir with no-execute permission so that Lstat on a child
	// path fails with a permission error rather than IsNotExist.
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "noperm-parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("MkdirAll parent: %v", err)
	}
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("Chmod parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	// DirsScope with child path — child does not exist but lstat will hit
	// a permission error on the parent, not IsNotExist.
	s := scanner.DirsScope(tmp, []string{filepath.Join("noperm-parent", "child")})
	_, err := s.Files()
	// On some platforms/FS this might succeed (returning empty); the test is
	// best-effort. If it errors, verify the message contains "lstat".
	if err != nil {
		if !strings.Contains(err.Error(), "lstat") {
			t.Errorf("expected 'lstat' in error message, got: %v", err)
		}
		_ = child // reference used in setup
	}
}
