package app

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/gomodutil"
)

// findRoot walks up from the current working directory to find the nearest
// directory that contains a project root marker.
//
// Project root markers (any one is sufficient):
//   - go.mod — standard Go module root
//   - go.work — Go workspace root
//   - .gocell/manifest.yaml — GoCell manifest-layout workspace root
//
// The search uses nearest-first semantics: a nested go.mod is returned before
// an ancestor go.work, preserving correct behavior for sub-modules within a
// workspace. If multiple markers are present in the same directory any one
// suffices.
//
// When the intended root is ambiguous (e.g. both a go.mod sub-module and a
// go.work workspace are in scope), use --root to specify the root explicitly.
//
// Tests that must scan the OUTER monorepo root (not the nearest module) use the
// repoRoot(t) test helper in graph_test.go, which walks up to go.work instead of
// the nearest go.mod. Since #1557 split cmd/gocell into its own go.work module,
// findRoot() invoked from within cmd/gocell/ resolves cmd/gocell/, not the repo
// root — correct for the runtime CLI (always invoked from the project root), but
// wrong for the self-referential CLI tests that scan the gocell repo itself.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return findRootFrom(dir)
}

// findRootFrom is the testable core of findRoot. It walks up from dir until it
// finds a directory containing go.mod, go.work, or .gocell/manifest.yaml.
func findRootFrom(dir string) (string, error) {
	for {
		if hasRootMarker(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no project root marker (go.mod, go.work, or .gocell/manifest.yaml) found in any parent directory")
		}
		dir = parent
	}
}

// hasRootMarker reports whether dir contains at least one project root marker.
func hasRootMarker(dir string) bool {
	markers := []string{
		"go.mod",
		"go.work",
		filepath.Join(".gocell", "manifest.yaml"),
	}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// readModule reads the module path from go.mod in the given root directory.
// Delegates to gomodutil.ReadModulePath — the single shared parser used by
// codegen, scaffold, the CLI, and archtest.
func readModule(root string) (string, error) {
	return gomodutil.ReadModulePath(root)
}

// buildLocatorOptions translates the --layout and --manifest flags into
// LocatorOption values for kernel/metadata.NewParser. Empty flag values mean
// "use defaults" (auto-detect mode + .gocell/manifest.yaml path).
//
// --layout=conventional and a non-empty --manifest are mutually exclusive:
// conventional mode never reads a manifest file, so the flag would be silently
// ignored. Pass --layout=manifest (or --layout=auto) to use a custom manifest
// path, or omit --manifest when --layout=conventional.
//
// Used by every subcommand that calls metadata.NewParser: validate, check,
// scaffold assembly, generate (assembly / metrics-schema / catalog), verify
// (all codegen variants), and export catalog.
func buildLocatorOptions(layout, manifest string) ([]metadata.LocatorOption, error) {
	var opts []metadata.LocatorOption
	mode, err := metadata.ParseLocatorMode(layout)
	if err != nil {
		return nil, err
	}
	if mode == metadata.LocatorConventional && manifest != "" {
		return nil, fmt.Errorf("--manifest has no effect with --layout=conventional; remove --manifest or use --layout=manifest|auto")
	}
	if mode != metadata.LocatorAuto {
		opts = append(opts, metadata.WithLocatorMode(mode))
	}
	if manifest != "" {
		opts = append(opts, metadata.WithManifestPath(manifest))
	}
	return opts, nil
}

// addLocatorFlags registers --layout and --manifest on fs and returns
// pointers to their string values. Shared by all subcommands that call
// metadata.NewParser: validate, check (all variants), scaffold assembly,
// generate (assembly / metrics-schema / catalog), verify (codegen variants),
// and export catalog.
func addLocatorFlags(fs *flag.FlagSet) (layout, manifestPath *string) {
	layout = fs.String("layout", "",
		"locator mode: auto (default; empty also resolves to auto) | conventional | manifest. "+
			"auto probes <root>/.gocell/manifest.yaml; manifest forces the manifest path.")
	manifestPath = fs.String("manifest", "",
		"explicit manifest file path (default: <root>/.gocell/manifest.yaml). "+
			"Only used when --layout=manifest or when auto-detect selects manifest mode (.gocell/manifest.yaml present); "+
			"has no effect otherwise.")
	return layout, manifestPath
}
