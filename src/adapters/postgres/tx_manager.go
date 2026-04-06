package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// txKey is the context key for an embedded pgx.Tx.
type txKey struct{}

// savepointDepthKey tracks nested savepoint depth in context.
type savepointDepthKey struct{}

// CtxWithTx returns a new context carrying the given pgx.Tx.
// Downstream code (e.g. OutboxWriter) retrieves it via TxFromContext.
func CtxWithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFromContext extracts a pgx.Tx from the context.
// The boolean return indicates whether a transaction was present.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

// savepointDepth returns the current savepoint nesting depth from context.
func savepointDepth(ctx context.Context) int {
	d, _ := ctx.Value(savepointDepthKey{}).(int)
	return d
}

// withSavepointDepth returns a context with the savepoint depth set.
func withSavepointDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, savepointDepthKey{}, depth)
}

// TxManager provides transactional execution with context-embedded pgx.Tx,
// savepoint-based nesting, and automatic panic rollback.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager creates a TxManager backed by the given Pool.
func NewTxManager(p *Pool) *TxManager {
	return &TxManager{pool: p.inner}
}

// RunInTx executes fn inside a database transaction. The pgx.Tx is stored in
// the context so that downstream code can retrieve it via TxFromContext.
//
// Nesting: if the context already carries a transaction, RunInTx creates a
// savepoint instead of a new top-level transaction. Savepoints are released on
// success and rolled back on error or panic.
//
// Panic safety: panics inside fn trigger a rollback (or savepoint rollback)
// before being re-raised.
func (tm *TxManager) RunInTx(ctx context.Context, fn func(ctx context.Context) error) (retErr error) {
	// Check for an existing transaction (nesting).
	if existingTx, ok := TxFromContext(ctx); ok {
		return tm.runInSavepoint(ctx, existingTx, fn)
	}

	// Start a new top-level transaction.
	tx, err := tm.pool.Begin(ctx)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGConnect, "postgres: begin tx", err)
	}

	txCtx := CtxWithTx(ctx, tx)
	txCtx = withSavepointDepth(txCtx, 0)

	// Panic recovery — rollback and re-panic.
	// Use context.WithoutCancel so rollback succeeds even if ctx is already cancelled
	// (e.g. HTTP timeout). Without this, a cancelled ctx causes rollback to fail,
	// leaving the transaction open until connection pool idle timeout.
	defer func() {
		if r := recover(); r != nil {
			rbErr := tx.Rollback(context.WithoutCancel(ctx))
			if rbErr != nil {
				slog.Error("postgres: rollback after panic failed",
					slog.Any("panic", r),
					slog.String("rollback_error", rbErr.Error()),
				)
			}
			panic(r)
		}
	}()

	retErr = fn(txCtx)
	if retErr != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil {
			slog.Error("postgres: rollback failed",
				slog.String("original_error", retErr.Error()),
				slog.String("rollback_error", rbErr.Error()),
			)
		}
		return retErr
	}

	if err := tx.Commit(ctx); err != nil {
		return errcode.Wrap(ErrAdapterPGConnect, "postgres: commit tx", err)
	}
	return nil
}

// runInSavepoint executes fn within a savepoint on the existing transaction.
func (tm *TxManager) runInSavepoint(ctx context.Context, tx pgx.Tx, fn func(ctx context.Context) error) (retErr error) {
	depth := savepointDepth(ctx)
	spName := fmt.Sprintf("sp_%d", depth)

	if _, err := tx.Exec(ctx, fmt.Sprintf("SAVEPOINT %s", spName)); err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, fmt.Sprintf("postgres: create savepoint %s", spName), err)
	}

	nestedCtx := withSavepointDepth(ctx, depth+1)

	// Panic recovery — rollback savepoint and re-panic.
	// Use context.WithoutCancel so savepoint rollback succeeds even if ctx is cancelled.
	defer func() {
		if r := recover(); r != nil {
			_, rbErr := tx.Exec(context.WithoutCancel(ctx), fmt.Sprintf("ROLLBACK TO SAVEPOINT %s", spName))
			if rbErr != nil {
				slog.Error("postgres: rollback savepoint after panic failed",
					slog.String("savepoint", spName),
					slog.Any("panic", r),
					slog.String("rollback_error", rbErr.Error()),
				)
			}
			panic(r)
		}
	}()

	retErr = fn(nestedCtx)
	if retErr != nil {
		if _, rbErr := tx.Exec(context.WithoutCancel(ctx), fmt.Sprintf("ROLLBACK TO SAVEPOINT %s", spName)); rbErr != nil {
			slog.Error("postgres: rollback savepoint failed",
				slog.String("savepoint", spName),
				slog.String("original_error", retErr.Error()),
				slog.String("rollback_error", rbErr.Error()),
			)
		}
		return retErr
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf("RELEASE SAVEPOINT %s", spName)); err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, fmt.Sprintf("postgres: release savepoint %s", spName), err)
	}
	return nil
}
