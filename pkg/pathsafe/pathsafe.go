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
	"errors"
	"os"
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
}

// ResolveRoot returns root resolved through filepath.EvalSymlinks so that
// subsequent ContainPath comparisons are stable even when root itself is a
// symlink. Returns an error if root does not exist or cannot be evaluated.
func ResolveRoot(root string) (string, error) {
	return "", errors.New("pathsafe: not implemented")
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
	return "", errors.New("pathsafe: not implemented")
}

// PlannedPaths returns the absolute target paths in plan order. Callers use
// this to surface dry-run output without parsing PlannedFile structs.
func PlannedPaths(plan []PlannedFile) []string {
	return nil
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
	return errors.New("pathsafe: not implemented")
}
