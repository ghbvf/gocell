package saga_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/framework/runtime/saga"
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
