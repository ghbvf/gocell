// Package violatesrogueholder is a RED fixture for SPAN-SETATTR-REDACT-01 A1:
// a non-otelSpan struct holds an oteltrace.Span field, creating a bypass
// surface that can call s.inner.SetAttributes directly. Parsed by archtest;
// not intended to compile.
package violatesrogueholder

import (
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

const attrValueMaxLen = 2048

type otelSpan struct {
	inner oteltrace.Span
}

// VIOLATION: rogueSpan is a second holder of oteltrace.Span; whoever owns
// rogueSpan can call rogueSpan.shadow.SetAttributes(...) without going
// through attrToKeyValue, bypassing the redaction funnel.
type rogueSpan struct {
	shadow oteltrace.Span
}

func safeStringAttr(key, raw string) attribute.KeyValue {
	if redaction.IsSensitiveKey(key) {
		return attribute.String(key, redaction.Mask)
	}
	return attribute.String(key, redaction.TruncateString(redaction.RedactString(raw), attrValueMaxLen))
}

func safeBytesAttr(key string, b []byte) attribute.KeyValue {
	return attribute.String(key, redactedBytesValue(b))
}

func redactedBytesValue(b []byte) string {
	_ = b
	return ""
}
