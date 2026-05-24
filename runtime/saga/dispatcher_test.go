package saga_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ghbvf/gocell/runtime/saga"
)

// compile-time interface check.
var _ saga.Dispatcher = saga.NoopDispatcher{}

func TestNoopDispatcher_Kick_NilCtx(t *testing.T) {
	var d saga.NoopDispatcher
	// Must not panic with nil context.
	d.Kick(nil) //nolint:staticcheck // intentional nil-ctx test
}

func TestNoopDispatcher_Kick_NonNilCtx(t *testing.T) {
	var d saga.NoopDispatcher
	d.Kick(context.Background())
}

func TestNoopDispatcher_Kick_NoSideEffects(t *testing.T) {
	// Call many times; nothing observable should change.
	var d saga.NoopDispatcher
	for i := range 10 {
		_ = i
		d.Kick(context.Background())
	}
}

// recordingDispatcher is a package-internal test helper for integration tests
// in later PRs. It counts how many times Kick has been called.
type recordingDispatcher struct {
	kicks atomic.Int32
}

func (r *recordingDispatcher) Kick(context.Context) {
	r.kicks.Add(1)
}

func (r *recordingDispatcher) KickCount() int {
	return int(r.kicks.Load())
}

func TestRecordingDispatcher_CountsKicks(t *testing.T) {
	d := &recordingDispatcher{}
	if got := d.KickCount(); got != 0 {
		t.Fatalf("initial KickCount = %d, want 0", got)
	}

	const n = 5
	for range n {
		d.Kick(context.Background())
	}
	if got := d.KickCount(); got != n {
		t.Fatalf("KickCount = %d, want %d", got, n)
	}
}

// recordingDispatcher must satisfy Dispatcher so integration tests can pass it
// as a Dispatcher.
var _ saga.Dispatcher = (*recordingDispatcher)(nil)
