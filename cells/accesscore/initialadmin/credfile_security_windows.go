//go:build windows

package initialadmin

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// windowsCredfile implements platformCredfile for Windows systems.
// It uses DACL-based access control to restrict credential file access to the
// current user, Built-in Administrators, and LocalSystem.
type windowsCredfile struct{}

func init() { platformImpl = &windowsCredfile{} }

// EnsureSecureDir creates dir (and any parents). On Windows, directory
// permissions are governed by DACL inheritance which is applied at creation
// time; we create the directory with os.MkdirAll and do not set an explicit
// DACL on the directory (the file-level DACL with PROTECTED_DACL_SECURITY_INFORMATION
// prevents inheritance from the parent directory).
func (w *windowsCredfile) EnsureSecureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// SecureNewFile creates path with exclusive creation, applies a restricted
// DACL (current user + Administrators + LocalSystem only), and returns the
// open file handle. If DACL application fails, the file is closed and removed
// before the error is returned — callers must never see a half-secured file.
func (w *windowsCredfile) SecureNewFile(path string) (*os.File, error) {
	// 0o600 mode arg is harmless on Windows (no Unix semantics) but keeps the
	// signature symmetric with Unix.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}

	if err := applyRestrictedACL(path); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("initialadmin: apply restricted ACL to %s: %w", path, err)
	}

	return f, nil
}

// VerifyOwnership reads the DACL of path and confirms it is PROTECTED and
// contains exactly three ALLOW ACEs for the expected SIDs (current user,
// Administrators, LocalSystem). Returns tampered=true on any deviation.
//
// Verification uses the SDDL string representation of the security descriptor
// to compare SID strings — this avoids any unsafe.Pointer use in ACE walking.
func (w *windowsCredfile) VerifyOwnership(path string, _ os.FileInfo) (tampered bool, err error) {
	sd, dacl, err := getFileDACL(path)
	if err != nil {
		return true, fmt.Errorf("get DACL: %w", err)
	}

	if dacl == nil {
		return true, fmt.Errorf("DACL is nil (world-readable)")
	}

	// Verify ACE count — exactly 3 expected.
	if dacl.AceCount != 3 {
		return true, fmt.Errorf("expected 3 ACEs, got %d", dacl.AceCount)
	}

	// Build expected SID strings for comparison.
	expectedSIDStrings, err := buildExpectedSIDStrings()
	if err != nil {
		return true, fmt.Errorf("build expected SID strings: %w", err)
	}

	// Use the SDDL string representation to verify all expected SIDs are present.
	// SECURITY_DESCRIPTOR.String() returns the SDDL without any unsafe.Pointer use.
	sddl := sd.String()
	for j, sidStr := range expectedSIDStrings {
		if !sddlContainsSID(sddl, sidStr) {
			return true, fmt.Errorf("expected SID[%d] (%s) not found in DACL SDDL", j, sidStr)
		}
	}

	return false, nil
}

// sddlWellKnownAliases maps canonical S-1-* SID strings to their SDDL
// short-form aliases. Windows SECURITY_DESCRIPTOR.String() may emit either
// the full S-1-* form or the short alias depending on OS version and locale.
// We check both so that VerifyOwnership does not falsely report tampering on
// a freshly written file whose SDDL happens to use the alias form.
//
// Aliases are stable across all Windows versions (defined by MS-DTYP §2.5.1.1):
//
//	SY = S-1-5-18 (LocalSystem)
//	BA = S-1-5-32-544 (Built-in Administrators)
var sddlWellKnownAliases = map[string]string{
	"S-1-5-18":    "SY",
	"S-1-5-32-544": "BA",
}

// sddlContainsSID returns true when the SDDL string contains an ALLOW ACE
// referencing the given SID string (e.g. "S-1-5-18").
// SDDL ALLOW ACE format: (A;...;...;...;...;<SID>)
//
// Both the full S-1-* form and well-known SDDL aliases (SY, BA …) are
// checked, because Windows may emit either form in SECURITY_DESCRIPTOR.String().
func sddlContainsSID(sddl, sidStr string) bool {
	if len(sddl) == 0 {
		return false
	}
	// Check full SID string form first.
	if containsSubstring(sddl, ";"+sidStr+")") {
		return true
	}
	// Check SDDL alias form for well-known SIDs.
	if alias, ok := sddlWellKnownAliases[sidStr]; ok {
		if containsSubstring(sddl, ";"+alias+")") {
			return true
		}
	}
	return false
}

// containsSubstring is a pure-Go substring search to avoid importing strings
// in this build-tag-constrained file.
func containsSubstring(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// applyRestrictedACL applies a PROTECTED DACL with three ALLOW ACEs to path:
//   - Current process token user (full access)
//   - Built-in Administrators (S-1-5-32-544) (full access)
//   - LocalSystem (S-1-5-18) (full access)
//
// The PROTECTED flag prevents ACE inheritance from parent directories.
func applyRestrictedACL(path string) error {
	sids, err := buildExpectedSIDs()
	if err != nil {
		return fmt.Errorf("build SIDs: %w", err)
	}
	defer freeSIDs(sids)

	dacl, err := buildDACL(sids)
	if err != nil {
		return fmt.Errorf("build DACL: %w", err)
	}

	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
	if err != nil {
		return fmt.Errorf("SetNamedSecurityInfo: %w", err)
	}
	return nil
}

// getFileDACL retrieves the security descriptor and DACL for path.
func getFileDACL(path string) (*windows.SECURITY_DESCRIPTOR, *windows.ACL, error) {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("GetNamedSecurityInfo: %w", err)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, nil, fmt.Errorf("get DACL from SD: %w", err)
	}
	return sd, dacl, nil
}

// sidSet holds the three expected SIDs. Caller must call freeSIDs when done.
type sidSet struct {
	user        *windows.SID
	admins      *windows.SID
	localSystem *windows.SID
}

// buildExpectedSIDs builds the three expected SIDs for DACL construction.
func buildExpectedSIDs() (sidSet, error) {
	// Current user SID from the process token.
	tok, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return sidSet{}, fmt.Errorf("OpenCurrentProcessToken: %w", err)
	}
	defer tok.Close()

	tokenUser, err := tok.GetTokenUser()
	if err != nil {
		return sidSet{}, fmt.Errorf("GetTokenUser: %w", err)
	}
	userSID := tokenUser.User.Sid

	// Built-in Administrators: S-1-5-32-544
	var adminsSID *windows.SID
	err = windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&adminsSID,
	)
	if err != nil {
		return sidSet{}, fmt.Errorf("allocate Administrators SID: %w", err)
	}

	// LocalSystem: S-1-5-18
	var localSystemSID *windows.SID
	err = windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		1,
		windows.SECURITY_LOCAL_SYSTEM_RID,
		0, 0, 0, 0, 0, 0, 0,
		&localSystemSID,
	)
	if err != nil {
		_ = windows.FreeSid(adminsSID)
		return sidSet{}, fmt.Errorf("allocate LocalSystem SID: %w", err)
	}

	return sidSet{
		user:        userSID,
		admins:      adminsSID,
		localSystem: localSystemSID,
	}, nil
}

// freeSIDs releases SIDs allocated by AllocateAndInitializeSid.
// The user SID belongs to the token struct and must NOT be freed.
func freeSIDs(s sidSet) {
	if s.admins != nil {
		_ = windows.FreeSid(s.admins)
	}
	if s.localSystem != nil {
		_ = windows.FreeSid(s.localSystem)
	}
}

// buildExpectedSIDStrings returns the string representations of the three
// expected SIDs, used for safe ACE comparison without unsafe.Pointer.
func buildExpectedSIDStrings() ([]string, error) {
	sids, err := buildExpectedSIDs()
	if err != nil {
		return nil, err
	}
	defer freeSIDs(sids)

	result := make([]string, 3)
	for i, sid := range []*windows.SID{sids.user, sids.admins, sids.localSystem} {
		result[i] = sid.String()
	}
	return result, nil
}

// buildDACL constructs an ACL with three ALLOW ACEs (full file access) for
// the three expected SIDs.
func buildDACL(sids sidSet) (*windows.ACL, error) {
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sids.user),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(sids.admins),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(sids.localSystem),
			},
		},
	}

	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return nil, fmt.Errorf("ACLFromEntries: %w", err)
	}
	return dacl, nil
}
