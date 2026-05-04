package app

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

// CodegenResult is the contract every <X>gen.Result type implements so
// codegen-cmd dispatchers can treat them uniformly. Both cellgen.Result
// and contractgen.Result expose Generated / Drifted as []string fields;
// the methods below are thin accessors that satisfy this interface.
type CodegenResult interface {
	GeneratedFiles() []string
	DriftedFiles() []string
}

// parseProject loads project metadata under root with the canonical error
// wrap used by every codegen sub-command (`metadata parse: %w`).
// Centralized so the wrap message stays consistent across dispatchers and
// is the single place to extend (e.g. with structured logging) later.
func parseProject(root string) (*metadata.ProjectMeta, error) {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return nil, fmt.Errorf("metadata parse: %w", err)
	}
	return project, nil
}

// codegenSpec parameterizes a `gocell generate <kind>` + `gocell verify codegen-<kind>`
// command pair (cell, contract, marker).
type codegenSpec[R CodegenResult] struct {
	// Kind is the noun used in the command (e.g. "cell", "contract").
	Kind string
	// GenerateUsage is the usage string for "gocell generate <kind>".
	// Example: "gocell generate cell <cellID> | --all [--dry-run | --verify]"
	GenerateUsage string
	// AllFlagDesc is the --all flag description.
	// Example: "generate for every cell with goStructName set"
	AllFlagDesc string
	// PluralNoun is the human-readable noun in success messages.
	// Example: "cell scaffolds" / "contract DTOs"
	PluralNoun string
	// SourceArtifacts identifies what the contract is checked against in
	// sandbox-mode error messages, e.g. "cell.yaml/slice.yaml".
	SourceArtifacts string
	// Generate runs the underlying codegen pipeline. dryRun + verify +
	// only are the standard flag set; only is the single-target id (empty
	// means --all).
	Generate func(root string, p *metadata.ProjectMeta, dryRun, verify bool, only string) (R, error)
}

// runCodegenGenerate implements `gocell generate <kind>` for the spec.
// Flag rules (post K#05 W2 DX defaults):
//   - --all defaults to true; no args = run all cells
//   - positional <id> overrides --all (scopes to single target)
//   - --all=false without positional id: error
//   - --dry-run + --verify: mutually exclusive
func runCodegenGenerate[R CodegenResult](spec codegenSpec[R], args []string) error {
	dryRun, verify, only, err := parseCodegenFlags(spec, args)
	if err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	project, err := parseProject(root)
	if err != nil {
		return err
	}
	res, err := spec.Generate(root, project, dryRun, verify, only)
	if err != nil {
		return err
	}
	drift := res.DriftedFiles()
	if verify && len(drift) > 0 {
		for _, f := range drift {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		return fmt.Errorf("codegen drift in %d files; run `gocell generate %s --all` to refresh", len(drift), spec.Kind)
	}
	for _, f := range res.GeneratedFiles() {
		fmt.Printf("Generated: %s\n", f)
	}
	return nil
}

// parseCodegenFlags is the common --all/--dry-run/--verify/<id> parser.
//
// Default behavior (K#05 W2 DX defaults):
//   - --all defaults to true: `gocell generate cell` runs all cells
//   - positional <id> wins over --all default: `gocell generate cell ordercell`
//     scopes to ordercell only (--all is implicitly cleared)
//   - explicit --all=false without a positional id is an error
func parseCodegenFlags[R CodegenResult](spec codegenSpec[R], args []string) (dryRun, verify bool, only string, err error) {
	fs := flag.NewFlagSet("generate "+spec.Kind, flag.ContinueOnError)
	all := fs.Bool("all", true, spec.AllFlagDesc)
	dr := fs.Bool("dry-run", false, "print would-write file paths without writing")
	ver := fs.Bool("verify", false, "diff against disk, exit non-zero on drift, no write")
	if perr := fs.Parse(args); perr != nil {
		return false, false, "", perr
	}
	if *dr && *ver {
		return false, false, "", errors.New("--dry-run (stdout preview) and --verify (CI drift check, no write) are mutually exclusive; pick one")
	}
	pos := fs.Args()
	// Positional id takes priority over --all (including the default true).
	if len(pos) > 0 {
		return *dr, *ver, pos[0], nil
	}
	// No positional id: honor --all flag value.
	if !*all {
		if *dr || *ver {
			return false, false, "", fmt.Errorf("specify a %s id or --all when using --dry-run/--verify", spec.Kind)
		}
		return false, false, "", fmt.Errorf("usage: %s", spec.GenerateUsage)
	}
	return *dr, *ver, "", nil
}

// runCodegenVerify implements `gocell verify codegen-<kind>` (sandbox + --local).
//
// Default behavior (K#05 W2 DX defaults):
//   - --local defaults to true: `gocell verify codegen-<kind>` runs in-place
//   - CI callers that need the ephemeral git worktree must pass --local=false
func runCodegenVerify[R CodegenResult](spec codegenSpec[R], args []string) error {
	fs := flag.NewFlagSet("verify codegen-"+spec.Kind, flag.ContinueOnError)
	local := fs.Bool("local", true,
		"skip git worktree sandbox; verify in-place against current working tree "+
			"(default true; CI should pass --local=false for sandbox mode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	if *local {
		return runCodegenVerifyInPlace(spec, root)
	}
	return runCodegenVerifySandbox(spec, root)
}

func runCodegenVerifyInPlace[R CodegenResult](spec codegenSpec[R], root string) error {
	project, err := parseProject(root)
	if err != nil {
		return err
	}
	res, err := spec.Generate(root, project, false, true, "")
	if err != nil {
		return err
	}
	drift := res.DriftedFiles()
	if len(drift) > 0 {
		for _, f := range drift {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		fmt.Fprintln(os.Stderr, "FIX: run locally and commit:")
		fmt.Fprintf(os.Stderr, "    gocell generate %s --all\n", spec.Kind)
		return fmt.Errorf("codegen drift in %d files; run `gocell generate %s --all` to refresh", len(drift), spec.Kind)
	}
	fmt.Printf("Generated %s OK (--local).\n", spec.PluralNoun)
	return nil
}

func runCodegenVerifySandbox[R CodegenResult](spec codegenSpec[R], root string) error {
	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		project, perr := parseProject(workdir)
		if perr != nil {
			return perr
		}
		_, gerr := spec.Generate(workdir, project, false, false, "")
		return gerr
	})
	if err != nil {
		return fmt.Errorf("verify codegen-%s sandbox: %w", spec.Kind, err)
	}
	if len(res.Drifted) > 0 {
		fmt.Fprintf(os.Stderr, "ERROR: generated %s files are out of sync with %s\n", spec.Kind, spec.SourceArtifacts)
		fmt.Fprintln(os.Stderr, "Drifted files:")
		for _, f := range res.Drifted {
			fmt.Fprintf(os.Stderr, "  %s\n", f)
		}
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Per-file diff (truncated to 200 lines per file):")
		fmt.Fprintln(os.Stderr, res.DiffSummary)
		fmt.Fprintln(os.Stderr, "FIX: run locally and commit:")
		fmt.Fprintf(os.Stderr, "    gocell generate %s --all\n", spec.Kind)
		return fmt.Errorf("codegen drift in %d files", len(res.Drifted))
	}
	fmt.Printf("Generated %s OK.\n", spec.PluralNoun)
	return nil
}
