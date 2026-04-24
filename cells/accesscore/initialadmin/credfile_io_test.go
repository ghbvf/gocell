//go:build unix || windows

package initialadmin

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFormatPayload_HasExpectedKeys verifies the canonical file format contains
// the required keys.
func TestFormatPayload_HasExpectedKeys(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	payload := credentialPayload{
		Username:  "admin",
		Password:  "s3cr3t",
		ExpiresAt: time.Unix(1713456000, 0),
	}

	if err := formatPayload(&sb, payload); err != nil {
		t.Fatalf("formatPayload: %v", err)
	}

	content := sb.String()
	for _, needle := range []string{"username=", "password=", "expires_at="} {
		if !strings.Contains(content, needle) {
			t.Errorf("content missing %q\ngot:\n%s", needle, content)
		}
	}
}

// TestReadCredentialExpiresAt_ParsesUnixTimestamp verifies that expires_at
// is correctly parsed from the credential file format.
func TestReadCredentialExpiresAt_ParsesUnixTimestamp(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	want := time.Unix(1713456000, 0).UTC()
	payload := credentialPayload{
		Username:  "admin",
		Password:  "pass",
		ExpiresAt: want,
	}
	if err := writeCredentialFile(path, payload); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	got, err := readCredentialExpiresAt(path)
	if err != nil {
		t.Fatalf("readCredentialExpiresAt: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestReadCredentialExpiresAt_MissingLine_ReturnsError verifies that a file
// without expires_at causes an error.
func TestReadCredentialExpiresAt_MissingLine_ReturnsError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	if err := os.WriteFile(path, []byte("username=admin\npassword=pass\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := readCredentialExpiresAt(path)
	if err == nil {
		t.Fatal("expected error for missing expires_at, got nil")
	}
	if !strings.Contains(err.Error(), "expires_at") {
		t.Errorf("error should mention 'expires_at', got: %v", err)
	}
}

// TestReadCredentialExpiresAt_BadFormat_ReturnsError verifies that a malformed
// expires_at value causes an error.
func TestReadCredentialExpiresAt_BadFormat_ReturnsError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	if err := os.WriteFile(path, []byte("expires_at=notanumber\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := readCredentialExpiresAt(path)
	if err == nil {
		t.Fatal("expected error for bad expires_at format, got nil")
	}
}

// TestWriteCredentialFile_WritesAndReadsBack verifies that writeCredentialFile
// produces a file that can be read back with the correct fields.
func TestWriteCredentialFile_WritesAndReadsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := credentialPayload{
		Username:  "admin",
		Password:  "mypassword",
		ExpiresAt: time.Unix(1713456000, 0),
	}

	if err := writeCredentialFile(path, payload); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	for _, needle := range []string{"username=admin", "password=mypassword", "expires_at=1713456000"} {
		if !strings.Contains(content, needle) {
			t.Errorf("content missing %q\ngot:\n%s", needle, content)
		}
	}
}

// TestWriteCredentialFile_RefusesOverwrite verifies that a second write to
// the same path returns errCredFileExists.
func TestWriteCredentialFile_RefusesOverwrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := credentialPayload{Username: "admin", Password: "pass", ExpiresAt: time.Now().Add(time.Hour)}

	if err := writeCredentialFile(path, payload); err != nil {
		t.Fatalf("first write: %v", err)
	}

	err := writeCredentialFile(path, payload)
	if err == nil {
		t.Fatal("expected errCredFileExists on second write, got nil")
	}
	if !errors.Is(err, errCredFileExists) {
		t.Errorf("expected errCredFileExists, got: %v", err)
	}
}

// TestWriteCredentialFile_AtomicTempCleanedOnError verifies that the .tmp
// file is removed when the writer returns an error.
func TestWriteCredentialFile_AtomicTempCleanedOnError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := credentialPayload{Username: "admin", Password: "pass", ExpiresAt: time.Now().Add(time.Hour)}

	err := writeCredentialFile(path, payload, withPayloadWriter(func(_ io.Writer, _ credentialPayload) error {
		return io.ErrClosedPipe
	}))
	if err == nil {
		t.Fatal("expected error from failing writer, got nil")
	}

	if _, statErr := os.Stat(path + ".tmp"); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf(".tmp residue found after write error")
	}
}

// TestRemoveCredentialFile_Idempotent verifies that removing a non-existent
// file twice does not return an error.
func TestRemoveCredentialFile_Idempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nonexistent_file")
	if err := removeCredentialFile(path); err != nil {
		t.Errorf("first remove (missing file): %v", err)
	}
	if err := removeCredentialFile(path); err != nil {
		t.Errorf("second remove (missing file): %v", err)
	}
}
