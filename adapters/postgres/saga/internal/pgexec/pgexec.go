// Package pgexec is the sealed PostgreSQL executor funnel for the saga
// adapter. It is the ONLY location in this adapter's package tree permitted to
// hold a *pgxpool.Pool. The sub-package boundary makes the pool field
// compile-time unreachable from the parent package; PGJournal consumes the
// pool exclusively through the exported PGExecutor interface.
//
// Saga's PGExecutor adds AcquireTx beyond the base Exec/Query/QueryRow surface:
// multi-statement mutations (Append, MarkTerminal, ClaimPending) need to either
// join the ambient transaction or open an owned one. The ambient-join path
// preserves the L2 OutboxFact invariant when Coordinator.commitStep wraps
// Append + outbox.Emit in a single txRunner.RunInTx — an Emit failure rolls
// back the journal write together with the outbox row.
//
// ref: ent/ent dialect/sql/driver.go (unexported impl + exported interface)
// ref: sqlc DBTX (`db DBTX` unexported field in generated Queries)
//
// Enforced by archtest PG-REPO-AMBIENT-TX-01 R1 (sealed holder: any
// *pgxpool.Pool field must live in a package whose import path ends with
// /internal/pgexec) and R2 (cross-package wrap funnel: New*-constructors with
// a *pgxpool.Pool parameter must call pgexec.New(pool)).
//
// Usage: call pgexec.New(pool) once inside your NewPG*-prefixed constructor
// and store the returned PGExecutor as an unexported field on your repo /
// store struct. All SQL goes through that field; the raw pool stays sealed
// in this sub-package.
package pgexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/persistence"
)

// PGExecutor is the sealed read/write surface for saga's journal. Outside this
// package the only way to obtain one is via New; the concrete type is
// unexported and the pool field unreachable.
//
// AcquireTx returns a transaction suitable for multi-statement atomic ops. If
// ctx carries an ambient transaction, AcquireTx returns (ambient, owned=false,
// nil) so the caller skips Commit/Rollback (lifecycle is the outer caller's
// responsibility). Otherwise it opens a fresh transaction on the pool — caller
// MUST Commit on success and Rollback on error (owned=true).
//
// ExecDirect is not included — saga mutations either join an ambient
// transaction or open their own via AcquireTx. Independent-commit bypass is
// not a saga concept.
type PGExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	AcquireTx(ctx context.Context) (tx pgx.Tx, owned bool, err error)
}

// pgExecutor is the unexported impl; outside this package the only way to
// obtain a PGExecutor is via New.
type pgExecutor struct {
	pool *pgxpool.Pool
}

// New wraps a *pgxpool.Pool in the sealed PGExecutor funnel. This is the ONLY
// sanctioned constructor; archtest R2 enforces that any New*-prefixed function
// in any package receiving a *pgxpool.Pool parameter must call pgexec.New on
// that parameter.
//
// Passing a nil pool is permitted in unit tests that exercise logic firing
// before any SQL method is called (e.g. ambient-tx guard tests in
// cells/accesscore/internal/adapters/postgres/tx_assert_test.go). Any SQL
// method invocation on a New(nil) instance will panic at the pool dereference.
func New(pool *pgxpool.Pool) PGExecutor {
	return &pgExecutor{pool: pool}
}

func (e *pgExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return e.pool.Exec(ctx, sql, args...)
}

func (e *pgExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return e.pool.QueryRow(ctx, sql, args...)
}

func (e *pgExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.Query(ctx, sql, args...)
	}
	return e.pool.Query(ctx, sql, args...)
}

func (e *pgExecutor) AcquireTx(ctx context.Context) (pgx.Tx, bool, error) {
	if ambient, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return ambient, false, nil
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	return tx, true, nil
}
