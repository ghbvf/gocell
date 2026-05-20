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

// TestRecorderConcurrentMixedReadWrite asserts the Recorder is race-free under
// concurrent Emit, Entries/EntriesByType reads, and Reset calls — mirroring the
// real usage pattern where a transactional service emits while test goroutines
// poll for captured entries.
func TestRecorderConcurrentMixedReadWrite(t *testing.T) {
	r := outboxtest.NewRecorder()
	ctx := context.Background()

	const (
		writers   = 8
		readers   = 4
		resetters = 2
		perOp     = 50
	)

	var wg sync.WaitGroup
	wg.Add(writers + readers + resetters)

	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perOp; j++ {
				_ = r.Emit(ctx, outbox.Entry{
					ID:        "w",
					EventType: "user.created.v1",
					Payload:   []byte(`{"id":1}`),
					Metadata:  map[string]string{"k": "v"},
				})
			}
		}()
	}

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perOp; j++ {
				_ = r.Entries()
				_ = r.EntriesByType("user.created.v1")
			}
		}()
	}

	for i := 0; i < resetters; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perOp; j++ {
				r.Reset()
			}
		}()
	}

	wg.Wait()
	// No assertion on final count — Reset may have cleared entries. The race
	// detector is the actual verification target; PASS without -race is trivial.
}

// TestRecorderEntriesByTypeDefensiveCopy asserts that mutating the slice
// returned by EntriesByType — including Payload bytes and Metadata map entries
// — does not affect the Recorder's internal state.
func TestRecorderEntriesByTypeDefensiveCopy(t *testing.T) {
	r := outboxtest.NewRecorder()
	ctx := context.Background()

	original := outbox.Entry{
		ID:        "orig",
		EventType: "user.created.v1",
		Payload:   []byte(`{"u":1}`),
		Metadata:  map[string]string{"key": "original"},
	}
	require.NoError(t, r.Emit(ctx, original))

	// First call — mutate the returned copy aggressively.
	snap := r.EntriesByType("user.created.v1")
	require.Len(t, snap, 1)

	snap[0].ID = "MUTATED_ID"
	snap[0].Payload[0] = 0xFF
	snap[0].Metadata["key"] = "mutated"
	snap[0].Metadata["newkey"] = "extra"

	// Second call — Recorder internal state must be pristine.
	clean := r.EntriesByType("user.created.v1")
	require.Len(t, clean, 1, "internal entries must not be removed by mutation")
	assert.Equal(t, "orig", clean[0].ID, "ID must not be mutated via returned copy")
	assert.Equal(t, []byte(`{"u":1}`), clean[0].Payload, "Payload must not be mutated via returned copy")
	assert.Equal(t, map[string]string{"key": "original"}, clean[0].Metadata,
		"Metadata must not be mutated via returned copy")
}
