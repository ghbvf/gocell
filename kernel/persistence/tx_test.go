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
	assert.Panics(t, func() {
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
