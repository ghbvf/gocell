package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// assertAmbientTx returns an error when ctx does not carry an ambient pgx.Tx.
// FOR UPDATE row locks are only meaningful inside a transaction — without a tx
// the lock is released at statement end, silently voiding the S4d serialization
// guarantee. Call this as the first statement of any FOR UPDATE query method.
func assertAmbientTx(ctx context.Context) error {
	if _, ok := persistence.TxFromContext[pgx.Tx](ctx); !ok {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"user_repo: FOR UPDATE row lock requires an ambient transaction; call inside RunInTx")
	}
	return nil
}
