package otel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTracer_Insecure(t *testing.T) {
	// NewTracer with Insecure flag — exercises the insecure branch in tracer.go.
	// This will attempt to create an OTLP exporter to a non-existent endpoint,
	// but the exporter creation itself succeeds (connection is lazy).
	tracer, shutdown, err := NewTracer(context.Background(), TracerConfig{
		ServiceName:      "test-svc",
		ExporterEndpoint: "127.0.0.1:4317",
		Insecure:         true,
		SampleRate:       0.5,
	})
	require.NoError(t, err)
	require.NotNil(t, tracer)
	require.NotNil(t, shutdown)

	// Start a span to verify the tracer works.
	ctx, span := tracer.Start(context.Background(), "test-op")
	assert.NotNil(t, span)
	_ = ctx
	span.End()

	// Shutdown.
	assert.NoError(t, shutdown(context.Background()))
}

func TestNewTracer_SecureDefault(t *testing.T) {
	// Without Insecure flag (default TLS path), but targeting a non-TLS
	// endpoint. The exporter creation succeeds (lazy connect), but actual
	// span export would fail. We just verify the tracer is created.
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
