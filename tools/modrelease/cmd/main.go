// Command modrelease drives the synchronized multi-module release transform for
// the GoCell workspace. With --version it rewrites every publishable library
// module's internal require versions in place (keeping replace); with
// --print-tag-paths it emits the per-module git tags (one per line) for the
// release workflow's tag loop; with --dry-run it previews the set and tags
// without writing. It is invoked by .github/workflows/release.yml.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

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

func run() error {
	version := flag.String("version", "", "release version tag, e.g. v1.2.3 (required)")
	printTags := flag.Bool("print-tag-paths", false, "print per-module git tags (one per line) instead of bumping")
	dryRun := flag.Bool("dry-run", false, "preview the publishable set and tags without writing go.mod files")
	flag.Parse()

	if *version == "" {
		return fmt.Errorf("--version is required (e.g. --version v1.2.3)")
	}
	root, err := workspace.WorkspaceRoot()
	if err != nil {
		return err
	}

	if *printTags {
		// Only --print-tag-paths feeds stdout to a shell `mapfile`; quiet the
		// workspace manifest cross-check's per-member INFO so the release-workflow
		// log isn't flooded. --version / --dry-run keep INFO for operator diagnosis.
		slog.SetLogLoggerLevel(slog.LevelWarn)
		tags, err := modrelease.TagPaths(root, *version)
		if err != nil {
			return err
		}
		for _, t := range tags {
			p("%s\n", t)
		}
		return nil
	}

	if *dryRun {
		return preview(root, *version)
	}

	results, err := modrelease.BumpTree(root, *version)
	if err != nil {
		return err
	}
	bumped := 0
	for _, res := range results {
		if len(res.Requires) > 0 {
			bumped++
			p("bumped %s: %v -> %s\n", res.Dir, res.Requires, *version)
		}
	}
	p("modrelease: bumped internal requires in %d/%d publishable modules to %s\n", bumped, len(results), *version)
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
	p("modrelease --dry-run: %d publishable modules would bump internal requires to %s\n", len(mods), version)
	for _, m := range mods {
		p("  %s (%s)\n", m.Dir, m.ImportPath)
	}
	p("would create %d tags:\n", len(tags))
	for _, t := range tags {
		p("  %s\n", t)
	}
	return nil
}
