package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface check.
var _ outbox.Writer = (*OutboxWriter)(nil)

// OutboxWriter writes outbox entries within a PostgreSQL transaction.
// It relies on TxFromContext to obtain the current transaction, ensuring
// atomicity with the business state write (same DB transaction).
//
// ref: ThreeDotsLabs/watermill-sql offset_adapter_postgresql.go — transactional outbox insert
// Adopted: INSERT within caller-provided transaction, JSON metadata serialization.
// Deviated: explicit fail-fast on missing tx instead of auto-begin.
type OutboxWriter struct{}

// NewOutboxWriter creates an OutboxWriter.
func NewOutboxWriter() *OutboxWriter {
	return &OutboxWriter{}
}

// Write inserts an outbox entry into the outbox_entries table using the
// transaction from the context. Returns ErrAdapterPGNoTx if no transaction
// is present.
func (w *OutboxWriter) Write(ctx context.Context, entry outbox.Entry) error {
	tx, ok := TxFromContext(ctx)
	if !ok {
		return errcode.New(ErrAdapterPGNoTx, "outbox write requires a transaction in context")
	}

	metadata, err := json.Marshal(entry.Metadata)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGMarshal, "outbox: failed to marshal metadata", err)
	}

	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	const query = `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, payload, metadata, created_at, published)
		VALUES ($1, $2, $3, $4, $5, $6, $7, false)`

	_, err = tx.Exec(ctx, query,
		entry.ID,
		entry.AggregateID,
		entry.AggregateType,
		entry.EventType,
		entry.Payload,
		metadata,
		createdAt,
	)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery,
			fmt.Sprintf("outbox: failed to insert entry %s", entry.ID), err)
	}

	return nil
}
