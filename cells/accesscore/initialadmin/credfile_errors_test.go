//go:build unix

package initialadmin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteCredentialFile_MkdirAllError exercises the MkdirAll failure branch
// by placing a regular file where the directory should be created.
func TestWriteCredentialFile_MkdirAllError(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	// Create a regular file at the path that writeCredentialFile will try to use as a directory.
	blocker := filepath.Join(base, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	// Path inside a file (not a directory) — MkdirAll must fail.
	path := filepath.Join(blocker, "subdir", "initial_admin_password")
	err := writeCredentialFile(path, makePayload("pass"))
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
	readonlyDirPerm := os.FileMode(0o500)
	if err := os.Chmod(dir, readonlyDirPerm); err != nil {
		t.Fatalf("setup: Chmod: %v", err)
	}
	restoreDirPerm := os.FileMode(0o700)
	t.Cleanup(func() { _ = os.Chmod(dir, restoreDirPerm) })

	path := filepath.Join(dir, "initial_admin_password")
	err := writeCredentialFile(path, makePayload("pass"))
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

	if err := writeCredentialFile(path, makePayload("pass")); err != nil {
		t.Fatalf("setup: writeCredentialFile: %v", err)
	}

	// Make parent directory read-only to prevent removal.
	readonlyDirPerm2 := os.FileMode(0o500)
	if err := os.Chmod(dir, readonlyDirPerm2); err != nil {
		t.Fatalf("setup: Chmod dir: %v", err)
	}
	restoreDirPerm2 := os.FileMode(0o700)
	t.Cleanup(func() { _ = os.Chmod(dir, restoreDirPerm2) })

	err := removeCredentialFile(path)
	if err == nil {
		t.Fatal("expected error from os.Remove on read-only dir, got nil")
	}
	// Must NOT be a tamper error — the mode is still 0600.
	if errors.Is(err, errCredFileTampered) {
		t.Errorf("got errCredFileTampered unexpectedly; want a remove error")
	}
}
