// Package compliant is a fixture for SPAN-RECORD-ERROR-REDACT-01 positive
// case: every span.RecordError call wraps its argument with
// redaction.RedactError. Parsed by archtest; not intended to compile.
package compliant

import "github.com/ghbvf/gocell/pkg/redaction"

// Span mirrors the wrapper.Span shape used by archtest fixture scanning.
type Span interface {
	RecordError(error)
}

// RecordWithRedaction is the canonical compliant pattern.
func RecordWithRedaction(span Span, err error) {
	span.RecordError(redaction.RedactError(err))
}

// RecordWithRedactionAndOtherCall verifies the scanner only flags the
// RecordError call site, not unrelated calls in the same function.
func RecordWithRedactionAndOtherCall(span Span, err error) {
	_ = err.Error()
	span.RecordError(redaction.RedactError(err))
}
