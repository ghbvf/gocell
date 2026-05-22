// Package violateskeychainstring is a RED fixture for SPAN-SETATTR-REDACT-01
// A2 chain shape: attrToKeyValue's default branch uses the SDK-supported
// `attribute.Key(name).String(value)` chain form to bypass the bare
// `attribute.String(name, value)` callsite check. Both shapes return the
// same attribute.KeyValue; A2 must lock both. Parsed by archtest; not
// intended to compile.
package violateskeychainstring

import (
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/pkg/redaction"
)

const attrValueMaxLen = 2048

type otelSpan struct {
	inner oteltrace.Span
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

func attrToKeyValue(key string, v interface{}) attribute.KeyValue {
	switch x := v.(type) {
	case string:
		return safeStringAttr(key, x)
	case []byte:
		return safeBytesAttr(key, x)
	default:
		// VIOLATION (A2 chain): attribute.Key(_).String(_) is the SDK
		// chain shape; identical to attribute.String(_, _) at runtime.
		// Bypasses safeStringAttr — caller's `key` and stringified value
		// reach the collector unredacted.
		return attribute.Key(key).String(fmt.Sprint(x))
	}
}
