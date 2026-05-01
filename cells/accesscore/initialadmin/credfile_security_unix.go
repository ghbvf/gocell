//go:build unix

package initialadmin

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// unixCredfile implements platformCredfile for Unix systems.
// It uses POSIX file permissions (0o600 file, 0o700 directory) for access
// control and detects tampering by checking the file mode at removal time.
type unixCredfile struct{}

func init() { platformImpl = &unixCredfile{} }

// EnsureSecureDir creates dir (and any parents) with mode 0o700 so that only
// the owning user can list or enter the directory.
func (u *unixCredfile) EnsureSecureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// SecureNewFile creates path with O_EXCL|O_CREATE|O_WRONLY|O_TRUNC|O_NOFOLLOW
// and mode 0o600 so that only the owning user can read the credential file.
//
// O_NOFOLLOW: refuse to open if path is a symlink at creation time (defense
// against TOCTOU where an attacker plants a symlink between Lstat and OpenFile).
// syscall.O_NOFOLLOW is available on all Unix targets (linux, darwin, *bsd).
func (u *unixCredfile) SecureNewFile(path string) (*os.File, error) {
	return os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
}

// VerifyOwnership checks that the file mode is still 0o600.
// Returns tampered=true if the mode has been changed, along with a descriptive
// error so the caller can log the anomaly.
func (u *unixCredfile) VerifyOwnership(path string, info os.FileInfo) (tampered bool, err error) {
	if info.Mode().Perm() != 0o600 {
		return true, fmt.Errorf("got mode %o, want 0600", info.Mode().Perm())
	}
	return false, nil
}
