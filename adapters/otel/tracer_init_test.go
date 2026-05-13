package otel

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Site-specific deadlines for shutdown-deadline probe tests. Kept here as
// package-level const per TEST-TIME-LITERAL-01 — none of these are
// cross-cutting enough to deserve a pkg/testutil/testtime entry.
const (
	shutdownProbeTimeout      = 100 * time.Millisecond
	shutdownProbeSlack        = 500 * time.Millisecond
	callerProbeTimeout        = 50 * time.Millisecond
	callerProbeOuterTimeout   = 5 * time.Second
	cleanShutdownProbeTimeout = 1 * time.Second
)

// saveOTelGlobals captures the OTel global TracerProvider and TextMapPropagator
// at call time and registers a t.Cleanup that restores them. Tests that touch
// the globals (B2-R-06 SetTracerProvider / SetTextMapPropagator inside NewTracer)
// MUST call this helper or they will leak state across the test process.
func saveOTelGlobals(t *testing.T) {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
}

func TestNewTracer_Insecure(t *testing.T) {
	saveOTelGlobals(t)
	// Insecure flag exercises the WithInsecure branch. The OTLP exporter
	// uses lazy dialing so no real connection to 127.0.0.1:4317 is made.
	// We do NOT exercise span emission here: with no live OTLP collector,
	// BSP shutdown can race against the 5s defaultShutdownTimeout
	// (~33% flake rate observed). Span emission contracts are covered by
	// tracer_test.go against an in-memory exporter; this test focuses on
	// constructor success + clean teardown.
	tracer, shutdown, err := NewTracer(context.Background(), TracerConfig{
		ServiceName:      "test-svc",
		ExporterEndpoint: "127.0.0.1:4317",
		Insecure:         true,
		SampleRate:       0.5,
	})
	require.NoError(t, err)
	require.NotNil(t, tracer)
	require.NotNil(t, shutdown)

	assert.NoError(t, shutdown(context.Background()))
}

func TestNewTracer_SecureDefault(t *testing.T) {
	saveOTelGlobals(t)
	// Without Insecure flag (default TLS path), but targeting a non-TLS
	// endpoint. The exporter creation succeeds (lazy connect); we verify
	// the tracer is created and shutdown unwinds cleanly when there is
	// nothing queued (no span emitted — see TestNewTracer_Insecure rationale).
	tracer, shutdown, err := NewTracer(context.Background(), TracerConfig{
		ServiceName:      "test-svc",
		ExporterEndpoint: "127.0.0.1:4317",
		SampleRate:       1.0,
	})
	require.NoError(t, err)
	require.NotNil(t, tracer)
	require.NotNil(t, shutdown)

	assert.NoError(t, shutdown(context.Background()))
}

func TestNewTracer_InvalidSampleRate(t *testing.T) {
	_, _, err := NewTracer(context.Background(), TracerConfig{
		ServiceName:      "test-svc",
		ExporterEndpoint: "127.0.0.1:4317",
		SampleRate:       2.0,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SampleRate")
}

// B2-R-06: NewTracer must register the constructed provider as the OTel
// global so auto-instrumented libraries (otelgrpc, otelhttp) see it.
func TestNewTracer_SetsGlobalTracerProvider(t *testing.T) {
	saveOTelGlobals(t)
	before := otel.GetTracerProvider()

	tracer, shutdown, err := NewTracer(context.Background(), TracerConfig{
		ServiceName:      "global-tp-svc",
		ExporterEndpoint: "127.0.0.1:4317",
		Insecure:         true,
	})
	require.NoError(t, err)
	require.NotNil(t, tracer)
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	after := otel.GetTracerProvider()
	assert.NotSame(t, before, after,
		"NewTracer must replace the OTel global TracerProvider")
}

// B2-R-06: composite propagator (W3C TraceContext + Baggage) must be
// installed so cross-process trace continuation and baggage propagation
// work in auto-instrumented libraries.
func TestNewTracer_SetsCompositePropagator(t *testing.T) {
	saveOTelGlobals(t)
	tracer, shutdown, err := NewTracer(context.Background(), TracerConfig{
		ServiceName:      "global-prop-svc",
		ExporterEndpoint: "127.0.0.1:4317",
		Insecure:         true,
	})
	require.NoError(t, err)
	require.NotNil(t, tracer)
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	prop := otel.GetTextMapPropagator()
	// CompositeTextMapPropagator.Fields() concatenates each member's Fields().
	// TraceContext returns ["traceparent","tracestate"], Baggage returns ["baggage"].
	fields := prop.Fields()
	assert.Contains(t, fields, "traceparent",
		"composite propagator must include W3C TraceContext")
	assert.Contains(t, fields, "baggage",
		"composite propagator must include W3C Baggage")
}

// B2-R-06: constructor errors (e.g., invalid SampleRate) must NOT mutate
// the OTel global. Registration happens only on the success path.
func TestNewTracer_ErrorDoesNotMutateGlobals(t *testing.T) {
	saveOTelGlobals(t)
	before := otel.GetTracerProvider()

	_, _, err := NewTracer(context.Background(), TracerConfig{
		ServiceName:      "bad-cfg",
		ExporterEndpoint: "127.0.0.1:4317",
		SampleRate:       2.0, // invalid → fail before SetTracerProvider
	})
	require.Error(t, err)

	assert.Same(t, before, otel.GetTracerProvider(),
		"failed NewTracer must not mutate global TracerProvider")
}

// B2-R-07: shutdown must respect a hard deadline so a stalled OTLP collector
// cannot turn shutdown into an indefinite hang.
//
// This test exercises shutdownTracerProvider directly with a stalling fake;
// the closure returned by NewTracer is a thin wrapper around it.
func TestShutdownTracerProvider_DeadlineEnforced(t *testing.T) {
	start := time.Now()
	err := shutdownTracerProvider(context.Background(), stallingShutdowner{}, shutdownProbeTimeout)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"shutdown beyond timeout must wrap context.DeadlineExceeded; got %v", err)
	assert.GreaterOrEqual(t, elapsed, shutdownProbeTimeout,
		"shutdown must wait at least the timeout before returning")
	assert.Less(t, elapsed, shutdownProbeTimeout+shutdownProbeSlack,
		"shutdown must not block significantly past the timeout; elapsed=%v", elapsed)
}

// B2-R-07: caller's own ctx deadline still applies — if caller passes a
// shorter deadline, it wins over defaultShutdownTimeout.
func TestShutdownTracerProvider_CallerDeadlinePreserved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), callerProbeTimeout)
	defer cancel()

	start := time.Now()
	err := shutdownTracerProvider(ctx, stallingShutdowner{}, callerProbeOuterTimeout)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Less(t, elapsed, shutdownProbeSlack,
		"caller's tighter deadline must take effect; elapsed=%v", elapsed)
}

// B2-R-07: clean shutdown (no stall) returns nil.
func TestShutdownTracerProvider_CleanReturnsNil(t *testing.T) {
	err := shutdownTracerProvider(context.Background(), noopShutdowner{}, cleanShutdownProbeTimeout)
	require.NoError(t, err)
}

// stallingShutdowner blocks until ctx is canceled — simulates an OTLP
// collector that never responds to flush.
type stallingShutdowner struct{}

func (stallingShutdowner) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

type noopShutdowner struct{}

func (noopShutdowner) Shutdown(context.Context) error { return nil }
