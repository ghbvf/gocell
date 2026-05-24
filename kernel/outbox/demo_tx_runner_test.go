package outbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestDemoTxRunner_Noop confirms DemoTxRunner implements outbox.Nooper and
// reports Noop()==true, which is the signal CheckNotNoop uses under
// DurabilityDurable to reject demo-only TxRunners.
func TestDemoTxRunner_Noop(t *testing.T) {
	t.Parallel()
	var _ outbox.Nooper = outbox.DemoTxRunner{}
	assert.True(t, outbox.DemoTxRunner{}.Noop(), "DemoTxRunner.Noop must return true")
}

// TestDemoTxRunner_RunInTx_NilFn covers the documented safety branch:
// passing a nil fn is treated as a no-op and returns nil without panicking.
func TestDemoTxRunner_RunInTx_NilFn(t *testing.T) {
	t.Parallel()
	err := outbox.DemoTxRunner{}.RunInTx(context.Background(), nil)
	require.NoError(t, err)
}

// TestDemoTxRunner_RunInTx_ExecutesFn covers the happy path: fn runs once with
// a ctx derived from the supplied one and its return value propagates verbatim.
func TestDemoTxRunner_RunInTx_ExecutesFn(t *testing.T) {
	t.Parallel()
	type marker struct{}
	called := 0
	var gotCtx context.Context
	wantCtx := context.WithValue(context.Background(), marker{}, "sentinel")

	err := outbox.DemoTxRunner{}.RunInTx(wantCtx, func(ctx context.Context) error {
		called++
		gotCtx = ctx
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, called, "fn must run exactly once")
	// DemoTxRunner installs an after-commit registry on the ctx, so fn receives
	// a derived ctx (not the same value); values from the supplied ctx remain
	// visible through it.
	assert.Equal(t, "sentinel", gotCtx.Value(marker{}),
		"fn must receive a ctx derived from the one passed to RunInTx")
}

// TestDemoTxRunner_RunInTx_PropagatesError confirms fn errors flow back to the
// caller unchanged (no wrap, no swallow) — DemoTxRunner is a pass-through.
func TestDemoTxRunner_RunInTx_PropagatesError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("biz error")
	err := outbox.DemoTxRunner{}.RunInTx(context.Background(), func(context.Context) error {
		return sentinel
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

// TestDemoCellTxManager_NotNilAndNooper verifies that the demo factory returns
// a non-nil persistence.CellTxManager whose Nooper transparency is preserved
// (the wrapper must surface DemoTxRunner.Noop()==true so CheckNotNoop rejects
// the runner under DurabilityDurable). Mirrors the contract documented on
// DemoCellTxManager: "demo fallbacks can never silently slip into a durable
// assembly".
func TestDemoCellTxManager_NotNilAndNooper(t *testing.T) {
	t.Parallel()
	mgr := outbox.DemoCellTxManager()
	require.NotNil(t, mgr, "DemoCellTxManager must return a non-nil CellTxManager")

	type nooper interface{ Noop() bool }
	n, ok := mgr.(nooper)
	require.True(t, ok, "DemoCellTxManager result must implement Nooper transparently")
	assert.True(t, n.Noop(), "DemoCellTxManager result must report Noop()==true")
}

// TestDemoCellEmitter_NotNilAndNonDurable verifies the emitter-leg demo factory
// (mirror of DemoCellTxManager): returns a non-nil sealed CellEmitter that is
// non-durable (NoopWriter-backed) so a DurabilityDurable assembly passing it to
// WithEmitter still fails fast at ResolveCellEmitter rather than silently
// losing L2 atomicity. Probes() is nil (WriterEmitter exposes no probes).
func TestDemoCellEmitter_NotNilAndNonDurable(t *testing.T) {
	t.Parallel()
	em := outbox.DemoCellEmitter()
	require.NotNil(t, em, "DemoCellEmitter must return a non-nil CellEmitter")
	assert.False(t, em.Durable(), "DemoCellEmitter (NoopWriter-backed) must report Durable()==false")
	assert.Empty(t, em.Probes(), "DemoCellEmitter (WriterEmitter) must expose zero probes")
}
