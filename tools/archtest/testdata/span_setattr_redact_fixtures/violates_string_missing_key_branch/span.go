// Package violatesstringmissingkeybranch is a RED fixture for
// SPAN-SETATTR-REDACT-01 A3a: safeStringAttr omits the key-aware
// `if redaction.IsSensitiveKey(key) { return attribute.String(key,
// redaction.Mask) }` structured-key bypass guard. Without it, a caller
// passing wrapper.Attr{Key: "password", Value: "hunter2"} bypasses
// redaction — value is bare, no `password=` anchor, RedactString does
// not fire. Parsed by archtest; not intended to compile.
package violatesstringmissingkeybranch

import (
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

const attrValueMaxLen = 2048

type otelSpan struct {
	inner oteltrace.Span
}

// VIOLATION (A3a): no IsSensitiveKey branch — the structured-key leak
// surface is left open. A3b shape is correct in isolation.
func safeStringAttr(key, raw string) attribute.KeyValue {
	return attribute.String(key, redaction.TruncateString(redaction.RedactString(raw), attrValueMaxLen))
}

func safeBytesAttr(key string, b []byte) attribute.KeyValue {
	return attribute.String(key, redactedBytesValue(b))
}

func redactedBytesValue(b []byte) string {
	_ = b
	return ""
}
