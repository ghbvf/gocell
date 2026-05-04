package app

import (
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

// verifyCodegenContract implements `gocell verify codegen-contract`. Two modes:
//
//	gocell verify codegen-contract           default: git worktree sandbox
//	                                         (clones HEAD into tmp, regenerates, diffs)
//	gocell verify codegen-contract --local   fast path: in-place re-render with
//	                                         drift detection, no worktree (no isolation)
//
// CI uses the sandbox mode for hermetic isolation; local development can use
// --local for ~10× speed at the cost of touching the working tree only on
// drift (no actual write — Verify mode in contractgen suppresses writes).
func verifyCodegenContract(args []string) error {
	fs := flag.NewFlagSet("verify codegen-contract", flag.ContinueOnError)
	local := fs.Bool("local", false, "skip git worktree sandbox; verify in-place against current working tree")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	if *local {
		return verifyCodegenContractInPlace(root)
	}
	return verifyCodegenContractSandbox(root)
}

// verifyCodegenContractInPlace runs contractgen.Generate against the live working
// tree in Verify mode. Detects drift without writing.
func verifyCodegenContractInPlace(root string) error {
	res, err := generateAllContractsInVerifyMode(root)
	if err != nil {
		return err
	}
	if len(res.Drifted) > 0 {
		for _, f := range res.Drifted {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		fmt.Fprintln(os.Stderr, "FIX: run locally and commit:")
		fmt.Fprintln(os.Stderr, "    gocell generate contract --all")
		return fmt.Errorf("codegen drift in %d files; run `gocell generate contract --all` to refresh", len(res.Drifted))
	}
	fmt.Println("Generated contract DTOs OK (--local).")
	return nil
}

// verifyCodegenContractSandbox runs contractgen.Generate inside an ephemeral git
// worktree detached at HEAD, then reports any diff as drift. Mirrors the
// kubernetes/kubernetes hack/lib/verify-generated.sh pattern.
func verifyCodegenContractSandbox(root string) error {
	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		_, genErr := generateAllContracts(workdir, false)
		return genErr
	})
	if err != nil {
		return fmt.Errorf("verify codegen-contract sandbox: %w", err)
	}
	if len(res.Drifted) > 0 {
		fmt.Fprintln(os.Stderr, "ERROR: generated contract files are out of sync with contract.yaml / schema files")
		fmt.Fprintln(os.Stderr, "Drifted files:")
		for _, f := range res.Drifted {
			fmt.Fprintf(os.Stderr, "  %s\n", f)
		}
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Per-file diff (truncated to 200 lines per file):")
		fmt.Fprintln(os.Stderr, res.DiffSummary)
		fmt.Fprintln(os.Stderr, "FIX: run locally and commit:")
		fmt.Fprintln(os.Stderr, "    gocell generate contract --all")
		return fmt.Errorf("codegen drift in %d files", len(res.Drifted))
	}
	fmt.Println("Generated contract DTOs OK.")
	return nil
}

// generateAllContractsInVerifyMode is the Verify=true codepath shared by
// --local and the in-sandbox generate.
func generateAllContractsInVerifyMode(root string) (contractgen.Result, error) {
	return generateAllContracts(root, true)
}

// generateAllContracts loads metadata under root and runs contractgen.Generate
// against every opted-in (Codegen=true) contract. verify=true returns drift
// list without writing; verify=false performs the writes (used inside the
// worktree sandbox).
func generateAllContracts(root string, verify bool) (contractgen.Result, error) {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return contractgen.Result{}, fmt.Errorf("metadata parse: %w", err)
	}
	return contractgen.Generate(root, project, contractgen.Options{Verify: verify})
}
