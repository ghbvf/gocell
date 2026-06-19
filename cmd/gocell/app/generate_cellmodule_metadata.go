package app

import (
	"flag"
	"fmt"

	"github.com/ghbvf/gocell/tools/codegen/cellmodulemeta"
)

// generateCellModuleMetadata implements:
//
//	gocell generate cellmodule-metadata --all [--dry-run]
//
// It derives the cellmodules exported-metadata bundle (#1515) — a byte-stable,
// re-parseable copy of the platform metadata closure (corecells cell.yaml +
// slice.yaml + the root contracts those slices reference + shared schemas) —
// into cellmodules/.gocell/exported-metadata/, pruning any stale committed file.
// The bundle ships in the cellmodules module cache so an external Operator-SDK
// consumer's `gocell generate assembly` can read platform metadata without a
// local workspace clone.
//
// The --all flag is required (it is the only mode; the closure is a fixed
// derivation, not a per-target scope). --dry-run prints would-write / would-prune
// paths without touching the tree.
func generateCellModuleMetadata(args []string) error {
	fs := flag.NewFlagSet("generate cellmodule-metadata", flag.ContinueOnError)
	all := fs.Bool("all", false, "generate the full bundle (required)")
	dryRun := fs.Bool("dry-run", false, "print would-write/would-prune paths without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*all {
		return fmt.Errorf("usage: gocell generate cellmodule-metadata --all [--dry-run]")
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	written, err := cellmodulemeta.Generate(root, *dryRun)
	if err != nil {
		return err
	}
	for _, p := range written {
		fmt.Printf("Generated: %s\n", p)
	}
	return nil
}
