package outbox

// ref: OpenTelemetry SpanContext -- typed carrier of trace identity across
// transport boundaries, kept distinct from application attributes.
// Adopted: separate struct for system-owned fields vs. producer-owned Metadata map.
// Deviated: only 4 fields (no sampled flag, no traceFlags struct) to keep the
// async boundary narrow; TraceParent is the W3C canonical form.

import (
	"context"
	"strings"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// ObservabilityMetadata carries cross-async tracing context that the
// gocell observability bridge owns. Producers MUST NOT populate these
// fields directly — the writer bridge (InjectObservabilityFromContext)
// fills them from context at persistence time. Consumer middleware
// (RestoreToContext) reads them back into handler context.
//
// The typed field replaces the pre-PR246-FU1 string-key metadata bridge
// (Merge*/IsReserved*) which allowed producers to forge reserved keys
// via entry.Metadata["trace_id"] = "evil" before the observability
// layer got a chance to overwrite them. With a typed field the forgery
// surface is removed by construction — the observability system and
// the business metadata map no longer share a key namespace.
type ObservabilityMetadata struct {
	TraceID       string `json:"traceId,omitempty"`
	TraceParent   string `json:"traceParent,omitempty"`
	RequestID     string `json:"requestId,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
}

// IsZero reports whether all fields are empty.
func (o ObservabilityMetadata) IsZero() bool {
	return o.TraceID == "" && o.TraceParent == "" &&
		o.RequestID == "" && o.CorrelationID == ""
}

// ContextObservability reads reserved observability values from ctx and
// returns a populated ObservabilityMetadata. Missing keys stay empty.
// Falls back to a synthesized W3C traceparent from trace_id+span_id
// when ctx has no explicit traceparent (preserves pre-FU1 semantics).
func ContextObservability(ctx context.Context) ObservabilityMetadata {
	var o ObservabilityMetadata

	if requestID, ok := ctxkeys.RequestIDFrom(ctx); ok && requestID != "" {
		o.RequestID = requestID
	}
	if correlationID, ok := ctxkeys.CorrelationIDFrom(ctx); ok && correlationID != "" {
		o.CorrelationID = correlationID
	}
	if traceID, ok := ctxkeys.TraceIDFrom(ctx); ok && traceID != "" {
		o.TraceID = traceID
	}
	if traceparent, ok := ctxkeys.TraceParentFrom(ctx); ok && validTraceParent(traceparent) {
		o.TraceParent = traceparent
	} else if tp := traceParentFromContextIDs(ctx); tp != "" {
		o.TraceParent = tp
	}

	return o
}

// RestoreToContext returns a new context populated with the non-empty
// fields of o, subject to existing ctx values winning (idempotent
// restore — matches pre-FU1 consumer semantics). Values that fail
// safety validation (overlong, unsafe chars, invalid traceparent)
// are silently dropped.
func (o ObservabilityMetadata) RestoreToContext(ctx context.Context) context.Context {
	ctx = withContextMetadata(ctx, o.RequestID, ctxkeys.RequestIDFrom, ctxkeys.WithRequestID)
	ctx = withContextMetadata(ctx, o.CorrelationID, ctxkeys.CorrelationIDFrom, ctxkeys.WithCorrelationID)
	ctx = withContextMetadata(ctx, o.TraceID, ctxkeys.TraceIDFrom, ctxkeys.WithTraceID)
	ctx = withTraceParentMetadata(ctx, o.TraceParent)
	return ctx
}

// InjectObservabilityFromContext populates e.Observability from ctx.
// The writer bridge calls this right before persistence so the entry
// carries the originating context's trace/request/correlation identity
// across the async boundary. Idempotent; overwrites any prior value.
func (e *Entry) InjectObservabilityFromContext(ctx context.Context) {
	e.Observability = ContextObservability(ctx)
}

// ObservabilityContextMiddleware restores entry.Observability into the
// handler context before calling next. Idempotent — existing non-empty
// ctx values win.
func ObservabilityContextMiddleware() SubscriptionMiddleware {
	return func(_ Subscription, next EntryHandler) EntryHandler {
		return func(ctx context.Context, entry Entry) HandleResult {
			return next(entry.Observability.RestoreToContext(ctx), entry)
		}
	}
}

// CloneMetadata returns an independent copy of metadata so callers can
// mutate the result without affecting the source. Nil input returns a
// freshly allocated empty map, which lets callers write unconditionally
// (no nil guard at every write site).
//
// The result has capacity for three extra keys so the common pattern of
// merging extra keys on top does not reallocate.
//
// Concurrency: CloneMetadata is safe for concurrent use. The returned map
// is not — callers own it fully and are responsible for any further
// synchronization.
func CloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return make(map[string]string, 3)
	}
	cloned := make(map[string]string, len(metadata)+3)
	for k, v := range metadata {
		cloned[k] = v
	}
	return cloned
}

// ---------------------------------------------------------------------------
// internal helpers shared between ContextObservability and RestoreToContext
// ---------------------------------------------------------------------------

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
