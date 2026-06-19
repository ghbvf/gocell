package app

import (
	"context"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/tools/codegen/cellmodulemeta"
)

// verifyCodegenCellModuleMetadata implements:
//
//	gocell verify codegen-cellmodule-metadata
//
// It checks the committed cellmodules/.gocell/exported-metadata/ bundle against
// the platform metadata closure: Layer-1 (every desired file present and
// byte-identical, no stale extra file) + Layer-2 (the re-parsed bundle's
// platform closure equals the monorepo's). Any drift is reported on stderr with
// the standard fix hint and a non-zero exit (#1515).
//
// Purely in-process file I/O — no git worktree / sandbox mode.
func verifyCodegenCellModuleMetadata(_ context.Context, _ []string) error {
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	drift, err := cellmodulemeta.Verify(root)
	if err != nil {
		return err
	}
	if len(drift) > 0 {
		for _, d := range drift {
			fmt.Fprintf(os.Stderr, "drift: %s\n", d)
		}
		writeDriftFixHint("cellmodule-metadata")
		return fmt.Errorf(driftErrorTemplate, len(drift), "cellmodule-metadata")
	}
	fmt.Printf("cellmodule-metadata bundle in sync.\n")
	return nil
}
