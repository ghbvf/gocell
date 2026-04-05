package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	}, time.Second, 10*time.Millisecond)

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

func TestPeriodicWorker_ExecutesFunction(t *testing.T) {
	var count atomic.Int32
	pw := NewPeriodicWorker(10*time.Millisecond, func(ctx context.Context) {
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
	}, time.Second, 5*time.Millisecond)

	cancel()
	<-done
}

func TestPeriodicWorker_PanicIsolation(t *testing.T) {
	var count atomic.Int32
	pw := NewPeriodicWorker(10*time.Millisecond, func(ctx context.Context) {
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
	}, time.Second, 5*time.Millisecond)

	cancel()
	<-done
}

func TestPeriodicWorker_Stop(t *testing.T) {
	pw := NewPeriodicWorker(time.Hour, func(ctx context.Context) {})

	done := make(chan error, 1)
	go func() {
		done <- pw.Start(context.Background())
	}()

	time.Sleep(20 * time.Millisecond)
	err := pw.Stop(context.Background())
	assert.NoError(t, err)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("periodic worker did not stop in time")
	}
}
