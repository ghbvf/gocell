//go:build windows

package initialadmin

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWriteCredentialFile_WindowsRestrictedACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := credentialPayload{
		Username:  "admin",
		Password:  "secret",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	if err := writeCredentialFile(path, payload); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}
	restricted, err := credentialFileACLRestricted(path)
	if err != nil {
		t.Fatalf("credentialFileACLRestricted: %v", err)
	}
	if !restricted {
		t.Fatal("credential file ACL is not restricted")
	}
}

func TestRemoveCredentialFile_WindowsDeletesTamperedACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "initial_admin_password")
	payload := credentialPayload{
		Username:  "admin",
		Password:  "secret",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := writeCredentialFile(path, payload); err != nil {
		t.Fatalf("writeCredentialFile: %v", err)
	}

	worldSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid: %v", err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		allowSID(worldSID, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	}, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatalf("SetNamedSecurityInfo: %v", err)
	}

	err = removeCredentialFile(path)
	if !errors.Is(err, errCredFileTampered) {
		t.Fatalf("removeCredentialFile error = %v, want errCredFileTampered", err)
	}
}
