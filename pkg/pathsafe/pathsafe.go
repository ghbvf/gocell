// Package pathsafe is the single funnel for scaffold/codegen file writes.
// Containment + conflict detection + atomic write live here so the rest of
// the tree cannot bypass them by accident.
//
// # Design
//
// All scaffold/codegen filesystem writes funnel through WritePlannedFiles.
// The caller builds a []PlannedFile (render phase), then calls WritePlannedFiles
// once (execute phase). This gives:
//   - root containment via ResolveRoot + ContainPath before any write
//   - all-or-nothing conflict detection (full plan checked before first write)
//   - atomic write with best-effort rollback on failure (no half-written state)
//
// # AI-Hard contract
//
// WritePlannedFiles is the only function in scaffold paths allowed to call
// os.MkdirAll / os.WriteFile. Direct calls in scaffold packages are statically
// forbidden by archtest SCAFFOLD-WRITE-FUNNEL-01.
//
// # Extension contract
//
// When adding a new scaffold sub-package that writes files, add it to the
// archtest SCAFFOLD-WRITE-FUNNEL-01 scope list in
// tools/archtest/scaffold_write_funnel_test.go.
package pathsafe

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// WriteFile is the single-file shorthand for WritePlannedFiles. Creates
// parent directories if missing, runs containment + leaf-symlink rejection +
// O_NOFOLLOW write. Used by codegen.Write so derived-artifact writes share
// the same safety contract as scaffold writes.
//
// Pre: realRoot is the output of ResolveRoot.
func WriteFile(realRoot, absPath string, content []byte, mode os.FileMode) error {
	return WritePlannedFiles(realRoot, []PlannedFile{{
		AbsPath:  absPath,
		Content:  content,
		FileMode: mode,
	}}, false)
}

// WriteFileForce writes content to absPath, overwriting any existing file.
// This is the codegen variant: generated files may already exist on disk and
// need to be replaced. The caller must have already verified the existing
// content is a generated file (governance.IsGoCellGenerated) before calling.
//
// Safety contract:
//  1. Parent directory is created via os.MkdirAll.
//  2. Existing file (if any) is removed before the atomic write so that
//     writeFileNoFollow (O_EXCL|O_NOFOLLOW) can reopen the slot safely.
//  3. O_NOFOLLOW prevents a race-window symlink placed between Remove and
//     the subsequent open from redirecting the write outside the tree.
//
// realRoot is used for path containment when non-empty (output of ResolveRoot).
// When empty, containment is skipped (caller is responsible for path safety).
func WriteFileForce(realRoot, absPath string, content []byte, mode os.FileMode) error {
	if realRoot != "" {
		// Derive a relative path and run containment check.
		targetRel, err := filepath.Rel(realRoot, absPath)
		if err != nil {
			return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"pathsafe: cannot relativize path", err,
				errcode.WithDetails(slog.String("path", absPath)))
		}
		if _, err := ContainPath(realRoot, targetRel); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(absPath), defaultDirMode); err != nil {
		return fmt.Errorf("pathsafe: create dir for %s: %w", absPath, err)
	}
	// Remove any existing file so writeFileNoFollow (O_EXCL) can create a fresh
	// inode. Ignore "not exist" error — if the file disappeared concurrently the
	// write below will still succeed.
	_ = os.Remove(absPath)
	return writeFileNoFollow(absPath, content, mode)
}

const (
	defaultDirMode  = os.FileMode(0o755)
	defaultFileMode = os.FileMode(0o644)
)

// PlannedFile pairs an absolute output path with its rendered content.
// Mode encodes file vs Go-source (Go-source files MAY round-trip through
// codegen.FormatGoSource at the caller before constructing PlannedFile —
// pathsafe is content-neutral by design).
type PlannedFile struct {
	AbsPath  string
	Content  []byte
	DirMode  os.FileMode // optional; defaults 0o755 (per helm/helm chartutil; F12)
	FileMode os.FileMode // optional; defaults 0o644 (per helm/helm chartutil; F12)
	// ForceOverwrite, when true, instructs WritePlannedFiles to bypass the
	// conflictPass "file already exists" rejection for this entry and instead
	// replace the existing inode (Remove → O_EXCL|O_NOFOLLOW recreate). Used by
	// codegen-regenerate flows where derived artifacts intentionally overwrite
	// previous output. O_NOFOLLOW is preserved: a leaf symlink at the target
	// path is still removed (not followed) before the fresh file is written.
	// Zero value (false) is the default and matches pre-A-API behavior.
	ForceOverwrite bool
}

// ResolveRoot returns root resolved through filepath.EvalSymlinks so that
// subsequent ContainPath comparisons are stable even when root itself is a
// symlink. Returns an error if root does not exist or cannot be evaluated.
func ResolveRoot(root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errcode.Wrap(errcode.KindNotFound, errcode.ErrValidationFailed,
			"pathsafe: resolve root", err,
			errcode.WithInternal("root="+root))
	}
	return resolved, nil
}

// ContainPath returns the cleaned absolute path of targetRel under realRoot
// after asserting no existing parent symlink resolves outside realRoot.
// Pre: realRoot is the output of ResolveRoot.
//
// Returns an error if:
//   - targetRel is absolute
//   - targetRel contains ".." segments that escape realRoot
//   - any existing parent directory in the resolved path lies outside realRoot
func ContainPath(realRoot, targetRel string) (string, error) {
	if filepath.IsAbs(targetRel) {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pathsafe: target must be relative",
			errcode.WithDetails(slog.String("target", targetRel)))
	}

	sep := string(filepath.Separator)
	cleanTarget := filepath.Clean(filepath.Join(realRoot, targetRel))

	// Ensure cleanTarget is strictly inside realRoot (not equal, and not escaping).
	if !strings.HasPrefix(cleanTarget, realRoot+sep) && cleanTarget != realRoot {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pathsafe: target escapes root",
			errcode.WithDetails(slog.String("target", targetRel)))
	}

	// Walk existing parent components from filepath.Dir(cleanTarget) up to realRoot.
	// For each that exists, check it is not a symlink pointing outside realRoot.
	if err := walkParentsForSymlinkContainment(realRoot, cleanTarget, targetRel); err != nil {
		return "", err
	}

	return cleanTarget, nil
}

// walkParentsForSymlinkContainment walks the existing parent directories of
// cleanTarget (up to realRoot) and verifies that no symlink among them resolves
// outside realRoot. targetRel is used only for error context.
func walkParentsForSymlinkContainment(realRoot, cleanTarget, targetRel string) error {
	sep := string(filepath.Separator)
	parent := filepath.Dir(cleanTarget)
	for parent != realRoot && parent != "/" && parent != "." && parent != sep {
		info, statErr := os.Lstat(parent)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				parent = filepath.Dir(parent)
				continue
			}
			return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"pathsafe: stat parent", statErr,
				errcode.WithInternal(fmt.Sprintf("parent=%s", parent)))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := checkSymlinkContained(parent, realRoot, targetRel, sep); err != nil {
				return err
			}
		}
		parent = filepath.Dir(parent)
	}
	return nil
}

// checkSymlinkContained resolves a symlink at symlinkPath and returns an error
// if it points outside realRoot. targetRel is used only in error context.
func checkSymlinkContained(symlinkPath, realRoot, targetRel, sep string) error {
	resolved, resolveErr := filepath.EvalSymlinks(symlinkPath)
	if resolveErr != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"pathsafe: resolve symlink", resolveErr,
			errcode.WithInternal(fmt.Sprintf("parent=%s", symlinkPath)))
	}
	if !strings.HasPrefix(resolved, realRoot+sep) && resolved != realRoot {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pathsafe: parent symlink escapes root",
			errcode.WithDetails(slog.String("target", targetRel)))
	}
	return nil
}

// PlannedPaths returns the absolute target paths in plan order. Callers use
// this to surface dry-run output without parsing PlannedFile structs.
func PlannedPaths(plan []PlannedFile) []string {
	if len(plan) == 0 {
		return []string{}
	}
	paths := make([]string, len(plan))
	for i, f := range plan {
		paths[i] = f.AbsPath
	}
	return paths
}

// WritePlannedFiles is the SINGLE filesystem write entry for scaffold/codegen.
//
//  1. Each plan[i].AbsPath is verified via ContainPath against realRoot.
//  2. Each AbsPath must not already exist (conflict detection over the
//     FULL plan — no partial-write semantics).
//  3. dryRun returns nil after steps 1-2 succeed (validation only, no write).
//  4. Otherwise: mkdir all required directories, write all files, then
//     on the FIRST failure remove every file and directory created during
//     this call (best-effort rollback). Returns the original error wrapped
//     with the rollback outcome.
//
// Pre: realRoot is the output of ResolveRoot. plan may be empty (no-op).
//
// AI-Hard contract: this is the only function in the project allowed to call
// os.MkdirAll / os.WriteFile in scaffold paths. All other call sites are
// statically rejected by archtest SCAFFOLD-WRITE-FUNNEL-01.
func WritePlannedFiles(realRoot string, plan []PlannedFile, dryRun bool) error {
	if len(plan) == 0 {
		return nil
	}
	if err := containmentPass(realRoot, plan); err != nil {
		return err
	}
	if err := conflictPass(plan); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	return writePass(realRoot, plan)
}

// containmentPass verifies that every AbsPath in plan is contained within
// realRoot (no path traversal, no escaping symlinks).
func containmentPass(realRoot string, plan []PlannedFile) error {
	sep := string(filepath.Separator)
	for _, f := range plan {
		targetRel, err := filepath.Rel(realRoot, f.AbsPath)
		if err != nil {
			return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"pathsafe: cannot relativize path", err,
				errcode.WithDetails(slog.String("path", f.AbsPath)))
		}
		cleanTarget := filepath.Clean(filepath.Join(realRoot, targetRel))
		if !strings.HasPrefix(cleanTarget, realRoot+sep) && cleanTarget != realRoot {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"pathsafe: target escapes root",
				errcode.WithDetails(slog.String("path", f.AbsPath)))
		}
		if _, err := ContainPath(realRoot, targetRel); err != nil {
			return err
		}
	}
	return nil
}

// conflictPass checks that none of the planned output paths already exist.
// The full plan is checked before any write (no partial-write semantics).
// Uses os.Lstat (not os.Stat) so that leaf symlinks — both dangling and
// non-dangling — are detected and rejected even when the symlink destination
// does not exist. This prevents an attacker from pre-placing a symlink at the
// target path to redirect writes outside root.
func conflictPass(plan []PlannedFile) error {
	for _, f := range plan {
		info, err := os.Lstat(f.AbsPath)
		if err != nil {
			// Path does not exist → no conflict. Continue.
			continue
		}
		// Path exists as a regular file, directory, or symlink → conflict.
		if info.Mode()&os.ModeSymlink != 0 {
			return errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"pathsafe: target is a symlink (rejected)",
				errcode.WithDetails(slog.String("path", f.AbsPath)))
		}
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"pathsafe: file already exists",
			errcode.WithDetails(slog.String("path", f.AbsPath)))
	}
	return nil
}

// writePass creates directories and writes all files, rolling back on the
// first failure. Returns the original error wrapped with rollback outcome.
func writePass(realRoot string, plan []PlannedFile) error {
	var writtenPaths []string
	var createdDirs []string

	for _, f := range plan {
		dirMode := f.DirMode
		if dirMode == 0 {
			dirMode = defaultDirMode
		}
		fileMode := f.FileMode
		if fileMode == 0 {
			fileMode = defaultFileMode
		}

		dir := filepath.Dir(f.AbsPath)
		if err := mkdirAllTracked(dir, dirMode, realRoot, &createdDirs); err != nil {
			return rollbackWrites(writtenPaths, createdDirs, err)
		}
		if err := writeFileNoFollow(f.AbsPath, f.Content, fileMode); err != nil {
			return rollbackWrites(writtenPaths, createdDirs, err)
		}
		writtenPaths = append(writtenPaths, f.AbsPath)
	}
	return nil
}

// rollbackWrites removes all written files and created directories (in reverse
// creation order) and wraps originalErr with rollback statistics.
func rollbackWrites(written, dirs []string, originalErr error) error {
	for i := len(written) - 1; i >= 0; i-- {
		_ = os.Remove(written[i])
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
		"pathsafe: write failed; rollback removed files and dirs", originalErr,
		errcode.WithInternal(fmt.Sprintf("rollback removed %d files %d dirs", len(written), len(dirs))))
}

// mkdirAllTracked creates dir (and all parents) using os.MkdirAll, tracking
// each directory that did not exist before. Only directories under realRoot are
// tracked (realRoot itself is assumed to pre-exist).
func mkdirAllTracked(dir string, mode os.FileMode, realRoot string, created *[]string) error {
	// Collect non-existent components from innermost outward.
	toCreate, err := collectMissingDirs(dir, realRoot)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}

	// Record in creation order (outermost first) so reverse-order removal
	// during rollback removes leaves before parents.
	for i := len(toCreate) - 1; i >= 0; i-- {
		*created = append(*created, toCreate[i])
	}
	return nil
}

// collectMissingDirs returns the directories that do not exist yet, starting
// from dir and walking up to (but not including) realRoot. Returned slice is
// ordered leaf-first (innermost first), so callers that reverse it get
// outermost-first creation order.
//
// The second return value carries any non-ENOENT stat error (most importantly
// EACCES when an intermediate parent is chmoded 0o000); callers MUST propagate
// it so rollback runs over previously-written entries. Treating EACCES as
// "directory exists" — the develop @ 41fc70074 behavior — causes the rollback
// path to skip dirs that were never actually created and leave goroutine-local
// state inconsistent with disk.
func collectMissingDirs(dir, realRoot string) ([]string, error) {
	var missing []string
	cur := dir
	for cur != realRoot && cur != filepath.Dir(cur) {
		if _, err := os.Stat(cur); err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, cur)
				cur = filepath.Dir(cur)
				continue
			}
			// RED-commit stub: original develop swallowed every non-ENOENT
			// error as "directory exists". Returning nil here preserves that
			// (buggy) behavior so the internal RED test fails until A9 GREEN
			// commit replaces this with `return nil, errcode.Wrap(...)`.
			break
		}
		// Once we hit an existing dir, all parents exist too.
		break
	}
	return missing, nil
}
