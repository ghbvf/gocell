package outbox

import (
	"context"
	"fmt"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/idutil"
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
	ctx = ctxkeys.WithTraceParent(ctx, "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")

	got := MergeObservabilityMetadata(ctx, map[string]string{
		"request_id":  "req-explicit",
		"trace_id":    "trace-explicit",
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})

	require.NotNil(t, got)
	assert.Equal(t, "req-explicit", got["request_id"])
	assert.Equal(t, "trace-explicit", got["trace_id"])
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", got["traceparent"])
	assert.Equal(t, "corr-from-ctx", got["correlation_id"])
}

func TestMergeObservabilityMetadata_FillsMissingOrEmptyReservedKeys(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-from-ctx")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-from-ctx")
	ctx = ctxkeys.WithTraceID(ctx, "trace-from-ctx")

	got := MergeObservabilityMetadata(ctx, map[string]string{
		"request_id":     "",
		"correlation_id": "corr-explicit",
		"source":         "worker",
	})

	require.NotNil(t, got)
	assert.Equal(t, "req-from-ctx", got["request_id"])
	assert.Equal(t, "corr-explicit", got["correlation_id"])
	assert.Equal(t, "trace-from-ctx", got["trace_id"])
	assert.Equal(t, "worker", got["source"])
}

func TestMergeObservabilityMetadata_BuildsTraceParentFromTraceAndSpan(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")
	ctx = ctxkeys.WithSpanID(ctx, "00f067aa0ba902b7")

	got := MergeObservabilityMetadata(ctx, nil)

	require.NotNil(t, got)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", got["trace_id"])
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", got["traceparent"])
	_, hasSpanID := got["span_id"]
	assert.False(t, hasSpanID, "span_id is not propagated as standalone metadata")
}

func TestMergeObservabilityMetadata_UsesContextTraceParentWhenPresent(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithTraceID(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	ctx = ctxkeys.WithSpanID(ctx, "bbbbbbbbbbbbbbbb")
	ctx = ctxkeys.WithTraceParent(ctx, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	got := MergeObservabilityMetadata(ctx, nil)

	require.NotNil(t, got)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", got["traceparent"])
}

func TestMergeObservabilityMetadata_NoObservabilityValuesReturnsOriginalMetadata(t *testing.T) {
	assert.Nil(t, MergeObservabilityMetadata(context.Background(), nil))

	metadata := map[string]string{"source": "worker"}
	got := MergeObservabilityMetadata(context.Background(), metadata)
	require.NotNil(t, got)
	assert.Equal(t, metadata, got)
	assert.Equal(t, "worker", got["source"])
}

func TestContextWithObservabilityMetadata_RestoresWhitelistedValues(t *testing.T) {
	ctx := ContextWithObservabilityMetadata(context.Background(), map[string]string{
		"request_id":     "req-456",
		"correlation_id": "corr-456",
		"trace_id":       "trace-456",
		"traceparent":    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
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

	traceParent, ok := ctxkeys.TraceParentFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", traceParent)

	_, ok = ctxkeys.SpanIDFrom(ctx)
	assert.False(t, ok, "span_id must not be restored across the async boundary")
}

func TestContextWithObservabilityMetadata_TraceParentSeedsTraceIDWhenMissing(t *testing.T) {
	ctx := ContextWithObservabilityMetadata(context.Background(), map[string]string{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})

	traceParent, ok := ctxkeys.TraceParentFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", traceParent)

	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", traceID)
}

func TestContextWithObservabilityMetadata_PreservesExistingContextValues(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-existing")
	ctx = ctxkeys.WithTraceID(ctx, "trace-existing")

	ctx = ContextWithObservabilityMetadata(ctx, map[string]string{
		"request_id":     "req-from-metadata",
		"correlation_id": "corr-from-metadata",
		"trace_id":       "trace-from-metadata",
	})

	requestID, ok := ctxkeys.RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-existing", requestID)

	correlationID, ok := ctxkeys.CorrelationIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "corr-from-metadata", correlationID)

	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "trace-existing", traceID)
}

func TestContextWithObservabilityMetadata_NilMetadataNoOp(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-existing")

	restored := ContextWithObservabilityMetadata(ctx, nil)
	requestID, ok := ctxkeys.RequestIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "req-existing", requestID)
	_, ok = ctxkeys.TraceIDFrom(restored)
	assert.False(t, ok)
}

func TestContextWithObservabilityMetadata_RejectsUnsafeValues(t *testing.T) {
	ctx := ContextWithObservabilityMetadata(context.Background(), map[string]string{
		"request_id":     "req-safe-1",
		"correlation_id": "has spaces",
		"trace_id":       "has\nnewline",
		"traceparent":    "00-not-a-valid-trace-id-00f067aa0ba902b7-01",
	})

	requestID, ok := ctxkeys.RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-safe-1", requestID, "safe value should be restored")

	_, ok = ctxkeys.CorrelationIDFrom(ctx)
	assert.False(t, ok, "unsafe value with spaces should be rejected")

	_, ok = ctxkeys.TraceIDFrom(ctx)
	assert.False(t, ok, "unsafe value with newlines should be rejected")

	_, ok = ctxkeys.TraceParentFrom(ctx)
	assert.False(t, ok, "invalid traceparent should be rejected")
}

func TestContextWithObservabilityMetadata_RejectsEmptyValues(t *testing.T) {
	ctx := ContextWithObservabilityMetadata(context.Background(), map[string]string{
		"request_id":     "",
		"correlation_id": "corr-ok",
	})
	_, ok := ctxkeys.RequestIDFrom(ctx)
	assert.False(t, ok, "empty metadata value must not be written to context")
	corrID, ok := ctxkeys.CorrelationIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "corr-ok", corrID)
}

func TestContextWithObservabilityMetadata_RejectsOverlongValues(t *testing.T) {
	longID := make([]byte, 300)
	for i := range longID {
		longID[i] = 'a'
	}
	ctx := ContextWithObservabilityMetadata(context.Background(), map[string]string{
		"request_id": string(longID),
	})
	_, ok := ctxkeys.RequestIDFrom(ctx)
	assert.False(t, ok, "overlong value should be rejected")
}

func TestIsReservedMetadataKey(t *testing.T) {
	assert.True(t, IsReservedMetadataKey("request_id"))
	assert.True(t, IsReservedMetadataKey("correlation_id"))
	assert.True(t, IsReservedMetadataKey("trace_id"))
	assert.True(t, IsReservedMetadataKey("traceparent"))
	assert.False(t, IsReservedMetadataKey("source"))
	assert.False(t, IsReservedMetadataKey("topic"))
	assert.False(t, IsReservedMetadataKey(""))
}

func TestObservabilityContextMiddleware_RestoresHandlerContext(t *testing.T) {
	mw := ObservabilityContextMiddleware()

	wrapped := mw(Subscription{Topic: "event.test.v1"}, func(ctx context.Context, _ Entry) HandleResult {
		requestID, ok := ctxkeys.RequestIDFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "req-789", requestID)

		correlationID, ok := ctxkeys.CorrelationIDFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "corr-789", correlationID)

		traceID, ok := ctxkeys.TraceIDFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "trace-789", traceID)

		traceParent, ok := ctxkeys.TraceParentFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", traceParent)

		return HandleResult{Disposition: DispositionAck}
	})

	res := wrapped(context.Background(), Entry{
		ID: "evt-789",
		Metadata: map[string]string{
			"request_id":     "req-789",
			"correlation_id": "corr-789",
			"trace_id":       "trace-789",
			"traceparent":    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		},
	})

	assert.Equal(t, DispositionAck, res.Disposition)
}

func TestCloneMetadata_NilReturnsEmptyMap(t *testing.T) {
	got := CloneMetadata(nil)
	require.NotNil(t, got, "nil input must return a fresh non-nil map so callers can write unconditionally")
	assert.Empty(t, got)
}

func TestCloneMetadata_EmptyMap(t *testing.T) {
	got := CloneMetadata(map[string]string{})
	require.NotNil(t, got)
	assert.Empty(t, got)
}

func TestCloneMetadata_DeepCopy(t *testing.T) {
	src := map[string]string{
		"request_id": "req-abc",
		"custom":     "value",
	}
	got := CloneMetadata(src)
	assert.Equal(t, src, got)

	// Mutating the clone must not affect src — defensive-copy contract.
	got["request_id"] = "mutated"
	got["new-key"] = "new-value"
	assert.Equal(t, "req-abc", src["request_id"], "source must be isolated from clone mutations")
	_, ok := src["new-key"]
	assert.False(t, ok, "source must not gain keys added to clone")
}

func TestCloneMetadata_MutatingSourceDoesNotAffectClone(t *testing.T) {
	src := map[string]string{"k": "v"}
	got := CloneMetadata(src)
	src["k"] = "mutated"
	src["added"] = "v2"
	assert.Equal(t, "v", got["k"], "clone must be isolated from source mutations")
	_, ok := got["added"]
	assert.False(t, ok)
}

func TestEntryID_RoundTrip_MetadataContextExtraction(t *testing.T) {
	entryID := NewEntryID()
	ctx := ctxkeys.WithRequestID(context.Background(), entryID)
	metadata := MergeObservabilityMetadata(ctx, nil)
	restored := ContextWithObservabilityMetadata(context.Background(), metadata)
	got, ok := ctxkeys.RequestIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, entryID, got)
	assert.True(t, idutil.IsSafeID(got))
}

// ExampleIsReservedMetadataKey demonstrates checking custom metadata keys
// against the observability-reserved set before writing. Writing to a
// reserved key would be overwritten by MergeObservabilityMetadata during
// broker publish, so callers should use IsReservedMetadataKey as a guard
// or pick a business-specific prefix.
func ExampleIsReservedMetadataKey() {
	keys := []string{"trace_id", "traceparent", "request_id", "correlation_id", "tenant_id", "actor"}
	for _, k := range keys {
		fmt.Printf("%s reserved=%v\n", k, IsReservedMetadataKey(k))
	}
	// Output:
	// trace_id reserved=true
	// traceparent reserved=true
	// request_id reserved=true
	// correlation_id reserved=true
	// tenant_id reserved=false
	// actor reserved=false
}
