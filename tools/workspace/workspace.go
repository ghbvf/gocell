// Package workspace enumerates the Go modules of a GoCell workspace.
//
// It is the single source from which archtest, tools/depgraph, and the
// `gocell graph` CLI learn "which modules exist". The module set is DERIVED from
// go.work's `use` directives — the Go toolchain's own truth about which modules
// it compiles in workspace mode — so a module extracted into a nested go.mod and
// added to go.work is automatically covered by the production scan. There is no
// hand-maintained module list to forget (zero drift surface); this is the
// keystone that lets archtest survive the go.work multi-module migration without
// silently losing coverage of an extracted module.
//
// go.work is authoritative for the Go module set. .gocell/manifest.yaml declares
// the (smaller-or-equal) set of modules that carry GoCell metadata
// (cells/contracts/journeys); [Modules] cross-checks manifest.modules ⊆
// go.work.use and fails closed on drift — a metadata module the toolchain does
// not compile is a configuration bug surfaced at archtest time. A workspace
// without a manifest has no metadata modules to cross-check (the Go scan still
// derives from go.work).
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/gomodutil"
)

// goWorkFile / goModFile are the markers walked for by [WorkspaceRoot].
const (
	goWorkFile = "go.work"
	goModFile  = "go.mod"
)

// Module pairs a workspace member's on-disk directory (relative to the
// workspace root, filepath.Clean'd — "." for the root module) with the Go
// module import path declared in that directory's go.mod.
type Module struct {
	// Dir is the use-directive disk path relative to the workspace root.
	Dir string
	// ImportPath is the module path from <Dir>/go.mod (e.g.
	// "github.com/ghbvf/gocell" or "github.com/ghbvf/gocell/mdm").
	ImportPath string
}

// WorkspaceRoot walks up from the process working directory to the workspace
// root and returns its absolute path. Resolution is go.work-FIRST: the nearest
// go.work wins, and only when no go.work exists above cwd does it fall back to
// the nearest go.mod.
//
// This is honest mode detection across the two contexts archtest serves, NOT a
// silent default:
//   - GoCell's own monorepo (and the multi-module fixtures): go.work is present,
//     so the walk anchors to the WORKSPACE root — never to a nested module's
//     go.mod when run from a subdirectory (the single-module bug #1555 fixes).
//   - An external single-module consumer repo (Operator-SDK, #1081) that has a
//     go.mod but no go.work: there is no workspace, so the single module's root
//     is correct. Without this branch RunStandardCellRules could not scan an
//     external module.
//
// go.work-first guarantees the monorepo never falls through to go.mod (its
// go.work sits above every nested module), so the fallback only fires where it
// is the right answer. A tree with neither marker returns an error.
func WorkspaceRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("workspace: getwd: %w", err)
	}
	return walkUpForMarkers(dir)
}

// walkUpForMarkers walks up from dir returning the nearest go.work directory; if
// none is found it returns the nearest go.mod directory; if neither exists above
// dir it errors.
func walkUpForMarkers(dir string) (string, error) {
	modRoot := ""
	for {
		if _, err := os.Stat(filepath.Join(dir, goWorkFile)); err == nil {
			return dir, nil
		}
		if modRoot == "" {
			if _, err := os.Stat(filepath.Join(dir, goModFile)); err == nil {
				modRoot = dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if modRoot != "" {
		return modRoot, nil
	}
	return "", fmt.Errorf("workspace: neither %s nor %s found above working directory", goWorkFile, goModFile)
}

// Modules returns the member modules of the workspace (or the single module)
// rooted at root, each paired with the Go module import path from its go.mod.
//
//   - When root/go.work exists: the members are its `use` directives, in `use`
//     order; if root/.gocell/manifest.yaml also exists its modules[].path set
//     MUST be a subset of the `use` dirs (fail-closed on drift). A workspace
//     without a manifest has no metadata modules and skips the cross-check.
//   - When root/go.work does NOT exist: root is a single (external) module; the
//     result is the one module {Dir ".", ImportPath read from root/go.mod}.
func Modules(root string) ([]Module, error) {
	if _, err := os.Stat(filepath.Join(root, goWorkFile)); errors.Is(err, fs.ErrNotExist) {
		importPath, mErr := gomodutil.ReadModulePath(root)
		if mErr != nil {
			return nil, fmt.Errorf("workspace: single-module root: %w", mErr)
		}
		return []Module{{Dir: ".", ImportPath: importPath}}, nil
	} else if err != nil {
		return nil, fmt.Errorf("workspace: stat go.work: %w", err)
	}

	useDirs, err := gomodutil.ReadWorkUseDirs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if len(useDirs) == 0 {
		return nil, fmt.Errorf("workspace: go.work at %s declares no use directives", root)
	}

	mods := make([]Module, 0, len(useDirs))
	useSet := make(map[string]struct{}, len(useDirs))
	for _, dir := range useDirs {
		useSet[dir] = struct{}{}
		importPath, err := gomodutil.ReadModulePath(filepath.Join(root, dir))
		if err != nil {
			return nil, fmt.Errorf("workspace: use %q: %w", dir, err)
		}
		mods = append(mods, Module{Dir: dir, ImportPath: importPath})
	}

	if err := crossCheckManifest(root, useSet); err != nil {
		return nil, err
	}
	return mods, nil
}

// crossCheckManifest enforces manifest.modules ⊆ go.work.use. It is a no-op when
// no manifest exists (no metadata modules to verify).
func crossCheckManifest(root string, useSet map[string]struct{}) error {
	manifestAbs := filepath.Join(root, metadata.DefaultManifestPath)
	if _, err := os.Stat(manifestAbs); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("workspace: stat manifest: %w", err)
	}

	manifestPaths, err := metadata.ReadManifestModulePaths(os.DirFS(root), metadata.DefaultManifestPath)
	if err != nil {
		return fmt.Errorf("workspace: read manifest: %w", err)
	}
	var missing []string
	for _, p := range manifestPaths {
		if _, ok := useSet[filepath.Clean(p)]; !ok {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf(
			"workspace: manifest module(s) %s not declared in go.work `use` "+
				"(manifest.modules must be a subset of go.work.use; add the module to go.work or remove it from .gocell/manifest.yaml)",
			strings.Join(missing, ", "),
		)
	}
	return nil
}
