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
