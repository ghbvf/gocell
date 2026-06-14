// Package violatesstringswapargs is a RED fixture for SPAN-SETATTR-REDACT-01
// A3b argument-identity binding: safeStringAttr's trailing return swaps
// `key` and `raw` formal-param positions. Both args are `*ast.Ident`, so
// the loose type-assertion check that preceded identity binding would have
// accepted this — current A3b binds args to FuncDecl formal params and
// rejects. Parsed by archtest; not intended to compile.
package violatesstringswapargs

import (
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

const attrValueMaxLen = 2048

type otelSpan struct {
	inner oteltrace.Span
}

// VIOLATION (A3b): args swapped — outer attribute.String(raw, …RedactString(key)…).
// Production code would emit user data as the key (label) and redact the
// const label as the value, a silent schema corruption the original
// loose-Ident A3 missed.
func safeStringAttr(key, raw string) attribute.KeyValue {
	if redaction.IsSensitiveKey(key) {
		return attribute.String(key, redaction.Mask)
	}
	return attribute.String(raw, redaction.TruncateString(redaction.RedactString(key), attrValueMaxLen))
}

func safeBytesAttr(key string, b []byte) attribute.KeyValue {
	return attribute.String(key, redactedBytesValue(b))
}

func redactedBytesValue(b []byte) string {
	_ = b
	return ""
}
