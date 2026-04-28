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

// runVerify implements:
//
//	gocell verify slice --id=<cellID/sliceID>
//	gocell verify cell --id=<cellID>
//	gocell verify journey --id=<journeyID>
//	gocell verify journey --active
//	gocell verify targets --files=<file1,file2,...>
//	gocell verify generated [--module=<module>]
func runVerify(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell verify <slice|cell|journey|targets|generated> [flags]")
	}

	subtype := args[0]
	subArgs := args[1:]

	switch subtype {
	case "slice":
		return verifySlice(subArgs)
	case "cell":
		return verifyCell(subArgs)
	case "journey":
		return verifyJourney(subArgs)
	case "targets":
		return verifyTargets(subArgs)
	case "generated":
		return verifyGenerated(subArgs)
	default:
		return fmt.Errorf("unknown verify type: %s (expected slice, cell, journey, targets, or generated)", subtype)
	}
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
func runVerifyResultCmd(args []string, spec verifyResultExec) error {
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
	result, err := spec.exec(context.Background(), runner, *id)
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

func verifySlice(args []string) error {
	return runVerifyResultCmd(args, verifyResultExec{
		name: "slice",
		flag: "id",
		exec: func(ctx context.Context, r *verify.Runner, id string) (*verify.VerifyResult, error) {
			return r.VerifySlice(ctx, id)
		},
	})
}

func verifyCell(args []string) error {
	return runVerifyResultCmd(args, verifyResultExec{
		name: "cell",
		flag: "id",
		exec: func(ctx context.Context, r *verify.Runner, id string) (*verify.VerifyResult, error) {
			return r.VerifyCell(ctx, id)
		},
	})
}

func verifyJourney(args []string) error {
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
		result, err = runner.RunActiveJourneys(context.Background())
	} else {
		result, err = runner.RunJourney(context.Background(), *id)
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

func verifyTargets(args []string) error {
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

func verifyGenerated(args []string) error {
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

	result, err := generatedverify.Verify(root, mod, project)
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
