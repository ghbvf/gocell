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
// Internally routed through secureMkdirAllAndWrite (forceOverwrite=true) so
// the parent walk and leaf write share the fd-anchored TOCTOU defense with
// the plan funnel. On unix, parent symlinks fail closed via syscall
// O_NOFOLLOW|O_DIRECTORY; the existing leaf inode is removed via unlinkat
// relative to the parent fd (symlinks unlinked rather than followed).
//
// realRoot must be non-empty and the output of ResolveRoot; an empty realRoot
// is rejected with ErrValidationFailed. The dropped/created dir list is
// discarded: WriteFileForce is the single-shot variant and is not transactional
// across multiple files.
func WriteFileForce(realRoot, absPath string, content []byte, mode os.FileMode) error {
	if realRoot == "" {
		// No containment anchor available → cannot run fd-walk against an
		// absent realRoot. Fall back to single-file plan with empty realRoot
		// is not meaningful; require explicit realRoot from caller.
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pathsafe: WriteFileForce requires non-empty realRoot",
			errcode.WithDetails(slog.String("path", absPath)))
	}
	targetRel, err := filepath.Rel(realRoot, absPath)
	if err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pathsafe: cannot relativize path", err,
			errcode.WithDetails(slog.String("path", absPath)))
	}
	if _, err := ContainPath(realRoot, targetRel); err != nil {
		return err
	}
	var discardedCreated []string
	return secureMkdirAllAndWrite(
		realRoot, absPath, content,
		defaultDirMode, mode,
		true, // forceOverwrite
		&discardedCreated,
	)
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
//
// Note: this is a caller-side early-reject check used during plan
// CONSTRUCTION (e.g., scaffold flag parsing). It accepts symlinks that
// resolve within realRoot. The authoritative TOCTOU defense at WRITE
// time is the fd-anchored openat chain in WritePlannedFiles, which
// rejects ALL parent symlinks regardless of destination. Callers MUST
// NOT treat a successful ContainPath as a guarantee that subsequent
// WritePlannedFiles will succeed for the same path.
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
//  1. Each plan[i].AbsPath is lexically verified against realRoot via
//     planContainmentPass (path-prefix only); authoritative symlink
//     rejection happens at writePass via the fd-anchored openat(O_NOFOLLOW)
//     chain (unix) or O_EXCL leaf write (windows advisory).
//  2. Each AbsPath must not already exist (conflict detection over the
//     FULL plan — no partial-write semantics). ForceOverwrite entries are
//     exempt here but still pass the forceOverwritePreflightPass inode-kind
//     gate so dry-run rejects exactly what live would (F2 parity).
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
	if err := duplicatePass(plan); err != nil {
		return err
	}
	if err := planContainmentPass(realRoot, plan); err != nil {
		return err
	}
	if err := conflictPass(plan); err != nil {
		return err
	}
	if err := forceOverwritePreflightPass(plan); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	return writePass(realRoot, plan)
}

// duplicatePass rejects plans containing two entries with the same AbsPath.
// Runs before planContainmentPass so dry-run callers also fail-closed on dup
// plans (the develop @ 41fc70074 dry-run silently accepted duplicates because
// O_EXCL only fired at writePass time). A plan-content invariant, not a
// filesystem-state check.
//
// Backlog: PATHSAFE-PLANSET-TYPED-HARD-01 (cap-14) — upgrade to
// type-system Hard via PlanSet newtype + dup-reject in constructor,
// scheduled with Lane E typed-scaffold-ID single-source收编.
func duplicatePass(plan []PlannedFile) error {
	seen := make(map[string]struct{}, len(plan))
	for _, f := range plan {
		if _, dup := seen[f.AbsPath]; dup {
			return errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"pathsafe: duplicate AbsPath in plan",
				errcode.WithDetails(slog.String("absPath", f.AbsPath)))
		}
		seen[f.AbsPath] = struct{}{}
	}
	return nil
}

// planContainmentPass verifies that every AbsPath in plan is **lexically**
// rooted under realRoot. This is a friendly early-reject path-only check
// (no os.Lstat / no symlink walk); the authoritative containment defense
// runs at writePass via syscall-level fd-anchored openat O_NOFOLLOW chain
// (unix) or O_EXCL leaf write (windows advisory).
//
// Path-based parent-symlink rejection has been removed FROM THE WRITE
// PIPELINE (containmentPass deleted); ContainPath (caller-side early-reject)
// still performs the path-based walk as an orthogonal early-fail signal —
// see ContainPath godoc for that boundary.
func planContainmentPass(realRoot string, plan []PlannedFile) error {
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
	}
	return nil
}

// conflictPass checks that none of the planned output paths already exist.
// The full plan is checked before any write (no partial-write semantics).
// Uses os.Lstat (not os.Stat) so that leaf symlinks — both dangling and
// non-dangling — are detected and rejected even when the symlink destination
// does not exist. This prevents an attacker from pre-placing a symlink at the
// target path to redirect writes outside root.
//
// Entries with ForceOverwrite=true are skipped: callers using force-overwrite
// expect to replace prior content (writePass will os.Remove the existing
// inode before O_EXCL|O_NOFOLLOW recreate). This is a path-based pre-check
// for friendlier error messages; the authoritative TOCTOU defense lives in
// writePass via syscall O_EXCL|O_NOFOLLOW at the actual write call.
func conflictPass(plan []PlannedFile) error {
	for _, f := range plan {
		if f.ForceOverwrite {
			continue
		}
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

// restoreKind classifies the original inode kind at a ForceOverwrite target,
// captured before the destructive unlinkat so rollback can restore it. ENOENT
// at capture time → kindNone (no original, rollback only removes the freshly
// written file). Directories / devices / pipes / sockets are rejected pre-write
// (cannot be sensibly restored by writePass).
type restoreKind int

const (
	kindNone restoreKind = iota
	kindRegular
	kindSymlink
)

// writeRecord pairs the written path with the captured original-inode state
// needed to make ForceOverwrite plans transactional. The default zero value
// is {kindNone}, matching the non-ForceOverwrite case where rollback only
// removes the newly written file.
type writeRecord struct {
	path           string
	originalKind   restoreKind
	originalBytes  []byte      // regular only
	originalMode   os.FileMode // regular only
	originalTarget string      // symlink only
}

// captureOriginal reads the existing inode at path so rollbackWrites can
// restore it if a subsequent plan entry fails. Called only for entries with
// ForceOverwrite=true. Returns:
//
//   - kindNone (no original to restore) when path does not exist
//   - kindRegular + content + mode for regular files
//   - kindSymlink + link target for symlinks
//   - error for anything else (directory, device, pipe, socket): ForceOverwrite
//     over a non-file/non-symlink slot cannot be rolled back coherently, so
//     it is rejected pre-write and the destructive unlinkat is never reached.
//
// Capture is path-based (os.Lstat / os.ReadFile / os.Readlink), consistent with
// the path-based os.Remove used by rollbackWrites already (see rollbackWrites
// godoc — rollback is not a TOCTOU security boundary; the fd-walk in writePass
// is the one-shot defense at write time).
func captureOriginal(path string) (writeRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return writeRecord{path: path, originalKind: kindNone}, nil
		}
		return writeRecord{}, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"pathsafe: lstat original for ForceOverwrite capture", err,
			errcode.WithInternal(fmt.Sprintf("path=%s", path)))
	}
	mode := info.Mode()
	if !forceOverwriteRestorable(mode) {
		return writeRecord{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pathsafe: ForceOverwrite supports regular files and symlinks only",
			errcode.WithDetails(slog.String("path", path)))
	}
	if mode.IsRegular() {
		// path already passed planContainmentPass + caller-side ContainPath;
		// this read is the capture-for-rollback step (path-based; rollback
		// is not a TOCTOU boundary — see rollbackWrites godoc).
		content, readErr := os.ReadFile(path) //nolint:gosec // R2-approved: G304 capture-for-rollback (see above)
		if readErr != nil {
			return writeRecord{}, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"pathsafe: read original for ForceOverwrite capture", readErr,
				errcode.WithInternal(fmt.Sprintf("path=%s", path)))
		}
		return writeRecord{
			path:          path,
			originalKind:  kindRegular,
			originalBytes: content,
			originalMode:  mode.Perm(),
		}, nil
	}
	// Guaranteed symlink: forceOverwriteRestorable already rejected every
	// other inode kind above (single source for the kind gate).
	target, readErr := os.Readlink(path)
	if readErr != nil {
		return writeRecord{}, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"pathsafe: readlink original for ForceOverwrite capture", readErr,
			errcode.WithInternal(fmt.Sprintf("path=%s", path)))
	}
	return writeRecord{
		path:           path,
		originalKind:   kindSymlink,
		originalTarget: target,
	}, nil
}

// forceOverwriteRestorable reports whether an existing inode of the given
// mode at a ForceOverwrite target can be coherently captured and rolled back
// (regular file or symlink). Directories / devices / pipes / sockets cannot
// and must be rejected pre-write. This is the SINGLE source for the
// ForceOverwrite inode-kind gate: captureOriginal (live writePass) and
// forceOverwritePreflightPass (dry-run + pre-write) both consult it, so
// dry-run can never accept a target that live would reject (F2 parity).
func forceOverwriteRestorable(mode os.FileMode) bool {
	return mode.IsRegular() || mode&os.ModeSymlink != 0
}

// forceOverwritePreflightPass runs the captureOriginal inode-kind gate over
// every ForceOverwrite entry WITHOUT the destructive capture. It runs for
// both dry-run and live (before any write), so:
//
//   - dry-run rejects exactly the ForceOverwrite targets live would reject
//     (closes the pre-PR#544 gap where dry-run returned after conflictPass —
//     which skips ForceOverwrite entries — and never reached the
//     captureOriginal kind check, so a dir/device squatting a generated path
//     passed dry-run but failed live);
//   - live fails before the FIRST write instead of mid-plan, preserving the
//     all-or-nothing contract symmetrically with conflictPass.
//
// Absent target → ok (writePass records kindNone). Lstat (not Stat) so a
// leaf symlink is classified as symlink, not its (possibly absent) target.
func forceOverwritePreflightPass(plan []PlannedFile) error {
	for _, f := range plan {
		if !f.ForceOverwrite {
			continue
		}
		info, err := os.Lstat(f.AbsPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"pathsafe: lstat ForceOverwrite target (preflight)", err,
				errcode.WithInternal(fmt.Sprintf("path=%s", f.AbsPath)))
		}
		if !forceOverwriteRestorable(info.Mode()) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"pathsafe: ForceOverwrite supports regular files and symlinks only",
				errcode.WithDetails(slog.String("path", f.AbsPath)))
		}
	}
	return nil
}

// writePass executes the plan via the platform-specific secureMkdirAllAndWrite
// funnel (fd-anchored openat/mkdirat/openat chain on unix; path-based
// os.MkdirAll + O_EXCL on Windows as advisory). ForceOverwrite entries have
// their original inode captured via captureOriginal before the destructive
// write so that rollback can restore the original on mid-plan failure
// (preserving the funnel's all-or-nothing contract). On the first failure all
// previously-written files are rolled back (originals restored where captured,
// otherwise removed) and previously-created directories are removed, in
// reverse order.
func writePass(realRoot string, plan []PlannedFile) error {
	var written []writeRecord
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
		record := writeRecord{path: f.AbsPath, originalKind: kindNone}
		if f.ForceOverwrite {
			captured, err := captureOriginal(f.AbsPath)
			if err != nil {
				return rollbackWrites(written, createdDirs, err)
			}
			record = captured
		}
		if err := secureMkdirAllAndWrite(
			realRoot, f.AbsPath, f.Content, dirMode, fileMode,
			f.ForceOverwrite, &createdDirs,
		); err != nil {
			return rollbackWrites(written, createdDirs, err)
		}
		written = append(written, record)
	}
	return nil
}

// rollbackWrites unwinds a partial plan: removes the freshly-written file at
// every recorded path, restores any ForceOverwrite original inode (regular
// content + mode, or symlink target), then removes any newly-created dirs
// in reverse creation order. Wraps originalErr with rollback statistics.
//
// Note: rollback uses path-based os.Remove / os.WriteFile / os.Symlink (NOT
// fd-anchored). This is best-effort: if a parent directory has been swapped
// to a symlink between the original write and rollback, those operations
// follow the symlink. The TOCTOU defense (writePass fd-walk) is one-shot at
// write time; rollback is forensic cleanup, not a security boundary.
func rollbackWrites(written []writeRecord, dirs []string, originalErr error) error {
	restored := 0
	for i := len(written) - 1; i >= 0; i-- {
		r := written[i]
		_ = os.Remove(r.path)
		switch r.originalKind {
		case kindRegular:
			if err := os.WriteFile(r.path, r.originalBytes, r.originalMode); err == nil {
				restored++
			}
		case kindSymlink:
			if err := os.Symlink(r.originalTarget, r.path); err == nil {
				restored++
			}
		case kindNone:
			// Nothing to restore; the entry didn't exist before the write.
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
		"pathsafe: write failed; rollback removed files and dirs", originalErr,
		errcode.WithInternal(fmt.Sprintf("rollback removed %d files %d dirs, restored %d originals", len(written), len(dirs), restored)))
}

// collectMissingDirs returns the directories that do not exist yet, starting
// from dir and walking up to (but not including) realRoot. Returned slice is
// ordered leaf-first (innermost first), so callers that reverse it get
// outermost-first creation order.
//
// The second return value carries any non-ENOENT stat error (most importantly
// EACCES when an intermediate parent is chmoded 0o000); callers MUST propagate
// it so rollback runs over previously-written entries. Treating EACCES as
// "directory exists" — the develop @ 41fc70074 behavior — caused the rollback
// path to skip directories that were never actually created and leave
// goroutine-local rollback state inconsistent with disk.
//
// On unix this helper is now only reachable through the Windows code path
// (nofollow_windows.go's secureMkdirAllAndWrite); the unix fd-walk handles
// EACCES natively via syscall.Openat propagation. Kept in the platform-neutral
// file so the internal correctness test runs on linux/macOS CI.
func collectMissingDirs(dir, realRoot string) ([]string, error) {
	var missing []string
	cur := dir
	for cur != realRoot && cur != filepath.Dir(cur) {
		_, err := os.Stat(cur)
		if err == nil {
			// Hit an existing dir → all parents exist too.
			break
		}
		if os.IsNotExist(err) {
			missing = append(missing, cur)
			cur = filepath.Dir(cur)
			continue
		}
		// EACCES (and any other non-ENOENT error): propagate so rollback
		// of previously-written entries actually runs. Wrap once with
		// errcode so callers' errors.Is(err, fs.ErrPermission) walk
		// continues to match the underlying syscall.Errno.
		return nil, errcode.Wrap(errcode.KindPermissionDenied, errcode.ErrInternal,
			"pathsafe: stat parent dir", err,
			errcode.WithInternal(fmt.Sprintf("dir=%s", cur)))
	}
	return missing, nil
}
