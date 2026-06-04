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
// (cells/contracts/journeys); [Modules] cross-checks the two in BOTH directions
// and fails closed on drift:
//
//   - forward — manifest.modules ⊆ go.work.use: a metadata module the toolchain
//     does not compile is a configuration bug.
//   - reverse — every go.work member that carries GoCell metadata must appear in
//     the manifest: an undeclared metadata-bearing member is compiled but
//     invisible to metadata tooling (a silent coverage hole). A pure-tool /
//     pure-library member with no metadata may stay out of the manifest.
//
// A workspace without a manifest has no metadata-module contract to cross-check
// (the Go scan still derives from go.work).
//
// Allowed import surface: this package imports only stdlib, kernel/metadata
// (manifest cross-check), and tools/gomodutil; it must not import
// golang.org/x/tools, cells/, runtime/, or adapters/.
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

// crossCheckManifest enforces the bidirectional go.work ↔ manifest closure. It
// is a no-op when no manifest exists (no metadata-module contract to verify).
//
//	forward:  manifest.modules ⊆ go.work.use   — a metadata module the toolchain
//	          does not compile is a drift bug.
//	reverse:  every go.work member that CARRIES GoCell metadata MUST be declared
//	          in the manifest — an undeclared metadata-bearing member is invisible
//	          to all metadata tooling (governance/codegen/catalog), the other half
//	          of the closure (#1555 review F5).
func crossCheckManifest(root string, useSet map[string]struct{}) error {
	manifestAbs := filepath.Join(root, metadata.DefaultManifestPath)
	if _, err := os.Stat(manifestAbs); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("workspace: stat manifest: %w", err)
	}

	// Root-confined read: os.Root rejects a symlinked .gocell/manifest.yaml (or
	// symlinked module dir) that escapes root, so the manifest contract's source
	// cannot leave the workspace (#1592 review F1). The os.Stat above only gates
	// the no-manifest skip; this is the protected read.
	manifestPaths, err := metadata.ReadManifestModulePathsRoot(root, metadata.DefaultManifestPath)
	if err != nil {
		return fmt.Errorf("workspace: read manifest: %w", err)
	}
	// Forward: manifest.modules ⊆ go.work.use.
	manifestSet := make(map[string]struct{}, len(manifestPaths))
	var missing []string
	for _, p := range manifestPaths {
		clean := filepath.Clean(p)
		manifestSet[clean] = struct{}{}
		if _, ok := useSet[clean]; !ok {
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
	// Reverse: no go.work member may carry GoCell metadata without being declared.
	return checkUndeclaredMetadataMembers(root, useSet, manifestSet)
}

// checkUndeclaredMetadataMembers fails closed when a go.work member that is NOT
// declared in .gocell/manifest.yaml carries GoCell metadata (cell.yaml /
// slice.yaml / contract.yaml / J-*.yaml / assembly.yaml / actors.yaml /
// status-board.yaml). Such a member is compiled by the toolchain but invisible
// to every metadata-driven tool (governance validate, codegen, catalog) — the
// reverse of the manifest ⊆ go.work check. A pure-tool / pure-library member
// with no metadata is legitimately allowed to stay out of the manifest.
func checkUndeclaredMetadataMembers(root string, useSet, manifestSet map[string]struct{}) error {
	var offenders []string
	for dir := range useSet {
		if _, declared := manifestSet[dir]; declared {
			continue
		}
		has, err := memberHasMetadata(root, dir)
		if err != nil {
			return err
		}
		if has {
			offenders = append(offenders, dir)
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf(
		"workspace: go.work member(s) %s carry GoCell metadata but are not declared in "+
			".gocell/manifest.yaml (metadata tooling would never discover them; add each to manifest.modules)",
		strings.Join(offenders, ", "),
	)
}

// memberHasMetadata reports whether the workspace member at root/dir contains
// any GoCell metadata file at the conventional layout. It uses the root-confined
// disk Locator (NewLocator → os.OpenRoot) so the scan cannot follow a symlink
// escaping the member, keeping every disk-backed metadata read in the workspace
// boundary on the same os.Root primitive (#1592 review F1); the conventional
// Locator's WalkDir additionally skips symlink entries and is match-capped.
func memberHasMetadata(root, dir string) (bool, error) {
	memberRoot := filepath.Join(root, dir)
	loc, err := metadata.NewLocator(memberRoot, metadata.WithLocatorMode(metadata.LocatorConventional))
	if err != nil {
		return false, fmt.Errorf("workspace: locate metadata in member %q: %w", dir, err)
	}
	defer func() { _ = loc.Close() }()
	srcs, err := loc.Discover()
	if err != nil {
		return false, fmt.Errorf("workspace: scan metadata in member %q: %w", dir, err)
	}
	return len(srcs) > 0, nil
}
