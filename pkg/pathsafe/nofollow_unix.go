//go:build !windows

package pathsafe

import (
	"os"
	"syscall"
)

// writeFileNoFollow opens path with O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW and
// writes content. O_EXCL prevents creation when the file already exists (race
// guard); O_NOFOLLOW causes the open to fail when path itself is a symlink,
// preventing follow-through to a redirected location.
//
// This is the leaf-write safety contract: even if an attacker races to place a
// symlink between conflictPass and writePass, O_NOFOLLOW blocks the follow.
func writeFileNoFollow(path string, content []byte, mode os.FileMode) error {
	// path is pre-validated by ContainPath (root containment + parent symlink walk)
	// before reaching this function; G304 variable-path is the entire point of
	// pathsafe (single funnel) and is documented in the archtest contract.
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL | syscall.O_NOFOLLOW
	f, err := os.OpenFile(path, flags, mode) //nolint:gosec // G304: pathsafe funnel
	if err != nil {
		return err
	}
	_, writeErr := f.Write(content)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
