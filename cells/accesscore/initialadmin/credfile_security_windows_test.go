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
// Regression guard for CI failure where Windows emitted SDDL alias "LA" for
// the Administrator account SID and our SID-form whitelist rejected it.
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
	if err := windows.SetNamedSecurityInfo(
		path,
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
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
		nil, nil, nil, nil,
	); err != nil {
		t.Fatalf("SetNamedSecurityInfo (strip ACL): %v", err)
	}

	err := removeCredentialFile(path)
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

// TestDaclHasAllowACEForSID_ResolvesAliases is the Windows-runtime regression
// guard for the alias-resolution path. It uses the actual Windows SAM database
// (via stringToSID → ConvertStringSidToSid) so that aliases like SY/BA are
// canonicalized to the same SIDs we build via AllocateAndInitializeSid.
func TestDaclHasAllowACEForSID_ResolvesAliases(t *testing.T) {
	t.Parallel()

	sids, err := buildExpectedSIDs()
	if err != nil {
		t.Fatalf("buildExpectedSIDs: %v", err)
	}
	defer freeSIDs(sids)

	tests := []struct {
		name     string
		sddl     string
		expected *windows.SID
		want     bool
	}{
		{"SY alias resolves to LocalSystem", "D:P(A;;FA;;;SY)", sids.localSystem, true},
		{"BA alias resolves to Administrators", "D:P(A;;FA;;;BA)", sids.admins, true},
		{"full S-1-5-18 matches LocalSystem", "D:P(A;;FA;;;S-1-5-18)", sids.localSystem, true},
		{"full S-1-5-32-544 matches Administrators", "D:P(A;;FA;;;S-1-5-32-544)", sids.admins, true},
		{"DENY same SID is rejected (B1)", "D:P(D;;FA;;;SY)", sids.localSystem, false},
		{"unknown SID returns false", "D:P(A;;FA;;;S-1-5-99-1)", sids.localSystem, false},
		{"different SID returns false", "D:P(A;;FA;;;BA)", sids.localSystem, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			aces, err := parseDACLAces(tc.sddl)
			if err != nil {
				t.Fatalf("parseDACLAces: %v", err)
			}
			if got := daclHasAllowACEForSID(aces, tc.expected); got != tc.want {
				t.Errorf("daclHasAllowACEForSID(%q, %s) = %v, want %v",
					tc.sddl, tc.expected.String(), got, tc.want)
			}
		})
	}
}
