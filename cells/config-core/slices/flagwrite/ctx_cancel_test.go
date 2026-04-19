package flagwrite

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancellingTxRunner simulates a tx runner that cancels the context and does
// NOT invoke fn — modelling a real PG transaction where the tx is rolled back
// before fn executes (e.g. context deadline already expired at tx start).
//
// This correctly tests rollback semantics: because fn is never called, the
// in-memory repo is never mutated, so we can assert the side-effect is absent.
type cancellingTxRunner struct {
	calls int
}

func (r *cancellingTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.calls++
	// Pre-cancel the context before fn runs, modelling a tx that detects
	// context cancellation at start and rolls back immediately.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	// Do not call fn: the transaction is rolled back, no side-effects applied.
	_ = fn // suppress unused-variable linter hint
	return cancelCtx.Err() // context.Canceled
}

var _ persistence.TxRunner = (*cancellingTxRunner)(nil)

// TestFlagWrite_CtxCancel_RollsBackTx verifies that when RunInTx cancels
// the context without calling fn:
//  1. Service.Create returns context.Canceled.
//  2. The in-memory repo has no entry for the key (rollback side-effect absent).
//  3. The outbox writer received no entries.
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
	assert.ErrorIs(t, err, context.Canceled, "error must wrap context.Canceled")
	assert.Equal(t, 1, txRunner.calls, "RunInTx must be called exactly once")

	// Verify rollback side-effect: repo must NOT have the flag.
	_, repoErr := repo.GetByKey(context.Background(), "cancel-flag")
	require.Error(t, repoErr, "repo must not have the flag after rollback")
	var ecErr *errcode.Error
	require.ErrorAs(t, repoErr, &ecErr)
	assert.Equal(t, errcode.ErrFlagNotFound, ecErr.Code,
		"ErrFlagNotFound expected — flag must not persist after ctx cancel rollback")

	// Verify outbox has no durable entries.
	assert.Empty(t, writer.entries, "outbox must have no entries after rollback")
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

	// Verify rollback: Toggle must not have changed the flag state.
	got, getErr := repo.GetByKey(context.Background(), "toggle-cancel")
	require.NoError(t, getErr)
	assert.False(t, got.Enabled, "Toggle rollback must not change enabled state")
}

// TestFlagWrite_Update_CtxCancel_ReturnsError verifies Update propagates
// context cancellation and leaves the flag unchanged.
func TestFlagWrite_Update_CtxCancel_ReturnsError(t *testing.T) {
	repo := mem.NewFlagRepository()
	writer := &recordingWriter{}
	seedSvc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	_, err := seedSvc.Create(context.Background(), CreateInput{
		Key:         "update-cancel",
		Description: "original",
	})
	require.NoError(t, err)

	cancelTx := &cancellingTxRunner{}
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(&recordingWriter{}), WithTxManager(cancelTx))

	_, err = svc.Update(context.Background(), UpdateInput{
		Key:         "update-cancel",
		Enabled:     true,
		Description: "should not apply",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	// Verify rollback: description must be the original value.
	got, getErr := repo.GetByKey(context.Background(), "update-cancel")
	require.NoError(t, getErr)
	assert.Equal(t, "original", got.Description, "Update rollback must not change description")
	assert.False(t, got.Enabled, "Update rollback must not change enabled state")
}

// TestFlagWrite_Delete_CtxCancel_ReturnsError verifies Delete propagates
// context cancellation and leaves the flag in the repo.
func TestFlagWrite_Delete_CtxCancel_ReturnsError(t *testing.T) {
	repo := mem.NewFlagRepository()
	writer := &recordingWriter{}
	seedSvc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	_, err := seedSvc.Create(context.Background(), CreateInput{Key: "delete-cancel"})
	require.NoError(t, err)

	cancelTx := &cancellingTxRunner{}
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(&recordingWriter{}), WithTxManager(cancelTx))

	err = svc.Delete(context.Background(), "delete-cancel")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	// Verify rollback: flag must still exist.
	_, getErr := repo.GetByKey(context.Background(), "delete-cancel")
	require.NoError(t, getErr, "Delete rollback must leave the flag in the repo")
}

// ErrFlagNotFoundSentinel is used in the integration test helper to detect
// not-found errors across errcode boundary.
var ErrFlagNotFoundSentinel = errors.New("flag not found sentinel")
