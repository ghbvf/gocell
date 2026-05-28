package app

import (
	"bufio"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// findRoot walks up from the current working directory to find the directory
// containing go.mod, which is treated as the project root.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found in any parent directory")
		}
		dir = parent
	}
}

// readModule reads the module path from go.mod in the given root directory.
func readModule(root string) (string, error) {
	f, err := os.Open(filepath.Clean(filepath.Join(root, "go.mod")))
	if err != nil {
		return "", fmt.Errorf("open go.mod: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			slog.Warn("close go.mod", slog.String("err", cerr.Error()))
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}

	return "", fmt.Errorf("module directive not found in go.mod")
}

// buildLocatorOptions translates the --layout and --manifest flags into
// LocatorOption values for kernel/metadata.NewParser. Empty flag values mean
// "use defaults" (auto-detect mode + .gocell/manifest.yaml path).
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
