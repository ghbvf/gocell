// Package fspath holds the single, symlink-aware root-containment predicate
// shared across the codebase: IsWithinRoot reports whether a target path
// resolves inside a root, and EvalExistingPrefix resolves the longest existing
// ancestor of a not-yet-existing path.
//
// # Single source
//
// This is the SOLE implementation. Both kernel/metadata (schema ref resolution)
// and kernel/governance (REF-11/REF-12 path-traversal rules, generated-artifact
// lookup) depend on it, as do cmd/ and tools/codegen. It lives under pkg/ —
// rather than in either kernel package — because kernel/metadata cannot import
// kernel/governance: the kernel-internal import DAG forbids it (enforced by
// archtest KERNEL-INTERNAL-DAG-01). pkg/ depends only on the standard library,
// so it is the one place both kernel layers may reach. Extracting it here is
// what eliminated the previous hand-synced duplicate pair (the old `// SYNC:`
// notes, #1255).
//
// This package comment is clarifying documentation, not an enforcement
// mechanism: the import ban that makes a shared home necessary is held by
// KERNEL-INTERNAL-DAG-01, not by this note.
//
// # Boundary
//
// Not a substitute for pkg/pathsafe.ContainPath: that helper takes a relative
// target under a pre-resolved root and walks parent symlinks as part of the
// scaffold/codegen write TOCTOU funnel. fspath is a read-only predicate over a
// (root, target) pair where both sides are absolutized internally; it makes no
// write-safety guarantee.
package fspath

import (
	"os"
	"path/filepath"
	"strings"
)

// IsWithinRoot reports whether target resolves to a path inside root.
//
// Both sides are absolutized and symlink-resolved before comparison, defeating
// both relative-path and symlink-based escapes. When a side cannot be resolved
// by filepath.EvalSymlinks (the common case where target does not exist yet, or
// a non-existent root), the longest existing ancestor is resolved via
// EvalExistingPrefix and the missing suffix appended — so a not-yet-created
// file under root is still reported as within root, while a symlink that
// redirects outside root is rejected.
func IsWithinRoot(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	} else {
		absRoot = EvalExistingPrefix(absRoot)
	}
	if resolved, err := filepath.EvalSymlinks(absTarget); err == nil {
		absTarget = resolved
	} else {
		absTarget = EvalExistingPrefix(absTarget)
	}
	prefix := absRoot + string(os.PathSeparator)
	return absTarget == absRoot || strings.HasPrefix(absTarget, prefix)
}

// EvalExistingPrefix resolves symlinks on the longest existing ancestor of p,
// then re-appends the non-existent suffix. This handles platforms where
// intermediate directories are symlinks (e.g., macOS /tmp → /private/tmp) for
// paths whose leaf does not exist yet.
func EvalExistingPrefix(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p // filesystem root, stop recursion
	}
	return filepath.Join(EvalExistingPrefix(parent), filepath.Base(p))
}
