package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
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
// It relies on persistence.TxFromContext[pgx.Tx] to obtain the current
// transaction, ensuring atomicity with the business state write (same DB
// transaction).
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
	tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
	if !ok {
		return errcode.New(errcode.KindInternal, ErrAdapterPGNoTx, "outbox write requires a transaction in context")
	}

	if strings.TrimSpace(entry.ID()) == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox entry ID must not be empty")
	}
	if entry.ID() == allZeroUUID {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox entry ID must not be all-zeros UUID (idempotency collision risk)")
	}

	// Observability and principal are injected at NewEntry construction time
	// (the single trust boundary). No injection step here.
	if err := entry.Validate(); err != nil {
		return err
	}

	metadata, err := json.Marshal(entry.Metadata())
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal, "outbox: failed to marshal metadata", err)
	}
	if len(metadata) > MaxMetadataBytes {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata too large",
			errcode.WithDetails(errcode.PublicInt("limit", MaxMetadataBytes), errcode.PublicInt("got", len(metadata))))
	}

	observabilityJSON, err := marshalObservability(entry.Observability())
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal, "outbox: failed to marshal observability", err)
	}

	principalJSON, err := marshalPrincipal(entry.Principal())
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal, "outbox: failed to marshal principal", err)
	}

	const query = `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, observability, principal, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	_, err = tx.Exec(
		ctx, query,
		entry.ID(),
		entry.AggregateID(),
		entry.AggregateType(),
		entry.EventType(),
		entry.RoutingTopic(),
		entry.Payload(),
		metadata,
		entry.CreatedAt(),
		outbox.StatePending.String(),
		observabilityJSON,
		principalJSON,
		entry.OccurredAt(),
	)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"outbox: failed to insert entry", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("entry_id=%s", entry.ID()))))
	}

	return nil
}

// writeBatchChunkSize is the maximum number of entries per INSERT statement.
// PostgreSQL supports at most 65535 bind parameters; each entry uses 12 columns
// (id, aggregate_id, aggregate_type, event_type, topic, payload, metadata,
// created_at, status, observability, principal, occurred_at), so the theoretical
// max is 65535/12 = 5461. We use 5400 as a safe margin.
const writeBatchChunkSize = 5400

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

	tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
	if !ok {
		return errcode.New(errcode.KindInternal, ErrAdapterPGNoTx, "outbox batch write requires a transaction in context")
	}

	// Validate upfront. Observability and principal are injected at NewEntry
	// construction time (the single trust boundary); no injection step here.
	for i := range entries {
		if strings.TrimSpace(entries[i].ID()) == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"outbox entry ID must not be empty",
				errcode.WithDetails(errcode.PublicInt("index", i)))
		}
		if entries[i].ID() == allZeroUUID {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"outbox entry ID must not be all-zeros UUID",
				errcode.WithDetails(errcode.PublicInt("index", i)))
		}
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
// created_at, status, observability, principal, occurred_at).
const writeBatchChunkCols = 12

// writeBatchChunk inserts a single chunk of entries via multi-row INSERT.
// globalOffset is the index of the first entry in the original slice (for error messages).
func (w *OutboxWriter) writeBatchChunk(ctx context.Context, tx pgx.Tx, entries []outbox.Entry, globalOffset int) error {
	var sb strings.Builder
	// Pre-allocate buffer to avoid reallocations during string building.
	// Approximate size: 170 bytes for header + (entries * ~60 bytes per value tuple).
	sb.Grow(170 + len(entries)*(writeBatchChunkCols*6+3))
	sb.WriteString(`INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, observability, principal, occurred_at)
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
			errcode.WithDetails(errcode.PublicInt("count", len(entries))))
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
	metadata, err := json.Marshal(e.Metadata())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal,
			"outbox entry: failed to marshal metadata", err,
			errcode.WithDetails(errcode.PublicInt("index", globalIndex)))
	}
	if len(metadata) > MaxMetadataBytes {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox entry: metadata too large",
			errcode.WithDetails(
				errcode.PublicInt("index", globalIndex),
				errcode.PublicInt("limit", MaxMetadataBytes),
				errcode.PublicInt("got", len(metadata))))
	}

	observabilityJSON, err := marshalObservability(e.Observability())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal,
			"outbox entry: failed to marshal observability", err,
			errcode.WithDetails(errcode.PublicInt("index", globalIndex)))
	}

	principalJSON, err := marshalPrincipal(e.Principal())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal,
			"outbox entry: failed to marshal principal", err,
			errcode.WithDetails(errcode.PublicInt("index", globalIndex)))
	}

	return []any{
		e.ID(), e.AggregateID(), e.AggregateType(),
		e.EventType(), e.RoutingTopic(), e.Payload(), metadata, e.CreatedAt(), outbox.StatePending.String(), observabilityJSON,
		principalJSON, e.OccurredAt(),
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

// marshalPrincipal serializes PrincipalMetadata to JSON for the principal
// JSONB column. Returns `{}` bytes (not nil) when the struct is zero —
// the column is NOT NULL with no DEFAULT, so the writer must always supply
// a value. An empty principal encodes to `{}` which is valid JSONB.
func marshalPrincipal(p outbox.PrincipalMetadata) ([]byte, error) {
	return json.Marshal(p)
}
