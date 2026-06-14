package ledger

import "time"

// Entry is the canonical audit ledger record persisted by Store implementations.
// Field layout is fixed by ADR-AuditLedger D1 (hash chain) and the equivalence
// requirement with corecells/auditcore/internal/domain/audit_entry.go.
//
// SeqNo is added by the store on Append — callers constructing Entry for Append
// leave SeqNo as 0; the store fills it in and returns the updated Entry via
// GetBySeq. Hash and PrevHash are computed by Protocol.ComputeHash and written
// by the store; callers must not pre-fill them for new entries.
type Entry struct {
	// SeqNo is the monotonically increasing sequence number assigned by the
	// store on Append. Starts at 1. Zero value indicates the entry has not
	// yet been persisted.
	SeqNo int64

	// ID is an optional opaque store-assigned identifier (UUID/ULID). The
	// store may populate this on Append; callers must not rely on it for
	// chain ordering — SeqNo is authoritative.
	ID string

	// EventID is the business-layer event identifier (UUID). Used as part of
	// the HMAC input and as the idempotency fingerprint key.
	EventID string

	// EventType is the event type label (e.g. "user.login", "config.updated").
	EventType string

	// ActorID identifies the principal that triggered the event (the
	// impersonator in OAuth `act.sub` semantics; equals SubjectID for normal
	// non-impersonated flows).
	ActorID string

	// SubjectID is the OAuth subject-of-record (the end-user's stable
	// identity, OAuth `sub`). Empty string when not available — the column is
	// NOT NULL with no DEFAULT in the DB schema (043_audit_entries_v2.sql);
	// callers (PG Store INSERT) supply the zero value explicitly so the chain
	// reflects the producer's lack of injection rather than a sentinel DEFAULT.
	//
	// Output policy at the auditquery API boundary (DTO exposure / redaction /
	// admin-gated filtering) is decided by issue #1229 §4 (PR-A2 sealed
	// construction); issue #1219 tracks the contract.yaml DTO extension.
	SubjectID string

	// TenantID is the tenant boundary identifier for multi-tenant deployments.
	// Empty string when not available. NOT NULL no-DEFAULT in DB. See SubjectID
	// for the auditquery output policy reference.
	TenantID string

	// SessionID is the session identifier of the triggering request (server-
	// side session binding). Empty string when not available. NOT NULL
	// no-DEFAULT in DB. SessionID matches `pkg/redaction` sensitive-key set;
	// issue #1229 §4 may strip it from the auditquery DTO entirely.
	SessionID string

	// CorrelationID carries the cross-cell correlation identifier from the
	// outbox observability envelope. Empty string when not available. NOT NULL
	// no-DEFAULT in DB.
	CorrelationID string

	// TraceID carries the OpenTelemetry trace id from the outbox observability
	// envelope. Observability metadata, NOT an audited fact — it is deliberately
	// EXCLUDED from the HMAC hash chain (Protocol.ComputeHash) so audit
	// tamper-evidence is unchanged. Empty string when absent. NOT NULL no-DEFAULT
	// in DB (app supplies explicit value).
	// Writes are restricted to the audit appender by AUDIT-TRACE-ID-WRITE-CALLER-01 (tools/archtest).
	TraceID string

	// OccurredAt is the producer-clock event time (distinct from Timestamp
	// which is the ledger persistence / HMAC time). The audit chain pins both
	// times so consumers can distinguish "when the business event happened"
	// from "when the audit row was sealed". NOT NULL no-DEFAULT in DB; the
	// Go zero (`time.Time{}`) marshals as epoch when no value is supplied.
	OccurredAt time.Time

	// Timestamp is the event wall-clock time in UTC. Used in the HMAC input
	// as UnixNano so the hash is timestamp-sensitive.
	Timestamp time.Time

	// Payload is the arbitrary JSON payload associated with the event. Strict
	// validation (valid JSON) is enforced by the store on Append.
	Payload []byte

	// PrevHash is the Hash of the immediately preceding entry in the chain.
	// Empty for the first entry (SeqNo == 1). Computed by the store.
	PrevHash string

	// Hash is the HMAC-SHA256 hex digest of this entry computed by
	// Protocol.ComputeHash. Computed by the store on Append.
	Hash string
}
