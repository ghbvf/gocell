package outbox

// ref: ThreeDotsLabs/watermill message/message.go — Metadata map[string]string propagation
// Adopted: same key-value metadata model with whitelisted observability keys.
// Deviated: only bridge request_id/correlation_id/trace_id (not full pass-through);
// span_id is intentionally excluded because spans should not cross async boundaries.

import (
	"context"
	"maps"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// Observability metadata keys injected by MergeObservabilityMetadata and
// restored by ContextWithObservabilityMetadata. These keys are reserved —
// business code should avoid writing to them directly. Use
// IsReservedMetadataKey to check before setting custom metadata keys.
//
// Note: span_id is intentionally excluded. Spans represent a single unit of
// work and do not survive the async boundary; the consumer should start a new
// span linked to the originating trace_id.
const (
	MetadataKeyTraceID       = "trace_id"
	MetadataKeyRequestID     = "request_id"
	MetadataKeyCorrelationID = "correlation_id"
)

// reservedMetadataKeys is the set of metadata keys managed by the
// observability bridge. Unexported to prevent external mutation.
var reservedMetadataKeys = map[string]struct{}{
	MetadataKeyTraceID:       {},
	MetadataKeyRequestID:     {},
	MetadataKeyCorrelationID: {},
}

// IsReservedMetadataKey reports whether key is reserved for observability.
func IsReservedMetadataKey(key string) bool {
	_, ok := reservedMetadataKeys[key]
	return ok
}

// maxObservabilityIDLen is the maximum length for an observability ID restored
// from broker metadata. Values exceeding this limit are silently dropped to
// prevent resource exhaustion from malformed messages.
const maxObservabilityIDLen = 256

// isSafeObservabilityID checks that a metadata value contains only safe
// characters for observability IDs: ASCII letters, digits, and ._:/-
// This mirrors the HTTP-side isSafeID validation in the request_id middleware.
func isSafeObservabilityID(s string) bool {
	if len(s) == 0 || len(s) > maxObservabilityIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '/' || c == '-':
		default:
			return false
		}
	}
	return true
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

	merged := CloneMetadata(metadata)
	for key, value := range additions {
		if merged[key] == "" {
			merged[key] = value
		}
	}

	return merged
}

// ContextWithObservabilityMetadata restores whitelisted observability values
// from metadata into ctx. Values that fail safety validation (non-ASCII,
// control chars, excessive length) are silently dropped. Existing non-empty
// context values win.
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
// handler context before calling the next handler. This is the canonical
// injection point; the subscriber adapter does not perform restoration.
func ObservabilityContextMiddleware() TopicHandlerMiddleware {
	return func(_ string, next EntryHandler) EntryHandler {
		return func(ctx context.Context, entry Entry) HandleResult {
			return next(ContextWithObservabilityMetadata(ctx, entry.Metadata), entry)
		}
	}
}

// contextValueGetter extracts a string value from context.
type contextValueGetter func(context.Context) (string, bool)

// contextValueSetter stores a string value in context.
type contextValueSetter func(context.Context, string) context.Context

func withContextMetadata(
	ctx context.Context,
	value string,
	getter contextValueGetter,
	setter contextValueSetter,
) context.Context {
	if !isSafeObservabilityID(value) {
		return ctx
	}
	if existing, ok := getter(ctx); ok && existing != "" {
		return ctx
	}
	return setter(ctx, value)
}

// CloneMetadata returns an independent copy of metadata so callers can
// mutate the result without affecting the source. Nil input returns a
// freshly allocated empty map, which lets callers write unconditionally
// (no nil guard at every write site).
//
// The result has capacity for three extra keys so the common pattern of
// merging observability IDs on top does not reallocate.
//
// Concurrency: CloneMetadata is safe for concurrent use. The returned map
// is not — callers own it fully and are responsible for any further
// synchronization.
//
// Use this before handing metadata to downstream code that may mutate it
// (e.g., MergeObservabilityMetadata, test assertions that cache snapshots).
func CloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return make(map[string]string, 3)
	}
	cloned := make(map[string]string, len(metadata)+3)
	maps.Copy(cloned, metadata)
	return cloned
}
