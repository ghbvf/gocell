//go:build windows

package pathsafe

import "os"

// writeFileNoFollow opens path with O_WRONLY|O_CREATE|O_EXCL and writes
// content. On Windows, O_NOFOLLOW is not available; O_EXCL provides a
// best-effort race guard (the kernel rejects creation when path already
// exists). Leaf-symlink protection on Windows depends on OS-level symlink
// privilege restrictions (non-elevated processes cannot follow symlinks
// without SeCreateSymbolicLinkPrivilege) rather than O_NOFOLLOW.
func writeFileNoFollow(path string, content []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
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
