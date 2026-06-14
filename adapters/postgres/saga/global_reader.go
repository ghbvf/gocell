package saga

// PGJournal GlobalReader implementation — cross-instance global ordered scan.
//
// PR-PG of EPIC #1630 (#1630 Batch 1): PGJournal now satisfies
// kernel/saga/journal.GlobalReader via a BIGINT IDENTITY column
// (migration 064 adds global_seq to saga_events). LoadSince pages the
// saga_events table ordered by global_seq; HeadSeq reads the maximum.
//
// Neither LoadSince nor HeadSeq starts a transaction; both are read-only and
// safe to execute on any pgExecutor connection (pooled or ambient-tx).
//
// Snapshot independence: HeadSeq and LoadSince are INDEPENDENT queries with
// no shared transaction snapshot. A Tailer that calls HeadSeq to estimate lag
// and then LoadSince to fetch events may observe a read window between them:
// new rows committed between HeadSeq and LoadSince are safe because LoadSince
// will pick them up on the next tick. A Tailer must therefore tolerate this
// window — gap-tolerant / level-triggered consumption covers it (the Tailer
// polls until LoadSince returns an empty page, meaning it has caught up).
//
// ref: kernel/saga/journal.GlobalReader
// ref: kernel/saga/journal/memjournal.go LoadSince / HeadSeq (reference semantics)
// ref: adapters/postgres/migrations/064_add_saga_events_global_seq.sql

import (
	"context"
	"log/slog"
	"time"

	kerrors "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
)

// Compile-time assertion: PGJournal must satisfy journal.GlobalReader.
// Adding LoadSince / HeadSeq here fulfills the PR-PG deferred promise recorded
// in kernel/saga/journal.GlobalReader's godoc.
var _ journal.GlobalReader = (*PGJournal)(nil)

// loadSinceQuery returns events whose global_seq is strictly greater than $1,
// ordered ascending, at most $2 rows. Passing global_seq directly to LIMIT is
// safe: PG LIMIT accepts int64 values without overflow; no application-side
// clamping is required (contrast with the MemJournal which must protect against
// int64 overflow when computing a slice end index).
const loadSinceQuery = `SELECT instance_id, global_seq, version, kind, step_name, payload, created_at
	FROM saga_events
	WHERE global_seq > $1
	ORDER BY global_seq
	LIMIT $2`

// headSeqQuery returns the highest global_seq across all rows, or 0 when the
// table is empty. COALESCE(MAX(global_seq), 0) matches the MemJournal contract:
// HeadSeq returns 0 on an empty journal and never returns NULL.
const headSeqQuery = `SELECT COALESCE(MAX(global_seq), 0) FROM saga_events`

// LoadSince implements journal.GlobalReader.LoadSince.
//
// Argument validation mirrors MemJournal exactly so that the shared
// sagajournaltest.RunGlobalReaderConformance suite passes against both backends:
//   - limit <= 0 → journal.NewNonPositiveLimitError
//   - afterGlobalSeq < 0 → journal.NewNegativeGlobalSeqError
//
// Returns a non-nil empty slice (not nil) when no rows are found, matching the
// interface contract ("caught-up" ≡ empty slice + nil error).
func (s *PGJournal) LoadSince(ctx context.Context, afterGlobalSeq int64, limit int) ([]journal.GlobalEvent, error) {
	if limit <= 0 {
		return nil, journal.NewNonPositiveLimitError(limit)
	}
	if afterGlobalSeq < 0 {
		return nil, journal.NewNegativeGlobalSeqError(afterGlobalSeq)
	}

	rows, err := s.db.Query(ctx, loadSinceQuery, afterGlobalSeq, limit)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: LoadSince query failed", err)
	}
	defer rows.Close()

	out := make([]journal.GlobalEvent, 0)
	for rows.Next() {
		var (
			instanceID string
			globalSeq  int64
			version    int64
			rawKind    int16
			stepName   *string
			payload    []byte
			createdAt  time.Time
		)
		if err := rows.Scan(&instanceID, &globalSeq, &version, &rawKind, &stepName, &payload, &createdAt); err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
				"saga journal: LoadSince scan failed", err)
		}
		kind, kindErr := kindFromInt16(rawKind)
		if kindErr != nil {
			slog.ErrorContext(ctx, "saga journal: LoadSince encountered invalid event kind",
				slog.String("instance_id", instanceID),
				slog.Int64("global_seq", globalSeq),
				slog.Int64("version", version),
				slog.Int("raw_kind", int(rawKind)),
				slog.Any("error", kindErr),
			)
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape,
				"saga journal: LoadSince encountered invalid event kind", kindErr)
		}
		var step idutil.SafeID
		if stepName != nil {
			step = idutil.SafeID(*stepName)
		}
		// Defensive payload copy: PGX may reuse the underlying buffer across
		// rows.Next() iterations. Copying here prevents callers from observing
		// data-race-style corruption if they retain the slice across further
		// iteration or concurrent LoadSince calls.
		var payloadCopy []byte
		if payload != nil {
			payloadCopy = append([]byte(nil), payload...)
		}
		evt := journal.Event{
			Version:   version,
			GlobalSeq: globalSeq, // mirror contract: Event.GlobalSeq == envelope GlobalSeq
			Kind:      kind,
			StepName:  step,
			Payload:   payloadCopy,
			CreatedAt: createdAt,
		}
		out = append(out, journal.GlobalEvent{
			GlobalSeq:  globalSeq,
			InstanceID: idutil.SafeID(instanceID),
			Event:      evt,
		})
	}
	if rows.Err() != nil {
		return nil, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: LoadSince iteration failed", rows.Err())
	}
	return out, nil
}

// HeadSeq implements journal.GlobalReader.HeadSeq.
//
// Returns 0 when saga_events is empty (COALESCE(MAX(global_seq), 0)) matching
// the MemJournal contract: GlobalSeq 0 is the "empty journal" sentinel and a
// conforming backend never returns an event with GlobalSeq 0.
func (s *PGJournal) HeadSeq(ctx context.Context) (int64, error) {
	var head int64
	if err := s.db.QueryRow(ctx, headSeqQuery).Scan(&head); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: HeadSeq query failed", err)
	}
	return head, nil
}
