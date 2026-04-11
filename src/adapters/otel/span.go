package otel

import (
	"fmt"

	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Compile-time check: otelSpan implements tracing.Span.
var _ tracing.Span = (*otelSpan)(nil)

// otelSpan wraps an OTel trace.Span to implement the tracing.Span interface.
type otelSpan struct {
	inner oteltrace.Span
}

// End completes the span.
func (s *otelSpan) End() {
	s.inner.End()
}

// SetAttribute records a key-value pair on the span. It uses a type switch
// to map Go types to the correct OTel attribute constructors.
func (s *otelSpan) SetAttribute(key string, value any) {
	switch v := value.(type) {
	case string:
		s.inner.SetAttributes(attribute.String(key, v))
	case int:
		s.inner.SetAttributes(attribute.Int(key, v))
	case int64:
		s.inner.SetAttributes(attribute.Int64(key, v))
	case float64:
		s.inner.SetAttributes(attribute.Float64(key, v))
	case bool:
		s.inner.SetAttributes(attribute.Bool(key, v))
	default:
		s.inner.SetAttributes(attribute.String(key, fmt.Sprint(v)))
	}
}

// RecordError adds an error event to the span.
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(err)
}

// SetStatus sets the span status. isError=true marks the span as failed.
func (s *otelSpan) SetStatus(isError bool, description string) {
	if isError {
		s.inner.SetStatus(codes.Error, description)
	} else {
		s.inner.SetStatus(codes.Ok, "")
	}
}

// TraceID returns the trace identifier.
func (s *otelSpan) TraceID() string {
	return s.inner.SpanContext().TraceID().String()
}

// SpanID returns the span identifier.
func (s *otelSpan) SpanID() string {
	return s.inner.SpanContext().SpanID().String()
}
