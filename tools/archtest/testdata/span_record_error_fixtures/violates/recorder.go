// Package violates is a fixture for SPAN-RECORD-ERROR-REDACT-01 negative
// case: span.RecordError is called with a raw error (no redaction wrap),
// which the scanner must report as a violation. Parsed by archtest; not
// intended to compile.
package violates

// Span mirrors the wrapper.Span shape used by archtest fixture scanning.
type Span interface {
	RecordError(error)
}

// RecordRaw is the canonical violation pattern: span.RecordError(err)
// without redaction.RedactError wrapping. Bypasses the fail-closed
// redaction guarantee documented in ADR §8.
func RecordRaw(span Span, err error) {
	span.RecordError(err)
}
