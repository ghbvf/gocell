// Package postgres provides a PostgreSQL implementation of audit-core ports.
// It does NOT import adapters/postgres — it defines its own DBTX interface
// to match pgx.Tx / pgxpool.Pool, keeping the cell decoupled from the adapter layer.
package postgres

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	db DBTX
}

// NewAuditRepository creates an AuditRepository backed by the given DBTX.
func NewAuditRepository(db DBTX) *AuditRepository {
	return &AuditRepository{db: db}
}

// Append inserts an audit entry.
func (r *AuditRepository) Append(ctx context.Context, entry *domain.AuditEntry) error {
	const query = `INSERT INTO audit_entries
		(id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	ts := entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
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
		return errcode.Wrap(errcode.ErrAuditRepoQuery, "audit repo: append failed", err)
	}

	return nil
}

// GetRange retrieves audit entries by sequential index range [from, to).
func (r *AuditRepository) GetRange(ctx context.Context, from, to int) ([]*domain.AuditEntry, error) {
	if from < 0 {
		from = 0
	}
	if to <= from {
		return nil, nil
	}

	limit := to - from
	if limit > getRangeLimit {
		limit = getRangeLimit
	}

	const query = `SELECT id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash
		FROM audit_entries
		ORDER BY timestamp
		LIMIT $1 OFFSET $2`

	rows, err := r.db.Query(ctx, query, limit, from)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrAuditRepoQuery, "audit repo: get range failed", err)
	}
	defer rows.Close()

	return scanAuditEntries(rows)
}

// Query retrieves audit entries matching the given filters with keyset pagination.
// Requires composite index: CREATE INDEX idx_audit_entries_ts_id ON audit_entries (timestamp DESC, id ASC)
func (r *AuditRepository) Query(ctx context.Context, filters ports.AuditFilters, params query.ListParams) ([]*domain.AuditEntry, error) {
	b := query.NewBuilder()
	b.Append("SELECT id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash FROM audit_entries WHERE 1=1")
	b.AppendIf(filters.EventType != "", "AND event_type = ", filters.EventType)
	b.AppendIf(filters.ActorID != "", "AND actor_id = ", filters.ActorID)
	b.AppendIf(!filters.From.IsZero(), "AND timestamp >= ", filters.From)
	b.AppendIf(!filters.To.IsZero(), "AND timestamp <= ", filters.To)

	if err := query.AppendKeyset(b, params); err != nil {
		return nil, errcode.Wrap(errcode.ErrAuditRepoQuery, "audit repo: keyset failed", err)
	}

	sql, args := b.Build()
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrAuditRepoQuery, "audit repo: query failed", err)
	}
	defer rows.Close()

	return scanAuditEntries(rows)
}

// scanAuditEntries scans rows into a slice of AuditEntry.
func scanAuditEntries(rows Rows) ([]*domain.AuditEntry, error) {
	var entries []*domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		if err := rows.Scan(
			&e.ID, &e.EventID, &e.EventType, &e.ActorID,
			&e.Timestamp, &e.Payload, &e.PrevHash, &e.Hash,
		); err != nil {
			return nil, errcode.Wrap(errcode.ErrAuditRepoQuery, "audit repo: scan failed", err)
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.ErrAuditRepoQuery, "audit repo: rows error", err)
	}
	return entries, nil
}
