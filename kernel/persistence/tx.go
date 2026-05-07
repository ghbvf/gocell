// Package persistence defines shared persistence abstractions for the
// GoCell framework. These interfaces are implemented by adapters (e.g.,
// adapters/postgres) and consumed by cells via dependency injection.
package persistence

import "context"

// TxRunner executes fn within a database transaction. The transaction is
// embedded in the context so that participants (notably outbox.Writer) can
// join it via TxFromContext(ctx).
//
// outbox.Writer.Write MUST be invoked from within RunInTx — calling Write
// outside this scope returns an error rather than silently committing the
// outbox row without transactional guarantees. RunInTx implementations
// commit on nil return and roll back on any non-nil return (including
// panics, which are converted to errors). Implementations live in adapters
// (e.g., adapters/postgres.TxManager).
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}
