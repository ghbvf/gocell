package outbox

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeObservabilityMetadata_FromContext(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-123")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-123")
	ctx = ctxkeys.WithTraceID(ctx, "trace-123")

	got := MergeObservabilityMetadata(ctx, map[string]string{"source": "http"})

	require.NotNil(t, got)
	assert.Equal(t, "http", got["source"])
	assert.Equal(t, "req-123", got["request_id"])
	assert.Equal(t, "corr-123", got["correlation_id"])
	assert.Equal(t, "trace-123", got["trace_id"])
}

func TestMergeObservabilityMetadata_PreservesExplicitValues(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-from-ctx")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-from-ctx")
	ctx = ctxkeys.WithTraceID(ctx, "trace-from-ctx")

	got := MergeObservabilityMetadata(ctx, map[string]string{
		"request_id": "req-explicit",
		"trace_id":   "trace-explicit",
	})

	require.NotNil(t, got)
	assert.Equal(t, "req-explicit", got["request_id"])
	assert.Equal(t, "trace-explicit", got["trace_id"])
	assert.Equal(t, "corr-from-ctx", got["correlation_id"])
}

func TestContextWithObservabilityMetadata_RestoresWhitelistedValues(t *testing.T) {
	ctx := ContextWithObservabilityMetadata(context.Background(), map[string]string{
		"request_id":     "req-456",
		"correlation_id": "corr-456",
		"trace_id":       "trace-456",
		"span_id":        "span-ignored",
	})

	requestID, ok := ctxkeys.RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-456", requestID)

	correlationID, ok := ctxkeys.CorrelationIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "corr-456", correlationID)

	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "trace-456", traceID)

	_, ok = ctxkeys.SpanIDFrom(ctx)
	assert.False(t, ok, "span_id must not be restored across the async boundary")
}

func TestObservabilityContextMiddleware_RestoresHandlerContext(t *testing.T) {
	mw := ObservabilityContextMiddleware()

	wrapped := mw("event.test.v1", func(ctx context.Context, _ Entry) HandleResult {
		requestID, ok := ctxkeys.RequestIDFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "req-789", requestID)

		correlationID, ok := ctxkeys.CorrelationIDFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "corr-789", correlationID)

		traceID, ok := ctxkeys.TraceIDFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "trace-789", traceID)

		return HandleResult{Disposition: DispositionAck}
	})

	res := wrapped(context.Background(), Entry{
		ID: "evt-789",
		Metadata: map[string]string{
			"request_id":     "req-789",
			"correlation_id": "corr-789",
			"trace_id":       "trace-789",
		},
	})

	assert.Equal(t, DispositionAck, res.Disposition)
}