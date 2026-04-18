//go:build unix

package initialadmin_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/initialadmin"
)

// TestWriteCredentialFile_MkdirAllError exercises the MkdirAll failure branch
// by placing a regular file where the directory should be created.
func TestWriteCredentialFile_MkdirAllError(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	// Create a regular file at the path that WriteCredentialFile will try to use as a directory.
	blocker := filepath.Join(base, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	// Path inside a file (not a directory) — MkdirAll must fail.
	path := filepath.Join(blocker, "subdir", "initial_admin_password")
	err := initialadmin.WriteCredentialFile(path, makePayload("admin", "pass"))
	if err == nil {
		t.Fatal("expected error from MkdirAll, got nil")
	}
}

// TestWriteCredentialFile_OpenFileError exercises the OpenFile failure branch
// by making the directory read-only after creation so the .tmp file can't be created.
func TestWriteCredentialFile_OpenFileError(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dir := filepath.Join(base, "protected")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("setup: MkdirAll: %v", err)
	}
	// Make directory read-only so no new files can be created.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("setup: Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	path := filepath.Join(dir, "initial_admin_password")
	err := initialadmin.WriteCredentialFile(path, makePayload("admin", "pass"))
	if err == nil {
		t.Fatal("expected error from OpenFile on read-only dir, got nil")
	}
}

// TestRemoveCredentialFile_RemoveError exercises the os.Remove failure branch
// by making the parent directory read-only so the file can't be deleted.
func TestRemoveCredentialFile_RemoveError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")

	if err := initialadmin.WriteCredentialFile(path, makePayload("admin", "pass")); err != nil {
		t.Fatalf("setup: WriteCredentialFile: %v", err)
	}

	// Make parent directory read-only to prevent removal.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("setup: Chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := initialadmin.RemoveCredentialFile(path)
	if err == nil {
		t.Fatal("expected error from os.Remove on read-only dir, got nil")
	}
	// Must NOT be a tamper error — the mode is still 0600.
	if errors.Is(err, initialadmin.ErrCredFileTampered) {
		t.Errorf("got ErrCredFileTampered unexpectedly; want a remove error")
	}
}
