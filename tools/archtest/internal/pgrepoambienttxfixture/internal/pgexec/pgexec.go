//go:build archtest_fixture

// Package pgexec is the fixture-side sealed PostgreSQL executor funnel,
// mirroring the production form at adapters/postgres/internal/pgexec.
// Parent fixture packages hold pgexec.PGExecutor (interface) and obtain
// instances via pgexec.New(pool); ExecDirect is a top-level function (not a
// method) whose first argument is a pgrepoapproved.Approval token, mirroring
// the production call-bound Hard form.
package pgexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

// PGExecutor mirrors the production sealed interface (Exec / Query / QueryRow
// + an unexported marker method so external packages cannot implement it).
// ExecDirect is intentionally NOT a method — it is the top-level function
// pgexec.ExecDirect(approval, e, ctx, sql, args...), closing the
// subset-interface bypass vector. R3 archtest checks the callsite identity via
// callee resolution, not receiver type.
type PGExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	sealPGExecutor()
}

// pgExecutor is the sanctioned holder of *pgxpool.Pool and the only PGExecutor
// implementation.
type pgExecutor struct {
	pool *pgxpool.Pool
}

// New is the sanctioned constructor. R2 looks for cross-package calls to
// a *types.Func with Pkg().Path() ending /internal/pgexec AND Name() == "New".
func New(pool *pgxpool.Pool) PGExecutor {
	return &pgExecutor{pool: pool}
}

func (*pgExecutor) sealPGExecutor() {}

func (e *pgExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.pool.Exec(ctx, sql, args...)
}

func (e *pgExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return e.pool.Query(ctx, sql, args...)
}

func (e *pgExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return e.pool.QueryRow(ctx, sql, args...)
}

// ExecDirect mirrors the production top-level function: first argument is a
// pgrepoapproved.Approval token (call-bound authorization), the rest forward
// to the pool. R3 archtest checks callee identity (Pkg().Path() ending
// /internal/pgexec AND Name() == "ExecDirect") plus arg[0] form.
func ExecDirect(_ pgrepoapproved.Approval, e PGExecutor, ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	impl, ok := e.(*pgExecutor)
	if !ok {
		return pgconn.CommandTag{}, nil
	}
	return impl.pool.Exec(ctx, sql, args...)
}
