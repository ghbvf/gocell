package postgres

import (
	"context"
	"database/sql"
)

// Executor abstracts the query execution capabilities shared by *sql.Tx
// and *sql.DB. This allows outbox operations to work with either a real
// transaction or a test mock.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Verify *sql.Tx satisfies Executor at compile time.
var _ Executor = (*sql.Tx)(nil)

// txKey is the context key for embedding an Executor (typically *sql.Tx).
type txKey struct{}

// ContextWithTx returns a new context carrying the given transaction.
func ContextWithTx(ctx context.Context, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, Executor(tx))
}

// contextWithExecutor returns a new context carrying the given Executor.
// This is used internally and in tests to embed mock executors.
func contextWithExecutor(ctx context.Context, exec Executor) context.Context {
	return context.WithValue(ctx, txKey{}, exec)
}

// ExecutorFromContext extracts an Executor from the context.
// Returns (nil, false) if no transaction/executor is present.
func ExecutorFromContext(ctx context.Context) (Executor, bool) {
	exec, ok := ctx.Value(txKey{}).(Executor)
	return exec, ok
}

// TxFromContext extracts a *sql.Tx from the context.
// Returns (nil, false) if no transaction is present or if the value is not a *sql.Tx.
func TxFromContext(ctx context.Context) (*sql.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(*sql.Tx)
	return tx, ok
}
