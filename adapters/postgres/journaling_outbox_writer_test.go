package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Unit tests for the journaling decorator's DB-free paths (pure SQL builder, the
// nil-inner fail-fast, the no-transaction error, and the append-failure log path).
// The happy double-write paths are covered by the //go:build integration suite.

func unitEntry(t *testing.T, topic string) outbox.Entry {
	t.Helper()
	e, err := outbox.NewEntry(clockmock.New(time.Now()), context.Background(), topic, []byte(`{"k":"v"}`))
	require.NoError(t, err)
	return e
}

func newUnitWriter(t *testing.T, topics ...string) *journalingOutboxWriter {
	t.Helper()
	w := NewJournalingOutboxWriter(NewOutboxWriter(clockmock.New(time.Now())), topics)
	return w.(*journalingOutboxWriter)
}

func TestNewJournalingOutboxWriter_NilInnerPanics(t *testing.T) {
	assert.Panics(t, func() { NewJournalingOutboxWriter(nil, []string{"event.x.v1"}) },
		"a nil inner is a composition-root programmer error and must fail-fast at construction")
}

func TestWrite_InnerErrorPropagates(t *testing.T) {
	w := newUnitWriter(t, "event.a.v1")
	// No tx → the base writer fails before journaling; the decorator returns its
	// error without attempting an append.
	err := w.Write(context.Background(), unitEntry(t, "event.a.v1"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "requires a transaction")
}

func TestWriteBatch_InnerErrorPropagates(t *testing.T) {
	w := newUnitWriter(t, "event.a.v1")
	err := w.WriteBatch(context.Background(), []outbox.Entry{unitEntry(t, "event.a.v1")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "requires a transaction")
}

func TestBuildProjectionInsert(t *testing.T) {
	entries := []outbox.Entry{unitEntry(t, "event.a.v1"), unitEntry(t, "event.b.v1")}
	query, args, err := buildProjectionInsert(entries)
	require.NoError(t, err)

	assert.Contains(t, query, "INSERT INTO projection_events")
	assert.Contains(t, query, "ON CONFLICT (id) DO NOTHING")
	assert.Contains(t, query, "VALUES")
	// One placeholder tuple per entry, projectionEventCols bind args each.
	assert.Len(t, args, len(entries)*projectionEventCols)
}

func TestAppendProjectionEvents_NoTransaction(t *testing.T) {
	w := newUnitWriter(t, "event.a.v1")
	// No pgx.Tx in ctx → fail-closed before any Exec.
	err := w.appendProjectionEvents(context.Background(), []outbox.Entry{unitEntry(t, "event.a.v1")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "requires a transaction")
}

func TestJournalProjectionSubset_EmptySubsetIsNoop(t *testing.T) {
	w := newUnitWriter(t, "event.a.v1")
	// Entry topic is NOT in the projection set → subset empty → returns nil without
	// touching the (txless) ambient context.
	err := w.journalProjectionSubset(context.Background(), []outbox.Entry{unitEntry(t, "event.other.v1")})
	require.NoError(t, err)
}

func TestJournalProjectionSubset_AppendFailurePropagates(t *testing.T) {
	w := newUnitWriter(t, "event.a.v1")
	// Topic matches → subset non-empty → appendProjectionEvents runs and fails on the
	// missing tx; journalProjectionSubset logs and propagates the error (covers the
	// append-failure path that rolls back the business tx).
	err := w.journalProjectionSubset(context.Background(), []outbox.Entry{unitEntry(t, "event.a.v1")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "requires a transaction")
}
