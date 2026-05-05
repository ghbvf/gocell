package ctxutil_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

const (
	// detachLongTimeout is generous enough that no test path should ever hit
	// it; used as the parent timeout in cases where the test only observes
	// short waits.
	detachLongTimeout = testtime.D5s
	// detachWaitObserve is a short wait used to verify a Done channel does
	// NOT fire (i.e. detach succeeded).
	detachWaitObserve = testtime.D50ms
	// detachShortDeadline forces the timeout to fire within the test budget.
	detachShortDeadline = testtime.D30ms
	// detachWaitForFire is the maximum wait before declaring the timeout did
	// not fire.
	detachWaitForFire = testtime.D200ms
)

func TestWithDetachedTimeout_ParentCancelDoesNotPropagate(t *testing.T) {
	t.Parallel()

	parent, parentCancel := context.WithCancel(context.Background())
	detached, cancel := ctxutil.WithDetachedTimeout(parent, detachLongTimeout)
	defer cancel()

	parentCancel()

	select {
	case <-detached.Done():
		t.Error("expected detached ctx to remain live after parent cancel")
	case <-time.After(detachWaitObserve):
		// expected: detached is not canceled by parent
	}

	if detached.Err() != nil {
		t.Errorf("detached.Err() = %v, want nil (timeout not yet reached)", detached.Err())
	}
}

func TestWithDetachedTimeout_TimeoutFires(t *testing.T) {
	t.Parallel()

	detached, cancel := ctxutil.WithDetachedTimeout(context.Background(), detachShortDeadline)
	defer cancel()

	select {
	case <-detached.Done():
		// expected
	case <-time.After(detachWaitForFire):
		t.Fatal("expected detached ctx timeout to fire within wait budget")
	}

	if !errors.Is(detached.Err(), context.DeadlineExceeded) {
		t.Errorf("detached.Err() = %v, want context.DeadlineExceeded", detached.Err())
	}
}

func TestWithDetachedTimeout_PreservesParentValues(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}

	parent := context.WithValue(context.Background(), ctxKey{}, "trace-123")
	detached, cancel := ctxutil.WithDetachedTimeout(parent, detachLongTimeout)
	defer cancel()

	if got := detached.Value(ctxKey{}); got != "trace-123" {
		t.Errorf("detached.Value(ctxKey{}) = %v, want %q", got, "trace-123")
	}
}

func TestWithDetachedTimeout_CancelFuncReleasesResources(t *testing.T) {
	t.Parallel()

	detached, cancel := ctxutil.WithDetachedTimeout(context.Background(), detachLongTimeout)

	cancel()

	select {
	case <-detached.Done():
		// expected: cancel() closes Done channel
	case <-time.After(detachWaitObserve):
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

	detached, cancel := ctxutil.WithDetachedTimeout(parent, detachLongTimeout)
	defer cancel()

	select {
	case <-detached.Done():
		t.Error("expected detached ctx to remain live even when parent was already canceled")
	case <-time.After(detachWaitObserve):
		// expected: detach cuts the already-canceled parent
	}
}
