package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface check.
var _ outbox.Writer = (*OutboxWriter)(nil)

// OutboxWriter implements outbox.Writer by inserting entries into a PostgreSQL
// outbox_entries table within the caller's transaction context.
//
// ref: ThreeDotsLabs/watermill-sql pkg/sql/publisher.go — InsertQuery pattern
// Adopted: parameterized INSERT within caller-managed transaction.
// Deviated: transaction extracted from context (GoCell convention) instead of
// accepting *sql.Tx directly; metadata stored as JSONB.
type OutboxWriter struct {
	pool *Pool
}

// NewOutboxWriter creates a new OutboxWriter.
// The pool is retained for future use (e.g., schema initialization) but writes
// always go through the context-embedded transaction.
func NewOutboxWriter(pool *Pool) *OutboxWriter {
	return &OutboxWriter{pool: pool}
}

// Write inserts an outbox entry within the transaction found in ctx.
// Returns ERR_ADAPTER_PG_NO_TX if no transaction is present in the context.
func (w *OutboxWriter) Write(ctx context.Context, entry outbox.Entry) error {
	exec, ok := ExecutorFromContext(ctx)
	if !ok {
		return errcode.New(ErrAdapterPGNoTx, "outbox write requires transaction context")
	}

	metadataJSON, err := json.Marshal(entry.Metadata)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGMarshal, "outbox: failed to marshal metadata", err)
	}

	const query = `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, payload, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, execErr := exec.ExecContext(ctx, query,
		entry.ID,
		entry.AggregateID,
		entry.AggregateType,
		entry.EventType,
		entry.Payload,
		metadataJSON,
		entry.CreatedAt,
	)
	if execErr != nil {
		slog.Error("outbox: insert failed",
			slog.String("entry_id", entry.ID),
			slog.String("aggregate_id", entry.AggregateID),
			slog.String("event_type", entry.EventType),
			slog.Any("error", execErr),
		)
		return errcode.Wrap(ErrAdapterPGQuery, fmt.Sprintf("outbox: insert entry %s", entry.ID), execErr)
	}

	return nil
}
