//go:build unix

package initialadmin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makePayload(username, password string) credentialPayload {
	return credentialPayload{
		Username:  username,
		Password:  password,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

func TestWriteCredentialFile_DirMode0700(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	// Use a sub-directory that does not exist yet.
	dir := filepath.Join(base, "subdir", "nested")
	path := filepath.Join(dir, "initial_admin_password")

	if err := writeCredentialFile(path, makePayload("admin", "s3cr3t")); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 0700", perm)
	}
}

func TestWriteCredentialFile_FileMode0600(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")

	if err := writeCredentialFile(path, makePayload("admin", "s3cr3t")); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}
}

func TestWriteCredentialFile_AtomicRename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")

	if err := writeCredentialFile(path, makePayload("admin", "s3cr3t")); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	// Target file must exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("target file missing after write: %v", err)
	}

	// No .tmp residue.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".tmp" {
			t.Errorf("found residual .tmp file: %s", e.Name())
		}
	}
}

func TestRemoveCredentialFile_IdempotentMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nonexistent_file")
	if err := removeCredentialFile(path); err != nil {
		t.Errorf("removeCredentialFile on missing file: expected nil, got %v", err)
	}
}

// TestSecureNewFile_RefusesSymlinkPath verifies that SecureNewFile (and by
// extension writeCredentialFile) refuses to write through a symlink at the
// target path. This guards against TOCTOU symlink-swap attacks (B2):
// if an attacker plants a symlink between Lstat and OpenFile, O_NOFOLLOW
// causes the open to fail with ELOOP (on Linux) or ENOTDIR (on Darwin).
func TestSecureNewFile_RefusesSymlinkPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a benign target file that a symlink would point to.
	target := filepath.Join(dir, "innocent_target")
	if err := os.WriteFile(target, []byte("unrelated"), 0o600); err != nil {
		t.Fatalf("create target: %v", err)
	}

	// Plant a symlink at the path where the credential file would be written.
	symlinkPath := filepath.Join(dir, "initial_admin_password")
	if err := os.Symlink(target, symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// writeCredentialFile must refuse to write through the symlink.
	err := writeCredentialFile(symlinkPath, makePayload("admin", "s3cr3t"))
	if err == nil {
		t.Fatal("expected error when writing through symlink, got nil")
	}
	// The error must originate from either the Lstat (errCredFileExists, because
	// Lstat sees the symlink as an existing entry) or from O_NOFOLLOW rejection.
	// In both cases an error must be returned and the benign target must be intact.
	targetData, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read target after refused write: %v", readErr)
	}
	if string(targetData) != "unrelated" {
		t.Errorf("symlink target was modified; got %q, want %q", string(targetData), "unrelated")
	}
}

// TestRemoveCredentialFile_DeletesEvenWhenModeTampered verifies that
// removeCredentialFile removes the file even when the mode has been tampered
// (security intent: destroy the credential regardless of the anomaly) and
// that the returned error wraps errCredFileTampered so callers can log it.
func TestRemoveCredentialFile_DeletesEvenWhenModeTampered(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")

	if err := writeCredentialFile(path, makePayload("admin", "s3cr3t")); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	// Tamper: change mode to 0644.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	err := removeCredentialFile(path)

	// Must still return errCredFileTampered so caller can log the anomaly.
	if err == nil {
		t.Fatal("expected errCredFileTampered, got nil")
	}
	if !errors.Is(err, errCredFileTampered) {
		t.Errorf("expected errCredFileTampered, got: %v", err)
	}

	// File must have been removed despite the tamper.
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected file to be removed after tamper detection, but file still exists")
	}
}
