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
// concrete implementation is unexported.
//
// ExecDirect is intentionally NOT a method on this interface — it is the
// top-level function pgexec.ExecDirect(e PGExecutor, ctx, sql, args...). This
// closes the subset-interface bypass vector (R3-Hard form, ai-robust.md §Hard
// 范本 #2 typed marker funnel): a caller cannot declare a local interface
// re-shape with the same method set and call ExecDirect through it.
//
// accesscore production code currently has no ExecDirect callsite; the
// top-level function exists only for integration tests
// (role_repo_integration_test.go) that need to manipulate DB state directly
// for fixture setup / verification. Test files are outside R3's archtest scope.
type PGExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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

// ExecDirect bypasses the ambient transaction. Sealed top-level function — see
// adapters/postgres/internal/pgexec.ExecDirect for full design rationale.
func ExecDirect(e PGExecutor, ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	impl, ok := e.(*pgExecutor)
	if !ok {
		return pgconn.CommandTag{}, errExecDirectOnNonSealedExecutor
	}
	return impl.pool.Exec(ctx, sql, args...)
}

var errExecDirectOnNonSealedExecutor = &execDirectMisuseError{}

type execDirectMisuseError struct{}

func (*execDirectMisuseError) Error() string {
	return "pgexec.ExecDirect: PGExecutor must originate from pgexec.New (mock impls cannot bypass ambient-tx routing via this function)"
}
