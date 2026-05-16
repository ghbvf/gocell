package pathsafe_test

// Lane A RED tests for:
//   - A-API: PlannedFile.ForceOverwrite + duplicate AbsPath preflight
//   - A4   : parent-symlink TOCTOU (fd-anchored walk rejects ALL parent symlinks)
//   - A9   : EACCES rollback (intermediate 0o000 parent → rollback cleans partial writes)
//
// Tests run against develop @ 41fc70074 are expected to:
//   - DupAbsPath_RejectsInDryRun                                   : FAIL  (no duplicatePass)
//   - ForceOverwrite_OverwritesExistingFile                        : FAIL  (conflictPass blocks)
//   - ForceOverwrite_ReplacesLeafSymlinkWithoutFollow              : FAIL  (conflictPass blocks leaf symlink before A-API; after A-API
//     conflictPass skips ForceOverwrite entries → writePass Remove
//     + O_NOFOLLOW recreate succeeds without following target)
//   - ParentSymlink_DirectParent / _Intermediate                   : FAIL  (in-root symlinks accepted)
//   - DupAbsPath_Rejects                                           : PASS  (whole-plan EEXIST rollback)
//   - EACCESRollbackCleansCreatedDirs                              : PASS  (containmentPass masks)
//
// After Lane A GREEN commits all pass for the correct reason.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ghbvf/gocell/pkg/pathsafe"
)

// =============================================================================
// A-API
// =============================================================================

// Two entries with the same AbsPath must be rejected (whole-plan rejection,
// no temporary file created). Develop accidentally passes this assertion via
// O_EXCL + rollback after plan[0] writes; A-API rejects pre-write via
// duplicatePass — both end in no file on disk.
func TestWritePlannedFiles_DupAbsPath_Rejects(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)

	abs := filepath.Join(root, "cells", "dup", "cell.yaml")
	plan := []pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("first")},
		{AbsPath: abs, Content: []byte("second")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(dup AbsPath): want error, got nil")
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		t.Error("dup AbsPath: file written despite duplicate rejection")
	}
}

// Dry-run must also catch duplicates: duplicatePass runs before the dry-run
// early return. On develop, dryRun returns nil for plans containing duplicates
// — this test is RED on develop, GREEN on A-API.
func TestWritePlannedFiles_DupAbsPath_RejectsInDryRun(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)

	abs := filepath.Join(root, "cells", "dup", "cell.yaml")
	plan := []pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("a")},
		{AbsPath: abs, Content: []byte("b")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, true); err == nil {
		t.Fatal("WritePlannedFiles(dup, dryRun): want error, got nil")
	}
}

// ForceOverwrite=true: existing regular file must be replaced with new content.
// Develop has no such field (zero-value false → conflictPass rejects existing
// file) → test RED. A-API: conflictPass skips ForceOverwrite entries; writePass
// removes existing file then writes fresh → test GREEN.
func TestWritePlannedFiles_ForceOverwrite_OverwritesExistingFile(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)

	abs := filepath.Join(root, "generated", "stamp.go")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("// old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("// regenerated\n"), ForceOverwrite: true},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles(ForceOverwrite=true over existing): unexpected error: %v", err)
	}
	data, err := os.ReadFile(abs) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "// regenerated\n" {
		t.Errorf("file content = %q, want %q", data, "// regenerated\n")
	}
}

// ForceOverwrite=true on a leaf symlink: the symlink must be removed and
// replaced with a real file at the leaf path; the symlink TARGET (outside
// root) must NOT be written to. Aligns with the existing WriteFileForce
// semantics (Remove → O_EXCL|O_NOFOLLOW write).
//
// Develop: conflictPass.Lstat sees ModeSymlink → returns ErrConflict → test
// fails ("unexpected error"). A-API: conflictPass skips ForceOverwrite entries
// → writePass os.Remove(absPath) removes the symlink → writeFileNoFollow
// creates a fresh inode at absPath → outside is untouched.
func TestWritePlannedFiles_ForceOverwrite_ReplacesLeafSymlinkWithoutFollow(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := resolveRealRoot(t)
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "evil.go")

	leafDir := filepath.Join(root, "generated", "cell")
	if err := os.MkdirAll(leafDir, 0o755); err != nil {
		t.Fatal(err)
	}
	leafLink := filepath.Join(leafDir, "stamp.go")
	if err := os.Symlink(outsideTarget, leafLink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: leafLink, Content: []byte("// regenerated\n"), ForceOverwrite: true},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles(ForceOverwrite over leaf symlink): unexpected error: %v", err)
	}
	// Outside target must NOT have been written through the symlink.
	if _, statErr := os.Stat(outsideTarget); statErr == nil {
		t.Error("leaf symlink escape: outside target was written despite O_NOFOLLOW")
	}
	// Leaf path must now be a regular file with the new content.
	info, err := os.Lstat(leafLink)
	if err != nil {
		t.Fatalf("Lstat leafLink after ForceOverwrite: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("leaf path is still a symlink; ForceOverwrite should have replaced it with a real file")
	}
	data, err := os.ReadFile(leafLink) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatalf("ReadFile leafLink: %v", err)
	}
	if string(data) != "// regenerated\n" {
		t.Errorf("leaf content = %q, want regenerated content", data)
	}
}

// =============================================================================
// A4 — Parent symlink TOCTOU (fd-anchored walk rejects ALL parent symlinks)
// =============================================================================

// Direct parent is an in-root symlink (pointing to a sibling real dir).
// Develop walkParentsForSymlinkContainment accepts in-root symlinks → write
// flows through symlink to realDir/cell.yaml. A4 fd-walk rejects any symlink
// in the parent chain via openat(O_NOFOLLOW|O_DIRECTORY) — fail-closed even
// for in-root targets, because a symlink at parse time could be swapped to
// out-of-root at write time (TOCTOU window).
func TestWritePlannedFiles_ParentSymlink_DirectParent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := resolveRealRoot(t)

	realDir := filepath.Join(root, "cells", "realdir")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symDir := filepath.Join(root, "cells", "symdir")
	if err := os.Symlink(realDir, symDir); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(symDir, "cell.yaml"), Content: []byte("id: symdir\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(direct parent is symlink): want error, got nil")
	}
	// File must NOT have been written through the symlink into realDir.
	if _, statErr := os.Stat(filepath.Join(realDir, "cell.yaml")); statErr == nil {
		t.Error("direct parent symlink: file written via symlink to realDir; fd-walk should have rejected")
	}
}

// Intermediate (non-direct) parent is an in-root symlink: `root/cells` is a
// symlink → `root/realcells`. Plan writes to `root/cells/mycell/cell.yaml`.
// Develop accepts; A4 fd-walk rejects at the intermediate openat call.
func TestWritePlannedFiles_ParentSymlink_Intermediate(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := resolveRealRoot(t)

	realCells := filepath.Join(root, "realcells")
	if err := os.MkdirAll(realCells, 0o755); err != nil {
		t.Fatal(err)
	}
	symCells := filepath.Join(root, "cells")
	if err := os.Symlink(realCells, symCells); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(symCells, "mycell", "cell.yaml"), Content: []byte("id: mycell\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(intermediate parent is symlink): want error, got nil")
	}
	if _, statErr := os.Stat(filepath.Join(realCells, "mycell")); statErr == nil {
		t.Error("intermediate parent symlink: dir created via realCells; fd-walk should have rejected")
	}
}

// =============================================================================
// A9 — EACCES rollback (end-to-end documentation; bug originally masked by
// containmentPass on develop. Test passes on develop for the wrong reason
// (containmentPass catches early), and on A4+A9 for the right reason
// (fd-walk propagates EACCES through writePass → rollback runs).
// =============================================================================

func TestWritePlannedFiles_EACCESRollbackCleansCreatedDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 semantics differ on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0o000 ineffective as root")
	}
	// Not Parallel: chmod 0o000 affects every os.Stat / unix.Openat in this
	// test binary. Other tests in package pathsafe_test that use chmod
	// 0o555 (TestWritePlannedFiles_MkdirFailureRollback) tolerate parallel
	// execution because 0o555 still permits Stat traversal; only 0o000
	// serializes against the whole binary. TestCollectMissingDirs_EACCES
	// (in pathsafe_internal_test.go) runs in a different binary (different
	// package) so does not interact.
	root := resolveRealRoot(t)

	goodFile := filepath.Join(root, "good", "ok.yaml")

	blockedRoot := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blockedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blockedRoot, 0o000); err != nil {
		t.Fatalf("Chmod 0o000: %v", err)
	}
	// LIFO: registered AFTER chmod → runs BEFORE t.TempDir cleanup.
	t.Cleanup(func() { _ = os.Chmod(blockedRoot, 0o755) })

	blockedFile := filepath.Join(blockedRoot, "sub", "bad.yaml")

	plan := []pathsafe.PlannedFile{
		{AbsPath: goodFile, Content: []byte("id: good\n")},
		{AbsPath: blockedFile, Content: []byte("id: bad\n")},
	}
	err := pathsafe.WritePlannedFiles(root, plan, false)
	if err == nil {
		t.Fatal("WritePlannedFiles(EACCES intermediate parent): want error, got nil")
	}
	// Error must not be misclassified as not-exist (the develop bug surface).
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("EACCES misclassified as not-exist: err=%v", err)
	}

	// Whole-plan / rollback invariant: goodFile and its created parent
	// directory must not exist after the failure.
	if _, statErr := os.Stat(goodFile); statErr == nil {
		t.Error("rollback: goodFile not removed after EACCES failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, "good")); statErr == nil {
		t.Error("rollback: root/good/ dir not cleaned after EACCES failure")
	}
}

// =============================================================================
// A4 invariant — post-ContainPath pre-write swap (deterministic, no goroutine)
// =============================================================================

// TestWritePass_TOCTOURaceWindow_PostContainmentPreSwap verifies the A4
// invariant: even when caller-side ContainPath has accepted a path
// (parent dir was a real directory at that moment), if the parent is
// swapped to a symlink before WritePlannedFiles' writePass runs, the
// fd-anchored openat(O_NOFOLLOW|O_DIRECTORY) chain fails closed.
//
// Deterministic post-ContainPath pre-swap injection (no goroutine, no
// time.Sleep): we manually replicate "caller passes ContainPath, then
// attacker swaps parent" as a single-threaded sequence — call ContainPath
// to confirm acceptance, replace the parent real dir with a symlink to
// outside, then call WritePlannedFiles and assert fail-closed.
func TestWritePass_TOCTOURaceWindow_PostContainmentPreSwap(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows; fd-anchored walk not implemented on windows")
	}

	root := resolveRealRoot(t)
	outside := t.TempDir()

	// Create a real parent directory so ContainPath succeeds.
	parentDir := filepath.Join(root, "cells", "racetest")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	targetAbs := filepath.Join(parentDir, "cell.yaml")
	targetRel, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	// Verify ContainPath accepts the path (parent is a real dir at this moment).
	_, err = pathsafe.ContainPath(targetRel, filepath.Join("cells", "racetest", "cell.yaml"))
	if err != nil {
		t.Fatalf("ContainPath: unexpected error before swap: %v", err)
	}

	// Swap: replace the real parent dir with a symlink to outside.
	// This simulates the TOCTOU window: attacker acts after ContainPath but
	// before WritePlannedFiles' writePass resolves the fd chain.
	if err := os.Remove(parentDir); err != nil {
		t.Fatalf("Remove real dir for swap: %v", err)
	}
	if err := os.Symlink(outside, parentDir); err != nil {
		t.Fatalf("Symlink parent to outside: %v", err)
	}

	// WritePlannedFiles must fail closed: the fd-anchored walk hits O_NOFOLLOW
	// on the symlink at cells/racetest and returns ENOTDIR or ELOOP.
	plan := []pathsafe.PlannedFile{
		{AbsPath: targetAbs, Content: []byte("id: racetest\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles post-ContainPath-pre-write swap: want error, got nil")
	}

	// outside must remain empty — nothing was written through the symlink.
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("TOCTOU escape: outside dir has %d entries after swap, want 0: %v", len(entries), entries)
	}
}

// =============================================================================
// Coverage backfill — exported single-file APIs (WriteFile / WriteFileForce)
// and lexical escape-root rejection at planContainmentPass.
//
// These were uncovered prior to Lane A because all existing tests used
// WritePlannedFiles directly. The fd-walk rewrite changed the internals of
// WriteFile / WriteFileForce so they need their own conformance coverage.
// =============================================================================

// TestWriteFile_HappyPath exercises the single-file shorthand: it must funnel
// through WritePlannedFiles, create parent dirs, and write content with
// O_EXCL|O_NOFOLLOW semantics.
func TestWriteFile_HappyPath(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)
	abs := filepath.Join(root, "cells", "wfcell", "cell.yaml")
	if err := pathsafe.WriteFile(root, abs, []byte("id: wfcell\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: unexpected error: %v", err)
	}
	data, err := os.ReadFile(abs) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "id: wfcell\n" {
		t.Errorf("WriteFile content = %q, want %q", data, "id: wfcell\n")
	}
}

// TestWriteFileForce_OverwritesExisting exercises the codegen-regenerate
// variant: an existing file at the target path is replaced (unlinkat at
// parent fd + O_EXCL recreate), preserving root containment.
func TestWriteFileForce_OverwritesExisting(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)
	abs := filepath.Join(root, "generated", "stamp.go")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("// old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pathsafe.WriteFileForce(root, abs, []byte("// regenerated\n"), 0o644); err != nil {
		t.Fatalf("WriteFileForce: unexpected error: %v", err)
	}
	data, err := os.ReadFile(abs) //nolint:gosec // R2-approved: G304 — tempdir test fixture, path constructed in-test
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "// regenerated\n" {
		t.Errorf("WriteFileForce content = %q, want %q", data, "// regenerated\n")
	}
}

// TestWriteFileForce_RejectsEmptyRealRoot verifies the F10 invariant: empty
// realRoot is no longer accepted (fd-walk requires an anchor; the previous
// "caller-responsible" mode is gone).
func TestWriteFileForce_RejectsEmptyRealRoot(t *testing.T) {
	t.Parallel()
	abs := filepath.Join(t.TempDir(), "stamp.go")
	err := pathsafe.WriteFileForce("", abs, []byte("// data\n"), 0o644)
	if err == nil {
		t.Fatal("WriteFileForce(realRoot=\"\"): want error, got nil")
	}
}

// TestWriteFileForce_EscapesRoot verifies containment is enforced: an absPath
// outside realRoot is rejected before any write happens.
func TestWriteFileForce_EscapesRoot(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)
	outside := t.TempDir()
	escapeAbs := filepath.Join(outside, "escape.go")
	err := pathsafe.WriteFileForce(root, escapeAbs, []byte("// data\n"), 0o644)
	if err == nil {
		t.Fatal("WriteFileForce(absPath outside root): want error, got nil")
	}
	if _, statErr := os.Stat(escapeAbs); statErr == nil {
		t.Error("escape: file written to outside root")
	}
}

// TestWritePlannedFiles_PlanContainmentPass_EscapesRoot exercises the
// lexical escape branch of planContainmentPass (separate from the existing
// "absolute path" / "dotdot" cases handled via ContainPath).
func TestWritePlannedFiles_PlanContainmentPass_EscapesRoot(t *testing.T) {
	t.Parallel()
	root := resolveRealRoot(t)
	// An AbsPath that lies outside root → planContainmentPass returns
	// "target escapes root" before the write funnel runs.
	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(t.TempDir(), "outside.yaml"), Content: []byte("id: x\n")},
	}
	if err := pathsafe.WritePlannedFiles(root, plan, false); err == nil {
		t.Fatal("WritePlannedFiles(AbsPath escapes root): want error, got nil")
	}
}
