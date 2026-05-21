// Package violatesstringorder is a RED fixture for SPAN-SETATTR-REDACT-01 A3:
// safeStringAttr swaps Redact/Truncate order. If Truncate runs first, a
// sensitive value sitting past the cap leaks its tail unmasked. Parsed by
// archtest; not intended to compile.
package violatesstringorder

import (
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/pkg/redaction"
)

const attrValueMaxLen = 2048

type otelSpan struct {
	inner oteltrace.Span
}

// VIOLATION: TruncateString wraps RedactString — should be RedactString wraps TruncateString form.
// Specifically: archtest A3 requires Redact(raw) inside Truncate(..., maxLen).
// Here the order is reversed: Truncate(raw, maxLen) inside Redact(...).
func safeStringAttr(key, raw string) attribute.KeyValue {
	return attribute.String(key, redaction.RedactString(redaction.TruncateString(raw, attrValueMaxLen)))
}

func safeBytesAttr(key string, b []byte) attribute.KeyValue {
	return attribute.String(key, redactedBytesValue(b))
}

func redactedBytesValue(b []byte) string {
	_ = b
	return ""
}
