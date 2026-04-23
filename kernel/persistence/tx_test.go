package persistence

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopTxRunner_ImplementsInterface(t *testing.T) {
	var _ TxRunner = NoopTxRunner{}
}

func TestNoopTxRunner_ExecutesFn(t *testing.T) {
	var called bool
	err := NoopTxRunner{}.RunInTx(context.Background(), func(ctx context.Context) error {
		called = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, called, "fn should be called")
}

func TestNoopTxRunner_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	got := NoopTxRunner{}.RunInTx(context.Background(), func(ctx context.Context) error {
		return want
	})
	assert.ErrorIs(t, got, want)
}

func TestNoopTxRunner_NilFnPanics(t *testing.T) {
	assert.PanicsWithValue(t, "persistence: nil fn passed to RunInTx", func() {
		_ = NoopTxRunner{}.RunInTx(context.Background(), nil)
	})
}

func TestNoopTxRunner_Noop(t *testing.T) {
	assert.True(t, NoopTxRunner{}.Noop())
}

func TestNoopTxRunner_PassesContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "val")
	err := NoopTxRunner{}.RunInTx(ctx, func(fnCtx context.Context) error {
		assert.Equal(t, "val", fnCtx.Value(key{}))
		return nil
	})
	require.NoError(t, err)
}

func TestRunnerOrNoop_NilReturnsNoop(t *testing.T) {
	runner := RunnerOrNoop(nil)

	_, ok := runner.(NoopTxRunner)
	assert.True(t, ok, "nil TxRunner must become NoopTxRunner")

	var called bool
	err := runner.RunInTx(context.Background(), func(context.Context) error {
		called = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, called, "NoopTxRunner fallback must execute fn")
}

func TestRunnerOrNoop_RealRunnerReturnedUnchanged(t *testing.T) {
	real := &recordingTxRunner{}

	runner := RunnerOrNoop(real)

	assert.Same(t, real, runner)
	err := runner.RunInTx(context.Background(), func(context.Context) error {
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, real.calls)
}

func TestRunnerOrNoop_NilFallbackPreservesNilFnPanic(t *testing.T) {
	runner := RunnerOrNoop(nil)

	assert.PanicsWithValue(t, "persistence: nil fn passed to RunInTx", func() {
		_ = runner.RunInTx(context.Background(), nil)
	})
}

type recordingTxRunner struct {
	calls int
}

func (r *recordingTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.calls++
	return fn(ctx)
}
