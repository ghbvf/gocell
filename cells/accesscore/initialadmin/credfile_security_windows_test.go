//go:build windows

package initialadmin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestSecureNewFile_AppliesRestrictedACL verifies that a file written via
// writeCredentialFile has a DACL that passes VerifyOwnership (tampered=false).
func TestSecureNewFile_AppliesRestrictedACL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := credentialPayload{
		Username:  "admin",
		Password:  "s3cr3t",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := writeCredentialFile(path, payload); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	w := &windowsCredfile{}
	tampered, verifyErr := w.VerifyOwnership(path, info)
	if verifyErr != nil {
		t.Fatalf("VerifyOwnership error: %v", verifyErr)
	}
	if tampered {
		t.Error("VerifyOwnership: expected tampered=false for a freshly written file")
	}
}

// TestVerifyOwnership_DetectsOpenedDACL verifies that when the DACL is stripped
// (replaced with a NULL DACL), VerifyOwnership detects tampering.
func TestVerifyOwnership_DetectsOpenedDACL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := credentialPayload{
		Username:  "admin",
		Password:  "s3cr3t",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := writeCredentialFile(path, payload); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	// Strip ACL — set a NULL DACL (grants everyone access).
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		pathPtr,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
		nil, nil, nil, nil,
	); err != nil {
		t.Fatalf("SetNamedSecurityInfo (strip ACL): %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	w := &windowsCredfile{}
	tampered, _ := w.VerifyOwnership(path, info)
	if !tampered {
		t.Error("VerifyOwnership: expected tampered=true after stripping DACL")
	}
}

// TestRemoveCredentialFile_DeletesEvenIfTampered verifies that removeCredentialFile
// removes the file even when the DACL has been tampered, and returns a wrapped
// errCredFileTampered error.
func TestRemoveCredentialFile_DeletesEvenIfTampered(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := credentialPayload{
		Username:  "admin",
		Password:  "s3cr3t",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := writeCredentialFile(path, payload); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	// Strip the DACL to simulate tampering.
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		pathPtr,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
		nil, nil, nil, nil,
	); err != nil {
		t.Fatalf("SetNamedSecurityInfo (strip ACL): %v", err)
	}

	err = removeCredentialFile(path)
	if err == nil {
		t.Fatal("expected errCredFileTampered, got nil")
	}
	if !errors.Is(err, errCredFileTampered) {
		t.Errorf("expected errCredFileTampered, got: %v", err)
	}

	// File must be gone.
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("expected file to be removed after tamper detection")
	}
}
