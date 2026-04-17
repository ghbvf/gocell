package postgres

import (
	"context"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session resolves the ambient pgx.Tx from ctx (placed there by
// adapters/postgres.TxManager via persistence.TxCtxKey) or falls back to the
// pool for non-transactional reads.
//
// Design: cells/ cannot import adapters/postgres — the layering rule forbids
// it. persistence.TxCtxKey is kernel-owned so both layers can share the key.
// Session wraps the concrete pgx types inside a dbtxAdapter that implements
// the cell-local DBTX interface (int64 Exec return), keeping the test mocks
// unchanged.
//
// ref: go-zero TransactCtx — session injected via context; downstream
// participants retrieve from ctx without knowing the adapter. Adopted pattern.
type Session struct {
	pool *pgxpool.Pool
}

// NewSession creates a Session backed by the given pool.
func NewSession(pool *pgxpool.Pool) *Session {
	return &Session{pool: pool}
}

// resolve returns the ambient pgx.Tx (wrapped as DBTX) if one is present in
// ctx, otherwise returns the pool (wrapped as DBTX).
func (s *Session) resolve(ctx context.Context) DBTX {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return &dbtxAdapter{tx: tx}
	}
	return &poolAdapter{pool: s.pool}
}

// dbtxAdapter wraps pgx.Tx to implement the cell-local DBTX interface.
// pgx.Tx.Exec returns (pgconn.CommandTag, error); DBTX.Exec returns (int64, error).
type dbtxAdapter struct {
	tx pgx.Tx
}

func (a *dbtxAdapter) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := a.tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (a *dbtxAdapter) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return a.tx.Query(ctx, sql, args...)
}

func (a *dbtxAdapter) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return a.tx.QueryRow(ctx, sql, args...)
}

// poolAdapter wraps pgxpool.Pool to implement the cell-local DBTX interface.
type poolAdapter struct {
	pool *pgxpool.Pool
}

func (a *poolAdapter) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := a.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (a *poolAdapter) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return a.pool.Query(ctx, sql, args...)
}

func (a *poolAdapter) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return a.pool.QueryRow(ctx, sql, args...)
}
