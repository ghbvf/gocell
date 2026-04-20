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
	strict := fs.Bool("strict", false,
		"upgrade kebab-case slice directory and allowedFiles-mismatch warnings to errors (FMT-16, FMT-17)")
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
		return runValidateFailFast(validator, depChecker, *strict)
	}
	return runValidateFull(validator, depChecker, *strict)
}

// runValidateFailFast runs validation in short-circuit mode: the validator
// and the dependency checker stop at the first SeverityError. When strict is
// true, FMT-16/17 are appended only if the base pass finds no errors.
func runValidateFailFast(validator *governance.Validator, depChecker *governance.DependencyChecker, strict bool) error {
	valResults := runValidatorFailFast(validator, strict)
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

// runValidatorFailFast selects the appropriate validator method for fail-fast mode.
func runValidatorFailFast(validator *governance.Validator, strict bool) []governance.ValidationResult {
	if strict {
		return validator.ValidateStrictFailFast()
	}
	return validator.ValidateFailFast()
}

// runValidateFull runs all validation rules and prints a summary.
func runValidateFull(validator *governance.Validator, depChecker *governance.DependencyChecker, strict bool) error {
	valResults := runValidatorFull(validator, strict)
	depResults := depChecker.Check()
	allResults := append(valResults, depResults...)

	formatResults(allResults)

	errCount, warnCount := countSeverities(allResults)
	fmt.Printf("\nValidation complete: %d error(s), %d warning(s)\n", errCount, warnCount)

	if errCount > 0 {
		return fmt.Errorf("validation failed with %d error(s)", errCount)
	}
	return nil
}

// runValidatorFull selects the appropriate validator method for full mode.
func runValidatorFull(validator *governance.Validator, strict bool) []governance.ValidationResult {
	if strict {
		return validator.ValidateStrict(true)
	}
	return validator.Validate()
}

// countSeverities returns the number of errors and warnings in results.
func countSeverities(results []governance.ValidationResult) (errCount, warnCount int) {
	for i := range results {
		switch results[i].Severity {
		case governance.SeverityError:
			errCount++
		case governance.SeverityWarning:
			warnCount++
		}
	}
	return
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
