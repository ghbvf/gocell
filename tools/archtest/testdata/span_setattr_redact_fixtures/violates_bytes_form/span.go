// Package violatesbytesform is a RED fixture for SPAN-SETATTR-REDACT-01 A4:
// safeBytesAttr inlines the SHA256 work instead of routing through
// redactedBytesValue, so the canonical fail-closed byte-encoding helper is
// bypassed. Parsed by archtest; not intended to compile.
package violatesbytesform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/framework/pkg/redaction"
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

// VIOLATION: safeBytesAttr inlines the hashing instead of calling
// redactedBytesValue. Hard-line check A4 mandates the explicit call so the
// canonical byte-redaction shape is the single source.
func safeBytesAttr(key string, b []byte) attribute.KeyValue {
	sum := sha256.Sum256(b)
	return attribute.String(key, fmt.Sprintf("[len=%d sha256=%s]", len(b), hex.EncodeToString(sum[:])))
}

func redactedBytesValue(b []byte) string {
	_ = b
	return ""
}
