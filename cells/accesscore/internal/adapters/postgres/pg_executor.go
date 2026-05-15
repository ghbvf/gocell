package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// pgExecutor routes SQL through the ambient transaction when ctx carries one.
// Direct bypass is explicit and reserved for trigger-level integration tests or
// other deliberate raw-SQL paths that must not join the caller's transaction.
type pgExecutor struct {
	pool *pgxpool.Pool
}

func newPGExecutor(pool *pgxpool.Pool) pgExecutor {
	return pgExecutor{pool: pool}
}

func (e pgExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return e.pool.Exec(ctx, sql, args...)
}

func (e pgExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Query(ctx, sql, args...)
	}
	return e.pool.Query(ctx, sql, args...)
}

func (e pgExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return e.pool.QueryRow(ctx, sql, args...)
}

func (e pgExecutor) ExecDirect(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.pool.Exec(ctx, sql, args...)
}

// assertAmbientTx returns an error when ctx does not carry an ambient pgx.Tx.
// FOR UPDATE row locks are only meaningful inside a transaction — without a tx
// the lock is released at statement end, silently voiding the S4d serialization
// guarantee. Call this as the first statement of any FOR UPDATE query method.
func assertAmbientTx(ctx context.Context) error {
	if _, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); !ok {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"user_repo: FOR UPDATE row lock requires an ambient transaction; call inside RunInTx")
	}
	return nil
}
