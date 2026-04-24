//go:build windows

package initialadmin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func WriteCredentialFile(path string, payload CredentialPayload, opts ...WriteCredentialFileOption) error {
	cfg := &writeCredentialFileConfig{writer: formatPayload}
	for _, o := range opts {
		o(cfg)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrCredFileExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("initialadmin: stat credential file %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := ensureSecureCredentialDir(dir); err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	_ = os.Remove(tmpPath)

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("initialadmin: create temp file %s: %w", tmpPath, err)
	}
	if err := applyRestrictedACL(tmpPath); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("initialadmin: secure temp file %s: %w", tmpPath, err)
	}

	writeErr := cfg.writer(f, payload)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		if writeErr != nil {
			return fmt.Errorf("initialadmin: write credential payload: %w", writeErr)
		}
		return fmt.Errorf("initialadmin: close temp file: %w", closeErr)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("initialadmin: rename %s -> %s: %w", tmpPath, path, err)
	}
	if err := applyRestrictedACL(path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("initialadmin: secure credential file %s: %w", path, err)
	}
	return nil
}

func RemoveCredentialFile(path string) error {
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
			return fmt.Errorf("%w: windows ACL check failed: %v", ErrCredFileTampered, checkErr)
		}
		return fmt.Errorf("%w: windows credential ACL is not restricted", ErrCredFileTampered)
	}
	return nil
}

func ReadCredentialExpiresAt(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("initialadmin: read credential file: %w", err)
	}
	for _, line := range splitLines(string(data)) {
		const prefix = "expires_at="
		if len(line) > len(prefix) && line[:len(prefix)] == prefix {
			var ts int64
			if _, scanErr := fmt.Sscanf(line[len(prefix):], "%d", &ts); scanErr != nil {
				return time.Time{}, fmt.Errorf("initialadmin: parse expires_at: %w", scanErr)
			}
			return time.Unix(ts, 0).UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("initialadmin: expires_at not found in credential file")
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
