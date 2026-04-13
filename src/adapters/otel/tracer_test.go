package otel

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTracer creates a Tracer backed by an in-memory span exporter for testing.
func newTestTracer(t *testing.T) (*Tracer, *tracetest.InMemoryExporter) {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})

	return &Tracer{inner: tp.Tracer("test-service")}, exporter
}

func TestTracer_ImplementsInterface(t *testing.T) {
	tracer, _ := newTestTracer(t)
	var _ tracing.Tracer = tracer
}

func TestTracer_StartCreatesSpan(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx := context.Background()

	ctx, span := tracer.Start(ctx, "test-operation")
	require.NotNil(t, span)

	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "test-operation", spans[0].Name)

	// Verify trace/span IDs are non-empty.
	assert.NotEmpty(t, span.TraceID())
	assert.NotEmpty(t, span.SpanID())

	// Verify ctxkeys propagation.
	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, span.TraceID(), traceID)

	spanID, ok := ctxkeys.SpanIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, span.SpanID(), spanID)
}

func TestTracer_SetAttribute(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx := context.Background()

	_, span := tracer.Start(ctx, "attr-test")

	span.SetAttribute("str_key", "value")
	span.SetAttribute("int_key", 42)
	span.SetAttribute("int64_key", int64(100))
	span.SetAttribute("float_key", 3.14)
	span.SetAttribute("bool_key", true)
	span.SetAttribute("fallback_key", []byte("bytes"))

	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	// Verify attributes were set (at least one).
	attrs := spans[0].Attributes
	assert.NotEmpty(t, attrs, "span should have attributes")
}

func TestTracer_NestedSpansShareTraceID(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx := context.Background()

	ctx, parentSpan := tracer.Start(ctx, "parent")
	_, childSpan := tracer.Start(ctx, "child")

	childSpan.End()
	parentSpan.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)

	// Both spans should share the same trace ID.
	assert.Equal(t, parentSpan.TraceID(), childSpan.TraceID())
}

func TestTracer_StartContinuesRemoteParent(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	parentTraceID := mustTraceID(t, "4bf92f3577b34da6a3ce929d0e0e4736")
	parentSpanID := mustSpanID(t, "00f067aa0ba902b7")
	ctx := oteltrace.ContextWithRemoteSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    parentTraceID,
		SpanID:     parentSpanID,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	}))

	ctx, span := tracer.Start(ctx, "server")
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, parentTraceID.String(), span.TraceID())
	assert.NotEqual(t, parentSpanID.String(), span.SpanID())

	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, parentTraceID.String(), traceID)

	spanID, ok := ctxkeys.SpanIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, span.SpanID(), spanID)
}

func TestNewTracer_MissingServiceName(t *testing.T) {
	_, _, err := NewTracer(context.Background(), TracerConfig{
		ExporterEndpoint: "localhost:4317",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ServiceName")
}

func TestNewTracer_MissingEndpoint(t *testing.T) {
	_, _, err := NewTracer(context.Background(), TracerConfig{
		ServiceName: "test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ExporterEndpoint")
}

func TestTracerConfig_Defaults(t *testing.T) {
	// Zero value → default 1.0.
	cfg := TracerConfig{}
	cfg.defaults()
	assert.Equal(t, 1.0, cfg.SampleRate)

	// Explicit value preserved.
	cfg2 := TracerConfig{SampleRate: 0.5}
	cfg2.defaults()
	assert.Equal(t, 0.5, cfg2.SampleRate)

	// DisableSampling forces 0.
	cfg3 := TracerConfig{SampleRate: 0.8, DisableSampling: true}
	cfg3.defaults()
	assert.Equal(t, 0.0, cfg3.SampleRate)
}

func TestTracerConfig_Validate(t *testing.T) {
	// Valid cases.
	assert.NoError(t, (&TracerConfig{}).validate())                      // zero = default
	assert.NoError(t, (&TracerConfig{SampleRate: 0.5}).validate())       // in range
	assert.NoError(t, (&TracerConfig{SampleRate: 1.0}).validate())       // boundary
	assert.NoError(t, (&TracerConfig{DisableSampling: true}).validate()) // disable

	// Invalid cases: out of range → error.
	assert.Error(t, (&TracerConfig{SampleRate: -0.5}).validate())
	assert.Error(t, (&TracerConfig{SampleRate: 2.0}).validate())
}

func TestSpan_ImplementsInterface(t *testing.T) {
	var _ tracing.Span = (*otelSpan)(nil)
	var _ tracing.SpanRecorder = (*otelSpan)(nil)
}

func TestSpan_RecordError(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx := context.Background()

	_, span := tracer.Start(ctx, "error-op")
	tracing.SpanRecordError(span, errors.New("connection refused"))
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	events := spans[0].Events
	require.NotEmpty(t, events, "RecordError should add an event to the span")
	assert.Equal(t, "exception", events[0].Name)
}

func TestSpan_SetStatus_Error(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx := context.Background()

	_, span := tracer.Start(ctx, "err-status")
	tracing.SpanSetStatus(span, true, "db connection failed")
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, otelcodes.Error, spans[0].Status.Code)
	assert.Equal(t, "db connection failed", spans[0].Status.Description)
}

func TestSpan_SetStatus_Ok(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx := context.Background()

	_, span := tracer.Start(ctx, "ok-status")
	tracing.SpanSetStatus(span, false, "")
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, otelcodes.Ok, spans[0].Status.Code)
}

func TestSpanHelper_NonRecorder(t *testing.T) {
	// simpleSpan does not implement SpanRecorder — helpers must not panic.
	simple := tracing.NewTracer("test")
	_, span := simple.Start(context.Background(), "op")
	defer span.End()

	assert.NotPanics(t, func() {
		tracing.SpanRecordError(span, errors.New("some error"))
	})
	assert.NotPanics(t, func() {
		tracing.SpanSetStatus(span, true, "fail")
	})
}

func mustTraceID(t *testing.T, hexValue string) oteltrace.TraceID {
	t.Helper()

	bytes, err := hex.DecodeString(hexValue)
	require.NoError(t, err)
	require.Len(t, bytes, len(oteltrace.TraceID{}))

	var id oteltrace.TraceID
	copy(id[:], bytes)
	return id
}

func mustSpanID(t *testing.T, hexValue string) oteltrace.SpanID {
	t.Helper()

	bytes, err := hex.DecodeString(hexValue)
	require.NoError(t, err)
	require.Len(t, bytes, len(oteltrace.SpanID{}))

	var id oteltrace.SpanID
	copy(id[:], bytes)
	return id
}
