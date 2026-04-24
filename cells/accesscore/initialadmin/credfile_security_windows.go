//go:build windows

package initialadmin

import (
	"fmt"
	"os"
	"unsafe"

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
// The DACL is verified by parsing the SDDL into ACE records (parseDACLAces in
// credfile_security_sddl.go — pure Go), then resolving each ACE's SID string
// to a *windows.SID via ConvertStringSidToSid and comparing with EqualSid.
// This handles all SDDL aliases (LA, SY, BA, WD, ...) automatically because
// the OS does the resolution; we never enumerate aliases manually.
func (w *windowsCredfile) VerifyOwnership(path string, _ os.FileInfo) (tampered bool, err error) {
	sd, dacl, err := getFileDACL(path)
	if err != nil {
		return true, fmt.Errorf("get DACL: %w", err)
	}

	if dacl == nil {
		return true, fmt.Errorf("DACL is nil (world-readable)")
	}

	// B3: verify SE_DACL_PROTECTED — without this bit, inheritance from parent
	// directories could silently add ACEs and widen file access.
	control, _, ctlErr := sd.Control()
	if ctlErr != nil {
		return true, fmt.Errorf("get SD control bits: %w", ctlErr)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return true, fmt.Errorf("DACL is not PROTECTED (inheritance allowed)")
	}

	// Verify ACE count — exactly 3 expected.
	if dacl.AceCount != 3 {
		return true, fmt.Errorf("expected 3 ACEs, got %d", dacl.AceCount)
	}

	sddl := sd.String()
	aces, err := parseDACLAces(sddl)
	if err != nil {
		return true, fmt.Errorf("parse DACL SDDL: %w (raw: %s)", err, sddl)
	}

	sids, err := buildExpectedSIDs()
	if err != nil {
		return true, fmt.Errorf("build expected SIDs: %w", err)
	}
	defer freeSIDs(sids)

	expected := []*windows.SID{sids.user, sids.admins, sids.localSystem}
	for i, exp := range expected {
		if !daclHasAllowACEForSID(aces, exp) {
			return true, fmt.Errorf("expected SID[%d] (%s) has no ALLOW ACE in DACL SDDL: %s",
				i, exp.String(), sddl)
		}
	}

	return false, nil
}

// daclHasAllowACEForSID reports whether parsed ACEs include at least one
// ALLOW-type ACE whose SID equals expected. SDDL aliases in ace.sidStr are
// resolved via stringToSID (Windows SAM lookup) so the comparison is
// form-agnostic.
//
// Defence-in-depth note (V-A12): the rights field on each ACE is not
// validated here. An attacker who narrows the ALLOW mask while keeping the
// SID would pass this check, but that attack only reduces the attacker's own
// access to the credential file — credentials remain protected. Strict mask
// validation was removed because it caused false-positive tamper reports on
// valid ACLs (Windows emits FILE_ALL_ACCESS in multiple SDDL forms depending
// on OS version and NTFS generic-to-specific mapping).
func daclHasAllowACEForSID(aces []sddlAce, expected *windows.SID) bool {
	for _, ace := range aces {
		if !isAllowACEType(ace.aceType) {
			continue
		}
		parsed, err := stringToSID(ace.sidStr)
		if err != nil {
			continue
		}
		match := windows.EqualSid(parsed, expected)
		freeSID(parsed)
		if match {
			return true
		}
	}
	return false
}

// stringToSID converts an SDDL SID string to a *windows.SID. The string may be
// a full S-1-5-* form OR a well-known SDDL alias (LA, SY, BA, WD, CO, ...).
// Aliases are resolved via the running machine's SAM database — we do not
// enumerate them manually.
//
// MEMORY: the returned SID is allocated by the Windows API (LocalAlloc).
// Callers MUST release it via freeSID once done — both stringToSID and freeSID
// are paired.
func stringToSID(sidStr string) (*windows.SID, error) {
	ptr, err := windows.UTF16PtrFromString(sidStr)
	if err != nil {
		return nil, fmt.Errorf("UTF16PtrFromString(%q): %w", sidStr, err)
	}
	var sid *windows.SID
	if err := windows.ConvertStringSidToSid(ptr, &sid); err != nil {
		return nil, fmt.Errorf("ConvertStringSidToSid(%q): %w", sidStr, err)
	}
	return sid, nil
}

// freeSID releases a SID allocated by stringToSID (LocalAlloc'd inside
// ConvertStringSidToSid). The unsafe.Pointer cast is the documented OS
// contract for converting a *SID to the uintptr Handle that LocalFree
// requires; this is the only use of unsafe.Pointer in the package and is
// scoped to a single, well-defined memory-management API.
func freeSID(sid *windows.SID) {
	if sid == nil {
		return
	}
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(sid)))
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
