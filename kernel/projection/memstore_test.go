package projection_test

import (
	"context"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// TestMemCheckpointStore_Conformance runs the shared conformance suite against
// MemCheckpointStore, verifying that it satisfies the CheckpointStore contract.
func TestMemCheckpointStore_Conformance(t *testing.T) {
	projectiontest.RunCheckpointConformance(t, projection.NewMemCheckpointStore())
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
