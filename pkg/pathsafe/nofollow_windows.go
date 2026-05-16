//go:build windows

package pathsafe

import (
	"os"
	"path/filepath"
)

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

// secureMkdirAllAndWrite is the windows-platform write funnel.
//
// Platform contract (降级声明 vs unix):
//   - O_NOFOLLOW is not available on Windows; O_EXCL provides best-effort
//     leaf race guard but cannot prevent following parent-dir symlinks.
//   - Handle-based parent walk (the unix openat/mkdirat fd-anchored chain)
//     is NOT implemented on Windows: Go stdlib lacks an openat-equivalent
//     primitive (NtCreateFile with RelativeTo handle requires CGO or
//     golang.org/x/sys/windows direct calls, which would expand the
//     depguard pkg-isolation allowlist).
//   - Protection against parent-dir symlink swap relies on Windows OS-level
//     symlink privilege restriction: non-elevated user processes lack
//     SeCreateSymbolicLinkPrivilege and cannot create symlinks to begin
//     with. The path-based os.MkdirAll + collectMissingDirs(EACCES-aware)
//     accepts this residual TOCTOU exposure.
//   - GoCell CI defines Windows as an advisory smoke platform
//     (.github/workflows/_build-lint.yml os-smoke continue-on-error:
//     true); scaffold/codegen on Windows is not part of the hard-gate
//     security contract.
//
// Callers MUST NOT rely on Windows pathsafe for untrusted-input writes.
func secureMkdirAllAndWrite(
	realRoot, absPath string,
	content []byte,
	dirMode, fileMode os.FileMode,
	forceOverwrite bool,
	created *[]string,
) error {
	dir := filepath.Dir(absPath)
	toCreate, err := collectMissingDirs(dir, realRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	// Record outermost-first creation order so rollback (reverse) removes
	// leaves before parents.
	for i := len(toCreate) - 1; i >= 0; i-- {
		*created = append(*created, toCreate[i])
	}
	if forceOverwrite {
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return writeFileNoFollow(absPath, content, fileMode)
}
