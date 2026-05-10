package ledger

import "time"

// Entry is the canonical audit ledger record persisted by Store implementations.
// Field layout is fixed by ADR-AuditLedger D1 (hash chain) and the equivalence
// requirement with cells/auditcore/internal/domain/audit_entry.go.
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

	// ActorID identifies the principal that triggered the event.
	ActorID string

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
