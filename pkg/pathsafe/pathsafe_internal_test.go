package pathsafe

// Internal (package pathsafe) tests for unexported helpers:
//   - collectMissingDirs: EACCES on intermediate parent must propagate as a
//     non-nil error, not be swallowed as "directory exists / break".
//   - captureOriginal / forceOverwritePreflightPass (ForceOverwrite
//     inode-kind gate): error/rejection branches the public-API tests cannot
//     reach (lstat ENOTDIR, non-restorable inode kind, unreadable regular
//     file).
//
// Skip windows + root: chmod 0o000 not reliable on windows; ineffective as
// root. Sequential (not Parallel) because chmod is process-global state.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// TestCollectMissingDirs_NormalChain exercises the happy path: all
// intermediate dirs are absent (ENOENT) → the function returns the missing
// chain leaf-first with nil err. The walk also stops when it hits an
// existing ancestor (rolled into this test via the realRoot terminator).
func TestCollectMissingDirs_NormalChain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	leaf := filepath.Join(root, "a", "b", "c")
	missing, err := collectMissingDirs(leaf, root)
	if err != nil {
		t.Fatalf("collectMissingDirs(normal chain): unexpected error: %v", err)
	}
	if len(missing) != 3 {
		t.Fatalf("collectMissingDirs(normal chain): want 3 missing, got %d (%v)", len(missing), missing)
	}
	// Leaf-first order: innermost first → outermost last.
	if missing[0] != leaf {
		t.Errorf("missing[0] = %q, want leaf %q", missing[0], leaf)
	}
}

// TestCollectMissingDirs_StopsAtExisting verifies the early-break behavior:
// once the walk encounters an existing directory, it stops (no further
// ancestor probing) — parents of an existing dir are implicitly present.
func TestCollectMissingDirs_StopsAtExisting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Pre-create root/a so the walk from root/a/b/c stops after recording b, c.
	existing := filepath.Join(root, "a")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(existing, "b", "c")
	missing, err := collectMissingDirs(leaf, root)
	if err != nil {
		t.Fatalf("collectMissingDirs(partial chain): unexpected error: %v", err)
	}
	if len(missing) != 2 {
		t.Fatalf("collectMissingDirs(partial chain): want 2 missing, got %d (%v)", len(missing), missing)
	}
}

// EACCES on an intermediate dir must surface as an error, not be silently
// dropped via the os.IsNotExist(err) check (the develop @ 41fc70074 bug).
func TestCollectMissingDirs_EACCESReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 semantics differ on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0o000 ineffective as root")
	}

	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("Chmod 0o000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	// Target dir lies under the 0o000 parent — os.Stat on it returns EACCES
	// (parent has no execute bit), NOT ENOENT.
	target := filepath.Join(blocked, "sub", "leaf")
	missing, err := collectMissingDirs(target, root)
	if err == nil {
		t.Fatalf("collectMissingDirs(target under 0o000 parent): want error, got nil (missing=%v)", missing)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("EACCES misclassified as not-exist: err=%v", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("expected fs.ErrPermission, got err=%v", err)
	}
}

// --- ForceOverwrite inode-kind gate: error/rejection branches ---
//
// These exercise captureOriginal (live writePass capture) and
// forceOverwritePreflightPass (dry-run + pre-write) failure paths that the
// public WritePlannedFiles tests cannot deterministically drive, keeping
// new-code coverage of pkg/pathsafe over the 80% gate.
//
// The Readlink failure branch in captureOriginal is intentionally not
// exercised: once Lstat classified the inode as a symlink, os.Readlink only
// fails under a concurrent inode-swap (TOCTOU) which cannot be forced
// deterministically. It is a defensive Wrap with no behavioral logic.

// captureOriginal must reject a non-restorable inode kind (directory) so that
// live writePass fails before the destructive write — covers the
// forceOverwriteRestorable gate inside captureOriginal.
func TestCaptureOriginal_RejectsNonRestorableKind(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "squatting-dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := captureOriginal(dir)
	if err == nil {
		t.Fatal("captureOriginal(directory): want rejection error, got nil")
	}
	if !strings.Contains(err.Error(), errMsgForceOverwriteKindGate) {
		t.Errorf("captureOriginal(directory): err = %v, want kind-gate rejection", err)
	}
}

// captureOriginal must Wrap an lstat failure that is NOT os.IsNotExist
// (here ENOTDIR: a path component is a regular file, not a directory).
// errcode.Error.Unwrap() returns the Cause, so errors.Is can chain through to
// syscall.ENOTDIR on unix. The syscall constant is unix-only — skip on Windows.
func TestCaptureOriginal_LstatNonNotExist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("syscall.ENOTDIR is a unix-only sentinel")
	}
	t.Parallel()
	root := t.TempDir()
	parentFile := filepath.Join(root, "afile")
	if err := os.WriteFile(parentFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// "afile" is a regular file → lstat of "afile/child" returns ENOTDIR.
	_, err := captureOriginal(filepath.Join(parentFile, "child"))
	if err == nil {
		t.Fatal("captureOriginal(path under non-dir parent): want error, got nil")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ENOTDIR misclassified as not-exist: err=%v", err)
	}
	// Positive assertion: errcode.Error.Unwrap() chains to the underlying
	// syscall error; ENOTDIR must be reachable via errors.Is.
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("expected syscall.ENOTDIR in error chain, got err=%v", err)
	}
}

// captureOriginal must Wrap a ReadFile failure on a regular file whose read
// permission was removed after the lstat kind check.
func TestCaptureOriginal_ReadFileError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 semantics differ on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0o000 ineffective as root")
	}
	f := filepath.Join(t.TempDir(), "unreadable")
	if err := os.WriteFile(f, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, 0o000); err != nil {
		t.Fatalf("Chmod 0o000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(f, 0o644) })

	_, err := captureOriginal(f)
	if err == nil {
		t.Fatal("captureOriginal(unreadable regular file): want error, got nil")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("expected fs.ErrPermission, got err=%v", err)
	}
}

// forceOverwritePreflightPass must reject a ForceOverwrite entry whose target
// is a non-restorable inode kind (directory) so dry-run rejects exactly what
// live would (F2 parity).
func TestForceOverwritePreflightPass_RejectsDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "gen-target-is-dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := forceOverwritePreflightPass([]PlannedFile{
		{AbsPath: dir, Content: []byte("x"), forceOverwrite: true},
	})
	if err == nil {
		t.Fatal("forceOverwritePreflightPass(dir target): want rejection, got nil")
	}
	if !strings.Contains(err.Error(), errMsgForceOverwriteKindGate) {
		t.Errorf("forceOverwritePreflightPass(dir target): err = %v, want kind-gate rejection", err)
	}
}

// forceOverwritePreflightPass must Wrap an lstat failure that is NOT
// os.IsNotExist (ENOTDIR: a path component is a regular file).
// errcode.Error.Unwrap() returns the Cause, so errors.Is can chain through to
// syscall.ENOTDIR on unix. The syscall constant is unix-only — skip on Windows.
func TestForceOverwritePreflightPass_LstatNonNotExist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("syscall.ENOTDIR is a unix-only sentinel")
	}
	t.Parallel()
	root := t.TempDir()
	parentFile := filepath.Join(root, "afile")
	if err := os.WriteFile(parentFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := forceOverwritePreflightPass([]PlannedFile{
		{AbsPath: filepath.Join(parentFile, "child"), forceOverwrite: true},
	})
	if err == nil {
		t.Fatal("forceOverwritePreflightPass(path under non-dir parent): want error, got nil")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ENOTDIR misclassified as not-exist: err=%v", err)
	}
	// Positive assertion: errcode.Error.Unwrap() chains to the underlying
	// syscall error; ENOTDIR must be reachable via errors.Is.
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("expected syscall.ENOTDIR in error chain, got err=%v", err)
	}
}

// Non-ForceOverwrite entries must be skipped by the preflight pass even when
// their target is a directory (covers the `continue` guard).
func TestForceOverwritePreflightPass_SkipsNonForceEntries(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "dir-but-not-force")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := forceOverwritePreflightPass([]PlannedFile{
		{AbsPath: dir, Content: []byte("x"), forceOverwrite: false},
	}); err != nil {
		t.Fatalf("forceOverwritePreflightPass(non-force dir): want nil, got %v", err)
	}
}
