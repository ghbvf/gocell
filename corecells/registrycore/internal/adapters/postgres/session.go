// Package postgres provides the PostgreSQL implementation of registrycore's
// ports.Registry (303-US5, #2236). It does NOT import adapters/postgres — it
// defines its own DBTX interface to match pgx.Tx / pgxpool.Pool, keeping the
// cell decoupled from the adapter layer (per layering rules). The shape mirrors
// configcore/internal/adapters/postgres.
//
// Both halves of that boundary are machine-enforced, not godoc-only: the
// cells-isolation depguard rule bans any adapters/postgres import here, and
// corecells-pgx-confined confines the raw jackc/pgx driver to this
// postgres-adapter package (#2387 F13, Soft→Medium).
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// DBTX is the cell-local DB abstraction (mirrors pgx.Tx and pgxpool.Pool);
// defined here rather than importing adapters/postgres, per the layering rule
// that corecells/ must not depend on adapters/. Both pgxpool.Pool and pgx.Tx
// satisfy it via the adapters below.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) RowScanner
}

// Rows abstracts a query result set (cell-local copy; see DBTX).
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// RowScanner is a local copy of adapters/postgres.RowScanner; cells/ cannot
// import adapters/ — intentional per layering rules.
type RowScanner interface {
	Scan(dest ...any) error
}

// Session resolves the ambient pgx.Tx from ctx (placed there by
// adapters/postgres.TxManager via persistence.TxCtxKey) or falls back to the
// pool for non-transactional reads.
//
// persistence.TxCtxKey is kernel-owned so both layers can share the key without
// cells/ importing adapters/.
type Session struct {
	pool *pgxpool.Pool
}

// NewSession creates a Session backed by the given pool.
func NewSession(pool *pgxpool.Pool) *Session {
	return &Session{pool: pool}
}

// resolve returns the ambient pgx.Tx (wrapped as DBTX) if one is present in ctx,
// otherwise returns the pool (wrapped as DBTX). Use for read-only paths.
func (s *Session) resolve(ctx context.Context) DBTX {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return &dbtxAdapter{tx: tx}
	}
	return &poolAdapter{pool: s.pool}
}

// resolveWrite returns the ambient pgx.Tx, or an error if none is present. L1
// write paths (Create, Transition) MUST go through this so the projection write
// and the append-only history write participate in the same transaction (write
// fails → roll back, no half state).
func (s *Session) resolveWrite(ctx context.Context) (DBTX, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return &dbtxAdapter{tx: tx}, nil
	}
	return nil, errcode.New(errcode.KindInternal, errcode.ErrAdapterPGNoTx,
		"registry repo: write requires a transaction in context")
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

func (a *dbtxAdapter) QueryRow(ctx context.Context, sql string, args ...any) RowScanner {
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

func (a *poolAdapter) QueryRow(ctx context.Context, sql string, args ...any) RowScanner {
	return a.pool.QueryRow(ctx, sql, args...)
}
