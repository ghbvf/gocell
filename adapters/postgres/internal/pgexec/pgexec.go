// Package pgexec is the sealed PostgreSQL executor funnel for adapters/postgres.
// It is the ONLY location in this adapter's package tree permitted to hold a
// *pgxpool.Pool. The sub-package boundary makes the pool field compile-time
// unreachable from the parent package; stores consume the pool exclusively
// through the exported PGExecutor interface.
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

// PGExecutor is the sealed read/write surface routed through the ambient
// transaction (when ctx carries one) or directly against the pool. The
// concrete implementation is unexported; the pool field is unreachable from
// outside this package by Go visibility — type-asserting back to the concrete
// type is impossible because the type name is unexported.
//
// ExecDirect is the explicit bypass path reserved for ADR-approved
// compensation paths that must commit independently of the ambient tx (e.g.
// adapters/postgres/refresh_store.go::revokeSessionDetachedAt cascade-revoke).
// Each callsite is form-checked by archtest R3(b): a sibling
// pgrepoapproved.ApprovedExecDirect("<kebab-case-reason>") marker must
// co-locate in the same approval scope.
type PGExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	ExecDirect(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
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

func (e *pgExecutor) ExecDirect(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.pool.Exec(ctx, sql, args...)
}
