//go:build !windows

package pathsafe

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ignoringEINTR retries fn while it returns syscall.EINTR. Mirrors
// golang.org/go src/os/root_unix.go ignoringEINTR (Go 1.24+).
// ref: golang/go src/os/root_unix.go@master
func ignoringEINTR(fn func() error) error {
	for {
		err := fn()
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// openatNoFollowDir opens name relative to parentFd as a directory,
// fail-closed on symlinks via O_NOFOLLOW|O_DIRECTORY. Caller must Close the
// returned fd. Symlinks (anywhere in the chain) surface as ENOTDIR/ELOOP
// — this is the syscall-level TOCTOU defense that supersedes the previous
// path-based walkParentsForSymlinkContainment pre-check.
// ref: golang/go src/os/root_unix.go@master rootOpenDir
// ref: opencontainers/runc@63c2908 (O_NOFOLLOW|O_DIRECTORY symlink reject)
func openatNoFollowDir(parentFd int, name string) (int, error) {
	var fd int
	err := ignoringEINTR(func() error {
		var openErr error
		fd, openErr = unix.Openat(parentFd, name,
			unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		return openErr
	})
	if err != nil {
		return -1, err
	}
	return fd, nil
}

// mkdiratThenOpen creates name relative to parentFd then opens it as
// O_NOFOLLOW|O_DIRECTORY. EEXIST is tolerated: another goroutine/process may
// have created the dir concurrently — retry the open to verify it really is
// a directory (parity with golang/go Issue #75114 fix in os.Root.MkdirAll).
// ref: golang/go Issue #75114, CL 698215
func mkdiratThenOpen(parentFd int, name string, mode os.FileMode) (int, error) {
	err := ignoringEINTR(func() error {
		return unix.Mkdirat(parentFd, name, uint32(mode.Perm()))
	})
	if err != nil && !errors.Is(err, syscall.EEXIST) {
		return -1, err
	}
	return openatNoFollowDir(parentFd, name)
}

// writeFileNoFollowAt creates basename relative to parentFd with
// O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW and writes content. parentFd MUST have
// been opened with O_DIRECTORY|O_NOFOLLOW (caller-anchored). On error nothing
// is written. This closes the last TOCTOU window: prior to this commit the
// leaf open was path-based (caller could race a parent swap between mkdirat
// and openat); now both anchor on the same fd chain.
//
// fd ownership: the descriptor opened by openat is wrapped by os.NewFile
// and closed by f.Close before return. Callers MUST NOT close any fd
// returned by this function — none is returned.
func writeFileNoFollowAt(parentFd int, basename string, content []byte, mode os.FileMode) error {
	var fd int
	err := ignoringEINTR(func() error {
		var openErr error
		fd, openErr = unix.Openat(parentFd, basename,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			uint32(mode.Perm()))
		return openErr
	})
	if err != nil {
		return err
	}
	// os.NewFile takes ownership of fd → Close releases it.
	// fd is guaranteed non-negative here (unix.Openat returns -1 with non-nil
	// err, which we already returned above) so the int→uintptr conversion is
	// safe; G115 cannot prove this statically.
	f := os.NewFile(uintptr(fd), basename) //nolint:gosec // R2-approved: G115 false-positive — POSIX openat(2) returns -1 on error (non-nil err returned above) or ≥0 on success
	_, writeErr := f.Write(content)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// unlinkatLeafIgnoreENOENT removes basename relative to parentFd; ENOENT is
// silently absorbed (force-overwrite semantics: "remove the slot if anything
// is there"). AT_REMOVEDIR is NOT set, so a directory at the leaf fails with
// EISDIR — pathsafe never overwrites a directory with a file.
func unlinkatLeafIgnoreENOENT(parentFd int, basename string) error {
	err := ignoringEINTR(func() error {
		return unix.Unlinkat(parentFd, basename, 0)
	})
	if err != nil && errors.Is(err, syscall.ENOENT) {
		return nil
	}
	return err
}

// openRootDirNoFollow opens realRoot as the fd-walk anchor
// (O_DIRECTORY|O_NOFOLLOW). The errcode wrap preserves the underlying syscall
// errno so callers' errors.Is(err, fs.ErrPermission|fs.ErrNotExist) walks
// continue to work.
func openRootDirNoFollow(realRoot string) (int, error) {
	var fd int
	err := ignoringEINTR(func() error {
		var openErr error
		fd, openErr = unix.Open(realRoot,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		return openErr
	})
	if err != nil {
		return -1, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"pathsafe: open root for fd-walk", err,
			errcode.WithDetails(slog.String("root", realRoot)))
	}
	return fd, nil
}

// openOrCreateDir opens an existing directory component relative to parentFd
// via openatNoFollowDir, or — if ENOENT — creates it via mkdiratThenOpen.
// The returned wasCreated flag tells the caller whether to track this dir
// in the rollback list. Errors other than ENOENT (notably EACCES, ENOTDIR,
// ELOOP for symlinks) propagate unchanged.
func openOrCreateDir(parentFd int, comp string, dirMode os.FileMode) (childFd int, wasCreated bool, err error) {
	fd, err := openatNoFollowDir(parentFd, comp)
	if err == nil {
		return fd, false, nil
	}
	if !errors.Is(err, syscall.ENOENT) {
		return -1, false, err
	}
	fd, err = mkdiratThenOpen(parentFd, comp, dirMode)
	if err != nil {
		return -1, false, err
	}
	return fd, true, nil
}

// walkAndCreateDirs descends parent-path components of rel (relative to
// realRoot/rootFd), opening or creating each via openOrCreateDir. Every new
// dir is appended to *created (outermost first). Every opened fd along the
// way is appended to *fds; caller's defer closes them all.
//
// Returns the final parent dir fd. When rel == "" or "." the leaf parent IS
// rootFd itself — returned unchanged with no descent.
//
// fd ownership contract:
//   - rootFd is NOT appended to *fds; caller is responsible for its
//     lifecycle (typically via defer Close).
//   - Every successfully opened intermediate childFd IS appended to *fds.
//   - The returned parent fd is the last element of *fds when rel != "" /
//     ".", or rootFd itself when rel is empty/dot.
//   - Callers MUST defer-close every fd in *fds after this function
//     returns, including failure paths.
func walkAndCreateDirs(rootFd int, realRoot, rel string, dirMode os.FileMode, fds *[]int, created *[]string) (int, error) {
	if rel == "." || rel == "" {
		return rootFd, nil
	}
	parentFd := rootFd
	currentPath := realRoot
	for _, comp := range strings.Split(filepath.ToSlash(rel), "/") {
		if comp == "" || comp == "." {
			continue
		}
		childFd, wasCreated, err := openOrCreateDir(parentFd, comp, dirMode)
		if err != nil {
			return -1, err
		}
		currentPath = filepath.Join(currentPath, comp)
		if wasCreated {
			*created = append(*created, currentPath)
		}
		*fds = append(*fds, childFd)
		parentFd = childFd
	}
	return parentFd, nil
}

// secureMkdirAllAndWrite is the unix-platform write funnel:
//
//  1. Open realRoot as the anchor fd (O_DIRECTORY|O_NOFOLLOW).
//  2. Walk parent-path components of absPath relative to that fd via
//     openat(O_NOFOLLOW|O_DIRECTORY). Any symlink in the chain fails closed
//     (ENOTDIR/ELOOP); attackers cannot swap a parent into a symlink between
//     check and use because there IS no separate check — every component is
//     resolved relative to the previous fd.
//  3. mkdirat any missing intermediate directories (EEXIST tolerated for
//     concurrent creation per Go Issue #75114 pattern).
//  4. (forceOverwrite=true) unlinkat the leaf slot, ignoring ENOENT. The
//     unlink uses the parent fd so directory swaps mid-flight cannot
//     redirect the deletion.
//  5. openat(O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW) the leaf relative to the
//     final parent fd; write content; close.
//
// Newly-created directories are recorded in *created (outermost first) so
// reverse-order removal during rollback removes leaves before parents.
//
// ref: cyphar/filepath-securejoin pathrs-lite/mkdir.go@main (MkdirAllHandle
//
//	returns parent fd for write-time anchoring)
//
// ref: golang/go src/os/root_unix.go@master (rootOpenDir + EEXIST-retry)
// ref: opencontainers/runc@63c2908 (O_NOFOLLOW|O_DIRECTORY parent walk)
func secureMkdirAllAndWrite(
	realRoot, absPath string,
	content []byte,
	dirMode, fileMode os.FileMode,
	forceOverwrite bool,
	created *[]string,
) error {
	parentPath := filepath.Dir(absPath)
	basename := filepath.Base(absPath)

	rel, err := filepath.Rel(realRoot, parentPath)
	if err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pathsafe: cannot relativize parent path", err,
			errcode.WithInternal(fmt.Sprintf("realRoot=%s parent=%s", realRoot, parentPath)))
	}

	rootFd, err := openRootDirNoFollow(realRoot)
	if err != nil {
		return err
	}
	fds := []int{rootFd}
	defer func() {
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
	}()

	parentFd, err := walkAndCreateDirs(rootFd, realRoot, rel, dirMode, &fds, created)
	if err != nil {
		return err
	}

	if forceOverwrite {
		if err := unlinkatLeafIgnoreENOENT(parentFd, basename); err != nil {
			return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"pathsafe: unlinkat leaf for force-overwrite", err,
				errcode.WithInternal(fmt.Sprintf("basename=%s", basename)))
		}
	}
	return writeFileNoFollowAt(parentFd, basename, content, fileMode)
}
