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

// buildLocatorOptions translates the validate/check --layout and --manifest
// flags into LocatorOption values for kernel/metadata.NewParser. Empty flag
// values mean "use defaults" (auto-detect mode + .gocell/manifest.yaml path).
//
// CLI flag wiring is currently restricted to `gocell validate` and `gocell check`
// (M1 #1082 scope). Other subcommands (generate / verify / export / scaffold /
// codegen) inherit auto-detect behavior transparently: if .gocell/manifest.yaml
// exists at root, manifest mode kicks in without a flag.
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
// pointers to their string values. Used by `gocell validate` and the five
// `gocell check ...` subcommands to share a single flag schema (M1 #1082).
func addLocatorFlags(fs *flag.FlagSet) (layout, manifestPath *string) {
	layout = fs.String("layout", "",
		"locator mode: empty=auto (default) | conventional | manifest. "+
			"auto detects .gocell/manifest.yaml at root.")
	manifestPath = fs.String("manifest", "",
		"explicit manifest path (default: <root>/.gocell/manifest.yaml). "+
			"Only consulted when manifest mode applies.")
	return layout, manifestPath
}
