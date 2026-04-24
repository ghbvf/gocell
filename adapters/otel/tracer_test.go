package otel

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spanIDs extracts trace/span ids from an otelSpan — replaces the removed
// TraceID()/SpanID() methods on the generic Span interface.
func spanIDs(t *testing.T, s tracing.Span) (string, string) {
	t.Helper()
	os, ok := s.(*otelSpan)
	require.True(t, ok, "expected *otelSpan, got %T", s)
	return os.TraceID(), os.SpanID()
}

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
	sTrace, sSpan := spanIDs(t, span)
	assert.NotEmpty(t, sTrace)
	assert.NotEmpty(t, sSpan)

	// Verify ctxkeys propagation.
	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, sTrace, traceID)

	spanID, ok := ctxkeys.SpanIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, sSpan, spanID)
}

func TestTracer_SetAttribute(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx := context.Background()

	_, span := tracer.Start(ctx, "attr-test")

	span.SetAttributes(
		wrapper.Attr{Key: "str_key", Value: "value"},
		wrapper.Attr{Key: "int_key", Value: 42},
		wrapper.Attr{Key: "int64_key", Value: int64(100)},
		wrapper.Attr{Key: "float_key", Value: 3.14},
		wrapper.Attr{Key: "bool_key", Value: true},
		wrapper.Attr{Key: "bytes_key", Value: []byte("bytes")},
		wrapper.Attr{Key: "fallback_key", Value: struct{ X int }{X: 1}},
	)

	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	// Build attribute map for precise assertions.
	got := make(map[string]string, len(spans[0].Attributes))
	for _, kv := range spans[0].Attributes {
		got[string(kv.Key)] = kv.Value.Emit()
	}

	// A6-d: []byte must emit as readable UTF-8, not fmt.Sprint's decimal
	// byte-slice form ("[98 121 116 101 115]").
	assert.Equal(t, "bytes", got["bytes_key"],
		"[]byte attributes must emit as UTF-8 string, got %q", got["bytes_key"])
	// fallback_key (unknown type) still flows through fmt.Sprint.
	assert.Equal(t, "{1}", got["fallback_key"])
	assert.Equal(t, "value", got["str_key"])
	assert.Equal(t, "42", got["int_key"])
	assert.Equal(t, "100", got["int64_key"])
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
	pTrace, _ := spanIDs(t, parentSpan)
	cTrace, _ := spanIDs(t, childSpan)
	assert.Equal(t, pTrace, cTrace)
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
	sTrace, sSpan := spanIDs(t, span)
	assert.Equal(t, parentTraceID.String(), sTrace)
	assert.NotEqual(t, parentSpanID.String(), sSpan)

	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, parentTraceID.String(), traceID)

	spanID, ok := ctxkeys.SpanIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, sSpan, spanID)
}

// TestTracer_IngressPropagation_OTel proves the full HTTP ingress path:
//
//	traceparent header → extractTraceContext → OTel Tracer.Start
//
// This is the P1 regression test that catches breakage when either the
// middleware extraction helper or the OTel adapter changes parent-
// continuation semantics independently. Existing tests only cover each
// half (helper unit tests and OTel remote-parent unit tests); this test
// binds them into a single contract.
func TestTracer_IngressPropagation_OTel(t *testing.T) {
	tracer, exporter := newTestTracer(t)

	var gotTraceID string
	var gotSpanID string

	handler := middleware.Tracing(tracer)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var ok bool
			gotTraceID, ok = ctxkeys.TraceIDFrom(r.Context())
			require.True(t, ok)
			gotSpanID, ok = ctxkeys.SpanIDFrom(r.Context())
			require.True(t, ok)
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/otel-e2e", nil)
	req.Header.Set("traceparent",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// trace_id must continue the upstream trace.
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotTraceID,
		"OTel tracer must continue the upstream trace_id from traceparent")

	// span_id must be a fresh server span, not the upstream parent span.
	assert.NotEqual(t, "00f067aa0ba902b7", gotSpanID,
		"OTel tracer must create a fresh span_id for the server span")

	// Verify in the OTel exporter that the span was actually recorded
	// with the correct upstream trace ID.
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736",
		spans[0].SpanContext.TraceID().String())
}

// TestTracer_IngressPropagation_OTel_B3Single proves the B3 single-header
// fallback path through the full HTTP ingress chain:
//
//	b3 header → extractTraceContext (W3C miss → B3 fallback) → OTel Tracer.Start
//
// Without this test, a regression that drops remote span context from the B3
// branch would only be caught by the helper unit test, not by the OTel
// adapter integration.
func TestTracer_IngressPropagation_OTel_B3Single(t *testing.T) {
	tracer, exporter := newTestTracer(t)

	var gotTraceID string
	var gotSpanID string

	handler := middleware.Tracing(tracer)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var ok bool
			gotTraceID, ok = ctxkeys.TraceIDFrom(r.Context())
			require.True(t, ok)
			gotSpanID, ok = ctxkeys.SpanIDFrom(r.Context())
			require.True(t, ok)
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/otel-b3-single", nil)
	req.Header.Set("b3",
		"4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotTraceID,
		"OTel tracer must continue trace_id from B3 single header")
	assert.NotEqual(t, "00f067aa0ba902b7", gotSpanID,
		"OTel tracer must create a fresh span_id for the server span")

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736",
		spans[0].SpanContext.TraceID().String())
}

// TestTracer_IngressPropagation_OTel_B3Multi proves the B3 multi-header
// fallback path through the full HTTP ingress chain:
//
//	X-B3-TraceId/X-B3-SpanId → extractTraceContext → OTel Tracer.Start
func TestTracer_IngressPropagation_OTel_B3Multi(t *testing.T) {
	tracer, exporter := newTestTracer(t)

	var gotTraceID string
	var gotSpanID string

	handler := middleware.Tracing(tracer)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var ok bool
			gotTraceID, ok = ctxkeys.TraceIDFrom(r.Context())
			require.True(t, ok)
			gotSpanID, ok = ctxkeys.SpanIDFrom(r.Context())
			require.True(t, ok)
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/otel-b3-multi", nil)
	req.Header.Set("X-B3-TraceId", "4bf92f3577b34da6a3ce929d0e0e4736")
	req.Header.Set("X-B3-SpanId", "00f067aa0ba902b7")
	req.Header.Set("X-B3-Sampled", "1")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotTraceID,
		"OTel tracer must continue trace_id from B3 multi headers")
	assert.NotEqual(t, "00f067aa0ba902b7", gotSpanID,
		"OTel tracer must create a fresh span_id for the server span")

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736",
		spans[0].SpanContext.TraceID().String())
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
	var _ wrapper.SpanRenamer = (*otelSpan)(nil)
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

func TestSpanHelper_SimpleSpanAcceptsAll(t *testing.T) {
	// simpleSpan now implements the full wrapper.Span interface — helpers
	// just delegate; assert they pass through without panic.
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
