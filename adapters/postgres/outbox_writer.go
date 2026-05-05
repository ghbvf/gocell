package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// allZeroUUID is the sentinel UUID that must be rejected — it would cause
// idempotency key collisions across unrelated entries.
const allZeroUUID = "00000000-0000-0000-0000-000000000000"

// MaxMetadataBytes caps the JSON-encoded size of an outbox entry's metadata
// column. A bug or malicious producer could otherwise stuff multi-MB JSON into
// the column, amplifying relay memory pressure and PG replication delay.
// 64 KiB is comfortably above any legitimate envelope use (request id,
// correlation id, trace context, a handful of business labels) while
// preventing a single entry from dominating a relay batch.
const MaxMetadataBytes = 64 << 10

// Compile-time interface checks.
var (
	_ outbox.Writer      = (*OutboxWriter)(nil)
	_ outbox.BatchWriter = (*OutboxWriter)(nil)
)

// OutboxWriter writes outbox entries within a PostgreSQL transaction.
// It relies on TxFromContext to obtain the current transaction, ensuring
// atomicity with the business state write (same DB transaction).
//
// ref: ThreeDotsLabs/watermill-sql offset_adapter_postgresql.go — transactional outbox insert
// Adopted: INSERT within caller-provided transaction, JSON metadata serialization.
// Deviated: explicit fail-fast on missing tx instead of auto-begin.
type OutboxWriter struct {
	clock clock.Clock
}

// NewOutboxWriter creates an OutboxWriter.
func NewOutboxWriter(clk clock.Clock) *OutboxWriter {
	clock.MustHaveClock(clk, "postgres.NewOutboxWriter")
	return &OutboxWriter{clock: clk}
}

// Write inserts an outbox entry into the outbox_entries table using the
// transaction from the context. Returns ErrAdapterPGNoTx if no transaction
// is present.
func (w *OutboxWriter) Write(ctx context.Context, entry outbox.Entry) error {
	tx, ok := TxFromContext(ctx)
	if !ok {
		return errcode.New(errcode.KindInternal, ErrAdapterPGNoTx, "outbox write requires a transaction in context")
	}

	if strings.TrimSpace(entry.ID) == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox entry ID must not be empty")
	}
	if entry.ID == allZeroUUID {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox entry ID must not be all-zeros UUID (idempotency collision risk)")
	}

	// Inject observability BEFORE Validate so any failure path (Validate or
	// downstream marshal) carries the originating request's trace/request/
	// correlation identity in slog/span attributes — without this ordering,
	// validate-rejected writes appear in error metrics with empty trace IDs
	// and break post-mortem correlation.
	entry.InjectObservabilityFromContext(ctx)

	if err := entry.Validate(); err != nil {
		return err
	}

	metadata, err := json.Marshal(entry.Metadata)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal, "outbox: failed to marshal metadata", err)
	}
	if len(metadata) > MaxMetadataBytes {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata too large",
			errcode.WithDetails(slog.Int("limit", MaxMetadataBytes), slog.Int("got", len(metadata))))
	}

	observabilityJSON, err := marshalObservability(entry.Observability)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal, "outbox: failed to marshal observability", err)
	}

	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = w.clock.Now()
	}

	const query = `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, observability)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '` + statusPending + `', $9)`

	_, err = tx.Exec(
		ctx, query,
		entry.ID,
		entry.AggregateID,
		entry.AggregateType,
		entry.EventType,
		entry.Topic,
		entry.Payload,
		metadata,
		createdAt,
		observabilityJSON,
	)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"outbox: failed to insert entry", err,
			errcode.WithInternal(fmt.Sprintf("entry_id=%s", entry.ID)))
	}

	return nil
}

// writeBatchChunkSize is the maximum number of entries per INSERT statement.
// PostgreSQL supports at most 65535 bind parameters; each entry uses 10 columns
// (added observability column), so the theoretical max is 65535/10 = 6553.
// We use 6500 as a safe margin.
const writeBatchChunkSize = 6500

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
		return errcode.New(errcode.KindInternal, ErrAdapterPGNoTx, "outbox batch write requires a transaction in context")
	}

	// Inject observability + validate upfront. Iteration uses indices so the
	// in-place mutation from InjectObservabilityFromContext propagates to
	// writeBatchChunk's later loop. Inject must precede Validate so any
	// failure path (Validate or downstream marshal) carries the request's
	// trace/request/correlation identity in slog/span attributes (B2-A-04).
	for i := range entries {
		if strings.TrimSpace(entries[i].ID) == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"outbox entry ID must not be empty",
				errcode.WithDetails(slog.Int("index", i)))
		}
		if entries[i].ID == allZeroUUID {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"outbox entry ID must not be all-zeros UUID",
				errcode.WithDetails(slog.Int("index", i)))
		}
		entries[i].InjectObservabilityFromContext(ctx)
		if err := entries[i].Validate(); err != nil {
			return fmt.Errorf("outbox entry[%d]: %w", i, err)
		}
	}

	// Split into chunks to stay within PostgreSQL's 65535 parameter limit.
	for offset := 0; offset < len(entries); offset += writeBatchChunkSize {
		end := min(offset+writeBatchChunkSize, len(entries))
		if err := w.writeBatchChunk(ctx, tx, entries[offset:end], offset); err != nil {
			return err
		}
	}
	return nil
}

// writeBatchChunkCols is the number of columns inserted per outbox entry
// (id, aggregate_id, aggregate_type, event_type, topic, payload, metadata,
// created_at, status, observability).
const writeBatchChunkCols = 10

// writeBatchChunk inserts a single chunk of entries via multi-row INSERT.
// globalOffset is the index of the first entry in the original slice (for error messages).
func (w *OutboxWriter) writeBatchChunk(ctx context.Context, tx pgx.Tx, entries []outbox.Entry, globalOffset int) error {
	var sb strings.Builder
	// Pre-allocate buffer to avoid reallocations during string building.
	// Approximate size: 170 bytes for header + (entries * ~60 bytes per value tuple).
	sb.Grow(170 + len(entries)*(writeBatchChunkCols*6+3))
	sb.WriteString(`INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, observability)
		VALUES `)

	var numBuf [32]byte
	args := make([]any, 0, len(entries)*writeBatchChunkCols)
	for i, e := range entries {
		entryArgs, err := w.encodeBatchEntry(e, globalOffset+i)
		if err != nil {
			return err
		}
		if i > 0 {
			sb.WriteString(", ")
		}
		appendBatchPlaceholders(&sb, i*writeBatchChunkCols, &numBuf)
		args = append(args, entryArgs...)
	}

	if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"outbox: failed to batch insert entries", err,
			errcode.WithDetails(slog.Int("count", len(entries))))
	}
	return nil
}

// encodeBatchEntry validates and serializes a single outbox.Entry for batch
// INSERT. Returns the 10-arg row in fixed column order. globalIndex is the
// caller's original-slice index, used only to produce ergonomic error
// messages when many entries are in flight.
//
// Observability injection happened upfront in WriteBatch so failure paths
// here carry the request's trace identity (B2-A-04).
func (w *OutboxWriter) encodeBatchEntry(e outbox.Entry, globalIndex int) ([]any, error) {
	metadata, err := json.Marshal(e.Metadata)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal,
			"outbox entry: failed to marshal metadata", err,
			errcode.WithDetails(slog.Int("index", globalIndex)))
	}
	if len(metadata) > MaxMetadataBytes {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox entry: metadata too large",
			errcode.WithDetails(slog.Int("index", globalIndex), slog.Int("limit", MaxMetadataBytes), slog.Int("got", len(metadata))))
	}

	observabilityJSON, err := marshalObservability(e.Observability)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal,
			"outbox entry: failed to marshal observability", err,
			errcode.WithDetails(slog.Int("index", globalIndex)))
	}

	createdAt := e.CreatedAt
	if createdAt.IsZero() {
		createdAt = w.clock.Now()
	}

	return []any{
		e.ID, e.AggregateID, e.AggregateType,
		e.EventType, e.Topic, e.Payload, metadata, createdAt, statusPending, observabilityJSON,
	}, nil
}

// appendBatchPlaceholders writes a `($base+1, $base+2, ..., $base+writeBatchChunkCols)`
// placeholder tuple to sb. numBuf is a caller-supplied scratch buffer to keep
// the inner integer formatting allocation-free.
func appendBatchPlaceholders(sb *strings.Builder, base int, numBuf *[32]byte) {
	sb.WriteString("(")
	for j := range writeBatchChunkCols {
		if j > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("$")
		sb.Write(strconv.AppendInt(numBuf[:0], int64(base+j+1), 10))
	}
	sb.WriteString(")")
}

// marshalObservability serializes ObservabilityMetadata to JSON.
// Returns nil (SQL NULL) when the struct is zero to avoid storing empty
// JSON objects in the observability column.
func marshalObservability(o outbox.ObservabilityMetadata) ([]byte, error) {
	if o.IsZero() {
		return nil, nil
	}
	return json.Marshal(o)
}
