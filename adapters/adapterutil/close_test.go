package adapterutil_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/adapterutil"
)

func TestCloseWithDeadline_PreCancelledContextStillInvokesCloseFn(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel before calling CloseWithDeadline

	var called atomic.Bool
	done := make(chan struct{})
	err := adapterutil.CloseWithDeadline(ctx, "test", func() error {
		called.Store(true)
		close(done)
		return nil
	})

	// The helper must return ctx.Err() (context.Canceled) — ctx.Done branch
	// wins or closeFn completes first; in either case ctx.Err() is the return value.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}

	// closeFn must have been invoked (best-effort admitted close).
	// Give it a short grace period since the goroutine may not have run yet.
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("closeFn was not invoked within 100ms — pre-cancelled ctx must still run closeFn")
	}
	if !called.Load() {
		t.Fatal("closeFn atomic flag not set — best-effort close must run even with pre-cancelled ctx")
	}
}

func TestCloseWithDeadline_CloseSucceeds(t *testing.T) {
	t.Parallel()

	err := adapterutil.CloseWithDeadline(context.Background(), "test", func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestCloseWithDeadline_CloseReturnsErrorVerbatim(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	err := adapterutil.CloseWithDeadline(context.Background(), "test", func() error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want %v, got %v", sentinel, err)
	}
}

func TestCloseWithDeadline_DeadlineFiresBeforeClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := adapterutil.CloseWithDeadline(ctx, "test", func() error {
		time.Sleep(500 * time.Millisecond)
		return nil
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("deadline enforcement too slow: %v", elapsed)
	}
}

func TestCloseWithDeadline_CloseCompletesBeforeDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := adapterutil.CloseWithDeadline(ctx, "test", func() error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestCloseWithDeadline_CloseErrorTakesPrecedenceOverDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	sentinel := errors.New("close failed")
	err := adapterutil.CloseWithDeadline(ctx, "test", func() error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want %v, got %v", sentinel, err)
	}
}
