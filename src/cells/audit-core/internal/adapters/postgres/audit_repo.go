// Package postgres implements the audit-core AuditRepository backed by
// PostgreSQL via pgx.
package postgres

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// queryLimit is a safety-net maximum for unbounded queries (ARCH-07).
const queryLimit = 1000

// Compile-time interface check.
var _ ports.AuditRepository = (*AuditRepository)(nil)

// DBTX abstracts pgxpool.Pool and pgx.Tx so the repository can participate
// in transactions without importing the adapters/postgres package directly.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// AuditRepository is a PostgreSQL-backed implementation of
// ports.AuditRepository. It uses pgxpool.Pool for connection management and
// supports transaction participation via TxFromContext.
type AuditRepository struct {
	pool *pgxpool.Pool
}

// NewAuditRepository creates an AuditRepository that uses the provided pgx
// connection pool.
func NewAuditRepository(pool *pgxpool.Pool) *AuditRepository {
	return &AuditRepository{pool: pool}
}

// db returns either the transaction from context or the pool.
func (r *AuditRepository) db(ctx context.Context) DBTX {
	if tx := TxFromContext(ctx); tx != nil {
		return tx
	}
	return r.pool
}

// Append inserts a single audit entry.
func (r *AuditRepository) Append(ctx context.Context, entry *domain.AuditEntry) error {
	const q = `INSERT INTO audit_entries
		(id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := r.db(ctx).Exec(ctx, q,
		entry.ID,
		entry.EventID,
		entry.EventType,
		entry.ActorID,
		entry.Timestamp,
		entry.Payload,
		entry.PrevHash,
		entry.Hash,
	)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal, fmt.Sprintf("audit_repo: append entry %s", entry.ID), err)
	}
	return nil
}

// GetRange returns audit entries by positional range [from, to). The range is
// 0-based and mapped to OFFSET/LIMIT.
func (r *AuditRepository) GetRange(ctx context.Context, from, to int) ([]*domain.AuditEntry, error) {
	if from < 0 {
		from = 0
	}
	if to <= from {
		return nil, nil
	}
	limit := to - from
	if limit > queryLimit {
		limit = queryLimit
	}

	const q = `SELECT id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash
		FROM audit_entries
		ORDER BY timestamp ASC
		OFFSET $1 LIMIT $2`

	rows, err := r.db(ctx).Query(ctx, q, from, limit)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "audit_repo: get range", err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// Query returns audit entries matching the provided filters, capped at
// queryLimit rows (ARCH-07 safety net).
func (r *AuditRepository) Query(ctx context.Context, filters ports.AuditFilters) ([]*domain.AuditEntry, error) {
	q := `SELECT id, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash
		FROM audit_entries WHERE 1=1`
	args := []any{}
	argIdx := 1

	if filters.EventType != "" {
		q += fmt.Sprintf(" AND event_type = $%d", argIdx)
		args = append(args, filters.EventType)
		argIdx++
	}
	if filters.ActorID != "" {
		q += fmt.Sprintf(" AND actor_id = $%d", argIdx)
		args = append(args, filters.ActorID)
		argIdx++
	}
	if !filters.From.IsZero() {
		q += fmt.Sprintf(" AND timestamp >= $%d", argIdx)
		args = append(args, filters.From)
		argIdx++
	}
	if !filters.To.IsZero() {
		q += fmt.Sprintf(" AND timestamp <= $%d", argIdx)
		args = append(args, filters.To)
		argIdx++
	}

	q += fmt.Sprintf(" ORDER BY timestamp ASC LIMIT %d", queryLimit)

	rows, err := r.db(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "audit_repo: query", err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// scanEntries collects rows into AuditEntry slices.
func scanEntries(rows pgx.Rows) ([]*domain.AuditEntry, error) {
	var result []*domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		if err := rows.Scan(
			&e.ID, &e.EventID, &e.EventType, &e.ActorID,
			&e.Timestamp, &e.Payload, &e.PrevHash, &e.Hash,
		); err != nil {
			return nil, errcode.Wrap(errcode.ErrInternal, "audit_repo: scan row", err)
		}
		result = append(result, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "audit_repo: rows iteration", err)
	}
	return result, nil
}
