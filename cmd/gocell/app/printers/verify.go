package printers

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ghbvf/gocell/kernel/verify"
)

// VerifyPrinter renders a kernel/verify.VerifyResult to a writer. Verify
// emits test-execution outcomes (passed/failed/manual-pending), not
// validation findings — so it owns its own printer interface, not the
// finding-shaped Printer used by validate / check.
type VerifyPrinter interface {
	Print(*verify.VerifyResult) error
}

// NewVerifyPrinter constructs the printer for the requested format. SARIF
// is rejected explicitly because verify outcomes are not findings; falling
// back silently to text would hide a misuse from CI consumers. An empty
// format string is treated as text.
func NewVerifyPrinter(format string, w io.Writer) (VerifyPrinter, error) {
	switch Format(format) {
	case FormatText, "":
		return &verifyTextPrinter{w: w}, nil
	case FormatJSON:
		return &verifyJSONPrinter{w: w}, nil
	case FormatSARIF:
		return nil, fmt.Errorf("verify: SARIF not supported (verify emits test-execution outcomes, not findings)")
	default:
		return nil, fmt.Errorf("verify: unsupported format %q (expected text|json)", format)
	}
}

// SupportedVerifyFormats returns the canonical format list for `gocell verify --format`.
func SupportedVerifyFormats() []string {
	return []string{string(FormatText), string(FormatJSON)}
}

// --- text ---

type verifyTextPrinter struct {
	w io.Writer
}

func (p *verifyTextPrinter) Print(r *verify.VerifyResult) error {
	status := "PASSED"
	if !r.Passed {
		status = "FAILED"
	}
	if _, err := fmt.Fprintf(p.w, "Verify %s: %s\n", r.TargetID, status); err != nil {
		return err
	}
	if err := p.printTestResults(r.Results); err != nil {
		return err
	}
	if err := p.printErrors(r.Errors); err != nil {
		return err
	}
	return p.printManualPending(r.ManualPending)
}

func (p *verifyTextPrinter) printTestResults(results []verify.TestResult) error {
	for _, tr := range results {
		marker := "PASS"
		if !tr.Passed {
			marker = "FAIL"
		}
		if _, err := fmt.Fprintf(p.w, "  [%s] %s\n", marker, tr.Name); err != nil {
			return err
		}
		if tr.Output != "" {
			for line := range strings.SplitSeq(strings.TrimRight(tr.Output, "\n"), "\n") {
				if _, err := fmt.Fprintf(p.w, "    %s\n", line); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *verifyTextPrinter) printErrors(errs []error) error {
	for _, e := range errs {
		if _, err := fmt.Fprintf(p.w, "  error: %v\n", e); err != nil {
			return err
		}
	}
	return nil
}

func (p *verifyTextPrinter) printManualPending(pending []string) error {
	for _, m := range pending {
		if _, err := fmt.Fprintf(p.w, "  [PENDING] %s (manual)\n", m); err != nil {
			return err
		}
	}
	return nil
}

// --- json ---

// JSON wire DTOs — camelCase, stable schema. Errors are flattened to
// strings (err.Error()); callers don't need the underlying error type.
type verifyResultJSON struct {
	TargetID      string           `json:"targetId"`
	Passed        bool             `json:"passed"`
	Results       []testResultJSON `json:"results"`
	Errors        []string         `json:"errors"`
	ManualPending []string         `json:"manualPending"`
}

type testResultJSON struct {
	Name      string `json:"name"`
	Passed    bool   `json:"passed"`
	Output    string `json:"output"`
	ZeroMatch bool   `json:"zeroMatch"`
}

type verifyJSONPrinter struct {
	w io.Writer
}

func (p *verifyJSONPrinter) Print(r *verify.VerifyResult) error {
	doc := verifyResultJSON{
		TargetID:      r.TargetID,
		Passed:        r.Passed,
		Results:       make([]testResultJSON, len(r.Results)),
		Errors:        make([]string, len(r.Errors)),
		ManualPending: make([]string, len(r.ManualPending)),
	}
	for i, tr := range r.Results {
		doc.Results[i] = testResultJSON{
			Name:      tr.Name,
			Passed:    tr.Passed,
			Output:    tr.Output,
			ZeroMatch: tr.ZeroMatch,
		}
	}
	for i, e := range r.Errors {
		doc.Errors[i] = e.Error()
	}
	copy(doc.ManualPending, r.ManualPending)

	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode verify json: %w", err)
	}
	return nil
}
