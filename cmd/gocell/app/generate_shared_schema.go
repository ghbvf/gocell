package app

import (
	"flag"
	"fmt"

	"github.com/ghbvf/gocell/tools/codegen/sharedschema"
)

// generateSharedSchema implements:
//
//	gocell generate shared-schema --all [--dry-run]
//
// It reads each canonical schema file declared in sharedschema.Mirrors and
// writes byte-identical mirror copies to every declared destination root.
// The --all flag is required (it is the only mode; a single-target scope is
// not meaningful for a fixed manifest).  --dry-run prints would-write paths
// without writing.
func generateSharedSchema(args []string) error {
	fs := flag.NewFlagSet("generate shared-schema", flag.ContinueOnError)
	all := fs.Bool("all", false, "generate all declared mirrors (required)")
	dryRun := fs.Bool("dry-run", false, "print would-write paths without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*all {
		return fmt.Errorf("usage: gocell generate shared-schema --all [--dry-run]")
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	written, err := sharedschema.Generate(root, *dryRun)
	if err != nil {
		return err
	}
	for _, p := range written {
		fmt.Printf("Generated: %s\n", p)
	}
	return nil
}
