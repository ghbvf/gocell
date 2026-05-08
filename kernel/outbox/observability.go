package outbox

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// MaxObservabilityTotalSize is a sizing-ceiling reference for downstream
// adapters that allocate buffers proportional to the worst-case total
// length of all observability fields combined. With four ID-shaped fields
// each capped at idutil.MaxMetadataIDLen (256B), the worst-case is 1024B;
// adapters/postgres uses 4× this value for the JSONB column allocation.
//
// Validate() does NOT enforce this aggregate bound: TraceParent is
// constrained to a fixed 55B W3C string and the three ID fields are each
// per-field-capped at MaxMetadataIDLen, so the reachable maximum is
// 3×256 + 55 = 823 < 1024. Per-field limits already cover the worst case
// at write time; the aggregate cap is unreachable and intentionally not
// enforced (see backlog OBS-TOTAL-CAP-DEAD-BRANCH-01 / PR#415 review F4).
const MaxObservabilityTotalSize = 4 * idutil.MaxMetadataIDLen

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
//
// ref: OpenTelemetry SpanContext -- typed carrier of trace identity across
// transport boundaries, kept distinct from application attributes.
// Adopted: separate struct for system-owned fields vs. producer-owned Metadata map.
// Deviated: only 4 fields (no sampled flag, no traceFlags struct) to keep the
// async boundary narrow; TraceParent is the W3C canonical form.
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

// Validate enforces per-field size + charset bounds. Each non-empty ID
// field (TraceID/RequestID/CorrelationID) must satisfy idutil.IsSafeID
// and len ≤ idutil.MaxMetadataIDLen; TraceParent must be a valid W3C
// traceparent (fixed 55-byte format, checked via validTraceParent).
//
// No aggregate size check: per-field caps already cover the worst case
// (see MaxObservabilityTotalSize doc).
//
// Producers MUST call Validate (via Entry.Validate, which is called by
// every Writer.Write impl) so size violations surface at write time
// rather than as silent broker rejections or downstream OOMs.
func (o ObservabilityMetadata) Validate() error {
	if err := validateObservabilityID("traceId", o.TraceID); err != nil {
		return err
	}
	if o.TraceParent != "" && !validTraceParent(o.TraceParent) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: observability.traceParent is not a valid W3C traceparent",
			errcode.WithDetails(slog.Int("length", len(o.TraceParent))))
	}
	if err := validateObservabilityID("requestId", o.RequestID); err != nil {
		return err
	}
	if err := validateObservabilityID("correlationId", o.CorrelationID); err != nil {
		return err
	}
	return nil
}

// validateObservabilityID enforces the per-field size + safe-charset
// invariant for ID-shaped observability fields (traceId/requestId/
// correlationId). Empty value is valid (zero-field semantic).
func validateObservabilityID(name, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > idutil.MaxMetadataIDLen {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: observability field length exceeds max",
			errcode.WithDetails(slog.String("field", name), slog.Int("length", len(value)), slog.Int("max", idutil.MaxMetadataIDLen)))
	}
	if !idutil.IsSafeID(value) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: observability field contains unsafe characters",
			errcode.WithDetails(slog.String("field", name)))
	}
	return nil
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
// fields of o. Existing non-empty ctx values WIN (idempotent restore —
// the consumer ctx may already carry trace identity from an inbound
// header on a synchronous spawn path; we do not stomp it). Values that
// fail safety validation (overlong, unsafe chars, invalid traceparent)
// are silently dropped.
//
// Producer/consumer asymmetry — by design:
//
//   - InjectObservabilityFromContext OVERWRITES e.Observability with
//     the producer-side context's identity (the writer is the source of
//     truth for what the entry carries).
//   - RestoreToContext does NOT overwrite existing ctx values (the
//     consumer ctx may legitimately have its own trace propagated by an
//     outer middleware; the entry's identity is a fallback, not a
//     mandate).
//
// Restoration also does not synthesize a TraceParent from a TraceID-only
// metadata: synthesis is a producer-side fallback (ContextObservability
// builds it from ctx trace_id+span_id when no traceparent is set), but
// on the consumer side the wire-captured TraceParent is the canonical
// truth. If it's empty, no synthesis can recover the original parent-id.
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
//
// Symmetric with the consumer-side restoration baked into
// SubscriberWithMiddleware.Subscribe: producers inject from ctx at write
// time, consumers automatically restore from entry.Observability at
// dispatch time. The two endpoints are coupled by construction —
// neither can be silently disabled.
func (e *Entry) InjectObservabilityFromContext(ctx context.Context) {
	e.Observability = ContextObservability(ctx)
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
	// Empty value is the no-op signal — make the short-circuit explicit
	// rather than implicit through idutil.IsSafeID("") returning false.
	if value == "" {
		return ctx
	}
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

// W3C Trace Context traceparent header layout (Level 2 §3.2):
//
//	version(2) "-" trace-id(32) "-" parent-id(16) "-" trace-flags(2)
//	→ total length = 2 + 1 + 32 + 1 + 16 + 1 + 2 = 55 bytes
//
// The named constants below replace the previous magic 55/2/35/52 indices
// so any future spec evolution (Level 3 widens trace-flags? a new version
// field?) shows up at the boundary check sites with intent.
const (
	traceparentTotalLen      = 55
	traceparentVersionEnd    = 2  // version is value[0:2]
	traceparentSep1          = 2  // '-' between version and trace-id
	traceparentTraceIDStart  = 3  // trace-id starts at value[3]
	traceparentTraceIDEnd    = 35 // trace-id ends at value[35]
	traceparentSep2          = 35 // '-' between trace-id and parent-id
	traceparentSpanIDStart   = 36 // parent-id starts at value[36]
	traceparentSpanIDEnd     = 52 // parent-id ends at value[52]
	traceparentSep3          = 52 // '-' between parent-id and trace-flags
	traceparentFlagsStart    = 53
	traceparentFlagsLen      = 2
	traceparentInvalidVerHex = "ff" // §3.2 reserved version, MUST be rejected
)

// traceParentFromContextIDs synthesizes a W3C traceparent from ctx's
// trace_id + span_id when no explicit traceparent is set. The flags byte
// is hardcoded to 0x01 (sampled): GoCell's ctxkeys do not carry an
// independent trace-flags slot today, so all synthesized traceparents
// declare themselves sampled to downstream consumers. If the upstream
// span was un-sampled, that information was already lost before reaching
// this point — synthesizing a sampled traceparent does not lose more.
// Adding fidelity later means widening ctxkeys + ObservabilityMetadata.
func traceParentFromContextIDs(ctx context.Context) string {
	traceID, traceOK := ctxkeys.TraceIDFrom(ctx)
	spanID, spanOK := ctxkeys.SpanIDFrom(ctx)
	if !traceOK || !spanOK || !validW3CTraceID(traceID) || !validW3CSpanID(spanID) {
		return ""
	}
	return "00-" + strings.ToLower(traceID) + "-" + strings.ToLower(spanID) + "-01"
}

func validTraceParent(value string) bool {
	if len(value) != traceparentTotalLen {
		return false
	}
	if value[traceparentSep1] != '-' || value[traceparentSep2] != '-' || value[traceparentSep3] != '-' {
		return false
	}
	version := value[0:traceparentVersionEnd]
	flags := value[traceparentFlagsStart : traceparentFlagsStart+traceparentFlagsLen]
	if version == traceparentInvalidVerHex || !isHex(version) || !isHex(flags) {
		return false
	}
	return validW3CTraceID(value[traceparentTraceIDStart:traceparentTraceIDEnd]) &&
		validW3CSpanID(value[traceparentSpanIDStart:traceparentSpanIDEnd])
}

func traceIDFromTraceParent(value string) string {
	if !validTraceParent(value) {
		return ""
	}
	return value[traceparentTraceIDStart:traceparentTraceIDEnd]
}

func validW3CTraceID(value string) bool {
	return len(value) == 32 && isHex(value) && !allZero(value)
}

func validW3CSpanID(value string) bool {
	return len(value) == 16 && isHex(value) && !allZero(value)
}

// isHex reports whether value consists solely of lowercase hexadecimal
// digits (0-9, a-f). Uppercase A-F are rejected per W3C Trace Context
// Level 2 §2.2.3, which requires traceparent fields to be lowercase hex.
func isHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
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
