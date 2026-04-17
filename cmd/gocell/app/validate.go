package app

import (
	"flag"
	"fmt"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// runValidate implements: gocell validate [--root <path>]
// Parses all metadata, runs validate-meta and depcheck.
// exit 0 = pass, exit 1 = errors found.
//
// --fail-fast: true short-circuit. The validator and the dependency checker
// stop at the first rule that produces a SeverityError — subsequent rules do
// not run, which is the point of the flag for CI pipelines over large repos.
// Output is also trimmed to a single error line.
func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	root := fs.String("root", "", "project root directory (default: auto-detect from go.mod)")
	failFast := fs.Bool("fail-fast", false,
		"stop at the first error and skip remaining rules; trims output to that error (CI-friendly)")
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

	validator := governance.NewValidator(project, rootDir)
	depChecker := governance.NewDependencyChecker(project)

	if *failFast {
		// True bailout: validator first (most errors originate there); if it
		// already flagged an error, depcheck is skipped entirely.
		valResults := validator.ValidateFailFast()
		if firstErr := firstError(valResults); firstErr != nil {
			formatResultsFailFast(valResults)
			return fmt.Errorf("validation failed: %s", firstErr.Code)
		}
		depResults := depChecker.CheckFailFast()
		if firstErr := firstError(depResults); firstErr != nil {
			formatResultsFailFast(depResults)
			return fmt.Errorf("validation failed: %s", firstErr.Code)
		}
		fmt.Println("OK: no errors.")
		return nil
	}

	valResults := validator.Validate()
	depResults := depChecker.Check()
	allResults := append(valResults, depResults...)

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

// firstError returns the first result whose severity is error, or nil.
func firstError(results []governance.ValidationResult) *governance.ValidationResult {
	for i := range results {
		if results[i].Severity == governance.SeverityError {
			return &results[i]
		}
	}
	return nil
}
