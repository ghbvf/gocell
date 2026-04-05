package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type ctxKey struct{}

// WithTx returns a derived context carrying the given transaction. Repositories
// check for this value via TxFromContext and, when present, execute queries
// within the transaction instead of using the pool directly.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, ctxKey{}, tx)
}

// TxFromContext returns the pgx.Tx stored in ctx, or nil if none is present.
func TxFromContext(ctx context.Context) pgx.Tx {
	tx, _ := ctx.Value(ctxKey{}).(pgx.Tx)
	return tx
}
