//go:build archtest_fixture

// Package pgexec is the fixture-side sealed PostgreSQL executor funnel,
// mirroring the production form at adapters/postgres/internal/pgexec.
// Parent fixture packages hold pgexec.PGExecutor (interface) and obtain
// instances via pgexec.New(pool); ExecDirect is a top-level function (not a
// method), mirroring the production Hard form.
package pgexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGExecutor mirrors the production interface (Exec / Query / QueryRow).
// ExecDirect is intentionally NOT a method — it is the top-level function
// pgexec.ExecDirect(e, ctx, sql, args...), closing the subset-interface
// bypass vector. R3 archtest checks the callsite identity via callee
// resolution, not receiver type.
type PGExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// pgExecutor is the sanctioned holder of *pgxpool.Pool. Two gates keep this
// file out of the production R1 scan: (1) the //go:build archtest_fixture
// tag keeps it out of the production module scan entirely; (2) the filename
// is pgexec.go (not _repo.go / _store.go) so R1's file-extension filter
// would also exempt it if it were ever loaded under production patterns.
type pgExecutor struct {
	pool *pgxpool.Pool
}

// New is the sanctioned constructor. R2 looks for cross-package calls to
// a *types.Func with Pkg().Path() ending /internal/pgexec AND Name() == "New".
func New(pool *pgxpool.Pool) PGExecutor {
	return &pgExecutor{pool: pool}
}

func (e *pgExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.pool.Exec(ctx, sql, args...)
}

func (e *pgExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return e.pool.Query(ctx, sql, args...)
}

func (e *pgExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return e.pool.QueryRow(ctx, sql, args...)
}

// ExecDirect mirrors the production top-level function. R3 archtest checks
// callee identity (Pkg().Path() ending /internal/pgexec AND Name() ==
// "ExecDirect"), not method receiver.
func ExecDirect(e PGExecutor, ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	impl, ok := e.(*pgExecutor)
	if !ok {
		return pgconn.CommandTag{}, nil
	}
	return impl.pool.Exec(ctx, sql, args...)
}
