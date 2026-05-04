// Package postgres provides a PostgreSQL implementation of auditcore ports.
// It does NOT import adapters/postgres — it defines its own DBTX interface
// to match pgx.Tx / pgxpool.Pool, keeping the cell decoupled from the adapter layer.
package postgres

import (
	"context"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/query"
)

const (
	// getRangeLimit is the safety-net row limit for unbounded queries.
	getRangeLimit = 1000
)

// DBTX abstracts the database operations needed by AuditRepository.
// Both pgxpool.Pool and pgx.Tx satisfy this interface.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// Rows abstracts a query result set.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// Row abstracts a single-row result.
type Row interface {
	Scan(dest ...any) error
}

// Compile-time interface check.
var _ ports.AuditRepository = (*AuditRepository)(nil)

// AuditRepository implements ports.AuditRepository using PostgreSQL.
type AuditRepository struct {
	db    DBTX
	clock clock.Clock
}

// NewAuditRepository creates an AuditRepository backed by the given DBTX.
// clk is used to fill in zero Timestamps on Append.
func NewAuditRepository(db DBTX, clk clock.Clock) *AuditRepository {
	clock.MustHaveClock(clk, "postgres.NewAuditRepository")
	return &AuditRepository{db: db, clock: clk}
}

// Append inserts an audit entry.
func (r *AuditRepository) Append(ctx context.Context, entry *domain.AuditEntry) error {
	const query = `INSERT INTO audit_entries
		(id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	ts := entry.Timestamp
	if ts.IsZero() {
		ts = r.clock.Now()
	}

	_, err := r.db.Exec(ctx, query,
		entry.ID,
		entry.EventID,
		entry.EventType,
		entry.ActorID,
		ts,
		entry.Payload,
		entry.PrevHash,
		entry.Hash,
	)
	if err != nil {
		return ctxcancel.WrapOrInfra(err, "Append", "id="+entry.ID,
			errcode.ErrAuditRepoQuery, "audit repo: append failed")
	}

	return nil
}

// GetRange retrieves audit entries by sequential index range [from, to).
func (r *AuditRepository) GetRange(ctx context.Context, from, to int) ([]*domain.AuditEntry, error) {
	if from < 0 {
		from = 0
	}
	if to <= from {
		return []*domain.AuditEntry{}, nil
	}

	limit := min(to-from, getRangeLimit)

	const query = `SELECT id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash
		FROM audit_entries
		ORDER BY timestamp
		LIMIT $1 OFFSET $2`

	rows, err := r.db.Query(ctx, query, limit, from)
	if err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "GetRange", "",
			errcode.ErrAuditRepoQuery, "audit repo: get range failed")
	}
	defer rows.Close()

	return scanAuditEntries(rows)
}

// Query retrieves audit entries matching the given filters with keyset pagination.
// Requires composite index: CREATE INDEX idx_audit_entries_ts_id ON audit_entries (timestamp DESC, id ASC).
func (r *AuditRepository) Query(ctx context.Context, filters ports.AuditFilters, params query.ListParams) ([]*domain.AuditEntry, error) {
	b := pgquery.NewBuilder()
	b.Append("SELECT id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash FROM audit_entries WHERE 1=1")
	b.AppendIf(filters.EventType != "", "AND event_type = ", filters.EventType)
	b.AppendIf(filters.ActorID != "", "AND actor_id = ", filters.ActorID)
	b.AppendIf(!filters.From.IsZero(), "AND timestamp >= ", filters.From)
	b.AppendIf(!filters.To.IsZero(), "AND timestamp <= ", filters.To)

	if err := pgquery.AppendKeyset(b, params); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrAuditRepoQuery, "audit repo: keyset failed", err)
	}

	sql, args := b.Build()
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "Query", "",
			errcode.ErrAuditRepoQuery, "audit repo: query failed")
	}
	defer rows.Close()

	return scanAuditEntries(rows)
}

// scanAuditEntries scans rows into a slice of AuditEntry.
//
// IO error sites (Scan / rows.Err) gate ctx-cancellation through
// ctxcancel.Wrap before falling through to the generic ErrAuditRepoQuery
// mapping so client-disconnect mid-stream still routes to HTTP 499 +
// slog.Warn rather than polluting the 5xx error rate.
func scanAuditEntries(rows Rows) ([]*domain.AuditEntry, error) {
	var entries []*domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		if err := rows.Scan(
			&e.ID, &e.EventID, &e.EventType, &e.ActorID,
			&e.Timestamp, &e.Payload, &e.PrevHash, &e.Hash,
		); err != nil {
			return nil, ctxcancel.WrapOrInfra(err, "ScanRow", "",
				errcode.ErrAuditRepoQuery, "audit repo: scan failed")
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "RowsErr", "",
			errcode.ErrAuditRepoQuery, "audit repo: rows error")
	}
	return entries, nil
}
