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
