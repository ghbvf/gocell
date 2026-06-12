//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
)

// The journaling decorator double-writes projection-source events into the durable
// projection_events journal inside the producer's business transaction (ADR
// 202606071600-1504 §D4). These integration tests exercise the three write-path
// invariants PR-02 must hold (ADR §9 PR-02), over BOTH Write and WriteBatch so the
// preserved BatchWriter path is covered identically:
//
//   ① atomicity   — the outbox_entries and projection_events rows commit or roll back
//                    together: a CALLER-initiated abort discards both, and a journal
//                    append FAILURE (the second write) aborts the business tx so the
//                    base outbox_entries row is rolled back too.
//   ② topic-filter — only projection-source topics are journaled; non-source topics
//                    land in outbox_entries only.
//   ③ idempotency  — ON CONFLICT (id) DO NOTHING: re-appending the same id is a no-op,
//                    global_seq does not advance.

const (
	projTopicA = "event.order-created.v1"
	projTopicB = "event.order-status-changed.v1"
	auditTopic = "event.audit.entry.created.v1" // NOT a projection source
)

// writeMode lets each invariant run identically over Write and WriteBatch.
type writeMode struct {
	name  string
	apply func(t *testing.T, ctx context.Context, w kout.Writer, entries []kout.Entry) error
}

func writeModes() []writeMode {
	return []writeMode{
		{name: "Write", apply: func(_ *testing.T, ctx context.Context, w kout.Writer, entries []kout.Entry) error {
			for _, e := range entries {
				if err := w.Write(ctx, e); err != nil {
					return err
				}
			}
			return nil
		}},
		{name: "WriteBatch", apply: func(t *testing.T, ctx context.Context, w kout.Writer, entries []kout.Entry) error {
			bw, ok := w.(kout.BatchWriter)
			require.True(t, ok, "decorator must preserve the base writer's BatchWriter capability")
			return bw.WriteBatch(ctx, entries)
		}},
	}
}

func newJournalingWriter(t *testing.T, topics ...string) (kout.Writer, *TxManager, *Pool) {
	t.Helper()
	pool := migratedPool(t)
	base := NewOutboxWriter(clockmock.New(time.Now()))
	return NewJournalingOutboxWriter(base, topics), NewTxManager(pool), pool
}

func newJournalEntry(t *testing.T, topic string) kout.Entry {
	t.Helper()
	// eventType == routing topic (no explicit WithTopic) — the decorator filters on
	// RoutingTopic(), so the entry's eventType is what the projectionTopics set matches.
	e, err := kout.NewEntry(clockmock.New(time.Now()), context.Background(), topic, []byte(`{"k":"v"}`))
	require.NoError(t, err)
	return e
}

func countRows(t *testing.T, pool *Pool, table, id string) int {
	t.Helper()
	// table is interpolated (not bindable), so whitelist it to a closed set — the
	// helper can never become an injection seam even if a future caller passes a
	// dynamic value.
	switch table {
	case "outbox_entries", "projection_events":
	default:
		t.Fatalf("countRows: unexpected table %q", table)
	}
	var n int
	require.NoError(t, pool.DB().QueryRow(context.Background(),
		"SELECT COUNT(*) FROM "+table+" WHERE id = $1", id).Scan(&n))
	return n
}

func journalGlobalSeq(t *testing.T, pool *Pool, id string) int64 {
	t.Helper()
	var seq int64
	require.NoError(t, pool.DB().QueryRow(context.Background(),
		"SELECT global_seq FROM projection_events WHERE id = $1", id).Scan(&seq))
	return seq
}

// breakProjectionJournal makes the projection_events append fail by dropping the table
// on the test's isolated migrated pool, so a subsequent double-write hits an undefined
// relation. (Mirrors the broken-pool seam in projection_event_source_integration_test.go;
// gives the PR-03 live-resolver failure tests a reusable journal-break helper.)
func breakProjectionJournal(t *testing.T, pool *Pool) {
	t.Helper()
	_, err := pool.DB().Exec(context.Background(), `DROP TABLE IF EXISTS projection_events CASCADE`)
	require.NoError(t, err, "drop projection_events to break the journal")
}

func TestJournalingOutboxWriter_DoubleWriteAtomicity(t *testing.T) {
	for _, m := range writeModes() {
		t.Run(m.name, func(t *testing.T) {
			w, txm, pool := newJournalingWriter(t, projTopicA)
			ctx := context.Background()
			e := newJournalEntry(t, projTopicA)
			sentinel := errors.New("force rollback")

			runErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
				if err := m.apply(t, txCtx, w, []kout.Entry{e}); err != nil {
					return err
				}
				return sentinel // abort: both writes must roll back together
			})
			require.ErrorIs(t, runErr, sentinel)

			assert.Equal(t, 0, countRows(t, pool, "outbox_entries", e.ID()),
				"outbox_entries row must be rolled back")
			assert.Equal(t, 0, countRows(t, pool, "projection_events", e.ID()),
				"projection_events row must be rolled back in the same business tx")
		})
	}
}

// TestJournalingOutboxWriter_AppendFailureRollsBackOutbox proves the SECOND-write
// failure direction of D4 atomicity: when the base outbox insert succeeds but the
// projection_events append then fails, the append error propagates out of Write/
// WriteBatch and aborts the producer's business tx, so the outbox_entries row is rolled
// back too (no orphaned business fact). DoubleWriteAtomicity only covers a CALLER-
// initiated abort AFTER a successful double-write; this covers the writer's own failure.
func TestJournalingOutboxWriter_AppendFailureRollsBackOutbox(t *testing.T) {
	for _, m := range writeModes() {
		t.Run(m.name, func(t *testing.T) {
			w, txm, pool := newJournalingWriter(t, projTopicA)
			ctx := context.Background()
			breakProjectionJournal(t, pool) // the append INSERT now hits an undefined relation
			e := newJournalEntry(t, projTopicA)

			runErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
				return m.apply(t, txCtx, w, []kout.Entry{e})
			})
			require.Error(t, runErr, "journal append failure must propagate out of the business tx")

			assert.Equal(t, 0, countRows(t, pool, "outbox_entries", e.ID()),
				"base outbox_entries insert must roll back when the journal append fails")
		})
	}
}

func TestJournalingOutboxWriter_TopicFilter(t *testing.T) {
	for _, m := range writeModes() {
		t.Run(m.name, func(t *testing.T) {
			w, txm, pool := newJournalingWriter(t, projTopicA, projTopicB)
			ctx := context.Background()
			src1 := newJournalEntry(t, projTopicA)
			src2 := newJournalEntry(t, projTopicB)
			nonSrc := newJournalEntry(t, auditTopic)

			require.NoError(t, txm.RunInTx(ctx, func(txCtx context.Context) error {
				return m.apply(t, txCtx, w, []kout.Entry{src1, src2, nonSrc})
			}))

			// Every entry lands in the transient outbox regardless of topic.
			for _, e := range []kout.Entry{src1, src2, nonSrc} {
				assert.Equal(t, 1, countRows(t, pool, "outbox_entries", e.ID()))
			}
			// Only the projection-source subset lands in the durable journal.
			assert.Equal(t, 1, countRows(t, pool, "projection_events", src1.ID()))
			assert.Equal(t, 1, countRows(t, pool, "projection_events", src2.ID()))
			assert.Equal(t, 0, countRows(t, pool, "projection_events", nonSrc.ID()),
				"non-projection-source topic must not be journaled")
		})
	}
}

// TestJournalingOutboxWriter_OnConflictIdempotency drives the unexported append for
// the same id twice (this test is in package postgres; a second full Write would
// abort on the outbox_entries PK, so appending directly isolates the journal's ON
// CONFLICT path). Covers both shapes the Write and WriteBatch entry points funnel
// into: a cross-transaction application-layer retry (the ADR §D4 idempotency
// scenario), and a duplicate id WITHIN one batched multi-row INSERT (the WriteBatch
// path — single Write only ever passes a 1-element slice).
func TestJournalingOutboxWriter_OnConflictIdempotency(t *testing.T) {
	w, txm, pool := newJournalingWriter(t, projTopicA)
	jw := w.(*journalingOutboxWriter)
	ctx := context.Background()

	t.Run("cross-tx re-append", func(t *testing.T) {
		e := newJournalEntry(t, projTopicA)
		require.NoError(t, txm.RunInTx(ctx, func(txCtx context.Context) error {
			return jw.appendProjectionEvents(txCtx, []kout.Entry{e})
		}))
		firstSeq := journalGlobalSeq(t, pool, e.ID())

		require.NoError(t, txm.RunInTx(ctx, func(txCtx context.Context) error {
			return jw.appendProjectionEvents(txCtx, []kout.Entry{e}) // same id again
		}))
		assert.Equal(t, 1, countRows(t, pool, "projection_events", e.ID()),
			"ON CONFLICT (id) DO NOTHING must keep a single journal row")
		assert.Equal(t, firstSeq, journalGlobalSeq(t, pool, e.ID()),
			"global_seq must not advance on a conflicting re-append")
	})

	t.Run("duplicate id within one batched INSERT", func(t *testing.T) {
		e := newJournalEntry(t, projTopicA)
		// Two identical-id rows in a single multi-row INSERT (the WriteBatch shape):
		// ON CONFLICT DO NOTHING must skip the second without erroring the statement.
		require.NoError(t, txm.RunInTx(ctx, func(txCtx context.Context) error {
			return jw.appendProjectionEvents(txCtx, []kout.Entry{e, e})
		}))
		assert.Equal(t, 1, countRows(t, pool, "projection_events", e.ID()),
			"a duplicate id within one batched INSERT must yield a single journal row")
	})
}
