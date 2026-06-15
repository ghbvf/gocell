// Command modrelease drives the synchronized multi-module release transform for
// the GoCell workspace. With --version it pins every workspace member's internal
// require versions in place (the pin set, keeping replace); with
// --print-tag-paths it emits the per-module library git tags (one per line) for
// the release workflow's resume-verification path; with --print-stable-tags it
// emits the FULL stable push set (the bare vX.Y.Z marker first + library tags)
// that the workflow's "Tag modules" step consumes; with --print-installable-tags
// it emits the installable binary git tags; with --installable it strips
// replace+pins internal requires in every installable binary module; with
// --dry-run it previews the pin set, stable tags, and installable tags without
// writing. It is invoked by .github/workflows/release.yml.
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
	printStableTags      bool
	printInstallableTags bool
	installable          bool
	dryRun               bool
}

func parseFlags() (flags, error) {
	version := flag.String("version", "", "release version tag, e.g. v1.2.3 (required)")
	printTags := flag.Bool("print-tag-paths", false,
		"print per-module library git tags (one per line) instead of bumping")
	printStableTags := flag.Bool("print-stable-tags", false,
		"print the full stable push set (bare vX.Y.Z marker first + library tags) for release.yml mapfile")
	printInstallableTags := flag.Bool("print-installable-tags", false,
		"print installable binary git tags (one per line) for release.yml mapfile")
	installable := flag.Bool("installable", false,
		"strip replace+pin internal requires in every installable binary module")
	dryRun := flag.Bool("dry-run", false,
		"preview the pin set, stable tags, and installable tags without writing go.mod files")
	flag.Parse()

	if *version == "" {
		return flags{}, fmt.Errorf("--version is required (e.g. --version v1.2.3)")
	}
	// The mode flags are mutually exclusive — run()'s switch silently honors the
	// first set case, so passing two (e.g. --print-stable-tags --print-tag-paths)
	// would quietly emit the wrong tag set into release.yml's `mapfile`. Reject
	// rather than pick one arbitrarily. (No flag set = the default bump mode.)
	modes := 0
	for _, set := range []bool{*printTags, *printStableTags, *printInstallableTags, *installable, *dryRun} {
		if set {
			modes++
		}
	}
	if modes > 1 {
		return flags{}, fmt.Errorf("--print-tag-paths / --print-stable-tags / --print-installable-tags / " +
			"--installable / --dry-run are mutually exclusive; pass at most one")
	}
	return flags{
		version:              *version,
		printTags:            *printTags,
		printStableTags:      *printStableTags,
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
	case f.printStableTags:
		return printStableTagPaths(root, f.version)
	case f.printInstallableTags:
		return printInstallableTagPaths(root, f.version)
	case f.dryRun:
		return preview(root, f.version)
	case f.installable:
		return stripInstallable(root, f.version)
	default:
		return pinInternalRequires(root, f.version)
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

// printStableTagPaths emits the full stable push set one per line: the bare
// vX.Y.Z marker FIRST, then library tags. Silences INFO logs so the output is
// machine-parseable by release.yml's `mapfile -t tags`.
func printStableTagPaths(root, version string) error {
	slog.SetLogLoggerLevel(slog.LevelWarn)
	tags, err := modrelease.StableTags(root, version)
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

// pinInternalRequires pins internal requires for every workspace member (the pin
// set — a superset of the publishable tag set), so the release `go work sync`
// finds nothing to rewrite (#2212).
func pinInternalRequires(root, version string) error {
	results, err := modrelease.BumpTree(root, version)
	if err != nil {
		return err
	}
	pinned := 0
	for _, res := range results {
		if len(res.Requires) > 0 {
			pinned++
			p("pinned %s: %v -> %s\n", res.Dir, res.Requires, version)
		}
	}
	p("modrelease: pinned internal requires in %d/%d workspace members to %s\n",
		pinned, len(results), version)
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
	// Pin set = every workspace member (a superset of the publishable tag set);
	// --version pins them all so the release `go work sync` is drift-free (#2212).
	pinMods, err := workspace.Modules(root)
	if err != nil {
		return err
	}
	// The fresh stable push set is StableTags (bare marker first + library tags),
	// matching what release.yml's "Tag modules" step consumes via --print-stable-tags
	// — so dry-run previews the marker, not just the library tags.
	stable, err := modrelease.StableTags(root, version)
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
	p("modrelease --dry-run: %d workspace members (pin set) would have internal requires pinned to %s "+
		"(members with no internal require are no-ops):\n", len(pinMods), version)
	for _, m := range pinMods {
		p("  %s (%s)\n", m.Dir, m.ImportPath)
	}
	p("fresh stable release would push %d tags (bare marker first + %d library):\n", len(stable), len(stable)-1)
	for _, t := range stable {
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
