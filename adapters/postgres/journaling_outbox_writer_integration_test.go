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
//   ① atomicity   — a rolled-back business tx discards BOTH the outbox_entries and
//                    the projection_events rows.
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

// TestJournalingOutboxWriter_OnConflictIdempotency drives the unexported append twice
// for the same id (this test is in package postgres). A second full Write would abort
// on the outbox_entries PK; appending directly isolates the journal's ON CONFLICT path.
func TestJournalingOutboxWriter_OnConflictIdempotency(t *testing.T) {
	w, txm, pool := newJournalingWriter(t, projTopicA)
	jw := w.(*journalingOutboxWriter)
	ctx := context.Background()
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
}
