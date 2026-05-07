package scanner_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestWalk_BubblesError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 not reliable on Windows")
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
