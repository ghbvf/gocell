package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/kernel/clock"
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
		"enforce strict-only governance rules"+
			" (VERIFY-06 executable journey auto checks, FMT-16 slice/cell/assembly dirs,"+
			" FMT-17 allowedFiles)")
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

	validator := governance.NewValidator(project, rootDir, clock.Real())
	depChecker := governance.NewDependencyChecker(project)

	// Boundary: the gocell sub-command dispatcher passes args, not ctx, so
	// each sub-command owns its own ctx lifetime. Declaring Background here
	// is the cancellation source the entire validate path honors — runGit
	// subprocesses and verifyJourneyRef both consume it. A future signal-
	// aware ctx (signal.NotifyContext at process entry) would replace this
	// declaration without touching the rest of the file.
	ctx := context.Background()
	if *failFast {
		return runValidateFailFast(ctx, printer, *format, validator, depChecker, *strict)
	}
	return runValidateFull(ctx, printer, validator, depChecker, *strict)
}

// runValidateFailFast runs validation in short-circuit mode: the validator
// and the dependency checker stop at the first SeverityError. When strict is
// true, FMT-16/17 are appended only if the base pass finds no errors.
//
// Output rendering depends on the --format and the run's outcome:
//
//   - errors present (any rule produced SeverityError): emit only the first
//     error in text mode (single line, no banner, no summary); in
//     json/sarif emit a full document containing that one issue.
//   - no errors but warnings present: emit the full warning set via the
//     printer's standard Print path. `ValidateFailFast` and `CheckFailFast`
//     in kernel/governance explicitly preserve warnings on the clean-error
//     path; dropping them at the command layer would silently hide
//     warning-only repos.
//   - no errors, no warnings: text emits the legacy "OK: no errors." line;
//     json/sarif emit an empty document so consumers can always parse a
//     result regardless of outcome.
//
// Printer write failures are returned as the command's error: a truncated
// JSON / SARIF report is more dangerous than a missing one because CI
// pipelines may still ingest it.
func runValidateFailFast(
	ctx context.Context,
	printer printers.Printer,
	format string,
	validator *governance.Validator,
	depChecker *governance.DependencyChecker,
	strict bool,
) error {
	valResults, valErr := runValidatorFailFast(ctx, validator, strict)
	if valErr != nil {
		return fmt.Errorf("validation interrupted: %w", valErr)
	}
	if firstErr := firstError(valResults); firstErr != nil {
		if err := emitFailFast(printer, format, valResults); err != nil {
			return fmt.Errorf(errEmitResultsFmt, err)
		}
		return fmt.Errorf("validation failed: %s", firstErr.Code)
	}
	depResults := depChecker.CheckFailFast()
	if firstErr := firstError(depResults); firstErr != nil {
		if err := emitFailFast(printer, format, depResults); err != nil {
			return fmt.Errorf(errEmitResultsFmt, err)
		}
		return fmt.Errorf("validation failed: %s", firstErr.Code)
	}

	// No errors. Combine the validator and depcheck results so warnings from
	// either accumulator are preserved.
	valResults = append(valResults, depResults...)

	if len(valResults) == 0 {
		// Truly clean run. Text mode keeps the legacy single-line "OK"
		// ack so existing scripts and TestRunValidate_FailFast_NoErrors_
		// PrintsOK stay green. Structured formats still emit an empty
		// document so consumers can parse a result regardless of outcome.
		if format == string(printers.FormatText) {
			fmt.Println("OK: no errors.")
			return nil
		}
		if err := printer.Print(nil); err != nil {
			return fmt.Errorf(errEmitResultsFmt, err)
		}
		return nil
	}

	// Warnings only — emit them through the standard printer path so they
	// reach CI / SARIF Explorer / jq just like in non-fail-fast runs. The
	// short-circuit guarantee is "stop at first error", not "drop warnings".
	if err := printer.Print(valResults); err != nil {
		return fmt.Errorf(errEmitResultsFmt, err)
	}
	return nil
}

// emitFailFast renders the first error using the printer's fail-fast mode if
// available; otherwise the printer receives a one-element slice. This keeps
// fail-fast a single concept across all three formats while letting text
// mode drop its banner / summary lines.
//
// Writer errors are returned to the caller; the caller wraps them with an
// "emit results:" prefix so the CLI's exit status reflects the output
// failure rather than the validation outcome (which the caller could not
// reliably report anyway when stdout is broken).
func emitFailFast(printer printers.Printer, format string, results []governance.ValidationResult) error {
	if format == string(printers.FormatText) {
		if ff, ok := printer.(printers.FailFastPrinter); ok {
			return ff.PrintFailFast(results)
		}
	}
	return printer.Print([]governance.ValidationResult{*firstError(results)})
}

// runValidatorFailFast runs validation in fail-fast mode.
func runValidatorFailFast(ctx context.Context, validator *governance.Validator, strict bool) ([]governance.ValidationResult, error) {
	return validator.ValidateStrict(ctx, strict, true)
}

// runValidateFull runs all validation rules and emits via the configured printer.
// The printer owns the summary line; we only return an error when SeverityError
// results are present so the CLI exit code reflects validation outcome.
func runValidateFull(
	ctx context.Context,
	printer printers.Printer,
	validator *governance.Validator,
	depChecker *governance.DependencyChecker,
	strict bool,
) error {
	valResults, valErr := runValidatorFull(ctx, validator, strict)
	if valErr != nil {
		return fmt.Errorf("validation interrupted: %w", valErr)
	}
	depResults := depChecker.Check()
	valResults = append(valResults, depResults...)

	if err := printer.Print(valResults); err != nil {
		return fmt.Errorf(errEmitResultsFmt, err)
	}

	errCount := 0
	for i := range valResults {
		if valResults[i].Severity == governance.SeverityError {
			errCount++
		}
	}
	if errCount > 0 {
		return fmt.Errorf("validation failed with %d error(s)", errCount)
	}
	return nil
}

// runValidatorFull runs all validation rules and returns all findings.
func runValidatorFull(ctx context.Context, validator *governance.Validator, strict bool) ([]governance.ValidationResult, error) {
	return validator.ValidateStrict(ctx, strict, false)
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
