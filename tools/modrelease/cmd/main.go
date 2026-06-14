// Command modrelease drives the synchronized multi-module release transform for
// the GoCell workspace. With --version it rewrites every publishable library
// module's internal require versions in place (keeping replace); with
// --print-tag-paths it emits the per-module git tags (one per line) for the
// release workflow's tag loop; with --print-installable-tags it emits the
// installable binary git tags; with --installable it strips replace+pins
// internal requires in every installable binary module; with --dry-run it
// previews the set and tags without writing. It is invoked by
// .github/workflows/release.yml.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/tools/modrelease"
	"github.com/ghbvf/gocell/tools/workspace"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "modrelease:", err)
		os.Exit(1)
	}
}

// p writes a line to stdout (the machine-readable output channel, e.g.
// --print-tag-paths). A failed stdout write is unactionable in a CLI, so the
// error is intentionally discarded.
func p(format string, a ...any) { _, _ = fmt.Fprintf(os.Stdout, format, a...) }

// flags holds all parsed CLI flags for modrelease.
type flags struct {
	version              string
	printTags            bool
	printInstallableTags bool
	installable          bool
	dryRun               bool
}

func parseFlags() (flags, error) {
	version := flag.String("version", "", "release version tag, e.g. v1.2.3 (required)")
	printTags := flag.Bool("print-tag-paths", false,
		"print per-module git tags (one per line) instead of bumping")
	printInstallableTags := flag.Bool("print-installable-tags", false,
		"print installable binary git tags (one per line) for release.yml mapfile")
	installable := flag.Bool("installable", false,
		"strip replace+pin internal requires in every installable binary module")
	dryRun := flag.Bool("dry-run", false,
		"preview the publishable set and tags without writing go.mod files")
	flag.Parse()

	if *version == "" {
		return flags{}, fmt.Errorf("--version is required (e.g. --version v1.2.3)")
	}
	return flags{
		version:              *version,
		printTags:            *printTags,
		printInstallableTags: *printInstallableTags,
		installable:          *installable,
		dryRun:               *dryRun,
	}, nil
}

func run() error {
	f, err := parseFlags()
	if err != nil {
		return err
	}
	root, err := workspace.WorkspaceRoot()
	if err != nil {
		return err
	}

	switch {
	case f.printTags:
		return printTagPaths(root, f.version)
	case f.printInstallableTags:
		return printInstallableTagPaths(root, f.version)
	case f.dryRun:
		return preview(root, f.version)
	case f.installable:
		return stripInstallable(root, f.version)
	default:
		return bumpLibraries(root, f.version)
	}
}

// printTagPaths emits library module git tags one per line for shell mapfile.
// Silences INFO logs so the output is machine-parseable without noise.
func printTagPaths(root, version string) error {
	// Only --print-tag-paths feeds stdout to a shell `mapfile`; quiet the
	// workspace manifest cross-check's per-member INFO so the release-workflow
	// log isn't flooded.
	slog.SetLogLoggerLevel(slog.LevelWarn)
	tags, err := modrelease.TagPaths(root, version)
	if err != nil {
		return err
	}
	for _, t := range tags {
		p("%s\n", t)
	}
	return nil
}

// printInstallableTagPaths emits installable binary git tags one per line.
// Silences INFO logs so the output is machine-parseable without noise.
func printInstallableTagPaths(root, version string) error {
	slog.SetLogLoggerLevel(slog.LevelWarn)
	tags, err := modrelease.InstallableTagPaths(root, version)
	if err != nil {
		return err
	}
	for _, t := range tags {
		p("%s\n", t)
	}
	return nil
}

// bumpLibraries rewrites internal requires for all publishable library modules.
func bumpLibraries(root, version string) error {
	results, err := modrelease.BumpTree(root, version)
	if err != nil {
		return err
	}
	bumped := 0
	for _, res := range results {
		if len(res.Requires) > 0 {
			bumped++
			p("bumped %s: %v -> %s\n", res.Dir, res.Requires, version)
		}
	}
	p("modrelease: bumped internal requires in %d/%d publishable modules to %s\n",
		bumped, len(results), version)
	return nil
}

// stripInstallable strips replace directives and pins internal requires for
// every installable binary module, writing the result to disk. The root module
// prefix is read from root/go.mod (never a hardcoded literal).
func stripInstallable(root, version string) error {
	prefix, err := workspace.CorePrefix(root)
	if err != nil {
		return fmt.Errorf("read root module path: %w", err)
	}
	mods, err := modrelease.InstallableBinaries(root)
	if err != nil {
		return err
	}
	for _, m := range mods {
		dir := filepath.Join(root, m.Dir)
		res, err := modrelease.StripReplaceAndPin(dir, prefix, version)
		if err != nil {
			return fmt.Errorf("strip+pin %s: %w", m.Dir, err)
		}
		p("stripped+pinned %s: %v -> %s\n", res.Dir, res.Requires, version)
	}
	return nil
}

func preview(root, version string) error {
	mods, err := modrelease.PublishableModules(root)
	if err != nil {
		return err
	}
	tags, err := modrelease.TagPaths(root, version)
	if err != nil {
		return err
	}
	installable, err := modrelease.InstallableBinaries(root)
	if err != nil {
		return err
	}
	installableTags, err := modrelease.InstallableTagPaths(root, version)
	if err != nil {
		return err
	}
	p("modrelease --dry-run: %d publishable modules would bump internal requires to %s\n",
		len(mods), version)
	for _, m := range mods {
		p("  %s (%s)\n", m.Dir, m.ImportPath)
	}
	p("would create %d library tags:\n", len(tags))
	for _, t := range tags {
		p("  %s\n", t)
	}
	p("%d installable binaries would have replace stripped and internal requires pinned to %s:\n",
		len(installable), version)
	for _, m := range installable {
		p("  %s (%s)\n", m.Dir, m.ImportPath)
	}
	p("would create %d installable tags:\n", len(installableTags))
	for _, t := range installableTags {
		p("  %s\n", t)
	}
	return nil
}
