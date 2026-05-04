package app

import (
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

// verifyCodegenCell implements `gocell verify codegen-cell`. Two modes:
//
//	gocell verify codegen-cell           default: K8s-style git worktree sandbox
//	                                     (clones HEAD into tmp, regenerates, diffs)
//	gocell verify codegen-cell --local   fast path: in-place re-render with
//	                                     drift detection, no worktree (no isolation)
//
// CI uses the sandbox mode for hermetic isolation; local development can use
// --local for ~10× speed at the cost of touching the working tree only on
// drift (no actual write — Verify mode in cellgen suppresses writes).
//
// Replaces the inline bash logic that previously lived in
// hack/verify-codegen-cell.sh; the script now delegates to this subcommand.
func verifyCodegenCell(args []string) error {
	fs := flag.NewFlagSet("verify codegen-cell", flag.ContinueOnError)
	local := fs.Bool("local", false, "skip git worktree sandbox; verify in-place against current working tree")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	if *local {
		return verifyCodegenCellInPlace(root)
	}
	return verifyCodegenCellSandbox(root)
}

// verifyCodegenCellInPlace runs cellgen.Generate against the live working tree
// in Verify mode. Detects drift without writing.
func verifyCodegenCellInPlace(root string) error {
	res, err := generateAllInVerifyMode(root)
	if err != nil {
		return err
	}
	if len(res.Drifted) > 0 {
		for _, f := range res.Drifted {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		return fmt.Errorf("codegen drift in %d files; run `gocell generate cell --all` to refresh", len(res.Drifted))
	}
	fmt.Println("Generated cell scaffolds OK (--local).")
	return nil
}

// verifyCodegenCellSandbox runs cellgen.Generate inside an ephemeral git
// worktree detached at HEAD, then reports any diff as drift. Mirrors the
// kubernetes/kubernetes hack/lib/verify-generated.sh pattern.
func verifyCodegenCellSandbox(root string) error {
	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		_, genErr := generateAll(workdir, false)
		return genErr
	})
	if err != nil {
		return fmt.Errorf("verify codegen-cell sandbox: %w", err)
	}
	if len(res.Drifted) > 0 {
		fmt.Fprintln(os.Stderr, "ERROR: generated cell files are out of sync with cell.yaml/slice.yaml")
		fmt.Fprintln(os.Stderr, "Drifted files:")
		for _, f := range res.Drifted {
			fmt.Fprintf(os.Stderr, "  %s\n", f)
		}
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Per-file diff (truncated to 200 lines per file):")
		fmt.Fprintln(os.Stderr, res.DiffSummary)
		fmt.Fprintln(os.Stderr, "FIX: run locally and commit:")
		fmt.Fprintln(os.Stderr, "    gocell generate cell --all")
		return fmt.Errorf("codegen drift in %d files", len(res.Drifted))
	}
	fmt.Println("Generated cell scaffolds OK.")
	return nil
}

// generateAllInVerifyMode is the Verify=true codepath shared by --local and
// the in-sandbox generate.
func generateAllInVerifyMode(root string) (cellgen.Result, error) {
	return generateAll(root, true)
}

// generateAll loads metadata under root and runs cellgen.Generate against
// every opted-in cell. verify=true returns drift list without writing;
// verify=false performs the writes (used inside the worktree sandbox).
func generateAll(root string, verify bool) (cellgen.Result, error) {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return cellgen.Result{}, fmt.Errorf("metadata parse: %w", err)
	}
	return cellgen.Generate(root, project, cellgen.Options{Verify: verify})
}
