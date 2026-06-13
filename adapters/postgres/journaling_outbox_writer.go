package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// journalingOutboxWriter decorates the base OutboxWriter with an emit-time
// same-transaction double-write into the durable projection_events journal
// (ADR 202606071600-1504 §D4, "event-store-primary"). For every entry whose
// RoutingTopic is a projection source, it appends a journal row inside the
// SAME ambient business transaction (persistence.TxFromContext[pgx.Tx]) the
// base writer already used to insert outbox_entries — so the journal row and
// the business fact commit or roll back atomically.
//
// projectionTopics is the topic-filtered allowlist (D4): only events that a
// projection will replay are journaled, bounding journal growth by construction.
// It is injected by the composition root from the cellgen-derived projection
// source set (generatedProjectionSourceTopics) — never hand-maintained
// (PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01).
//
// The decorator is wired as the assembly's sole outbox.Writer, so the WriterEmitter
// emit funnel is unchanged and producers are untouched. The base BatchWriter
// capability is preserved (WriteBatch below) so a batched emit path keeps its
// multi-row insert rather than silently degrading to per-entry writes.
//
// ref: AxonFramework JdbcEventStore + TrackingToken — events written in the
// business transaction as the source of truth; projections derive from them.
type journalingOutboxWriter struct {
	inner            *OutboxWriter
	projectionTopics map[string]struct{}
}

// Compile-time interface checks: the decorator is an outbox.Writer and preserves
// the wrapped *OutboxWriter's BatchWriter capability.
var (
	_ outbox.Writer      = (*journalingOutboxWriter)(nil)
	_ outbox.BatchWriter = (*journalingOutboxWriter)(nil)
)

// NewJournalingOutboxWriter wraps a base OutboxWriter so projection-source events
// are journaled in the producer's transaction. projectionTopics is the set of
// routing topics (== projection contract ids) to journal; a nil or empty set means
// the decorator forwards writes unchanged (journals nothing). Exported because the
// composition root (a different package) wires it; construction is locked to the
// capability provider funnel (CAPABILITY-PROVIDER-FUNNEL-01).
//
// inner is a strong dependency: a nil inner is a composition-root programmer error
// (NewOutboxWriter never returns nil), so it fail-fasts at construction rather than
// deferring to a nil-deref on the first Write (Option 范式 fail-fast; mirrors
// clock.MustHaveClock).
func NewJournalingOutboxWriter(inner *OutboxWriter, projectionTopics []string) outbox.Writer {
	if inner == nil {
		panic(panicregister.Approved("journaling-outbox-writer-nil-inner", nil))
	}
	set := make(map[string]struct{}, len(projectionTopics))
	for _, t := range projectionTopics {
		set[t] = struct{}{}
	}
	return &journalingOutboxWriter{inner: inner, projectionTopics: set}
}

// Write inserts the entry into outbox_entries (base writer, ambient tx) and then,
// if the entry's topic is a projection source, appends a journal row in the same tx.
func (w *journalingOutboxWriter) Write(ctx context.Context, entry outbox.Entry) error {
	if err := w.inner.Write(ctx, entry); err != nil {
		return err
	}
	return w.journalProjectionSubset(ctx, []outbox.Entry{entry})
}

// WriteBatch inserts all entries via the base batch writer (preserving the
// multi-row insert), then journals the projection-source subset.
func (w *journalingOutboxWriter) WriteBatch(ctx context.Context, entries []outbox.Entry) error {
	if err := w.inner.WriteBatch(ctx, entries); err != nil {
		return err
	}
	return w.journalProjectionSubset(ctx, entries)
}

// journalProjectionSubset filters entries to the projection-source set and is the
// SOLE caller of appendProjectionEvents — the single sanctioned journal-append
// chokepoint (PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01). It returns early when no
// entry is a projection source so non-projection writes touch only outbox_entries.
func (w *journalingOutboxWriter) journalProjectionSubset(ctx context.Context, entries []outbox.Entry) error {
	var subset []outbox.Entry
	for _, e := range entries {
		if _, ok := w.projectionTopics[e.RoutingTopic()]; ok {
			subset = append(subset, e)
		}
	}
	if len(subset) == 0 {
		return nil
	}
	if err := w.appendProjectionEvents(ctx, subset); err != nil {
		// The append runs in the producer's business tx, so a failure rolls the
		// whole transaction back (the business fact is discarded with the journal
		// row — atomicity). Log at the decorator so the journal-specific failure is
		// diagnosable rather than surfacing only as an opaque business-tx abort.
		slog.ErrorContext(ctx, "projection journal: append failed, business transaction will roll back",
			slog.Int("count", len(subset)), slog.Any("error", err))
		return err
	}
	return nil
}

// projectionEventCols is the column count per projection_events row (global_seq is
// GENERATED ALWAYS and never inserted).
const projectionEventCols = 11

// projectionEventChunkSize caps rows per INSERT to stay within PostgreSQL's 65535
// bind-parameter limit (65535/11 ≈ 5957); 5400 mirrors writeBatchChunkSize's margin.
const projectionEventChunkSize = 5400

// projectionEventInsertPrefix / projectionEventInsertConflict frame the multi-row
// upsert. ON CONFLICT (id) DO NOTHING makes the append idempotent against
// application-layer retries of the producer transaction.
const projectionEventInsertPrefix = `INSERT INTO projection_events
	(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, observability, principal, created_at, occurred_at)
	VALUES `

const projectionEventInsertConflict = ` ON CONFLICT (id) DO NOTHING`

// appendProjectionEvents writes the given entries to projection_events on the
// ambient business transaction, chunked to respect the bind-parameter limit. It is
// unexported and is the SOLE site that executes an INSERT into projection_events —
// the SQL builder it delegates to (buildProjectionInsert) is side-effect-free, so
// the journal-write path has exactly one executing function for
// PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01 to lock (no callable INSERT helper to
// bypass it). No package outside adapters/postgres can reach a journal-append path.
// Entries are already validated by the base writer, so this does not re-validate.
func (w *journalingOutboxWriter) appendProjectionEvents(ctx context.Context, entries []outbox.Entry) error {
	tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
	if !ok {
		return errcode.New(errcode.KindInternal, ErrAdapterPGNoTx,
			"projection journal append requires a transaction in context")
	}
	for offset := 0; offset < len(entries); offset += projectionEventChunkSize {
		end := min(offset+projectionEventChunkSize, len(entries))
		query, args, err := buildProjectionInsert(entries[offset:end])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"projection journal: failed to append events", err,
				// 5xx strips public details on the wire; count is server-log only.
				errcode.WithInternal(errcode.InternalAttr("count", end-offset)))
		}
	}
	return nil
}

// buildProjectionInsert renders the multi-row INSERT statement + bind args for one
// chunk. It is PURE — no transaction, no journal side effect — so it is not a
// journal-write path the append caller-allowlist must lock; the only INSERT
// execution lives in appendProjectionEvents.
func buildProjectionInsert(entries []outbox.Entry) (string, []any, error) {
	var sb strings.Builder
	sb.WriteString(projectionEventInsertPrefix)

	var numBuf [32]byte
	args := make([]any, 0, len(entries)*projectionEventCols)
	for i, e := range entries {
		rowArgs, err := encodeProjectionEntry(e)
		if err != nil {
			return "", nil, err
		}
		if i > 0 {
			sb.WriteString(", ")
		}
		appendProjectionPlaceholders(&sb, i*projectionEventCols, &numBuf)
		args = append(args, rowArgs...)
	}
	sb.WriteString(projectionEventInsertConflict)
	return sb.String(), args, nil
}

// encodeProjectionEntry serializes one entry into the fixed projectionEventCols
// argument order. It reuses the outbox writer's envelope marshalers so the journal
// row carries the same observability/principal/metadata shape as outbox_entries.
func encodeProjectionEntry(e outbox.Entry) ([]any, error) {
	metadata, err := json.Marshal(e.Metadata())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal,
			"projection journal: failed to marshal metadata", err)
	}
	observabilityJSON, err := marshalObservability(e.Observability())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal,
			"projection journal: failed to marshal observability", err)
	}
	principalJSON, err := marshalPrincipal(e.Principal())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGMarshal,
			"projection journal: failed to marshal principal", err)
	}
	return []any{
		e.ID(), e.AggregateID(), e.AggregateType(), e.EventType(), e.RoutingTopic(),
		e.Payload(), metadata, observabilityJSON, principalJSON, e.CreatedAt(), e.OccurredAt(),
	}, nil
}

// appendProjectionPlaceholders writes a `($base+1, …, $base+projectionEventCols)`
// tuple to sb. numBuf is a caller-supplied scratch buffer to keep integer
// formatting allocation-free (mirrors appendBatchPlaceholders for the 11-col row).
func appendProjectionPlaceholders(sb *strings.Builder, base int, numBuf *[32]byte) {
	sb.WriteString("(")
	for j := range projectionEventCols {
		if j > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("$")
		sb.Write(strconv.AppendInt(numBuf[:0], int64(base+j+1), 10))
	}
	sb.WriteString(")")
}
