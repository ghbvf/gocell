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
// --fail-fast controls output, not traversal: the validator always evaluates all
// rules; only the printed output is short-circuited. With the flag set, only the
// first error encountered is printed and banners/summary are suppressed.
func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	root := fs.String("root", "", "project root directory (default: auto-detect from go.mod)")
	failFast := fs.Bool("fail-fast", false,
		"print only the first error encountered and skip banners/summary; the validator still evaluates all rules (this flag controls output, not traversal)")
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

	if *failFast {
		if firstErr := firstError(allResults); firstErr != nil {
			formatResultsFailFast(allResults)
			return fmt.Errorf("validation failed: %s", firstErr.Code)
		}
		fmt.Println("OK: no errors.")
		return nil
	}

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

// firstError returns the first result whose severity is error, or nil.
func firstError(results []governance.ValidationResult) *governance.ValidationResult {
	for i := range results {
		if results[i].Severity == governance.SeverityError {
			return &results[i]
		}
	}
	return nil
}
