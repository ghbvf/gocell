package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kworker "github.com/ghbvf/gocell/kernel/worker"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// testWorker is a simple Worker implementation for testing.
type testWorker struct {
	started  atomic.Bool
	stopped  atomic.Bool
	startErr error
	stopErr  error
	blockCh  chan struct{}
}

func newTestWorker() *testWorker {
	return &testWorker{blockCh: make(chan struct{})}
}

func (w *testWorker) Start(ctx context.Context) error {
	w.started.Store(true)
	if w.startErr != nil {
		return w.startErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.blockCh:
		return nil
	}
}

func (w *testWorker) Stop(_ context.Context) error {
	w.stopped.Store(true)
	select {
	case <-w.blockCh:
	default:
		close(w.blockCh)
	}
	return w.stopErr
}

func TestWorkerGroup_StartStop(t *testing.T) {
	g := NewWorkerGroup()
	w1 := newTestWorker()
	w2 := newTestWorker()
	g.Add(w1)
	g.Add(w2)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- g.Start(ctx)
	}()

	// Wait for workers to start.
	assert.Eventually(t, func() bool {
		return w1.started.Load() && w2.started.Load()
	}, testtime.EventuallyShort, testtime.D10ms)

	// Stop workers.
	cancel()
	err := <-done
	// context.Canceled is expected.
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWorkerGroup_StartError(t *testing.T) {
	g := NewWorkerGroup()
	w := newTestWorker()
	w.startErr = errors.New("start failed")
	g.Add(w)

	err := g.Start(context.Background())
	assert.Error(t, err)
	assert.Equal(t, "start failed", err.Error())
}

// earlyExitWorker.Start returns nil immediately (without ctx cancellation).
// WorkerGroup must convert that into ErrWorkerExitedEarly so callers can
// detect the abnormal signal via errors.Is.
type earlyExitWorker struct{}

func (earlyExitWorker) Start(_ context.Context) error { return nil }
func (earlyExitWorker) Stop(_ context.Context) error  { return nil }

func TestWorkerGroup_NilExitMappedToErrExitedEarly(t *testing.T) {
	g := NewWorkerGroup()
	g.Add(earlyExitWorker{})

	err := g.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, kworker.ErrWorkerExitedEarly)
}

func TestWorkerGroup_Stop(t *testing.T) {
	g := NewWorkerGroup()
	w1 := newTestWorker()
	w2 := newTestWorker()
	g.Add(w1)
	g.Add(w2)

	// Just test Stop directly.
	err := g.Stop(context.Background())
	require.NoError(t, err)
	assert.True(t, w1.stopped.Load())
	assert.True(t, w2.stopped.Load())
}

func TestWorkerGroup_StopSerialReverseOrder(t *testing.T) {
	g := NewWorkerGroup()
	var order []string
	var mu sync.Mutex

	// Create workers that record their stop order.
	for _, name := range []string{"first", "second", "third"} {
		w := newTestWorker()
		n := name
		w.stopErr = nil
		// Override Stop to record order.
		g.Add(&orderWorker{testWorker: w, name: n, order: &order, mu: &mu})
	}

	err := g.Stop(context.Background())
	require.NoError(t, err)

	// Serial reverse: third, second, first.
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"third", "second", "first"}, order)
}

// orderWorker wraps testWorker and records stop order.
type orderWorker struct {
	*testWorker
	name  string
	order *[]string
	mu    *sync.Mutex
}

func (w *orderWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	*w.order = append(*w.order, w.name)
	w.mu.Unlock()
	return w.testWorker.Stop(ctx)
}

func TestPeriodicWorker_ExecutesFunction(t *testing.T) {
	var count atomic.Int32
	pw := NewPeriodicWorker(testtime.D10ms, func(ctx context.Context) {
		count.Add(1)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- pw.Start(ctx)
	}()

	// Wait for at least 3 executions.
	assert.Eventually(t, func() bool {
		return count.Load() >= 3
	}, testtime.EventuallyShort, testtime.FastPoll)

	cancel()
	<-done
}

func TestPeriodicWorker_PanicIsolation(t *testing.T) {
	var count atomic.Int32
	pw := NewPeriodicWorker(testtime.D10ms, func(ctx context.Context) {
		n := count.Add(1)
		if n == 1 {
			panic("test panic")
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- pw.Start(ctx)
	}()

	// After panic on first call, subsequent calls should still work.
	assert.Eventually(t, func() bool {
		return count.Load() >= 3
	}, testtime.EventuallyShort, testtime.FastPoll)

	cancel()
	<-done
}

func TestPeriodicWorker_Stop(t *testing.T) {
	pw := NewPeriodicWorker(time.Hour, func(ctx context.Context) {})

	done := make(chan error, 1)
	go func() {
		done <- pw.Start(context.Background())
	}()

	time.Sleep(testtime.D20ms) //archtest:allow:test-sleep wait for goroutine to enter blocking Start; no started observable
	err := pw.Stop(context.Background())
	assert.NoError(t, err)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("periodic worker did not stop in time")
	}
}

func TestPeriodicWorker_RestartAfterStop(t *testing.T) {
	var count atomic.Int32
	pw := NewPeriodicWorker(testtime.D10ms, func(ctx context.Context) {
		count.Add(1)
	})

	// First run.
	done := make(chan error, 1)
	go func() {
		done <- pw.Start(context.Background())
	}()

	assert.Eventually(t, func() bool {
		return count.Load() >= 2
	}, testtime.EventuallyShort, testtime.FastPoll)

	err := pw.Stop(context.Background())
	require.NoError(t, err)
	<-done

	// Record count after first stop.
	countAfterFirstStop := count.Load()

	// Second run — should work without error.
	done2 := make(chan error, 1)
	go func() {
		done2 <- pw.Start(context.Background())
	}()

	assert.Eventually(t, func() bool {
		return count.Load() >= countAfterFirstStop+2
	}, testtime.EventuallyShort, testtime.FastPoll)

	err = pw.Stop(context.Background())
	require.NoError(t, err)
	<-done2
}

func TestWorkerGroup_CancelsSiblingsOnError(t *testing.T) {
	g := NewWorkerGroup()

	// failWorker returns an error immediately.
	fail := newTestWorker()
	fail.startErr = errors.New("boom")

	// longWorker blocks until context is canceled.
	long := newTestWorker()

	g.Add(long)
	g.Add(fail)

	done := make(chan error, 1)
	go func() {
		done <- g.Start(context.Background())
	}()

	select {
	case err := <-done:
		// The group should have returned with the fail worker's error.
		assert.Error(t, err)
		assert.Equal(t, "boom", err.Error())
	case <-time.After(testtime.EventuallyDefault):
		t.Fatal("WorkerGroup.Start did not return after sibling failure — sibling was not canceled")
	}
}

// Verify compile-time interface check (Fix #9).
var _ Worker = (*PeriodicWorker)(nil)
