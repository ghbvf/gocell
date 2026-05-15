package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/verify"
	"github.com/ghbvf/gocell/tools/generatedverify"
)

// verifySubcommands is the single source of truth for `gocell verify`
// (see subcommand.go / CLI-UNIMPL-HIDE-01). slice/cell/journey/generated
// thread ctx into kernel/verify.Runner / generatedverify.Verify, whose
// go test subprocesses honor cancellation (pkg/cmdrun process-group
// kill); targets and the codegen-* sandbox checks have no cancelable
// downstream and discard ctx.
var verifySubcommands = []subcommand[func(ctx context.Context, args []string) error]{
	{
		name: "slice",
		help: []string{
			"Run verify.unit + verify.contract for a slice.",
			"--id=<cellID/sliceID> [--format text|json|sarif]",
		},
		run: verifySlice,
	},
	{
		name: "cell",
		help: []string{
			"Run verify.smoke + per-slice checks for a cell.",
			"--id=<cellID> [--format text|json|sarif]",
		},
		run: verifyCell,
	},
	{
		name: "journey",
		help: []string{
			"Run a single journey or every active journey.",
			"--id=<journeyID> | --active [--format text|json|sarif]",
		},
		run: verifyJourney,
	},
	{
		name: "targets",
		help: []string{
			"List slices/cells/contracts/journeys reachable from",
			"the given files. --files=<file1,file2,...>",
		},
		run: verifyTargets,
	},
	{
		name: "generated",
		help: []string{
			"Verify assembly entrypoints, boundary.yaml, and",
			"metrics-schema.yaml against metadata-derived",
			"expectations and HEAD. Fails on stale, staged-only,",
			"or unexpected committed artifacts. [--module=<module>]",
		},
		run: verifyGenerated,
	},
	{
		name: "codegen-cell",
		help: []string{
			"Verify cell_gen.go / slice_gen.go are in sync with",
			"cell.yaml / slice.yaml. Default: --local in-place verify",
			"(fast, no sandbox). CI: pass --local=false to use the",
			"K8s-style git worktree sandbox mode.",
		},
		run: verifyCodegenCell,
	},
	{
		name: "codegen-contract",
		help: []string{
			"Verify generated/contracts/**/*_gen.go are in sync with",
			"contract.yaml / schema files. Default: --local in-place",
			"verify (fast, no sandbox). CI: pass --local=false for",
			"git worktree sandbox mode.",
		},
		run: verifyCodegenContract,
	},
	{
		name: "codegen-assembly",
		help: []string{
			"Verify cmd/*/modules_gen.go are in sync with assembly.yaml /",
			"cell.yaml goStructName. Default --local in-place verify (fast).",
			"CI: pass --local=false for git worktree sandbox.",
		},
		run: runVerifyCodegenAssembly,
	},
}

// runVerify dispatches `gocell verify <type>` through the
// verifySubcommands registry. ctx is the signal-aware context from
// main.go; it reaches the go test subprocesses run by kernel/verify.
func runVerify(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell verify <%s> [flags]",
			strings.Join(subNames(verifySubcommands), "|"))
	}
	if isHelpFlag(args[0]) {
		return renderSubHelp("verify", verifySubcommands)
	}
	run, ok := findSub(verifySubcommands, args[0])
	if !ok {
		return fmt.Errorf("unknown verify type: %s (expected %s)",
			args[0], strings.Join(subNames(verifySubcommands), ", "))
	}
	return run(ctx, args[1:])
}

// verifyResultExec captures the per-subcommand differences (flag name +
// runner method) so runVerifyResultCmd can share the rest of the wiring.
type verifyResultExec struct {
	name string // "slice" / "cell" / "journey"
	flag string // "id"
	exec func(ctx context.Context, runner *verify.Runner, id string) (*verify.VerifyResult, error)
}

// runVerifyResultCmd parses --id + --format, validates the printer choice
// up front (so an unsupported format is rejected before any metadata
// parse or test execution), builds the runner, executes `exec`, then
// renders the VerifyResult. Used by verify slice / cell / journey —
// verify targets has a different output shape (AffectedTargets) and
// stays separate.
func runVerifyResultCmd(ctx context.Context, args []string, spec verifyResultExec) error {
	fs := flag.NewFlagSet("verify "+spec.name, flag.ContinueOnError)
	id := fs.String(spec.flag, "", "<required>")
	format := fs.String("format", "text",
		"output format: "+strings.Join(printers.SupportedVerifyFormats(), " | "))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--%s is required", spec.flag)
	}

	// Format validation happens before metadata parse / test execution
	// so a misconfigured CI invocation fails in milliseconds with a
	// clear message, rather than running the whole verify suite and
	// then refusing to render the result.
	printer, err := printers.NewVerifyPrinter(*format, os.Stdout)
	if err != nil {
		return err
	}

	root, project, err := parseProjectMeta()
	if err != nil {
		return err
	}

	runner := verify.NewRunner(project, root)
	result, err := spec.exec(ctx, runner, *id)
	if err != nil {
		return fmt.Errorf("verify %s: %w", spec.name, err)
	}

	if err := printer.Print(result); err != nil {
		return fmt.Errorf("emit verify result: %w", err)
	}
	if !result.Passed {
		return fmt.Errorf("verify %s %s: FAILED", spec.name, *id)
	}
	return nil
}

func verifySlice(ctx context.Context, args []string) error {
	return runVerifyResultCmd(ctx, args, verifyResultExec{
		name: "slice",
		flag: "id",
		exec: func(ctx context.Context, r *verify.Runner, id string) (*verify.VerifyResult, error) {
			return r.VerifySlice(ctx, id)
		},
	})
}

func verifyCell(ctx context.Context, args []string) error {
	return runVerifyResultCmd(ctx, args, verifyResultExec{
		name: "cell",
		flag: "id",
		exec: func(ctx context.Context, r *verify.Runner, id string) (*verify.VerifyResult, error) {
			return r.VerifyCell(ctx, id)
		},
	})
}

func verifyJourney(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify journey", flag.ContinueOnError)
	id := fs.String("id", "", "journey id")
	active := fs.Bool("active", false, "run all active journeys")
	format := fs.String("format", "text",
		"output format: "+strings.Join(printers.SupportedVerifyFormats(), " | "))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" && !*active {
		return fmt.Errorf("exactly one of --id or --active is required")
	}
	if *id != "" && *active {
		return fmt.Errorf("exactly one of --id or --active is required")
	}

	printer, err := printers.NewVerifyPrinter(*format, os.Stdout)
	if err != nil {
		return err
	}
	root, project, err := parseProjectMeta()
	if err != nil {
		return err
	}

	runner := verify.NewRunner(project, root)
	var result *verify.VerifyResult
	if *active {
		result, err = runner.RunActiveJourneys(ctx)
	} else {
		result, err = runner.RunJourney(ctx, *id)
	}
	if err != nil {
		return fmt.Errorf("verify journey: %w", err)
	}

	if err := printer.Print(result); err != nil {
		return fmt.Errorf("emit verify result: %w", err)
	}
	if !result.Passed {
		target := *id
		if *active {
			target = "--active"
		}
		return fmt.Errorf("verify journey %s: FAILED", target)
	}
	return nil
}

// verifyTargets has no cancelable downstream (pure metadata selection);
// ctx is part of the uniform verifySubcommands handler signature.
func verifyTargets(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("verify targets", flag.ContinueOnError)
	files := fs.String("files", "", "comma-separated file paths (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *files == "" {
		return fmt.Errorf("--files is required")
	}

	_, project, err := parseProjectMeta()
	if err != nil {
		return err
	}

	fileList := strings.Split(*files, ",")
	for i := range fileList {
		fileList[i] = strings.TrimSpace(fileList[i])
	}

	selector := governance.NewTargetSelector(project)
	targets := selector.SelectFromFiles(fileList)

	fmt.Println("Affected targets:")
	printTargetList("  Slices", targets.Slices)
	printTargetList("  Cells", targets.Cells)
	printTargetList("  Contracts", targets.Contracts)
	printTargetList("  Journeys", targets.Journeys)

	total := len(targets.Slices) + len(targets.Cells) + len(targets.Contracts) + len(targets.Journeys)
	if total == 0 {
		fmt.Println("  (none)")
	}

	return nil
}

func verifyGenerated(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify generated", flag.ContinueOnError)
	module := fs.String("module", "", "Go module path (default: read from go.mod)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, project, err := parseProjectMeta()
	if err != nil {
		return err
	}
	mod := *module
	if mod == "" {
		mod, err = readModule(root)
		if err != nil {
			return fmt.Errorf("cannot read module from go.mod: %w", err)
		}
	}

	result, err := generatedverify.Verify(ctx, root, mod, project)
	if err != nil {
		return fmt.Errorf("verify generated: %w", err)
	}
	if !result.Passed() {
		for _, drift := range result.Drifts {
			fmt.Fprintf(os.Stderr, "generated artifact drift: %s (%s, assembly %s): %s\n",
				drift.Path, drift.Kind, drift.AssemblyID, drift.Message)
		}
		return fmt.Errorf("verify generated: %d drift(s); run 'make generate' and commit the result",
			len(result.Drifts))
	}
	fmt.Printf("Generated artifacts verified: %d files\n", len(result.Artifacts))
	return nil
}

// parseProjectMeta finds the project root, parses metadata, and returns both.
func parseProjectMeta() (root string, project *metadata.ProjectMeta, err error) {
	root, err = findRoot()
	if err != nil {
		return "", nil, fmt.Errorf("cannot find project root: %w", err)
	}

	parser := metadata.NewParser(root)
	project, err = parser.Parse()
	if err != nil {
		return "", nil, fmt.Errorf("metadata parse: %w", err)
	}
	return root, project, nil
}

func printTargetList(label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Printf("%s:\n", label)
	for _, item := range items {
		fmt.Printf("    - %s\n", item)
	}
}
