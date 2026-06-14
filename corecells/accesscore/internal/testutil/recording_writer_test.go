package testutil

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

// TestRecordingWriter_AppendsEntriesOnSuccess verifies the happy-path:
// Write appends each entry to Entries in order and returns nil.
func TestRecordingWriter_AppendsEntriesOnSuccess(t *testing.T) {
	t.Parallel()
	w := &RecordingWriter{}

	e1, err := outbox.NewEntry(clock.Real(), context.Background(), "topic.a", []byte(`{"k":"v1"}`), outbox.WithID("evt-1"))
	require.NoError(t, err)
	e2, err := outbox.NewEntry(clock.Real(), context.Background(), "topic.b", []byte(`{"k":"v2"}`), outbox.WithID("evt-2"))
	require.NoError(t, err)

	require.NoError(t, w.Write(context.Background(), e1))
	require.NoError(t, w.Write(context.Background(), e2))

	require.Len(t, w.Entries, 2)
	assert.Equal(t, "evt-1", w.Entries[0].ID())
	assert.Equal(t, "topic.a", w.Entries[0].EventType())
	assert.Equal(t, "evt-2", w.Entries[1].ID())
	assert.Equal(t, "topic.b", w.Entries[1].EventType())
}

// TestRecordingWriter_ErrFieldShortCircuitsWrite verifies that when Err is
// pre-set, Write returns it immediately and does NOT append to Entries.
// This is the contract callers rely on for rollback-path tests
// (e.g., TestService_Durable_OutboxWriteFailure_PropagatesError).
func TestRecordingWriter_ErrFieldShortCircuitsWrite(t *testing.T) {
	t.Parallel()
	injected := errors.New("simulated outbox failure")
	w := &RecordingWriter{Err: injected}

	entry, err := outbox.NewEntry(clock.Real(), context.Background(), "topic.x", []byte(`{}`), outbox.WithID("evt-x"))
	require.NoError(t, err)
	err = w.Write(context.Background(), entry)

	assert.ErrorIs(t, err, injected)
	assert.Empty(t, w.Entries, "Err must short-circuit before appending")
}

// TestRecordingWriter_ImplementsOutboxWriter is a compile-time + runtime
// witness that *RecordingWriter satisfies outbox.Writer. The package-level
// `var _ outbox.Writer = (*RecordingWriter)(nil)` enforces it at build
// time; this test pins the same assertion in the test binary so the
// guarantee is visible in coverage reports.
func TestRecordingWriter_ImplementsOutboxWriter(t *testing.T) {
	t.Parallel()
	var _ outbox.Writer = (*RecordingWriter)(nil)
}
