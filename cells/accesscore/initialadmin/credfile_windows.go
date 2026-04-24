//go:build windows

package initialadmin

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func removeCredentialFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("initialadmin: stat credential file: %w", err)
	}

	restricted, checkErr := credentialFileACLRestricted(path)
	tampered := info.IsDir() || checkErr != nil || !restricted

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("initialadmin: remove credential file: %w", err)
	}
	if tampered {
		if checkErr != nil {
			return fmt.Errorf("%w: windows ACL check failed: %v", errCredFileTampered, checkErr)
		}
		return fmt.Errorf("%w: windows credential ACL is not restricted", errCredFileTampered)
	}
	return nil
}

func ensureSecureCredentialDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("initialadmin: create directory %s: %w", dir, err)
	}
	if err := applyRestrictedACL(dir); err != nil {
		return fmt.Errorf("initialadmin: secure directory %s: %w", dir, err)
	}
	return nil
}

func secureCredentialTempFile(path string) error {
	return applyRestrictedACL(path)
}

func secureCredentialFinalFile(path string) error {
	return applyRestrictedACL(path)
}

func applyRestrictedACL(path string) error {
	acl, err := restrictedACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func restrictedACL() (*windows.ACL, error) {
	userSID, systemSID, adminSID, err := credentialAllowedSIDs()
	if err != nil {
		return nil, err
	}
	entries := []windows.EXPLICIT_ACCESS{
		allowSID(userSID, windows.TRUSTEE_IS_USER),
		allowSID(systemSID, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		allowSID(adminSID, windows.TRUSTEE_IS_GROUP),
	}
	return windows.ACLFromEntries(entries, nil)
}

func allowSID(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func credentialAllowedSIDs() (userSID, systemSID, adminSID *windows.SID, err error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open current process token: %w", err)
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get current user SID: %w", err)
	}
	systemSID, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create LocalSystem SID: %w", err)
	}
	adminSID, err = windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create Administrators SID: %w", err)
	}
	return user.User.Sid, systemSID, adminSID, nil
}

func credentialFileACLRestricted(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return false, err
	}
	control, _, err := sd.Control()
	if err != nil {
		return false, err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return false, nil
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, err
	}
	if dacl == nil {
		return false, nil
	}

	allowed, err := allowedSIDSet()
	if err != nil {
		return false, err
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sidInSet(aceSID, allowed) {
			return false, nil
		}
	}
	return true, nil
}

func allowedSIDSet() ([]*windows.SID, error) {
	userSID, systemSID, adminSID, err := credentialAllowedSIDs()
	if err != nil {
		return nil, err
	}
	return []*windows.SID{userSID, systemSID, adminSID}, nil
}

func sidInSet(sid *windows.SID, allowed []*windows.SID) bool {
	for _, candidate := range allowed {
		if sid.Equals(candidate) {
			return true
		}
	}
	return false
}
