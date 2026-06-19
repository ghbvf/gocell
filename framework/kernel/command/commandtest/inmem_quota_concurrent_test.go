package commandtest_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/kernel/command/commandtest"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// TestInMemQueue_MaxPendingPerDevice_Concurrent proves the per-device Pending cap
// (EnqueueOptions.MaxPendingPerDevice, F-S-005 #822) is a HARD invariant under
// concurrency: count + insert happen under one lock, so M concurrent enqueues for
// the same device admit AT MOST `limit` — there is no read-then-write TOCTOU
// window. Run with -race. Against the pre-#2457 design (the service counting via a
// separate ScanActive read before Enqueue) every racer would pass the check and
// all M would be admitted; this test is the regression guard for that race.
func TestInMemQueue_MaxPendingPerDevice_Concurrent(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	const (
		limit  = 5
		racers = 64
		device = "dev-race"
	)
	now := time.Now()

	var (
		wg       sync.WaitGroup
		admitted atomic.Int64
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := q.Enqueue(context.Background(),
				makeEntry(fmt.Sprintf("race-%d", i), device, now),
				command.EnqueueOptions{MaxPendingPerDevice: limit})
			if err == nil {
				admitted.Add(1)
				return
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) || ec.Code != errcode.ErrRateLimited {
				t.Errorf("racer %d: unexpected error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if got := admitted.Load(); got != limit {
		t.Errorf("admitted = %d, want exactly %d (hard cap holds under concurrency)", got, limit)
	}
	pending, err := q.ScanActive(context.Background(), command.ScanFilter{
		DeviceID: device, Statuses: []command.Status{command.StatusPending},
	})
	if err != nil {
		t.Fatalf("ScanActive: %v", err)
	}
	if len(pending) != limit {
		t.Errorf("Pending = %d, want exactly %d (no overshoot)", len(pending), limit)
	}
}
