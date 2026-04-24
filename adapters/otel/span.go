package otel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Compile-time checks: otelSpan implements tracing.Span (aliased to
// wrapper.Span) and wrapper.SpanRenamer.
var (
	_ tracing.Span        = (*otelSpan)(nil)
	_ wrapper.SpanRenamer = (*otelSpan)(nil)
)

// otelSpan wraps an OTel trace.Span to implement the kernel/wrapper.Span
// interface (re-exported as tracing.Span for backwards-compatible callers).
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
		return attribute.String(a.Key, v)
	case int:
		return attribute.Int(a.Key, v)
	case int64:
		return attribute.Int64(a.Key, v)
	case float64:
		return attribute.Float64(a.Key, v)
	case bool:
		return attribute.Bool(a.Key, v)
	case []byte:
		return attribute.String(a.Key, redactedBytesValue(v))
	default:
		return attribute.String(a.Key, fmt.Sprint(v))
	}
}

func redactedBytesValue(v []byte) string {
	sum := sha256.Sum256(v)
	return fmt.Sprintf("[redacted bytes len=%d sha256=%s]", len(v), hex.EncodeToString(sum[:])[:16])
}

// RecordError adds an error event to the span.
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(err)
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
