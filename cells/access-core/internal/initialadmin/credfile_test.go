//go:build unix

package initialadmin_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/initialadmin"
)

func makePayload(username, password string) initialadmin.CredentialPayload {
	return initialadmin.CredentialPayload{
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

	if err := initialadmin.WriteCredentialFile(path, makePayload("admin", "s3cr3t")); err != nil {
		t.Fatalf("WriteCredentialFile: %v", err)
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

	if err := initialadmin.WriteCredentialFile(path, makePayload("admin", "s3cr3t")); err != nil {
		t.Fatalf("WriteCredentialFile: %v", err)
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

	if err := initialadmin.WriteCredentialFile(path, makePayload("admin", "s3cr3t")); err != nil {
		t.Fatalf("WriteCredentialFile: %v", err)
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
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("found residual .tmp file: %s", e.Name())
		}
	}
}

func TestWriteCredentialFile_RefusesOverwrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")

	// First write succeeds.
	if err := initialadmin.WriteCredentialFile(path, makePayload("admin", "pass1")); err != nil {
		t.Fatalf("first WriteCredentialFile: %v", err)
	}

	// Second write must refuse.
	err := initialadmin.WriteCredentialFile(path, makePayload("admin", "pass2"))
	if err == nil {
		t.Fatal("expected ErrCredFileExists, got nil")
	}
	if !errors.Is(err, initialadmin.ErrCredFileExists) {
		t.Errorf("expected ErrCredFileExists, got: %v", err)
	}
}

func TestWriteCredentialFile_PayloadFormat(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := initialadmin.CredentialPayload{
		Username:  "admin",
		Password:  "mypassword",
		ExpiresAt: time.Unix(1713456000, 0),
	}

	if err := initialadmin.WriteCredentialFile(path, payload); err != nil {
		t.Fatalf("WriteCredentialFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	required := []string{
		"# GoCell initial admin credential",
		"username=admin",
		"password=mypassword",
		"expires_at=",
	}
	for _, needle := range required {
		if !strings.Contains(content, needle) {
			t.Errorf("file content missing %q\ngot:\n%s", needle, content)
		}
	}
}

func TestRemoveCredentialFile_IdempotentMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nonexistent_file")
	if err := initialadmin.RemoveCredentialFile(path); err != nil {
		t.Errorf("RemoveCredentialFile on missing file: expected nil, got %v", err)
	}
}

// TestRemoveCredentialFile_DeletesEvenWhenModeTampered verifies that
// RemoveCredentialFile removes the file even when the mode has been tampered
// (security intent: destroy the credential regardless of the anomaly) and
// that the returned error wraps ErrCredFileTampered so callers can log it.
func TestRemoveCredentialFile_DeletesEvenWhenModeTampered(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")

	if err := initialadmin.WriteCredentialFile(path, makePayload("admin", "s3cr3t")); err != nil {
		t.Fatalf("WriteCredentialFile: %v", err)
	}

	// Tamper: change mode to 0644.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	err := initialadmin.RemoveCredentialFile(path)

	// Must still return ErrCredFileTampered so caller can log the anomaly.
	if err == nil {
		t.Fatal("expected ErrCredFileTampered, got nil")
	}
	if !errors.Is(err, initialadmin.ErrCredFileTampered) {
		t.Errorf("expected ErrCredFileTampered, got: %v", err)
	}

	// File must have been removed despite the tamper.
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected file to be removed after tamper detection, but file still exists")
	}
}
