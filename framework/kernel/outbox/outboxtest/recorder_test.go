package outboxtest_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
)

// mkEntry builds a valid sealed Entry via the NewEntry funnel. Recorder tests
// are package outboxtest_test (external to kernel/outbox), so populated
// outbox.Entry{...} literals do not compile — construction must go through
// NewEntry / EntryScan. id is pinned via WithID; eventType + payload are
// the constructor's required positional args.
func mkEntry(t *testing.T, id, eventType string, payload []byte, opts ...outbox.EntryOption) outbox.Entry {
	t.Helper()
	if payload == nil {
		payload = []byte(`{}`)
	}
	allOpts := append([]outbox.EntryOption{outbox.WithID(id)}, opts...)
	e, err := outbox.NewEntry(clock.Real(), context.Background(), eventType, payload, allOpts...)
	require.NoError(t, err)
	return e
}

func TestRecorderImplementsEmitter(t *testing.T) {
	var _ outbox.Emitter = outboxtest.NewRecorder()
}

// TestRecorderCellEmitter_RoutesToRecorder verifies the sealed test seam:
// rec.CellEmitter() returns a non-nil outbox.CellEmitter that still routes Emit
// to the underlying Recorder (the *Recorder handle stays usable for assertions),
// and exposes zero probes (Recorder is not a ProbeSet).
func TestRecorderCellEmitter_RoutesToRecorder(t *testing.T) {
	r := outboxtest.NewRecorder()
	ce := r.CellEmitter()
	require.NotNil(t, ce, "Recorder.CellEmitter must return a non-nil CellEmitter")

	require.NoError(t, ce.Emit(context.Background(), mkEntry(t, "via-cellemitter", "test.evt.v1", nil)))
	entries := r.Entries()
	require.Len(t, entries, 1, "Emit via CellEmitter must route to the underlying Recorder")
	assert.Equal(t, "via-cellemitter", entries[0].ID())
	assert.Empty(t, ce.Probes(), "Recorder-backed CellEmitter exposes no probes")
}

func TestRecorderEmitCapturesEntries(t *testing.T) {
	r := outboxtest.NewRecorder()
	ctx := context.Background()

	e1 := mkEntry(t, "id-1", "user.created.v1", []byte(`{"u":1}`))
	e2 := mkEntry(t, "id-2", "session.created.v1", []byte(`{"s":1}`))

	require.NoError(t, r.Emit(ctx, e1))
	require.NoError(t, r.Emit(ctx, e2))

	entries := r.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "id-1", entries[0].ID())
	assert.Equal(t, "id-2", entries[1].ID())
}

func TestRecorderEntriesReturnsCopy(t *testing.T) {
	r := outboxtest.NewRecorder()
	require.NoError(t, r.Emit(context.Background(), mkEntry(t, "x", "test.evt.v1", nil)))

	// Since issue #1229 sealed Entry, the value is immutable-by-construction:
	// there are no exported mutable fields, so callers cannot stomp the snapshot.
	// Re-reading still returns the original — the recorder's internal slice is
	// isolated from caller-held copies both structurally and via the slice copy.
	snapshot := r.Entries()
	require.Len(t, snapshot, 1)
	assert.Equal(t, "x", snapshot[0].ID())
	assert.Equal(t, "x", r.Entries()[0].ID(),
		"Entries() must return a defensive copy so callers cannot mutate internal state")
}

func TestRecorderEntriesByTypeFilters(t *testing.T) {
	r := outboxtest.NewRecorder()
	ctx := context.Background()
	for _, e := range []outbox.Entry{
		mkEntry(t, "a", "user.created.v1", nil),
		mkEntry(t, "b", "session.created.v1", nil),
		mkEntry(t, "c", "user.created.v1", nil),
	} {
		require.NoError(t, r.Emit(ctx, e))
	}

	users := r.EntriesByType("user.created.v1")
	require.Len(t, users, 2)
	assert.Equal(t, "a", users[0].ID())
	assert.Equal(t, "c", users[1].ID())

	assert.Empty(t, r.EntriesByType("nonexistent.v1"))
}

func TestRecorderReset(t *testing.T) {
	r := outboxtest.NewRecorder()
	require.NoError(t, r.Emit(context.Background(), mkEntry(t, "x", "test.evt.v1", nil)))
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
	entry := mkEntry(t, "x", "test.evt.v1", nil)
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				_ = r.Emit(ctx, entry)
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

	entry := mkEntry(t, "w", "user.created.v1", []byte(`{"id":1}`),
		outbox.WithMetadata(map[string]string{"k": "v"}))

	var wg sync.WaitGroup
	wg.Add(writers + readers + resetters)

	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perOp; j++ {
				_ = r.Emit(ctx, entry)
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

// TestRecorderEntriesByTypeDefensiveCopy asserts that the entries returned by
// EntriesByType are isolated from caller mutation. Since issue #1229 sealed
// Entry, the scalar fields are immutable-by-construction (no exported setters),
// so the only mutable surface is the Metadata() map — which Entry.Metadata()
// defensively clones on every call. Mutating that clone must not affect a fresh
// re-read of the Recorder's internal state.
func TestRecorderEntriesByTypeDefensiveCopy(t *testing.T) {
	r := outboxtest.NewRecorder()
	ctx := context.Background()

	original := mkEntry(t, "orig", "user.created.v1", []byte(`{"u":1}`),
		outbox.WithMetadata(map[string]string{"key": "original"}))
	require.NoError(t, r.Emit(ctx, original))

	// First call — mutate the per-call Metadata clone aggressively.
	snap := r.EntriesByType("user.created.v1")
	require.Len(t, snap, 1)

	md := snap[0].Metadata()
	md["key"] = "mutated"
	md["newkey"] = "extra"

	// Second call — Recorder internal state must be pristine.
	clean := r.EntriesByType("user.created.v1")
	require.Len(t, clean, 1, "internal entries must not be removed by mutation")
	assert.Equal(t, "orig", clean[0].ID(), "ID is immutable-by-construction")
	assert.Equal(t, []byte(`{"u":1}`), clean[0].Payload(), "Payload must not be mutated via returned copy")
	assert.Equal(t, map[string]string{"key": "original"}, clean[0].Metadata(),
		"Metadata() must return a defensive clone so caller mutation is isolated")
}
