// locator_symlink_test.go — regression for #1592: the disk-backed Locator must
// confine all filesystem access to the workspace root, so a manifest module path
// (or any subpath) that is a SYMLINK escaping the root cannot make metadata
// discovery read cells/contracts from outside the repo.
//
// Pre-fix the Locator used os.DirFS, which follows symlinks: a manifest
// `modules[].path` pointing at a symlink-to-outside passed validation
// (fs.Stat follows the link) and discovery walked the external tree. The fix
// builds the Locator's fsys from os.OpenRoot(root).FS(), which rejects any path
// that escapes the root ("path escapes from parent") at the syscall layer.
package metadata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocator_RootConfinement_SymlinkEscape is the #1592 regression. A manifest
// module path that is a symlink escaping the workspace root must fail closed —
// Parse() returns an error rather than silently discovering external metadata.
func TestLocator_RootConfinement_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir() // sibling dir OUTSIDE root, carrying real metadata

	// External cell.yaml the attacker/misconfig would pull into the scan.
	writeFile(t, filepath.Join(external, "cells", "leak", "cell.yaml"),
		cellYAML("leak", "ext-team", "leak.primary"))

	// root/linked -> external (a symlink escaping the workspace root).
	if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	// Manifest declares the symlinked dir as a module — string-cleaning (abs /
	// "..") cannot catch this; only root confinement does.
	writeFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
		"version: v1\nmodules:\n  - path: linked\n")

	// The user-visible contract is fail-closed: an escaping-symlink module path
	// makes Parse() return an ERROR (os.Root rejects it), not silently skip it.
	// Assert err != nil unconditionally, then bind it to the os.Root confinement
	// signal so an unrelated failure can't masquerade as the #1592 fix. Pre-fix
	// Parse returned a nil error and discovered the external "leak" cell.
	pm, err := NewParser(root).Parse()
	if err == nil {
		t.Fatalf("Parse() = nil error; want fail-closed (symlink escapes root). pm.Cells=%v", keysOf(pm.Cells))
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Parse() failed, but not via root confinement (want \"escapes\"): %v", err)
	}
}

// TestLocator_OpenRootError covers NewLocator's os.OpenRoot failure branch: a
// non-existent root must fail closed at construction (not defer to Discover).
func TestLocator_OpenRootError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	_, err := NewLocator(missing)
	if err == nil {
		t.Fatalf("NewLocator(%q) = nil error; want fail-closed open-root error", missing)
	}
	if !strings.Contains(err.Error(), "open root") {
		t.Fatalf("NewLocator error = %q, want substring %q", err.Error(), "open root")
	}
}

// TestNewLocator_InvalidManifestPathClosesRoot covers NewLocator's
// validateManifestRelativePath rejection branch, which Closes the just-opened
// os.Root before returning. A parent-escaping WithManifestPath on an otherwise
// valid disk root must fail at construction (releasing the directory fd rather
// than leaking it). The existing unsafe-path table
// (TestLocator_WithManifestPathRejectsUnsafe) exercises only NewLocatorFS,
// never this disk NewLocator branch.
func TestNewLocator_InvalidManifestPathClosesRoot(t *testing.T) {
	root := t.TempDir() // valid root → os.OpenRoot succeeds; only the manifest path is rejected
	_, err := NewLocator(root, WithManifestPath("../outside/manifest.yaml"))
	if err == nil {
		t.Fatalf("NewLocator with parent-escaping manifest path = nil error; want rejection")
	}
	if !strings.Contains(err.Error(), "path escape not allowed") {
		t.Fatalf("NewLocator error = %q, want substring %q", err.Error(), "path escape not allowed")
	}
}

// TestLocator_RootConfinement_NormalLayoutParses guards against the confinement
// change breaking ordinary (symlink-free) on-disk discovery.
func TestLocator_RootConfinement_NormalLayoutParses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
		"version: v1\nmodules:\n  - path: .\n")
	writeFile(t, filepath.Join(root, "cells", "ok", "cell.yaml"),
		cellYAML("ok", "team", "ok.primary"))

	pm, err := NewParser(root).Parse()
	if err != nil {
		t.Fatalf("Parse() unexpected error for normal layout: %v", err)
	}
	if _, ok := pm.Cells["ok"]; !ok {
		t.Fatalf("Parse() did not discover in-root cell %q; got %v", "ok", keysOf(pm.Cells))
	}
}

// TestLocator_Close_Idempotent verifies the os.Root handle closes cleanly and
// Close is safe to call more than once.
func TestLocator_Close_Idempotent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
		"version: v1\nmodules:\n  - path: .\n")
	loc, err := NewLocator(root)
	if err != nil {
		t.Fatalf("NewLocator: %v", err)
	}
	if err := loc.Close(); err != nil {
		t.Fatalf("Close() #1: %v", err)
	}
	if err := loc.Close(); err != nil {
		t.Fatalf("Close() #2 (idempotent): %v", err)
	}
}

func keysOf(m map[string]*CellMeta) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return strings.Join(ks, ",")
}
