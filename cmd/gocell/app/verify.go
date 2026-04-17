package app

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/verify"
)

// runVerify implements:
//
//	gocell verify slice --id=<cellID/sliceID>
//	gocell verify cell --id=<cellID>
//	gocell verify journey --id=<journeyID>
//	gocell verify targets --files=<file1,file2,...>
func runVerify(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell verify <slice|cell|journey|targets> [flags]")
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
	default:
		return fmt.Errorf("unknown verify type: %s (expected slice, cell, journey, or targets)", subtype)
	}
}

func verifySlice(args []string) error {
	fs := flag.NewFlagSet("verify slice", flag.ContinueOnError)
	id := fs.String("id", "", "slice ID in cellID/sliceID format (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}

	root, project, err := parseProjectMeta()
	if err != nil {
		return err
	}

	runner := verify.NewRunner(project, root)
	result, err := runner.VerifySlice(context.Background(), *id)
	if err != nil {
		return fmt.Errorf("verify slice: %w", err)
	}

	printVerifyResult(result)
	if !result.Passed {
		return fmt.Errorf("verify slice %s: FAILED", *id)
	}
	return nil
}

func verifyCell(args []string) error {
	fs := flag.NewFlagSet("verify cell", flag.ContinueOnError)
	id := fs.String("id", "", "cell ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}

	root, project, err := parseProjectMeta()
	if err != nil {
		return err
	}

	runner := verify.NewRunner(project, root)
	result, err := runner.VerifyCell(context.Background(), *id)
	if err != nil {
		return fmt.Errorf("verify cell: %w", err)
	}

	printVerifyResult(result)
	if !result.Passed {
		return fmt.Errorf("verify cell %s: FAILED", *id)
	}
	return nil
}

func verifyJourney(args []string) error {
	fs := flag.NewFlagSet("verify journey", flag.ContinueOnError)
	id := fs.String("id", "", "journey ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}

	root, project, err := parseProjectMeta()
	if err != nil {
		return err
	}

	runner := verify.NewRunner(project, root)
	result, err := runner.RunJourney(context.Background(), *id)
	if err != nil {
		return fmt.Errorf("verify journey: %w", err)
	}

	printVerifyResult(result)
	if !result.Passed {
		return fmt.Errorf("verify journey %s: FAILED", *id)
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

// printVerifyResult prints a VerifyResult to stdout.
func printVerifyResult(r *verify.VerifyResult) {
	status := "PASSED"
	if !r.Passed {
		status = "FAILED"
	}
	fmt.Printf("Verify %s: %s\n", r.TargetID, status)
	for _, tr := range r.Results {
		marker := "PASS"
		if !tr.Passed {
			marker = "FAIL"
		}
		fmt.Printf("  [%s] %s\n", marker, tr.Name)
		if tr.Output != "" {
			for _, line := range strings.Split(strings.TrimRight(tr.Output, "\n"), "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}
	for _, e := range r.Errors {
		fmt.Printf("  error: %v\n", e)
	}
	for _, m := range r.ManualPending {
		fmt.Printf("  [PENDING] %s (manual)\n", m)
	}
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

func formatTestList(tests []string) string {
	if len(tests) == 0 {
		return "(none)"
	}
	return strings.Join(tests, ", ")
}
