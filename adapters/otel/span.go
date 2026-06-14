package otel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

// INVARIANT: SPAN-SETATTR-REDACT-01 — every string-valued span attribute
// in this adapter is funneled through safeStringAttr or safeBytesAttr, so
// caller-supplied values (cell IDs, HTTP routes, contract metadata, error
// text from middleware-injected resolvers) cannot reach an OTLP collector
// without redaction + length cap. archtest tools/archtest/span_setattr_redact_test.go
// locks the funnel (holder uniqueness + callsite uniqueness + helper form).

// Compile-time checks: otelSpan implements wrapper.Span and
// wrapper.SpanRenamer.
var (
	_ wrapper.Span        = (*otelSpan)(nil)
	_ wrapper.SpanRenamer = (*otelSpan)(nil)
)

// attrValueMaxLen caps the rune length of a single span-attribute string
// value after redaction. The OTel SDK's AttributeValueLengthLimit defaults
// to -1 (unlimited); a fail-closed adapter sets an explicit positive cap so
// that one outsized attribute cannot inflate wire size or DOS the collector.
// 2048 runes balances Jaeger/Tempo UI display caps with retaining enough
// context for typical error / route / contract metadata. Always applied
// AFTER RedactString — see safeStringAttr godoc.
const attrValueMaxLen = 2048

// otelSpan wraps an OTel trace.Span to implement the kernel/wrapper.Span
// interface.
type otelSpan struct {
	inner oteltrace.Span
}

// End completes the span.
func (s *otelSpan) End() {
	s.inner.End()
}

// SetAttributes records key-value pairs on the span. It uses a type switch
// to map Go types to the correct OTel attribute constructors.
func (s *otelSpan) SetAttributes(attrs ...wrapper.Attr) {
	if len(attrs) == 0 {
		return
	}
	kvs := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		kvs = append(kvs, attrToKeyValue(a))
	}
	s.inner.SetAttributes(kvs...)
}

func attrToKeyValue(a wrapper.Attr) attribute.KeyValue {
	switch v := a.Value.(type) {
	case string:
		return safeStringAttr(a.Key, v)
	case int:
		return attribute.Int(a.Key, v)
	case int64:
		return attribute.Int64(a.Key, v)
	case float64:
		return attribute.Float64(a.Key, v)
	case bool:
		return attribute.Bool(a.Key, v)
	case []byte:
		return safeBytesAttr(a.Key, v)
	default:
		return safeStringAttr(a.Key, fmt.Sprint(v))
	}
}

// safeStringAttr is the single sanctioned out-of-package boundary for a
// string-valued span attribute. Two-layer fail-closed scrubber:
//
//  1. Key-aware: if key names a sensitive field (per redaction.IsSensitiveKey),
//     the value is replaced with redaction.Mask regardless of contents. This
//     covers the structured leak where a caller passes
//     wrapper.Attr{Key: "password", Value: "hunter2"} — the value is a bare
//     string with no `password=` anchor, so RedactString alone would never
//     match it.
//  2. Free-form: non-sensitive keys flow through RedactString (mask
//     `key=value` / `Authorization: Bearer …` substrings) then TruncateString
//     (cap at attrValueMaxLen runes).
//
// Ordering within layer 2 is correctness-critical: RedactString MUST run
// before TruncateString. If reversed, a sensitive value sitting past
// attrValueMaxLen would have its mask anchor consumed by the cut, leaving
// the tail unmasked when emitted to the collector.
//
// ref: pkg/redaction.IsSensitiveKey (structured key matcher)
// ref: pkg/redaction.RedactString (mask `key=value` / `key: value` sensitive substrings)
// ref: pkg/redaction.TruncateString (UTF-8 rune-safe cap; non-positive is no-op)
func safeStringAttr(key, raw string) attribute.KeyValue {
	if redaction.IsSensitiveKey(key) {
		return attribute.String(key, redaction.Mask)
	}
	return attribute.String(key, redaction.TruncateString(redaction.RedactString(raw), attrValueMaxLen))
}

// safeBytesAttr is the single sanctioned boundary for a []byte span
// attribute. Binary payloads cannot be meaningfully masked by the
// `key=value` regex in pkg/redaction; instead, we replace the value with a
// SHA256 hash + length, which preserves operator debugging (correlate by
// hash, compare lengths) without exposing the bytes themselves.
func safeBytesAttr(key string, b []byte) attribute.KeyValue {
	return attribute.String(key, redactedBytesValue(b))
}

func redactedBytesValue(v []byte) string {
	sum := sha256.Sum256(v)
	return fmt.Sprintf("[redacted bytes len=%d sha256=%s]", len(v), hex.EncodeToString(sum[:])[:16])
}

// RecordError adds an error event to the span. The error text is redacted via
// pkg/redaction.RedactError before being forwarded to the OTel SDK. This sink is
// the SOLE redaction point for span errors: callers (kernel/wrapper, saga,
// runtime/http/middleware, runtime/grpc/interceptor) pass the RAW error and must
// NOT pre-redact — the SPAN-RECORD-ERROR-SEAL-01 archtest locks every
// oteltrace.Span.RecordError callsite into this body with the redaction.RedactError
// argument form. There is no caller-side opt-out; dev/test surfaces that need raw
// error text read it from slog structured fields instead.
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(redaction.RedactError(err))
}

// SetStatus sets the span status. wrapper.StatusError maps to codes.Error;
// wrapper.StatusOK maps to codes.Ok.
func (s *otelSpan) SetStatus(code wrapper.StatusCode, description string) {
	if code == wrapper.StatusError {
		s.inner.SetStatus(codes.Error, description)
		return
	}
	s.inner.SetStatus(codes.Ok, "")
}

// TraceID returns the trace identifier.
func (s *otelSpan) TraceID() string {
	return s.inner.SpanContext().TraceID().String()
}

// SpanID returns the span identifier.
func (s *otelSpan) SpanID() string {
	return s.inner.SpanContext().SpanID().String()
}

// SetName updates the span's display name.
func (s *otelSpan) SetName(name string) {
	s.inner.SetName(name)
}
