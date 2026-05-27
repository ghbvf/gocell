//go:build archtest_fixture

// Package pgexec is the fixture-side sealed PostgreSQL executor funnel,
// mirroring the production form at adapters/postgres/internal/pgexec.
// Parent fixture packages hold pgexec.PGExecutor (interface) and obtain
// instances via pgexec.New(pool); the concrete pgExecutor struct and the
// *pgxpool.Pool field are package-private here so the only sanctioned
// construction path is the New factory.
package pgexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGExecutor mirrors the production interface (Exec / Query / QueryRow /
// ExecDirect) so the fixture can exercise R3's ExecDirect marker rule.
type PGExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	ExecDirect(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
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

func (e *pgExecutor) ExecDirect(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.pool.Exec(ctx, sql, args...)
}
