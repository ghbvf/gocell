package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/persistence"
)

// pgExecutor routes SQL through the ambient transaction when ctx carries one.
// Direct bypass is explicit and reserved for compensation paths that must
// commit independently of a caller-owned transaction.
type pgExecutor struct {
	pool *pgxpool.Pool
}

func newPGExecutor(pool *pgxpool.Pool) pgExecutor {
	return pgExecutor{pool: pool}
}

func (e pgExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return e.pool.Exec(ctx, sql, args...)
}

func (e pgExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return e.pool.QueryRow(ctx, sql, args...)
}

func (e pgExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.Query(ctx, sql, args...)
	}
	return e.pool.Query(ctx, sql, args...)
}

// ExecDirect bypasses the ambient transaction by executing directly on the
// pool. Callers in *_repo.go / *_store.go MUST place a sibling
// pgrepoapproved.ApprovedExecDirect("<kebab-case-reason>") marker call in the
// same FuncDecl body; archtest PG-REPO-AMBIENT-TX-01 R3(b) enforces this.
// pgExecutor's own methods are exempt.
//
// See pkg/pgrepoapproved and ADR
// docs/architecture/202605241400-003-pg-repo-ambient-tx-discovery-hard.md.
func (e pgExecutor) ExecDirect(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.pool.Exec(ctx, sql, args...)
}
