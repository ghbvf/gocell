package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeWorker is a test-only Worker that records Start/Stop invocations.
type fakeWorker struct {
	startCount atomic.Int32
	stopCount  atomic.Int32
	gotCtx     atomic.Pointer[context.Context]
	startErr   error
}

func (f *fakeWorker) Start(ctx context.Context) error {
	f.startCount.Add(1)
	f.gotCtx.Store(&ctx)
	return f.startErr
}

func (f *fakeWorker) Stop(ctx context.Context) error {
	f.stopCount.Add(1)
	return nil
}

// TestLazyWorker_NotResolved_StartStopNoOp verifies that an unresolved LazyWorker
// returns nil from Start and Stop without panicking.
func TestLazyWorker_NotResolved_StartStopNoOp(t *testing.T) {
	t.Parallel()

	lazy := Lazy()
	ctx := context.Background()

	if err := lazy.Start(ctx); err != nil {
		t.Fatalf("Start on nil delegate: want nil, got %v", err)
	}
	if err := lazy.Stop(ctx); err != nil {
		t.Fatalf("Stop on nil delegate: want nil, got %v", err)
	}
}

// TestLazyWorker_Resolved_DelegatesStart verifies that after Set, Start is
// forwarded to the delegate with the same context.
func TestLazyWorker_Resolved_DelegatesStart(t *testing.T) {
	t.Parallel()

	lazy := Lazy()
	fake := &fakeWorker{}

	stored := lazy.Set(fake)
	if !stored {
		t.Fatal("Set: want true on first call, got false")
	}

	ctx := context.WithValue(context.Background(), struct{ k string }{"key"}, "val")
	if err := lazy.Start(ctx); err != nil {
		t.Fatalf("Start: unexpected error %v", err)
	}

	if n := fake.startCount.Load(); n != 1 {
		t.Fatalf("startCount: want 1, got %d", n)
	}
	if got := fake.gotCtx.Load(); got == nil || *got != ctx {
		t.Fatal("Start received wrong context")
	}
}

// TestLazyWorker_Resolved_DelegatesStop verifies that after Set, Stop is
// forwarded to the delegate.
func TestLazyWorker_Resolved_DelegatesStop(t *testing.T) {
	t.Parallel()

	lazy := Lazy()
	fake := &fakeWorker{}

	if !lazy.Set(fake) {
		t.Fatal("Set: want true on first call, got false")
	}

	ctx := context.Background()
	if err := lazy.Stop(ctx); err != nil {
		t.Fatalf("Stop: unexpected error %v", err)
	}
	if n := fake.stopCount.Load(); n != 1 {
		t.Fatalf("stopCount: want 1, got %d", n)
	}
}

// TestLazyWorker_ConcurrentSet_FirstWins verifies that exactly one of 100
// concurrent goroutines wins the Set race and the rest return false.
func TestLazyWorker_ConcurrentSet_FirstWins(t *testing.T) {
	t.Parallel()

	lazy := Lazy()

	const goroutines = 100
	winners := make([]*fakeWorker, goroutines)
	for i := range winners {
		winners[i] = &fakeWorker{}
	}

	var (
		wg        sync.WaitGroup
		winCount  atomic.Int32
		winWorker atomic.Pointer[Worker]
	)

	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(w *fakeWorker) {
			defer wg.Done()
			<-start
			if lazy.Set(w) {
				winCount.Add(1)
				var iw Worker = w
				winWorker.Store(&iw)
			}
		}(winners[i])
	}

	close(start)
	wg.Wait()

	if n := winCount.Load(); n != 1 {
		t.Fatalf("Set winner count: want 1, got %d", n)
	}

	// The stored delegate must equal the single winner.
	stored := lazy.ptr.Load()
	if stored == nil {
		t.Fatal("ptr.Load(): want non-nil")
	}
	expected := winWorker.Load()
	if expected == nil || *stored != *expected {
		t.Fatal("stored delegate does not match the Set winner")
	}
}

// TestLazyWorker_SetNil_Rejected verifies that Set(nil) returns false,
// does not flip hasSet, and allows a subsequent Set with a real worker.
func TestLazyWorker_SetNil_Rejected(t *testing.T) {
	t.Parallel()

	lazy := Lazy()

	if lazy.Set(nil) {
		t.Fatal("Set(nil): want false, got true")
	}
	if lazy.hasSet.Load() {
		t.Fatal("hasSet should remain false after Set(nil)")
	}

	fake := &fakeWorker{}
	if !lazy.Set(fake) {
		t.Fatal("Set(real) after Set(nil): want true, got false")
	}
}

// TestLazyWorker_SetAfterStart_IgnoredBecauseFirstWon verifies that a second
// Set after Start is ignored (first-wins) and Stop still delegates to the
// original worker.
func TestLazyWorker_SetAfterStart_IgnoredBecauseFirstWon(t *testing.T) {
	t.Parallel()

	lazy := Lazy()
	workerA := &fakeWorker{}
	workerB := &fakeWorker{startErr: errors.New("should not be called")}

	if !lazy.Set(workerA) {
		t.Fatal("Set(workerA): want true")
	}

	ctx := context.Background()
	if err := lazy.Start(ctx); err != nil {
		t.Fatalf("Start: unexpected error %v", err)
	}
	if n := workerA.startCount.Load(); n != 1 {
		t.Fatalf("workerA.startCount: want 1, got %d", n)
	}

	if lazy.Set(workerB) {
		t.Fatal("Set(workerB): want false (first-wins), got true")
	}

	if err := lazy.Stop(ctx); err != nil {
		t.Fatalf("Stop: unexpected error %v", err)
	}
	if n := workerA.stopCount.Load(); n != 1 {
		t.Fatalf("workerA.stopCount: want 1, got %d", n)
	}
	if n := workerB.stopCount.Load(); n != 0 {
		t.Fatalf("workerB.stopCount: want 0, got %d", n)
	}
}
