package app

import (
	"context"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/tools/codegen/sharedschema"
)

// verifyCodegenSharedSchema implements:
//
//	gocell verify codegen-shared-schema
//
// It performs an in-process byte diff between each canonical schema file
// declared in sharedschema.Mirrors and all declared mirror destinations.
// Drifted or missing mirrors are reported on stderr; on any drift the
// standard fix hint is emitted and the command exits non-zero.
//
// This command has no --local / sandbox mode: the verification is purely
// in-process file I/O and does not need a git worktree.
func verifyCodegenSharedSchema(_ context.Context, args []string) error {
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	drift, err := sharedschema.Verify(root)
	if err != nil {
		return err
	}
	if len(drift) > 0 {
		for _, f := range drift {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		writeDriftFixHint("shared-schema")
		return fmt.Errorf(driftErrorTemplate, len(drift), "shared-schema")
	}
	fmt.Printf("shared-schema mirrors in sync.\n")
	return nil
}
