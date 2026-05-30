package projection_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// TestMemCheckpointStore_Conformance runs the shared conformance suite against
// MemCheckpointStore, verifying that it satisfies the CheckpointStore contract.
func TestMemCheckpointStore_Conformance(t *testing.T) {
	projectiontest.RunCheckpointConformance(t, projection.NewMemCheckpointStore())
}

// TestMemReplaySource_Conformance enrolls MemReplaySource in the shared
// ReplaySource conformance suite (PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01).
// Called from the external test package to avoid the import cycle
// projection → projectiontest → projection.
func TestMemReplaySource_Conformance(t *testing.T) {
	clk := clockmock.New(time.Now())
	seedEntries := func(n int) []outbox.Entry {
		entries := make([]outbox.Entry, n)
		for i := range entries {
			e, err := outbox.NewEntry(clk, context.Background(), "topic.v1", []byte(`{}`))
			if err != nil {
				t.Fatalf("outbox.NewEntry: %v", err)
			}
			entries[i] = e
		}
		return entries
	}
	projectiontest.RunReplaySourceConformance(t, projection.NewMemReplaySource(), seedEntries)
}

// TestMemCheckpointStore_ConcurrentSafety verifies that concurrent SaveOffset
// calls do not cause data races. Run with -race.
func TestMemCheckpointStore_ConcurrentSafety(t *testing.T) {
	t.Parallel()
	store := projection.NewMemCheckpointStore()
	ctx := context.Background()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			_ = store.SaveOffset(ctx, "cell-race", "proj-race", int64(n))
			_, _ = store.LoadOffset(ctx, "cell-race", "proj-race")
		}(i)
	}
	wg.Wait()
}
