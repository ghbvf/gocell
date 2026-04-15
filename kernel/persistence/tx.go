// Package persistence defines shared persistence abstractions for the
// GoCell framework. These interfaces are implemented by adapters (e.g.,
// adapters/postgres) and consumed by cells via dependency injection.
package persistence

import "context"

// TxRunner executes fn within a database transaction. The transaction
// is embedded in the context so that participants (e.g., outbox.Writer)
// can join it. Implementations live in adapters (e.g.,
// adapters/postgres.TxManager).
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// NoopTxRunner executes fn directly without a real transaction.
// Use for demo mode and unit tests that do not require transactional
// guarantees. Unlike a nil TxRunner, NoopTxRunner is safe to call
// unconditionally, enabling unified code paths (no demo/durable fork).
type NoopTxRunner struct{}

// RunInTx calls fn with the provided context (no transaction wrapping).
func (NoopTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

var _ TxRunner = NoopTxRunner{}

// IsNoop implements cell.Noop. CheckNotNoop rejects NoopTxRunner in durable mode.
func (NoopTxRunner) IsNoop() bool { return true }
