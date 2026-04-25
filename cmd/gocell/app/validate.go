package app

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// runValidate implements: gocell validate [--root <path>] [--fail-fast] [--strict] [--format text|json|sarif]
// Parses all metadata, runs validate-meta and depcheck.
// exit 0 = pass, exit 1 = errors found.
//
// --fail-fast: short-circuits at the first SeverityError. Output is trimmed
// to a single error line in text mode; JSON and SARIF still emit a full
// document containing that one issue.
//
// --format selects the output renderer. The default "text" format is
// declared non-stable: scripts that need machine-parseable output should
// use --format=json or --format=sarif.
func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	root := fs.String("root", "", "project root directory (default: auto-detect from go.mod)")
	failFast := fs.Bool("fail-fast", false,
		"stop at the first error and skip remaining rules; trims output to that error (CI-friendly)")
	strict := fs.Bool("strict", false,
		"enforce no-dash naming and allowedFiles-mismatch rules (FMT-16 slice/cell/assembly dirs, FMT-17 allowedFiles, FMT-C1 cell id, FMT-A1 assembly id); strict-only, silent without this flag")
	format := fs.String("format", string(printers.FormatText),
		"output format: text (non-stable, default) | json | sarif")
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

	printer, err := printers.New(*format, os.Stdout, toolVersion())
	if err != nil {
		return err
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
		return runValidateFailFast(printer, *format, validator, depChecker, *strict)
	}
	return runValidateFull(printer, validator, depChecker, *strict)
}

// runValidateFailFast runs validation in short-circuit mode: the validator
// and the dependency checker stop at the first SeverityError. When strict is
// true, FMT-16/17 are appended only if the base pass finds no errors.
//
// Output rendering depends on the --format:
//   - text: only the first error is shown (no banner, no summary). On clean
//     runs, prints "OK: no errors." to keep the existing CI surface.
//   - json / sarif: full document with one issue (or zero on success).
func runValidateFailFast(
	printer printers.Printer,
	format string,
	validator *governance.Validator,
	depChecker *governance.DependencyChecker,
	strict bool,
) error {
	valResults := runValidatorFailFast(validator, strict)
	if firstErr := firstError(valResults); firstErr != nil {
		emitFailFast(printer, format, valResults)
		return fmt.Errorf("validation failed: %s", firstErr.Code)
	}
	depResults := depChecker.CheckFailFast()
	if firstErr := firstError(depResults); firstErr != nil {
		emitFailFast(printer, format, depResults)
		return fmt.Errorf("validation failed: %s", firstErr.Code)
	}

	// Clean run. Text mode keeps the legacy single-line "OK" ack so existing
	// scripts and the TestRunValidate_FailFast_NoErrors_PrintsOK contract
	// remain green. Structured formats emit an empty document so consumers
	// can always parse a result regardless of outcome.
	if format == string(printers.FormatText) {
		fmt.Println("OK: no errors.")
		return nil
	}
	return printer.Print(nil)
}

// emitFailFast renders the first error using the printer's fail-fast mode if
// available; otherwise the printer receives a one-element slice. This keeps
// fail-fast a single concept across all three formats while letting text
// mode drop its banner / summary lines.
//
// Writer errors (closed pipe, full disk on a stdout redirect, etc.) are
// surfaced to stderr — silently swallowing them previously meant CI saw
// "no validation output" without explanation. The validation result itself
// is still propagated by the caller via its returned error, so an output
// failure is observable but does not mask the original validation outcome.
func emitFailFast(printer printers.Printer, format string, results []governance.ValidationResult) {
	var emitErr error
	if format == string(printers.FormatText) {
		if ff, ok := printer.(printers.FailFastPrinter); ok {
			emitErr = ff.PrintFailFast(results)
		} else {
			emitErr = printer.Print([]governance.ValidationResult{*firstError(results)})
		}
	} else {
		emitErr = printer.Print([]governance.ValidationResult{*firstError(results)})
	}
	if emitErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to emit fail-fast result: %v\n", emitErr)
	}
}

// runValidatorFailFast selects the appropriate validator method for fail-fast mode.
func runValidatorFailFast(validator *governance.Validator, strict bool) []governance.ValidationResult {
	if strict {
		return validator.ValidateStrictFailFast()
	}
	return validator.ValidateFailFast()
}

// runValidateFull runs all validation rules and emits via the configured printer.
// The printer owns the summary line; we only return an error when SeverityError
// results are present so the CLI exit code reflects validation outcome.
func runValidateFull(
	printer printers.Printer,
	validator *governance.Validator,
	depChecker *governance.DependencyChecker,
	strict bool,
) error {
	valResults := runValidatorFull(validator, strict)
	depResults := depChecker.Check()
	allResults := append(valResults, depResults...)

	if err := printer.Print(allResults); err != nil {
		return fmt.Errorf("emit results: %w", err)
	}

	errCount := 0
	for i := range allResults {
		if allResults[i].Severity == governance.SeverityError {
			errCount++
		}
	}
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

// firstError returns the first result whose severity is error, or nil.
func firstError(results []governance.ValidationResult) *governance.ValidationResult {
	for i := range results {
		if results[i].Severity == governance.SeverityError {
			return &results[i]
		}
	}
	return nil
}

// toolVersion derives a SARIF-friendly version string from the Go build info.
// VCS revision wins (matches release tooling); falls back to the module
// version, then the literal "dev" so the SARIF output is never empty.
func toolVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			rev := s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return rev
		}
	}
	if info.Main.Version != "" && !strings.HasPrefix(info.Main.Version, "(devel") {
		return info.Main.Version
	}
	return "dev"
}
