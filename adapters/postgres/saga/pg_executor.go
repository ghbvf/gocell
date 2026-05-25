package saga

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/persistence"
)

// pgExecutor routes SQL through the ambient transaction when ctx carries one,
// falling back to the pool otherwise. Saga's own funnel — mirrors the parent
// adapters/postgres.pgExecutor pattern so PG-REPO-AMBIENT-TX-01 auto-discovers
// this package (discovery signal is "package scope declares struct named
// pgExecutor"; see tools/archtest/pg_repo_ambient_tx_test.go discovery godoc).
//
// The funnel is required because saga.PGJournal is consumed by
// runtime/saga.Coordinator inside its txRunner.RunInTx block — Append +
// outbox.Emit MUST land in the same ambient transaction so a downstream
// Emit failure rolls back the journal write atomically (L2 OutboxFact
// invariant).
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

// acquireTx returns a transaction suitable for multi-statement atomic ops
// (Append / MarkTerminal / ClaimPending). If the context carries an ambient
// transaction, returns (ambient, owned=false, nil) so the caller skips
// Commit/Rollback (ambient lifecycle is the outer caller's responsibility).
// Otherwise opens a fresh transaction on the pool — caller MUST Commit on
// success and Rollback on error (owned=true).
//
// Routing through this single helper preserves the L2 OutboxFact invariant:
// Coordinator.commitStep wraps Append + outbox.Emit + RegisterAfterCommit in
// one txRunner.RunInTx; saga.PGJournal joins that ambient tx so an Emit
// failure rolls back the journal write together with the outbox row.
func (e pgExecutor) acquireTx(ctx context.Context) (tx pgx.Tx, owned bool, err error) {
	if ambient, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return ambient, false, nil
	}
	tx, err = e.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	return tx, true, nil
}
