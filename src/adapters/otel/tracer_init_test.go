package otel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTracer_Insecure(t *testing.T) {
	// Insecure flag exercises the WithInsecure branch. The OTLP exporter
	// uses lazy dialing so no real connection to 127.0.0.1:4317 is made.
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
	_, span := tracer.Start(context.Background(), "test-op")
	assert.NotNil(t, span)
	span.End()

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
