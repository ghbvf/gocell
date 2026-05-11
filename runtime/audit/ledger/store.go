package ledger

import (
	"context"
	"time"
)

// TailSnapshot holds a point-in-time snapshot of the ledger chain tail.
// Returned by Store.Tail to allow restart recovery and chain verification
// without reading all entries.
type TailSnapshot struct {
	// SeqNo is the sequence number of the last committed entry.
	// Zero if the store is empty.
	SeqNo int64

	// PrevHash is the Hash of the last committed entry.
	// Empty if the store is empty.
	PrevHash string

	// EntryCount is the total number of entries in the store.
	EntryCount int64
}

// AuditFilters holds optional filter predicates for Store.Query.
// Zero-value fields are treated as "no filter" (match all).
type AuditFilters struct {
	// EventType filters by exact event type label. Empty means no filter.
	EventType string

	// ActorID filters by exact actor identifier. Empty means no filter.
	ActorID string

	// From filters entries with Timestamp >= From. Zero means no lower bound.
	From time.Time

	// To filters entries with Timestamp <= To. Zero means no upper bound.
	To time.Time
}

// QueryListParams holds pagination parameters for Store.Query.
// Intentionally simple (no cursor) for the MemStore implementation; the PG
// store will extend this with keyset cursor support in S8+.
type QueryListParams struct {
	// Limit is the maximum number of entries to return. Zero or negative
	// values are treated as "no limit" for the MemStore; PG store applies
	// query.MaxPageSize capping.
	Limit int

	// Offset is a simple row offset for MemStore. PG store will replace this
	// with keyset cursor semantics.
	Offset int
}

// Store persists audit entries in a tamper-evident hash chain. Implementations
// must obey the protocol decisions encoded in *Protocol — Append rejects
// entries with invalid JSON payload (strict mode), computes and stores the
// HMAC-SHA256 hash chain link, and enforces idempotency via content
// fingerprint.
//
// Method semantics (ADR-AuditLedger §4.2):
//   - Append: persist a new entry. Computes PrevHash from Tail, computes
//     Hash via Protocol.ComputeHash, assigns SeqNo. Rejects invalid JSON
//     payload (ErrValidationFailed). Rejects duplicate content fingerprint
//     (ErrAuditLedgerAlreadyExists). Thread-safe (PG uses advisory lock;
//     MemStore uses sync.Mutex).
//   - Tail: returns the current chain tail snapshot. Returns zero TailSnapshot
//     for an empty store (not an error).
//   - GetBySeq: fetch entry by sequence number. Missing → ErrAuditLedgerNotFound.
//   - Query: list entries matching AuditFilters with simple pagination.
//     Returns empty slice (not error) when no entries match.
//   - Verify: re-compute HMAC for each entry in [fromSeq, toSeq] and check
//     chain linkage. Returns valid=true and firstInvalidSeq=-1 when all
//     entries are intact.
type Store interface {
	// Append persists a new entry into the namespace's hash chain. Computes
	// PrevHash from Tail, assigns SeqNo, and computes Hash via Protocol.ComputeHash.
	// Rejects invalid JSON payload (ErrValidationFailed) and duplicate content
	// fingerprints (ErrAuditLedgerAlreadyExists). Thread-safe.
	Append(ctx context.Context, e *Entry) error

	// Tail returns the current chain tail snapshot (SeqNo, PrevHash, EntryCount).
	// Returns zero TailSnapshot when the store is empty (not an error).
	Tail(ctx context.Context) (TailSnapshot, error)

	// GetBySeq fetches a single entry by sequence number. Returns
	// ErrAuditLedgerNotFound when the sequence number does not exist.
	GetBySeq(ctx context.Context, seq int64) (*Entry, error)

	// Query lists entries matching AuditFilters with simple pagination.
	// Returns an empty (non-nil) slice when no entries match.
	Query(ctx context.Context, filters AuditFilters, params QueryListParams) ([]*Entry, error)

	// Verify re-computes the HMAC for each entry in [fromSeq, toSeq] and checks
	// chain linkage (PrevHash). Returns valid=true and firstInvalidSeq=-1 when
	// all entries are intact. Returns valid=false and the first invalid seq_no
	// when tampering is detected.
	Verify(ctx context.Context, fromSeq, toSeq int64) (valid bool, firstInvalidSeq int64, err error)
}
