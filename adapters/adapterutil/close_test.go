package adapterutil_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/adapterutil"
)

func TestCloseWithDeadline_PreCancelledContextSkipsCloseFn(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := adapterutil.CloseWithDeadline(ctx, "test", func() error {
		t.Fatal("closeFn must not run when ctx is already done")
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
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
