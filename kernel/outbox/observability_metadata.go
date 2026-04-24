package outbox

// ref: ThreeDotsLabs/watermill message/message.go — Metadata map[string]string propagation
// Adopted: same key-value metadata model with whitelisted observability keys.
// Deviated: only bridge request_id/correlation_id/trace_id/traceparent (not
// full pass-through); standalone span_id is intentionally excluded because
// spans should not cross async boundaries.

import (
	"context"
	"maps"
	"strings"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// Observability metadata keys injected by MergeObservabilityMetadata and
// restored by ContextWithObservabilityMetadata. These keys are reserved —
// business code should avoid writing to them directly. Use
// IsReservedMetadataKey to check before setting custom metadata keys.
//
// Note: span_id is intentionally excluded. Spans represent a single unit of
// work and do not survive the async boundary; traceparent is the async parent
// carrier that lets the consumer create a new child span in the originating
// trace.
const (
	MetadataKeyTraceID       = "trace_id"
	MetadataKeyTraceParent   = "traceparent"
	MetadataKeyRequestID     = "request_id"
	MetadataKeyCorrelationID = "correlation_id"
)

// reservedMetadataKeys is the set of metadata keys managed by the
// observability bridge. Unexported to prevent external mutation.
var reservedMetadataKeys = map[string]struct{}{
	MetadataKeyTraceID:       {},
	MetadataKeyTraceParent:   {},
	MetadataKeyRequestID:     {},
	MetadataKeyCorrelationID: {},
}

// IsReservedMetadataKey reports whether key is reserved for observability.
func IsReservedMetadataKey(key string) bool {
	_, ok := reservedMetadataKeys[key]
	return ok
}

// MergeObservabilityMetadata copies whitelisted observability values from ctx
// into metadata. Existing non-empty metadata values win over context values.
func MergeObservabilityMetadata(ctx context.Context, metadata map[string]string) map[string]string {
	additions := make(map[string]string, 4)

	if requestID, ok := ctxkeys.RequestIDFrom(ctx); ok && requestID != "" {
		additions[MetadataKeyRequestID] = requestID
	}
	if correlationID, ok := ctxkeys.CorrelationIDFrom(ctx); ok && correlationID != "" {
		additions[MetadataKeyCorrelationID] = correlationID
	}
	if traceID, ok := ctxkeys.TraceIDFrom(ctx); ok && traceID != "" {
		additions[MetadataKeyTraceID] = traceID
	}
	if traceparent, ok := ctxkeys.TraceParentFrom(ctx); ok && validTraceParent(traceparent) {
		additions[MetadataKeyTraceParent] = traceparent
	} else if traceparent := traceParentFromContextIDs(ctx); traceparent != "" {
		additions[MetadataKeyTraceParent] = traceparent
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
	ctx = withTraceParentMetadata(ctx, metadata[MetadataKeyTraceParent])

	return ctx
}

// ObservabilityContextMiddleware restores observability metadata into the
// handler context before calling the next handler. This is the canonical
// injection point; the subscriber adapter does not perform restoration.
func ObservabilityContextMiddleware() SubscriptionMiddleware {
	return func(_ Subscription, next EntryHandler) EntryHandler {
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
	if len(value) > idutil.MaxMetadataIDLen || !idutil.IsSafeID(value) {
		return ctx
	}
	if existing, ok := getter(ctx); ok && existing != "" {
		return ctx
	}
	return setter(ctx, value)
}

func withTraceParentMetadata(ctx context.Context, value string) context.Context {
	if !validTraceParent(value) {
		return ctx
	}
	if existing, ok := ctxkeys.TraceParentFrom(ctx); ok && existing != "" {
		return ctx
	}

	ctx = ctxkeys.WithTraceParent(ctx, value)
	if existingTraceID, ok := ctxkeys.TraceIDFrom(ctx); !ok || existingTraceID == "" {
		ctx = ctxkeys.WithTraceID(ctx, traceIDFromTraceParent(value))
	}
	return ctx
}

func traceParentFromContextIDs(ctx context.Context) string {
	traceID, traceOK := ctxkeys.TraceIDFrom(ctx)
	spanID, spanOK := ctxkeys.SpanIDFrom(ctx)
	if !traceOK || !spanOK || !validW3CTraceID(traceID) || !validW3CSpanID(spanID) {
		return ""
	}
	return "00-" + strings.ToLower(traceID) + "-" + strings.ToLower(spanID) + "-01"
}

func validTraceParent(value string) bool {
	if len(value) != 55 {
		return false
	}
	if value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return false
	}
	version := value[0:2]
	flags := value[53:55]
	if version == "ff" || !isHex(version) || !isHex(flags) {
		return false
	}
	return validW3CTraceID(value[3:35]) && validW3CSpanID(value[36:52])
}

func traceIDFromTraceParent(value string) string {
	if !validTraceParent(value) {
		return ""
	}
	return value[3:35]
}

func validW3CTraceID(value string) bool {
	return len(value) == 32 && isHex(value) && !allZero(value)
}

func validW3CSpanID(value string) bool {
	return len(value) == 16 && isHex(value) && !allZero(value)
}

func isHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func allZero(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] != '0' {
			return false
		}
	}
	return true
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
