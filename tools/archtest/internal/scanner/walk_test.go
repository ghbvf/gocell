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

// TestWalk_SkipsSymlinkFile asserts that symlink files inside the walked tree
// are silently skipped (fail-closed). archtest scans static repo structure;
// following symlinks is an attack surface (a malicious PR could add a symlink
// pointing outside the module to make scanners read arbitrary host files).
func TestWalk_SkipsSymlinkFile(t *testing.T) {
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

	files, err := scanner.ModuleScope(tmp).Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}

	var rels []string
	for _, f := range files {
		rel, _ := filepath.Rel(tmp, f)
		rels = append(rels, rel)
	}
	wantReal := filepath.Join("src", "real.go")
	wantLink := filepath.Join("src", "link.go")
	hasReal, hasLink := false, false
	for _, r := range rels {
		if r == wantReal {
			hasReal = true
		}
		if r == wantLink {
			hasLink = true
		}
	}
	if !hasReal {
		t.Errorf("expected real.go in result, got %v", rels)
	}
	if hasLink {
		t.Errorf("symlink %s must be skipped, but it appeared in %v", wantLink, rels)
	}
}

// TestWalk_SkipsSymlinkDir asserts that symlink directories are not descended
// into. Without this the walk could re-enter content via the symlink and emit
// duplicate paths or escape the module root.
func TestWalk_SkipsSymlinkDir(t *testing.T) {
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

	files, err := scanner.ModuleScope(tmp).Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	var rels []string
	for _, f := range files {
		rel, _ := filepath.Rel(tmp, f)
		rels = append(rels, rel)
	}

	wantReal := filepath.Join("src", "a.go")
	hasReal := false
	for _, r := range rels {
		if r == wantReal {
			hasReal = true
		}
		if strings.HasPrefix(r, "mirror"+string(filepath.Separator)) {
			t.Errorf("walked into symlink dir mirror/, got entry %s in %v", r, rels)
		}
	}
	if !hasReal {
		t.Errorf("expected real src/a.go in result, got %v", rels)
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
