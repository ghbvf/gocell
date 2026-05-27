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
package pgexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
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
//
// ExecDirect is intentionally NOT a method on this interface — it is the
// top-level function pgexec.ExecDirect(approval, e, ctx, sql, args...) whose
// first argument is a call-bound pgrepoapproved.Approval token. accesscore
// production code currently has no ExecDirect callsite; the top-level function
// exists only for integration tests (role_repo_integration_test.go) that
// manipulate DB state directly for fixture setup / verification.
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

// ExecDirect bypasses the ambient transaction. The first parameter is a
// call-bound pgrepoapproved.Approval token (mint inline via
// pgrepoapproved.Approve("<reason>")). Sealed top-level function — see
// adapters/postgres/internal/pgexec.ExecDirect for full design rationale.
func ExecDirect(_ pgrepoapproved.Approval, e PGExecutor, ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	impl, ok := e.(*pgExecutor)
	if !ok {
		// Unreachable in production: PGExecutor is sealed, so every value is
		// *pgExecutor. A non-*pgExecutor here is an in-package-test-only
		// programmer error — A-class assertion panic, not an error return.
		panic(panicregister.Approved("pgexec-execdirect-non-sealed",
			errcode.Assertion("pgexec.ExecDirect: PGExecutor must originate from pgexec.New")))
	}
	return impl.pool.Exec(ctx, sql, args...)
}
