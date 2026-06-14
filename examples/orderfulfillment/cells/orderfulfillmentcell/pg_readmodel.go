// pg_readmodel.go — PostgreSQL-backed order-status read model for the
// orderfulfillment example. It is the durable counterpart of the in-memory
// MemReadModel used in demo mode (see cell.go initInternal).
//
// Ambient-transaction semantics: Upsert is called by the saga Tailer INSIDE
// its ambient transaction (the same transaction that calls AdvanceIfOwner),
// so it must join that transaction rather than execute against the pool
// directly. We use persistence.TxFromContext[pgx.Tx](ctx) mirroring the
// adapters/postgres/internal/pgexec pattern: ambient tx when present, pool
// otherwise. Get always uses the pool (HTTP handler path, no ambient tx).
package orderfulfillmentcell

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/projection"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
)

const (
	upsertOrderStatusSQL = `INSERT INTO order_saga_status (order_id, status, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (order_id) DO UPDATE SET status = $2, updated_at = now()`

	selectOrderStatusSQL = `SELECT status FROM order_saga_status WHERE order_id = $1`
)

// PGOrderStatusReadModel is the PostgreSQL implementation of
// projection.OrderStatusReadModel for the orderfulfillment example.
// It survives process restarts — all rows live in the order_saga_status table.
//
// Upsert routes through the ambient transaction (persistence.TxFromContext[pgx.Tx])
// so the status write commits atomically with the Tailer's AdvanceIfOwner.
// Get uses the pool directly (HTTP handler path, no ambient transaction).
type PGOrderStatusReadModel struct {
	pool *pgxpool.Pool
}

// compile-time interface check.
var _ projection.OrderStatusReadModel = (*PGOrderStatusReadModel)(nil)

// NewPGOrderStatusReadModel wraps pool in a PGOrderStatusReadModel. A nil pool
// is rejected with ErrValidationFailed.
func NewPGOrderStatusReadModel(pool *adapterpg.Pool) (*PGOrderStatusReadModel, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"orderfulfillmentcell: NewPGOrderStatusReadModel: pool must not be nil")
	}
	return &PGOrderStatusReadModel{pool: pool.DB()}, nil
}

// Upsert stores status for orderID, routing through the ambient pgx.Tx when
// ctx carries one (Tailer path), or executing directly against the pool
// (standalone/test path). Idempotent: re-applying the same status is a
// no-op UPDATE.
func (m *PGOrderStatusReadModel) Upsert(ctx context.Context, orderID string, status orderstatusgen.ResponseDataStatus) error {
	var err error
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		_, err = tx.Exec(ctx, upsertOrderStatusSQL, orderID, string(status))
	} else {
		_, err = m.pool.Exec(ctx, upsertOrderStatusSQL, orderID, string(status))
	}
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, adapterpg.ErrAdapterPGQuery,
			"pg order status read model: upsert", err,
			errcode.WithInternal(errcode.InternalAttr("order_id", orderID)))
	}
	return nil
}

// Get reads the current status for orderID from the pool. Returns (_, false, nil)
// when no row exists (projection has not applied any event for the order yet).
func (m *PGOrderStatusReadModel) Get(ctx context.Context, orderID string) (orderstatusgen.ResponseDataStatus, bool, error) {
	var status string
	err := m.pool.QueryRow(ctx, selectOrderStatusSQL, orderID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errcode.Wrap(errcode.KindInternal, adapterpg.ErrAdapterPGQuery,
			"pg order status read model: get", err,
			errcode.WithInternal(errcode.InternalAttr("order_id", orderID)))
	}
	return orderstatusgen.ResponseDataStatus(status), true, nil
}
