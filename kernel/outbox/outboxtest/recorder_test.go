package outboxtest_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
)

func TestRecorderImplementsEmitter(t *testing.T) {
	var _ outbox.Emitter = outboxtest.NewRecorder()
}

func TestRecorderEmitCapturesEntries(t *testing.T) {
	r := outboxtest.NewRecorder()
	ctx := context.Background()

	e1 := outbox.Entry{ID: "id-1", EventType: "user.created.v1", Payload: []byte(`{"u":1}`)}
	e2 := outbox.Entry{ID: "id-2", EventType: "session.created.v1", Payload: []byte(`{"s":1}`)}

	require.NoError(t, r.Emit(ctx, e1))
	require.NoError(t, r.Emit(ctx, e2))

	entries := r.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "id-1", entries[0].ID)
	assert.Equal(t, "id-2", entries[1].ID)
}

func TestRecorderEntriesReturnsCopy(t *testing.T) {
	r := outboxtest.NewRecorder()
	require.NoError(t, r.Emit(context.Background(), outbox.Entry{ID: "x"}))

	snapshot := r.Entries()
	snapshot[0].ID = "MUTATED"

	assert.Equal(t, "x", r.Entries()[0].ID,
		"Entries() must return a defensive copy so callers cannot mutate internal state")
}

func TestRecorderEntriesByTypeFilters(t *testing.T) {
	r := outboxtest.NewRecorder()
	ctx := context.Background()
	for _, e := range []outbox.Entry{
		{ID: "a", EventType: "user.created.v1"},
		{ID: "b", EventType: "session.created.v1"},
		{ID: "c", EventType: "user.created.v1"},
	} {
		require.NoError(t, r.Emit(ctx, e))
	}

	users := r.EntriesByType("user.created.v1")
	require.Len(t, users, 2)
	assert.Equal(t, "a", users[0].ID)
	assert.Equal(t, "c", users[1].ID)

	assert.Empty(t, r.EntriesByType("nonexistent.v1"))
}

func TestRecorderReset(t *testing.T) {
	r := outboxtest.NewRecorder()
	require.NoError(t, r.Emit(context.Background(), outbox.Entry{ID: "x"}))
	require.Len(t, r.Entries(), 1)

	r.Reset()
	assert.Empty(t, r.Entries())
}

// TestRecorderConcurrentEmit asserts the Recorder is safe for concurrent Emit
// calls — required because production Emitter implementations are called from
// multiple goroutines (transactional outbox writer + direct publishers).
func TestRecorderConcurrentEmit(t *testing.T) {
	r := outboxtest.NewRecorder()
	ctx := context.Background()

	const writers = 8
	const perWriter = 100
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				_ = r.Emit(ctx, outbox.Entry{ID: "x"})
			}
		}()
	}
	wg.Wait()

	assert.Len(t, r.Entries(), writers*perWriter)
}
