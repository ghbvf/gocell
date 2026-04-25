// Package printers renders []governance.ValidationResult to one of three
// stable output formats: text (human-readable, IDE click-to-open), json
// (CI / jq friendly), or sarif (SARIF 2.1.0 — VS Code SARIF Explorer,
// GitHub Code Scanning).
//
// Design ref: golangci-lint pkg/printers (Print(issues) interface) — adopted
// the io.Writer-injected printer pattern but fixed to a single concrete
// dispatcher (golangci-lint allows multiple simultaneous output sinks; gocell
// has one stdout-bound consumer per invocation).
//
// The kernel/governance.ValidationResult type stays a pure domain struct with
// no encoding tags; printers convert via private DTOs to keep the wire format
// owned here, not in the domain layer.
package printers

import (
	"fmt"
	"io"
	"sort"

	"github.com/ghbvf/gocell/kernel/governance"
)

// Format identifies a supported output format. Use the FormatXxx constants
// when invoking New so callers do not hard-code stringly-typed values.
type Format string

const (
	FormatText  Format = "text"
	FormatJSON  Format = "json"
	FormatSARIF Format = "sarif"
)

// Printer renders validation results to a writer. Each implementation chooses
// its own serialisation; callers do not depend on which concrete type they
// hold.
type Printer interface {
	Print(results []governance.ValidationResult) error
}

// FailFastPrinter is implemented by printers that have a distinct fail-fast
// rendering (e.g. text mode trims to a single line). When a printer does not
// implement this interface, callers should fall back to Print with the
// already-truncated result slice.
type FailFastPrinter interface {
	PrintFailFast(results []governance.ValidationResult) error
}

// SupportedFormats returns the canonical list of format names accepted by New.
// Used by --help and by error messages so the discoverable set lives in one
// place.
func SupportedFormats() []string {
	return []string{string(FormatText), string(FormatJSON), string(FormatSARIF)}
}

// New constructs the printer for the requested format, writing to w. An
// unknown format returns a non-nil error so the CLI can surface a usage
// message rather than producing silently empty output.
//
// The toolVersion is only consumed by the SARIF printer (where it lands in
// runs[].tool.driver.version); other printers ignore it. Pass the binary's
// build version, or "dev" when unknown.
func New(format string, w io.Writer, toolVersion string) (Printer, error) {
	switch Format(format) {
	case FormatText:
		return NewTextPrinter(w), nil
	case FormatJSON:
		return NewJSONPrinter(w), nil
	case FormatSARIF:
		return NewSARIFPrinter(w, toolVersion), nil
	default:
		return nil, fmt.Errorf("unknown format %q: supported formats are %v", format, SupportedFormats())
	}
}

// sortResults returns a new slice with results sorted into a deterministic
// order so all three printers produce reproducible (golden-friendly) output.
//
// Order:
//  1. Severity: errors before warnings (errors first matches the visual
//     priority of the text printer's banner layout).
//  2. Code: ascending. Stable rule grouping; SARIF rules[] dedup keys off this.
//  3. File: ascending; "" sorts after non-empty (scope-only results land
//     after file-anchored ones for the same code).
//  4. Scope: ascending (only meaningful when File is empty).
//  5. Line: ascending (0 sorts first within the same file).
//  6. Column: ascending.
//  7. Field: ascending. Last tie-breaker so two results from the same
//     position-with-different-fields produce stable golden output.
//
// The input slice is not mutated; the caller can keep using it post-call.
func sortResults(in []governance.ValidationResult) []governance.ValidationResult {
	out := make([]governance.ValidationResult, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if av, bv := severityRank(a.Severity), severityRank(b.Severity); av != bv {
			return av < bv
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if av, bv := fileRank(a.File), fileRank(b.File); av != bv {
			return av < bv
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Field < b.Field
	})
	return out
}

// severityRank maps a Severity to a sort key. Unknown severities sort last so
// future additions never silently displace error/warning ordering.
func severityRank(s governance.Severity) int {
	switch s {
	case governance.SeverityError:
		return 0
	case governance.SeverityWarning:
		return 1
	default:
		return 2
	}
}

// fileRank pushes "" (no file — scope-only) after non-empty file paths within
// the same code group. We can't rely on lexicographic ordering alone: ""
// sorts before every non-empty string, which would surface scope-only
// findings ahead of concrete file references and read confusingly.
func fileRank(file string) int {
	if file == "" {
		return 1
	}
	return 0
}

// countSeverities returns the per-severity count used by JSON and text
// summaries. Exposed package-level so check-style commands can reuse the
// same count without re-implementing the loop.
func countSeverities(results []governance.ValidationResult) (errCount, warnCount int) {
	for i := range results {
		switch results[i].Severity {
		case governance.SeverityError:
			errCount++
		case governance.SeverityWarning:
			warnCount++
		}
	}
	return errCount, warnCount
}
