package ctxutil_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxutil"
)

func TestWithDetachedTimeout_ParentCancelDoesNotPropagate(t *testing.T) {
	t.Parallel()

	parent, parentCancel := context.WithCancel(context.Background())
	detached, cancel := ctxutil.WithDetachedTimeout(parent, 5*time.Second)
	defer cancel()

	parentCancel()

	select {
	case <-detached.Done():
		t.Error("expected detached ctx to remain live after parent cancel")
	case <-time.After(50 * time.Millisecond):
		// expected: detached is not canceled by parent
	}

	if detached.Err() != nil {
		t.Errorf("detached.Err() = %v, want nil (timeout not yet reached)", detached.Err())
	}
}

func TestWithDetachedTimeout_TimeoutFires(t *testing.T) {
	t.Parallel()

	detached, cancel := ctxutil.WithDetachedTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	select {
	case <-detached.Done():
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected detached ctx timeout to fire within 200ms")
	}

	if !errors.Is(detached.Err(), context.DeadlineExceeded) {
		t.Errorf("detached.Err() = %v, want context.DeadlineExceeded", detached.Err())
	}
}

func TestWithDetachedTimeout_PreservesParentValues(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}

	parent := context.WithValue(context.Background(), ctxKey{}, "trace-123")
	detached, cancel := ctxutil.WithDetachedTimeout(parent, time.Second)
	defer cancel()

	if got := detached.Value(ctxKey{}); got != "trace-123" {
		t.Errorf("detached.Value(ctxKey{}) = %v, want %q", got, "trace-123")
	}
}

func TestWithDetachedTimeout_CancelFuncReleasesResources(t *testing.T) {
	t.Parallel()

	detached, cancel := ctxutil.WithDetachedTimeout(context.Background(), 5*time.Second)

	cancel()

	select {
	case <-detached.Done():
		// expected: cancel() closes Done channel
	case <-time.After(50 * time.Millisecond):
		t.Fatal("expected Done to be closed after cancel()")
	}

	if !errors.Is(detached.Err(), context.Canceled) {
		t.Errorf("detached.Err() after cancel() = %v, want context.Canceled", detached.Err())
	}
}

func TestWithDetachedTimeout_AlreadyCanceledParent(t *testing.T) {
	t.Parallel()

	parent, parentCancel := context.WithCancel(context.Background())
	parentCancel() // cancel before creating detached

	detached, cancel := ctxutil.WithDetachedTimeout(parent, time.Second)
	defer cancel()

	select {
	case <-detached.Done():
		t.Error("expected detached ctx to remain live even when parent was already canceled")
	case <-time.After(50 * time.Millisecond):
		// expected: detach cuts the already-canceled parent
	}
}
