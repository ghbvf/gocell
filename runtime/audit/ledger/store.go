package ledger

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/query"
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

// AuditFilters holds optional filter predicates for Store.Query. Zero-value
// fields are treated as "no filter" (match all), including TenantID.
//
// Tenant isolation is NOT enforced at this generic store layer (so non-HTTP
// callers — conformance suites, namespace-isolation tests, ops tooling — can
// query tenant-agnostically). The isolation boundary lives in the auditquery
// HTTP HANDLER, which always sets TenantID from the authenticated principal so a
// tenant-bearing caller only ever reads its own tenant's rows (epic #1337 PR-2a,
// replacing the PR-1 #1339 403 gate). DB-layer RLS (PR-3) is the backstop for
// the residual tenant-less case.
type AuditFilters struct {
	// EventType filters by exact event type label. Empty means no filter.
	EventType string

	// ActorID filters by exact actor identifier. Empty means no filter.
	ActorID string

	// SubjectID filters by exact subject identifier — the principal the audited
	// action targeted. Empty means no filter. Unlike ActorID (who performed the
	// action), SubjectID lets an admin investigate impersonation where actor !=
	// subject (#1290). For non-admin callers it composes with the actor-self
	// scoping the auditquery policy enforces, so it only ever narrows within the
	// caller's own actions.
	SubjectID string

	// TenantID scopes the query to a single tenant boundary. Empty means no
	// filter (like the other predicates). The auditquery handler always sets this
	// from principal.TenantID so a tenant-bearing caller can only ever read its
	// own tenant's audit trail (epic #1337 PR-2a) — that handler is the isolation
	// boundary, not this generic store filter. DB-layer RLS (PR-3) is the
	// defense-in-depth backstop for the residual tenant-less case.
	TenantID string

	// From filters entries with Timestamp >= From. Zero means no lower bound.
	From time.Time

	// To filters entries with Timestamp <= To. Zero means no upper bound.
	To time.Time
}

// QuerySort returns the canonical ordering for audit ledger listings: newest
// first (timestamp DESC) with the store-assigned id as a stable ASC tie-breaker.
// It is the single source of truth shared by every Store.Query caller and
// matches the idx_audit_namespace_ts_id composite index, so PG keyset pagination
// is an index scan. Store.Query requires a non-empty Sort — callers pass this.
//
// A fresh slice is returned on each call so callers cannot mutate shared package
// state (the exported value would otherwise be an aliasable mutable global).
func QuerySort() []query.SortColumn {
	return []query.SortColumn{
		{Name: "timestamp", Direction: query.SortDESC},
		{Name: "id", Direction: query.SortASC},
	}
}

// Store persists audit entries in a tamper-evident hash chain. Implementations
// must obey the protocol decisions encoded in *Protocol — Append rejects
// entries with invalid JSON payload (strict mode), computes and stores the
// HMAC-SHA256 hash chain link, and enforces idempotency via content
// fingerprint.
//
// Store also satisfies healthz.RepoProber: RepoReady exercises the
// audit_entries relation directly (differentiated check) so that schema/migration
// drift or table-level permission loss is detected independently from the
// pool-level postgres_ready ping. In-memory implementations return nil (always
// ready). See kernel/healthz.RepoProber godoc for the full contract.
//
// Method semantics (ADR-AuditLedger §4.2):
//   - Protocol: returns the immutable protocol decisions used by this store.
//     Must not return nil.
//   - Append: persist a new entry. Computes PrevHash from Tail, computes
//     Hash via Protocol.ComputeHash, assigns SeqNo. Rejects invalid JSON
//     payload (ErrValidationFailed). Rejects duplicate content fingerprint
//     (ErrAuditLedgerAlreadyExists). Thread-safe (PG uses advisory lock;
//     MemStore uses sync.Mutex).
//   - Tail: returns the current chain tail snapshot. Returns zero TailSnapshot
//     for an empty store (not an error).
//   - GetBySeq: fetch entry by sequence number. Missing → ErrAuditLedgerNotFound.
//   - Query: list entries matching AuditFilters with keyset cursor pagination.
//     Returns empty slice (not error) when no entries match.
//   - Verify: re-compute HMAC for each entry in [fromSeq, toSeq] and check
//     chain linkage. Returns valid=true and firstInvalidSeq=-1 when all
//     entries are intact.
//   - RepoReady: differentiated readiness check against the audit_entries
//     relation. Distinct failure domain from pool-level postgres_ready probe.
//     In-memory implementations return nil (always ready).
type Store interface {
	// Protocol returns the immutable protocol decisions backing this store.
	// Callers use it for composition-time invariant checks such as namespace
	// disjointness; implementations must not return nil.
	Protocol() *Protocol

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

	// Query lists entries matching AuditFilters using keyset cursor pagination
	// defined by params (Limit + decoded CursorValues + Sort). It returns up to
	// params.FetchLimit() (Limit+1) rows for N+1 hasMore detection, ordered by
	// params.Sort. params.Sort must be non-empty (callers pass QuerySort);
	// an empty Sort is a programmer error and yields ErrValidationFailed.
	// Returns an empty (non-nil) slice when no entries match.
	Query(ctx context.Context, filters AuditFilters, params query.ListParams) ([]*Entry, error)

	// Verify re-computes the HMAC for each entry in [fromSeq, toSeq] and checks
	// chain linkage (PrevHash). Returns valid=true and firstInvalidSeq=-1 when
	// all entries are intact. Returns valid=false and the first invalid seq_no
	// when tampering is detected.
	Verify(ctx context.Context, fromSeq, toSeq int64) (valid bool, firstInvalidSeq int64, err error)

	// RepoReady is a differentiated readiness check that exercises the
	// audit_entries relation directly. SQL-backed implementations issue a
	// representative query (e.g. Tail) so that schema/migration drift or
	// table-level permission loss surfaces independently from the pool-level
	// postgres_ready ping. In-memory implementations return nil (always ready).
	RepoReady(ctx context.Context) error
}
