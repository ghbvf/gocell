package app

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

// fixHintHeader and fixHintCommand format the standard drift-fix instructions
// emitted on stderr by every verify path. Centralized so the wording stays
// consistent across runCodegenGenerate / runCodegenVerifyInPlace /
// runCodegenVerifySandbox and Sonar's duplicate-literal smell stays quiet.
const (
	fixHintHeader      = "FIX: run locally and commit:"
	fixHintCommandLine = "    gocell generate %s --all\n"
	driftErrorTemplate = "codegen drift in %d files; run `gocell generate %s --all` to refresh"
)

// writeDriftFixHint emits the standard two-line "FIX" stderr block for the
// given codegen kind (e.g. "cell", "contract"). Callers should invoke this
// before returning a drift error so users see the same actionable hint
// regardless of which verify entry point detected the drift.
func writeDriftFixHint(kind string) {
	fmt.Fprintln(os.Stderr, fixHintHeader)
	fmt.Fprintf(os.Stderr, fixHintCommandLine, kind)
}

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
func parseProject(root string, opts ...metadata.LocatorOption) (*metadata.ProjectMeta, error) {
	project, err := metadata.NewParser(root, opts...).Parse()
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
	// means --all). modulePath is the consuming repo's Go module path
	// (resolved flag-or-go.mod by the caller), threaded into the generator so
	// the formatter groups module-local imports for the target repo (#1083).
	Generate func(root string, p *metadata.ProjectMeta, dryRun, verify bool, only, modulePath string) (R, error)
}

// runCodegenGenerate implements `gocell generate <kind>` for the spec.
// Flag rules (post K#05 W2 DX defaults):
//   - --all defaults to true; no args = run all cells
//   - positional <id> overrides --all (scopes to single target)
//   - --all=false without positional id: error
//   - --dry-run + --verify: mutually exclusive
func runCodegenGenerate[R CodegenResult](spec codegenSpec[R], args []string) error {
	dryRun, verify, only, modulePathFlag, locatorOpts, err := parseCodegenFlags(spec, args)
	if err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	modulePath, err := resolveModule(root, modulePathFlag)
	if err != nil {
		return err
	}
	project, err := parseProject(root, locatorOpts...)
	if err != nil {
		return err
	}
	if err := metadata.ValidateProjectHTTPAuthModes(project); err != nil {
		return err
	}
	res, err := spec.Generate(root, project, dryRun, verify, only, modulePath)
	if err != nil {
		return err
	}
	drift := res.DriftedFiles()
	if verify && len(drift) > 0 {
		for _, f := range drift {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		writeDriftFixHint(spec.Kind)
		return fmt.Errorf(driftErrorTemplate, len(drift), spec.Kind)
	}
	for _, f := range res.GeneratedFiles() {
		fmt.Printf("Generated: %s\n", f)
	}
	return nil
}

// parseCodegenFlags is the common --all/--dry-run/--verify/<id>/--layout/--manifest parser.
//
// Default behavior (K#05 W2 DX defaults):
//   - --all defaults to true: `gocell generate cell` runs all cells
//   - positional <id> wins over --all default: `gocell generate cell ordercell`
//     scopes to ordercell only (--all is implicitly cleared)
//   - explicit --all=false without a positional id is an error
func parseCodegenFlags[R CodegenResult](
	spec codegenSpec[R], args []string,
) (dryRun, verify bool, only, modulePath string, locatorOpts []metadata.LocatorOption, err error) {
	fs := flag.NewFlagSet("generate "+spec.Kind, flag.ContinueOnError)
	all := fs.Bool("all", true, spec.AllFlagDesc)
	dr := fs.Bool("dry-run", false, "print would-write file paths without writing")
	ver := fs.Bool("verify", false, "diff against disk, exit non-zero on drift, no write")
	mp := fs.String("module-path", "",
		"consuming repo's Go module path (e.g. github.com/acme/svc); drives local "+
			"import grouping in generated files. Default: read from go.mod")
	layout, manifestPath := addLocatorFlags(fs)
	if perr := fs.Parse(args); perr != nil {
		return false, false, "", "", nil, perr
	}
	const dryVerMutexMsg = "--dry-run (stdout preview) and --verify " +
		"(CI drift check, no write) are mutually exclusive; pick one"
	if *dr && *ver {
		return false, false, "", "", nil, errors.New(dryVerMutexMsg)
	}
	opts, lerr := buildLocatorOptions(*layout, *manifestPath)
	if lerr != nil {
		return false, false, "", "", nil, fmt.Errorf("generate %s: %w", spec.Kind, lerr)
	}
	pos := fs.Args()
	// Reject more than one positional id to avoid silent arg-drop surprises.
	if len(pos) > 1 {
		return false, false, "", "", nil, fmt.Errorf("only one %s id allowed; got: %v", spec.Kind, pos)
	}
	// Positional id takes priority over --all (including the default true).
	if len(pos) == 1 {
		return *dr, *ver, pos[0], *mp, opts, nil
	}
	// No positional id: honor --all flag value.
	if !*all {
		if *dr || *ver {
			return false, false, "", "", nil, fmt.Errorf("specify a %s id or --all when using --dry-run/--verify", spec.Kind)
		}
		return false, false, "", "", nil, fmt.Errorf("usage: %s", spec.GenerateUsage)
	}
	return *dr, *ver, "", *mp, opts, nil
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
	mp := fs.String("module-path", "",
		"consuming repo's Go module path (e.g. github.com/acme/svc); must match the "+
			"--module-path used to generate, else the diff reports spurious drift. "+
			"Default: read from go.mod")
	layout, manifestPath := addLocatorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	locatorOpts, err := buildLocatorOptions(*layout, *manifestPath)
	if err != nil {
		return fmt.Errorf("verify codegen-%s: %w", spec.Kind, err)
	}
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	if *local {
		return runCodegenVerifyInPlace(spec, root, *mp, locatorOpts...)
	}
	return runCodegenVerifySandbox(spec, root, *mp)
}

func runCodegenVerifyInPlace[R CodegenResult](
	spec codegenSpec[R], root, modulePathFlag string, locatorOpts ...metadata.LocatorOption,
) error {
	modulePath, err := resolveModule(root, modulePathFlag)
	if err != nil {
		return err
	}
	project, err := parseProject(root, locatorOpts...)
	if err != nil {
		return err
	}
	if err := metadata.ValidateProjectHTTPAuthModes(project); err != nil {
		return err
	}
	res, err := spec.Generate(root, project, false, true, "", modulePath)
	if err != nil {
		return err
	}
	drift := res.DriftedFiles()
	if len(drift) > 0 {
		for _, f := range drift {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		writeDriftFixHint(spec.Kind)
		return fmt.Errorf(driftErrorTemplate, len(drift), spec.Kind)
	}
	fmt.Printf("Generated %s OK (--local).\n", spec.PluralNoun)
	return nil
}

func runCodegenVerifySandbox[R CodegenResult](spec codegenSpec[R], root, modulePathFlag string) error {
	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		modulePath, merr := resolveModule(workdir, modulePathFlag)
		if merr != nil {
			return merr
		}
		project, perr := parseProject(workdir)
		if perr != nil {
			return perr
		}
		if verr := metadata.ValidateProjectHTTPAuthModes(project); verr != nil {
			return verr
		}
		_, gerr := spec.Generate(workdir, project, false, false, "", modulePath)
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
		writeDriftFixHint(spec.Kind)
		return fmt.Errorf("codegen drift in %d files", len(res.Drifted))
	}
	fmt.Printf("Generated %s OK.\n", spec.PluralNoun)
	return nil
}
