package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5"
)

// allZeroUUID is the sentinel UUID that must be rejected — it would cause
// idempotency key collisions across unrelated entries.
const allZeroUUID = "00000000-0000-0000-0000-000000000000"

// Compile-time interface checks.
var _ outbox.Writer = (*OutboxWriter)(nil)
var _ outbox.BatchWriter = (*OutboxWriter)(nil)

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

	if strings.TrimSpace(entry.ID) == "" {
		return errcode.New(errcode.ErrValidationFailed, "outbox entry ID must not be empty")
	}
	if entry.ID == allZeroUUID {
		return errcode.New(errcode.ErrValidationFailed, "outbox entry ID must not be all-zeros UUID (idempotency collision risk)")
	}

	if err := entry.Validate(); err != nil {
		return err
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
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '` + statusPending + `')`

	_, err = tx.Exec(ctx, query,
		entry.ID,
		entry.AggregateID,
		entry.AggregateType,
		entry.EventType,
		entry.Topic,
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

// writeBatchChunkSize is the maximum number of entries per INSERT statement.
// PostgreSQL supports at most 65535 bind parameters; each entry uses 9 columns,
// so the theoretical max is 65535/9 = 7281. We use 7000 as a safe margin.
const writeBatchChunkSize = 7000

// WriteBatch inserts multiple outbox entries within the caller's transaction.
// All entries are validated upfront (ID format + Entry.Validate); if any entry
// is invalid, no entries are written.
//
// For batches exceeding writeBatchChunkSize, entries are split into chunks
// and each chunk is inserted with a separate multi-row INSERT within the
// same transaction, preserving all-or-nothing semantics.
//
// An empty entries slice is a no-op and returns nil.
func (w *OutboxWriter) WriteBatch(ctx context.Context, entries []outbox.Entry) error {
	if len(entries) == 0 {
		return nil
	}

	tx, ok := TxFromContext(ctx)
	if !ok {
		return errcode.New(ErrAdapterPGNoTx, "outbox batch write requires a transaction in context")
	}

	// Validate all entries upfront.
	for i, e := range entries {
		if strings.TrimSpace(e.ID) == "" {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("outbox entry[%d] ID must not be empty", i))
		}
		if e.ID == allZeroUUID {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("outbox entry[%d] ID must not be all-zeros UUID (idempotency collision risk)", i))
		}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("outbox entry[%d]: %w", i, err)
		}
	}

	// Split into chunks to stay within PostgreSQL's 65535 parameter limit.
	for offset := 0; offset < len(entries); offset += writeBatchChunkSize {
		end := offset + writeBatchChunkSize
		if end > len(entries) {
			end = len(entries)
		}
		if err := w.writeBatchChunk(ctx, tx, entries[offset:end], offset); err != nil {
			return err
		}
	}
	return nil
}

// writeBatchChunk inserts a single chunk of entries via multi-row INSERT.
// globalOffset is the index of the first entry in the original slice (for error messages).
func (w *OutboxWriter) writeBatchChunk(ctx context.Context, tx pgx.Tx, entries []outbox.Entry, globalOffset int) error {
	const cols = 9 // id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status
	var sb strings.Builder
	sb.WriteString(`INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status)
		VALUES `)

	args := make([]any, 0, len(entries)*cols)
	for i, e := range entries {
		metadata, err := json.Marshal(e.Metadata)
		if err != nil {
			return errcode.Wrap(ErrAdapterPGMarshal,
				fmt.Sprintf("outbox entry[%d]: failed to marshal metadata", globalOffset+i), err)
		}

		createdAt := e.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}

		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * cols
		sb.WriteString("(")
		for j := range cols {
			if j > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("$")
			sb.WriteString(strconv.Itoa(base + j + 1))
		}
		sb.WriteString(")")

		args = append(args, e.ID, e.AggregateID, e.AggregateType,
			e.EventType, e.Topic, e.Payload, metadata, createdAt, statusPending)
	}

	_, err := tx.Exec(ctx, sb.String(), args...)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery,
			fmt.Sprintf("outbox: failed to batch insert %d entries", len(entries)), err)
	}
	return nil
}
