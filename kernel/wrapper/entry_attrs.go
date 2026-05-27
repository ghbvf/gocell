package wrapper

import (
	"github.com/ghbvf/gocell/kernel/outbox"
)

// entryEnvelopeAttrs builds delivery-time span attributes from non-empty
// envelope fields. Returns a slice; empty fields are omitted so spans
// stay quiet on events whose producer did not populate Principal /
// OccurredAt.
//
// Principal four-tuple keys (actor_id / subject_id / tenant_id / session_id)
// route through the adapters/otel span funnel's safeStringAttr two-layer
// scrubber:
//   - session_id goes through key-aware redaction: pkg/redaction.IsSensitiveKey
//     returns true for "session_id" (and "session-id"), so the value is
//     unconditionally replaced with Mask before reaching the OTel backend.
//     Session IDs are credential-associated identifiers whose leakage to trace
//     backends carries session-hijack risk.
//   - actor_id, subject_id, tenant_id emit cleartext (opaque UUIDs, no PII
//     per design); they fall through to the free-form RedactString + Truncate
//     path (key-aware gate misses; SafeID chars contain no key=value anchors).
//
// OccurredAt is encoded as Unix nanoseconds via the typed int64 attribute
// constructor to avoid the string redact path entirely.
//
// PII NOTE: actor_id / subject_id / tenant_id assume Principal IDs are opaque
// UUIDs (per idutil.SafeID charset constraint — no human-readable PII). If a
// deployment uses human-readable identifiers in those fields, additional
// redaction at the OTel Collector layer is needed.
//
// CARDINALITY WARNING: actor_id / subject_id / tenant_id / session_id are
// per-user UUIDs and produce high-cardinality span attributes. Production
// OTel Collector deployments should configure a filter/attributes processor
// or Tempo metrics_generator cardinality limits to avoid cardinality
// explosion in metrics derived from span attributes.
//
// OTel semconv deviation: GoCell uses the gocell.principal.* namespace
// instead of OTel canonical enduser.id / session.id to keep all
// gocell envelope-derived attrs grouped under one prefix and to leave
// room for future OTel-aligned aliasing without renaming source fields.
// Reference: ADR docs/architecture/202605281200-1042-outbox-wire-envelope-principal-occurred-at.md.
//
// ref: opentelemetry-semconv messaging — "enduser.id" / "session.id" are
// the OTel canonical principal attrs; gocell uses gocell.principal.*
// prefix to keep the span attrs grouped and to leave room for future
// OTel-aligned aliases without renaming the source field.
func entryEnvelopeAttrs(entry outbox.Entry) []Attr {
	attrs := make([]Attr, 0, 5)
	if id := string(entry.Principal.ActorID); id != "" {
		attrs = append(attrs, Attr{Key: "gocell.principal.actor_id", Value: id})
	}
	if id := string(entry.Principal.SubjectID); id != "" {
		attrs = append(attrs, Attr{Key: "gocell.principal.subject_id", Value: id})
	}
	if id := string(entry.Principal.TenantID); id != "" {
		attrs = append(attrs, Attr{Key: "gocell.principal.tenant_id", Value: id})
	}
	if id := string(entry.Principal.SessionID); id != "" {
		attrs = append(attrs, Attr{Key: "gocell.principal.session_id", Value: id})
	}
	if !entry.OccurredAt.IsZero() {
		attrs = append(attrs, Attr{Key: "gocell.event.occurred_at_unix_nano", Value: entry.OccurredAt.UnixNano()})
	}
	return attrs
}
