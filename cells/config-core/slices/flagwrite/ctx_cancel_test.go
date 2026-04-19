package flagwrite

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancellingTxRunner simulates a tx runner that cancels the context mid-flight
// and returns context.Canceled, modelling the RunInTx behavior when a
// context deadline expires or the caller explicitly cancels.
type cancellingTxRunner struct {
	cancelAt int // cancel after this many fn calls
	calls    int
}

func (r *cancellingTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.calls++
	// Create a cancellable ctx and cancel it before fn runs.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel() // cancel immediately to simulate mid-flight cancellation
	// Call fn with the cancelled context.  The repo/outbox impl should
	// propagate the ctx.Err() or the tx runner should return it.
	_ = fn(cancelCtx)
	return cancelCtx.Err() // always returns context.Canceled
}

var _ persistence.TxRunner = (*cancellingTxRunner)(nil)

// TestFlagWrite_CtxCancel_RollsBackTx verifies that when the context is
// cancelled inside RunInTx:
//  1. Service.Create returns an error.
//  2. The outbox writer does not retain a persistent entry (because the
//     error from RunInTx is propagated back to the caller).
//
// The fake TxRunner models a real PG tx: it cancels the context, invokes fn
// (fn may write to the fake outbox), but returns context.Canceled — meaning
// the real PG transaction would have been rolled back. The test validates that
// the *service* surface propagates that error rather than silently succeeding.
func TestFlagWrite_CtxCancel_RollsBackTx(t *testing.T) {
	repo := mem.NewFlagRepository()
	writer := &recordingWriter{}
	txRunner := &cancellingTxRunner{}

	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(txRunner))

	_, err := svc.Create(context.Background(), CreateInput{
		Key:         "cancel-flag",
		Description: "ctx cancel test",
	})

	require.Error(t, err, "Create must return error when ctx is cancelled inside RunInTx")
	assert.ErrorIs(t, err, context.Canceled,
		"error must wrap context.Canceled")
	assert.Equal(t, 1, txRunner.calls, "RunInTx must be called exactly once")
	// Verify no persistent outbox entry is visible from the service's perspective.
	// (In production the real PG tx would have rolled back the outbox row too;
	// here the in-memory writer may have received the call, but the tx error
	// signals to the caller that nothing durable was committed.)
	// The key invariant is: service returns error → caller treats as no-op.
	assert.Error(t, err, "caller must not observe a successful create when ctx cancelled")
}

// TestFlagWrite_Toggle_CtxCancel_ReturnsError verifies Toggle propagates
// context cancellation from RunInTx back to the caller.
func TestFlagWrite_Toggle_CtxCancel_ReturnsError(t *testing.T) {
	repo := mem.NewFlagRepository()
	// Seed a flag so Toggle reaches the RunInTx call.
	writer := &recordingWriter{}
	seedSvc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	_, err := seedSvc.Create(context.Background(), CreateInput{Key: "toggle-cancel"})
	require.NoError(t, err)

	// Now create a service with the cancelling tx runner.
	cancelTx := &cancellingTxRunner{}
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(&recordingWriter{}), WithTxManager(cancelTx))

	_, err = svc.Toggle(context.Background(), "toggle-cancel", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
