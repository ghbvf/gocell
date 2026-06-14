// Package pgexec is the sealed PostgreSQL executor funnel for the accesscore
// adapter. It is the ONLY location in this adapter's package tree permitted to
// hold a *pgxpool.Pool. The sub-package boundary makes the pool field
// compile-time unreachable from the parent package; repos consume the pool
// exclusively through the exported PGExecutor interface.
//
// ref: ent/ent dialect/sql/driver.go (unexported impl + exported interface)
// ref: sqlc DBTX (`db DBTX` unexported field in generated Queries)
//
// Enforced by archtest PG-REPO-AMBIENT-TX-01 R1 (sealed holder: any
// *pgxpool.Pool field must live in a package whose import path ends with
// /internal/pgexec) and R2 (cross-package wrap funnel: New*-constructors with
// a *pgxpool.Pool parameter must call pgexec.New(pool)).
//
// Usage: call pgexec.New(pool) once inside your New*-prefixed constructor
// and store the returned PGExecutor as an unexported field on your repo /
// store struct. All SQL goes through that field; the raw pool stays sealed
// in this sub-package.
//
// # ExecDirect surface — integration-build-only
//
// accesscore production code has zero ExecDirect callsites: the cell never
// needs an ADR-approved ambient-tx bypass. To prevent the test-only ExecDirect
// function from leaking onto the production API surface, it lives in a sibling
// file gated by the `integration` build tag (exec_direct_integration.go).
// Default (production) builds do not compile it; integration tests (which set
// `-tags=integration`) get the full top-level function. This mirrors the
// per-cell ExecDirect exposure policy used by saga and devicecell (neither
// exposes ExecDirect at all). The adapters/postgres root package is the only
// production location holding an ExecDirect callsite — refresh_store.go
// revokeSessionDetachedAt — and its sub-package keeps ExecDirect compiled
// unconditionally.
package pgexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/framework/kernel/persistence"
)

// PGExecutor is the sealed read/write surface routed through the ambient
// transaction (when ctx carries one) or directly against the pool. The
// concrete implementation is unexported.
//
// The interface is SEALED via the unexported sealPGExecutor marker method:
// only *pgExecutor implements it, so a parent package cannot declare a
// parallel PGExecutor that holds its own raw pool. See
// adapters/postgres/internal/pgexec.PGExecutor for full rationale. Regression-
// guarded by archtest PG-REPO-AMBIENT-TX-01 InterfaceSealed.
type PGExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	sealPGExecutor()
}

// pgExecutor is the unexported impl; outside this package the only way to
// obtain a PGExecutor is via New.
type pgExecutor struct {
	pool *pgxpool.Pool
}

// New wraps a *pgxpool.Pool in the sealed PGExecutor funnel.
//
// Passing a nil pool is permitted in unit tests that exercise logic firing
// before any SQL method is called (e.g. ambient-tx guard tests in
// tx_assert_test.go). Any SQL method invocation on a New(nil) instance will
// panic at the pool dereference.
func New(pool *pgxpool.Pool) PGExecutor {
	return &pgExecutor{pool: pool}
}

func (*pgExecutor) sealPGExecutor() {}

func (e *pgExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return e.pool.Exec(ctx, sql, args...)
}

func (e *pgExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.Query(ctx, sql, args...)
	}
	return e.pool.Query(ctx, sql, args...)
}

func (e *pgExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return e.pool.QueryRow(ctx, sql, args...)
}
