// Package compliant is a GREEN fixture for SPAN-SETATTR-REDACT-01: a fully
// compliant funnel — safeStringAttr + safeBytesAttr cover all attribute.String
// callsites, otelSpan is the sole holder of oteltrace.Span. Parsed by archtest;
// not intended to compile.
package compliant

import (
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/pkg/redaction"
)

const attrValueMaxLen = 2048

type otelSpan struct {
	inner oteltrace.Span
}

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

func attrToKeyValue(key string, v interface{}) attribute.KeyValue {
	switch x := v.(type) {
	case string:
		return safeStringAttr(key, x)
	case []byte:
		return safeBytesAttr(key, x)
	default:
		return safeStringAttr(key, "fallback")
	}
}
