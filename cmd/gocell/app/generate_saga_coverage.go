package app

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/codegen/sagacoveragegen"
)

// generateSagaCoverage implements:
//
//	gocell generate saga-coverage [--dry-run]
//
// It renders the saga fanout artifacts from the saga.Status / journal.EventKind
// const sets (the single source of truth for SAGA-STATUS-FANOUT-COVERAGE-01) and
// writes three byte-locked targets:
//
//   - kernel/saga/sagajournaltest/terminal_coverage_gen.go — the per-terminal
//     coverage struct whose keyless literal in conformance.go is the compile-time
//     exhaustiveness gate.
//   - the saga lifecycle status table region in docs/ops/readyz.md.
//   - the "kind 速查" legend region in docs/ops/alerting-rules.md.
//
// The archtest SAGA-STATUS-FANOUT-COVERAGE-01 byte-compares each committed target
// against a fresh Render(), so a stale target surfaces as a red test.
func generateSagaCoverage(args []string) error {
	fs := flag.NewFlagSet("generate saga-coverage", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print would-write paths without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}

	art, err := sagacoveragegen.Render()
	if err != nil {
		return err
	}

	written, err := writeSagaCoverageArtifacts(realRoot, art, *dryRun)
	if err != nil {
		return err
	}
	for _, p := range written {
		if *dryRun {
			fmt.Printf("would write: %s\n", p)
		} else {
			fmt.Printf("Generated: %s\n", p)
		}
	}
	return nil
}

type sagaCoverageDocTarget struct {
	path       string
	start, end string
	body       string
}

func sagaCoverageDocTargets(realRoot string, art sagacoveragegen.Artifacts) []sagaCoverageDocTarget {
	return []sagaCoverageDocTarget{
		{
			path:  filepath.Join(realRoot, "docs", "ops", "readyz.md"),
			start: sagacoveragegen.ReadyzTableStartMarker,
			end:   sagacoveragegen.ReadyzTableEndMarker,
			body:  art.ReadyzTable,
		},
		{
			path:  filepath.Join(realRoot, "docs", "ops", "alerting-rules.md"),
			start: sagacoveragegen.KindLegendStartMarker,
			end:   sagacoveragegen.KindLegendEndMarker,
			body:  art.KindLegend,
		},
	}
}

// writeSagaCoverageArtifacts writes the gen file (through the codegen funnel) and
// replaces each doc region, returning the paths that were (or would be) written.
func writeSagaCoverageArtifacts(realRoot string, art sagacoveragegen.Artifacts, dryRun bool) ([]string, error) {
	var written []string

	genPath := filepath.Join(realRoot, "kernel", "saga", "sagajournaltest", "terminal_coverage_gen.go")
	res, err := codegen.Write(codegen.WriteOptions{
		Path:     genPath,
		Content:  art.TerminalCoverageGo,
		RepoRoot: realRoot,
		DryRun:   dryRun,
	})
	if err != nil {
		return nil, fmt.Errorf("write %s: %w", genPath, err)
	}
	if res.Action == codegen.ActionWritten || res.Action == codegen.ActionWouldWrite {
		written = append(written, genPath)
	}

	for _, d := range sagaCoverageDocTargets(realRoot, art) {
		changed, werr := writeDocRegion(realRoot, d.path, d.start, d.end, d.body, dryRun)
		if werr != nil {
			return nil, werr
		}
		if changed {
			written = append(written, d.path)
		}
	}
	return written, nil
}

// writeDocRegion replaces the marker-delimited region in the doc at path with
// body, returning whether the file content changed.
func writeDocRegion(realRoot, path, start, end, body string, dryRun bool) (bool, error) {
	existing, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	updated, err := sagacoveragegen.ReplaceRegion(string(existing), start, end, body)
	if err != nil {
		return false, fmt.Errorf("replace region in %s: %w", path, err)
	}
	if updated == string(existing) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := pathsafe.WriteFileForce(realRoot, path, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
