package main

import (
	"flag"
	"fmt"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// runValidate implements: gocell validate [--root <path>]
// Parses all metadata, runs validate-meta and depcheck.
// exit 0 = pass, exit 1 = errors found.
func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	root := fs.String("root", "", "project root directory (default: auto-detect from go.mod)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rootDir := *root
	if rootDir == "" {
		var err error
		rootDir, err = findRoot()
		if err != nil {
			return fmt.Errorf("cannot find project root: %w", err)
		}
	}

	// Parse all metadata.
	parser := metadata.NewParser(rootDir)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf("metadata parse: %w", err)
	}

	// Run validation rules.
	validator := governance.NewValidator(project, rootDir)
	valResults := validator.Validate()

	// Run dependency checks.
	depChecker := governance.NewDependencyChecker(project)
	depResults := depChecker.Check()

	// Merge all results.
	allResults := append(valResults, depResults...)

	// Output results.
	formatResults(allResults)

	// Summary.
	var errCount, warnCount int
	for i := range allResults {
		switch allResults[i].Severity {
		case governance.SeverityError:
			errCount++
		case governance.SeverityWarning:
			warnCount++
		}
	}

	fmt.Printf("\nValidation complete: %d error(s), %d warning(s)\n", errCount, warnCount)

	if errCount > 0 {
		return fmt.Errorf("validation failed with %d error(s)", errCount)
	}
	return nil
}
