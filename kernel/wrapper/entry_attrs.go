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
// route through the adapters/otel span funnel's free-form RedactString +
// Truncate path; none are in IsSensitiveKey so values are emitted verbatim
// (subject to PII-safe redaction of any embedded `key=value` patterns).
// OccurredAt is encoded as Unix nanoseconds via the typed int64 attribute
// constructor to avoid the string redact path entirely.
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
