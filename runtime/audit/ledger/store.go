package ledger

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// RowScopeAllUnsupportedError reports that a RowVisibility carrying
// tenant.RowScopeAll reached a ledger read path (Query / GetBySeq).
// RowScopeAll is cross-tenant super-admin visibility whose audited BYPASSRLS
// path is not wired until epic #1337 PR-5; until then EVERY ledger backend
// fail-closes it (no silent degrade to tenant scope). It is shared by MemStore
// and the PG LedgerStore so the rejection is byte-identical across backends and
// exercised uniformly by the conformance suite. The classification is
// KindInternal: in PR-4 no caller constructs RowScopeAll for these reads, so its
// arrival is a wiring/programmer error, not user input.
func RowScopeAllUnsupportedError() error {
	return errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"audit ledger: RowScopeAll is not supported on this read path")
}

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

// AuditFilters holds optional non-tenant filter predicates for Store.Query.
// Zero-value fields are treated as "no filter" (match all).
//
// The TENANT axis is NOT a filter here — it is a mandatory typed parameter of
// Store.Query (the t tenant.TenantID positional param[1], #1618). Tenant
// isolation is enforced on two Hard layers: (1) DB-layer FORCE RLS keyed on the
// app.tenant_id GUC (the primary, Postgres-enforced backstop), and (2) the typed
// tenant parameter the auditquery handler passes from the authenticated
// principal (compile-time required, mem-store isolation). The pre-#1618 Soft
// AuditFilters.TenantID string field is removed — a tenant-less Query is no
// longer expressible (every Query is tenant-scoped, fail-closed).
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

	// TraceID filters by exact trace_id. Empty means no filter. This field
	// allows correlating audit entries with distributed traces for operational
	// investigation. trace_id is an observability field and is NOT part of the
	// HMAC hash chain.
	TraceID string

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

	// GetBySeq fetches a single entry by sequence number within the caller's
	// tenant chain. TWO orthogonal axes are enforced:
	//
	//   - TENANT axis (#1618): the entry must belong to the tenant scope on ctx
	//     (tenant.ScopeFromContext, defaulting to "" — the system/framework chain —
	//     when absent). Under per-(namespace, tenant) chains, seq_no is unique per
	//     (namespace, tenant), so a by-seq read of another tenant's entry returns
	//     ErrAuditLedgerNotFound. This closes PR #1715 F1 (an admin RowScopeTenant
	//     obligation can no longer read a cross-tenant entry by seq_no). On PG the
	//     explicit tenant predicate also disambiguates the now-non-unique
	//     (namespace, seq_no) under the RLS `OR tenant_id=''` system-rows clause;
	//     FORCE RLS is the DB-Hard backstop.
	//   - OWNER axis: the vis obligation is enforced on the actor_id column. If the
	//     entry exists in the tenant chain but vis.Allows(entry.ActorID) is false,
	//     the implementation returns ErrAuditLedgerNotFound (IDOR-safe collapse —
	//     existence is not leaked).
	//
	// vis must be valid (NewRowVisibility must succeed). A vis carrying RowScopeAll
	// is fail-closed on every backend (RowScopeAllUnsupportedError) until the
	// audited super-admin path lands (epic #1337 PR-5). GetBySeq has NO production
	// caller today (chain replay / conformance / startup tail-verify only).
	GetBySeq(ctx context.Context, vis tenant.RowVisibility, seq int64) (*Entry, error)

	// Query lists entries matching AuditFilters using keyset cursor pagination
	// defined by params (Limit + decoded CursorValues + Sort). It returns up to
	// params.FetchLimit() (Limit+1) rows for N+1 hasMore detection, ordered by
	// params.Sort. params.Sort must be non-empty (callers pass QuerySort);
	// an empty Sort is a programmer error and yields ErrValidationFailed.
	// Returns an empty (non-nil) slice when no entries match.
	//
	// t is the TENANT axis (#1618): the mandatory typed tenant scope (param[1],
	// TENANT-REPO-PARAM-FUNNEL-01). Results are restricted to t's own rows PLUS
	// tenant-less system/framework rows (tenant_id == "" — e.g. bootstrap.auth.fail
	// and other pre-auth events), and NEVER another tenant's rows. On PG this is
	// the app-layer half of the dual-layer tenant isolation; FORCE RLS on the
	// app.tenant_id GUC is the DB-Hard primary. On mem (no RLS) the typed param is
	// the sole tenant isolation. The auditquery handler passes t from the
	// authenticated principal — every Query is tenant-scoped (fail-closed; a
	// tenant-less Query is not expressible).
	//
	// vis is the row-visibility obligation enforced on the actor_id owner column
	// (the orthogonal OWNER axis). Self/device scopes restrict results to entries
	// whose actor_id matches the obligation subject. Tenant scope returns all
	// matching rows in t. vis must be valid (NewRowVisibility must succeed). A vis
	// carrying RowScopeAll is fail-closed on every backend
	// (RowScopeAllUnsupportedError) until the audited super-admin path lands (PR-5).
	Query(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, filters AuditFilters, params query.ListParams) ([]*Entry, error)

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
