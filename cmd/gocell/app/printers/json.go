package printers

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ghbvf/gocell/kernel/governance"
)

// JSONPrinter renders results as a single, indented JSON document with a
// stable shape:
//
//	{
//	  "issues": [ { code, severity, issueType, file, scope, field, message,
//	                line, column }, ... ],
//	  "summary": { "errors": N, "warnings": M }
//	}
//
// Camel-case keys per the GoCell JSON convention. Empty results emit
// "issues": [] (never null) so consumers can safely iterate without
// nil-checking.
type JSONPrinter struct {
	w io.Writer
}

// NewJSONPrinter constructs a JSON printer writing to w. w must not be nil.
func NewJSONPrinter(w io.Writer) *JSONPrinter {
	return &JSONPrinter{w: w}
}

// resultJSON is the wire-format DTO for one ValidationResult. We deliberately
// do not put JSON tags on governance.ValidationResult: the kernel domain
// type stays free of encoding concerns, and any future change to the wire
// format only edits this file.
//
// All scalar fields are emitted unconditionally to keep the consumer-facing
// shape stable across results — the value will be "" or 0 when unset,
// rather than the field being absent.
type resultJSON struct {
	Code      string `json:"code"`
	Severity  string `json:"severity"`
	IssueType string `json:"issueType"`
	File      string `json:"file"`
	Scope     string `json:"scope"`
	Field     string `json:"field"`
	Message   string `json:"message"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
}

// summaryJSON is the per-document tally. Counted from the same sorted slice
// the consumer sees, so iterating issues + checking summary always agrees.
type summaryJSON struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

// documentJSON is the top-level JSON shape. Defined in one place so future
// additions (e.g. a "schemaVersion" field) are an obvious one-line edit.
type documentJSON struct {
	Issues  []resultJSON `json:"issues"`
	Summary summaryJSON  `json:"summary"`
}

// Print writes the full document. Sorting is applied before emit so two runs
// over the same input produce identical bytes (golden tests, CI diff).
func (p *JSONPrinter) Print(results []governance.ValidationResult) error {
	sorted := sortResults(results)

	issues := make([]resultJSON, len(sorted))
	for i := range sorted {
		issues[i] = toResultJSON(sorted[i])
	}
	errCount, warnCount := countSeverities(sorted)

	doc := documentJSON{
		Issues: issues,
		Summary: summaryJSON{
			Errors:   errCount,
			Warnings: warnCount,
		},
	}

	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	// Disable HTML escaping: validation messages routinely contain `<`, `>`,
	// and `&` (XML-style placeholders, comparison operators, etc.). Default
	// escaping renders these as < / > / & which is unreadable
	// in jq output and SARIF viewers without changing meaning. The output
	// remains valid JSON; we just don't pre-defend against being embedded
	// in HTML, which is not a use case for CLI output.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// toResultJSON copies every documented field over. We resolve Severity /
// IssueType from their typed constants here — the wire format speaks plain
// strings so jq queries don't need to know about the typed enums.
func toResultJSON(r governance.ValidationResult) resultJSON {
	return resultJSON{
		Code:      string(r.Code),
		Severity:  string(r.Severity),
		IssueType: string(r.IssueType),
		File:      r.File,
		Scope:     r.Scope,
		Field:     r.Field,
		Message:   r.Message,
		Line:      r.Line,
		Column:    r.Column,
	}
}
