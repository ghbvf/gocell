package outbox

import (
	"context"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// Observability metadata keys injected by MergeObservabilityMetadata and
// restored by ContextWithObservabilityMetadata. These keys are reserved —
// business code should avoid writing to them directly. Use
// IsReservedMetadataKey to check before setting custom metadata keys.
const (
	MetadataKeyTraceID       = "trace_id"
	MetadataKeyRequestID     = "request_id"
	MetadataKeyCorrelationID = "correlation_id"
)

// ReservedMetadataKeys is the set of metadata keys managed by the
// observability bridge. Business code that sets these keys directly risks
// collision with framework-injected values. MergeObservabilityMetadata
// preserves existing values (business wins), but downstream consumers may
// misinterpret a business value as an observability ID.
var ReservedMetadataKeys = map[string]struct{}{
	MetadataKeyTraceID:       {},
	MetadataKeyRequestID:     {},
	MetadataKeyCorrelationID: {},
}

// IsReservedMetadataKey reports whether key is reserved for observability.
func IsReservedMetadataKey(key string) bool {
	_, ok := ReservedMetadataKeys[key]
	return ok
}

// MergeObservabilityMetadata copies whitelisted observability values from ctx
// into metadata. Existing non-empty metadata values win over context values.
func MergeObservabilityMetadata(ctx context.Context, metadata map[string]string) map[string]string {
	additions := make(map[string]string, 3)

	if requestID, ok := ctxkeys.RequestIDFrom(ctx); ok && requestID != "" {
		additions[MetadataKeyRequestID] = requestID
	}
	if correlationID, ok := ctxkeys.CorrelationIDFrom(ctx); ok && correlationID != "" {
		additions[MetadataKeyCorrelationID] = correlationID
	}
	if traceID, ok := ctxkeys.TraceIDFrom(ctx); ok && traceID != "" {
		additions[MetadataKeyTraceID] = traceID
	}

	if len(additions) == 0 {
		return metadata
	}

	merged := cloneMetadata(metadata)
	for key, value := range additions {
		if merged[key] == "" {
			merged[key] = value
		}
	}

	return merged
}

// ContextWithObservabilityMetadata restores whitelisted observability values
// from metadata into ctx. Existing non-empty context values win.
func ContextWithObservabilityMetadata(ctx context.Context, metadata map[string]string) context.Context {
	if metadata == nil {
		return ctx
	}

	ctx = withContextMetadata(ctx, metadata[MetadataKeyRequestID], ctxkeys.RequestIDFrom, ctxkeys.WithRequestID)
	ctx = withContextMetadata(ctx, metadata[MetadataKeyCorrelationID], ctxkeys.CorrelationIDFrom, ctxkeys.WithCorrelationID)
	ctx = withContextMetadata(ctx, metadata[MetadataKeyTraceID], ctxkeys.TraceIDFrom, ctxkeys.WithTraceID)

	return ctx
}

// ObservabilityContextMiddleware restores observability metadata into the
// handler context before calling the next handler.
func ObservabilityContextMiddleware() TopicHandlerMiddleware {
	return func(_ string, next EntryHandler) EntryHandler {
		return func(ctx context.Context, entry Entry) HandleResult {
			return next(ContextWithObservabilityMetadata(ctx, entry.Metadata), entry)
		}
	}
}

type contextValueGetter func(context.Context) (string, bool)
type contextValueSetter func(context.Context, string) context.Context

func withContextMetadata(
	ctx context.Context,
	value string,
	getter contextValueGetter,
	setter contextValueSetter,
) context.Context {
	if value == "" {
		return ctx
	}
	if existing, ok := getter(ctx); ok && existing != "" {
		return ctx
	}
	return setter(ctx, value)
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return make(map[string]string, 3)
	}
	cloned := make(map[string]string, len(metadata)+3)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
