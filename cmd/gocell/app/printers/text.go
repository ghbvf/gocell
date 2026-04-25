package printers

import (
	"fmt"
	"io"
	"strings"

	"github.com/ghbvf/gocell/kernel/governance"
)

// TextPrinter renders results in the human-readable format also used by the
// rest of the gocell CLI's diagnostic surface — banner per severity, one block
// per result with a "[CODE] message (field: <field>)" first line and an
// "         at <file:line:col>" or "[scope: ...]" anchor line. Trailing
// summary line counts errors and warnings.
//
// The format is explicitly documented as non-stable: scripts that need
// machine-parseable output should use --format=json or --format=sarif. We
// keep the format byte-equivalent to pre-PR-A10 output so editor
// click-to-open jumpers (GoLand, VS Code, iTerm2) keep working.
type TextPrinter struct {
	w io.Writer
}

// NewTextPrinter constructs a TextPrinter writing to w. w must not be nil.
func NewTextPrinter(w io.Writer) *TextPrinter {
	return &TextPrinter{w: w}
}

// textWriter is the per-call rendering buffer-with-error-latch. Print and
// PrintFailFast share this type so the long sequence of writeln /
// writelnf calls below stays free of repetitive `if err != nil` ladders
// while still surfacing the first io.Writer failure to the caller via
// err().
type textWriter struct {
	w   io.Writer
	err error
}

func (t *textWriter) writelnf(format string, args ...any) {
	if t.err != nil {
		return
	}
	_, t.err = fmt.Fprintf(t.w, format, args...)
}

func (t *textWriter) writeln(s string) {
	if t.err != nil {
		return
	}
	_, t.err = fmt.Fprintln(t.w, s)
}

// Print renders the full result list grouped by severity, followed by a
// summary line. The output shape matches pre-PR-A10 byte-for-byte:
//
//   - Empty input: "No issues found.\n" + blank line + "Validation complete: ...".
//   - Non-empty:   "ERRORS (N):" / "WARNINGS (M):" blocks then summary.
//
// **Input order is preserved** within each severity group. Validator.rules()
// in kernel/governance emits results in a deterministic rule sequence
// (REF → TOPO → VERIFY → FMT → ...), and downstream tooling has come to
// rely on that ordering. JSON / SARIF apply sortResults for stable
// machine-parseable output; text mode keeps the legacy ordering so
// IDE-style click-through remains predictable.
//
// The first write error is returned and aborts further output; later
// writes are skipped.
func (p *TextPrinter) Print(results []governance.ValidationResult) error {
	tw := &textWriter{w: p.w}

	if len(results) == 0 {
		tw.writeln("No issues found.")
		tw.writelnf("\nValidation complete: 0 error(s), 0 warning(s)\n")
		return tw.err
	}

	var errors, warnings []governance.ValidationResult
	for i := range results {
		switch results[i].Severity {
		case governance.SeverityError:
			errors = append(errors, results[i])
		case governance.SeverityWarning:
			warnings = append(warnings, results[i])
		}
	}

	if len(errors) > 0 {
		tw.writelnf("ERRORS (%d):\n", len(errors))
		for _, r := range errors {
			p.writeOne(tw, r)
		}
		tw.writeln("")
	}

	if len(warnings) > 0 {
		tw.writelnf("WARNINGS (%d):\n", len(warnings))
		for _, r := range warnings {
			p.writeOne(tw, r)
		}
		tw.writeln("")
	}

	errCount, warnCount := countSeverities(results)
	tw.writelnf("\nValidation complete: %d error(s), %d warning(s)\n", errCount, warnCount)
	return tw.err
}

// PrintFailFast renders only the first error in the slice with no banner,
// summary, or warnings — same shape as the pre-PR-A10 helpers used for
// --fail-fast mode. If no error is present, output is empty.
func (p *TextPrinter) PrintFailFast(results []governance.ValidationResult) error {
	for i := range results {
		if results[i].Severity == governance.SeverityError {
			tw := &textWriter{w: p.w}
			p.writeOne(tw, results[i])
			return tw.err
		}
	}
	return nil
}

// writeOne renders a single result block. We accept the writer (rather
// than reading p.w directly) so Print and PrintFailFast share the per-call
// textWriter and stop emitting on first failure.
//
// Layout (single-line message):
//
//	[CODE] message (field: <field>)
//	       at <file>[:<line>[:<col>]]
//
// or, for scope-only findings:
//
//	[CODE] message (field: <field>)
//	       at [scope: <name>]
//
// Layout (multi-line message — e.g. FMT-13's copy-pasteable YAML hint):
// the (field: <field>) suffix lands on the first line only; remaining
// lines follow verbatim so embedded snippets stay copy-paste-correct.
//
//	[CODE] first line of message (field: <field>)
//	  rest of the message verbatim
//	  ...
//	       at <file>[:<line>[:<col>]]
//
// Field is omitted entirely when empty. The anchor line is omitted when
// neither File nor Scope is set.
func (p *TextPrinter) writeOne(tw *textWriter, r governance.ValidationResult) {
	firstLine, rest, multiline := strings.Cut(r.Message, "\n")
	header := firstLine
	if r.Field != "" {
		header += fmt.Sprintf(" (field: %s)", r.Field)
	}
	tw.writelnf("  [%s] %s\n", r.Code, header)
	if multiline {
		// Render remaining lines unmodified so any embedded YAML / code
		// fragment keeps its native indentation. Trailing blank line is
		// preserved by the final "\n".
		tw.writelnf("%s\n", rest)
	}

	switch {
	case r.Scope != "":
		tw.writelnf("         at [scope: %s]\n", r.Scope)
	case r.File != "":
		location := r.File
		if r.Line > 0 {
			location += fmt.Sprintf(":%d", r.Line)
			if r.Column > 0 {
				location += fmt.Sprintf(":%d", r.Column)
			}
		}
		tw.writelnf("         at %s\n", location)
	}
}
