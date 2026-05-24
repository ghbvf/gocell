package persistence

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentinelTx is a stand-in tx carrier used to verify RunAfterCommitHooks strips
// the ambient transaction from the ctx passed to hooks.
type sentinelTx struct{ id int }

func TestRegisterAfterCommit_FiresInRegistrationOrder(t *testing.T) {
	ctx, top := WithAfterCommitRegistry(context.Background())
	require.True(t, top, "fresh ctx must install a registry")

	var order []int
	for i := 0; i < 3; i++ {
		i := i
		RegisterAfterCommit(ctx, func(context.Context) { order = append(order, i) })
	}

	RunAfterCommitHooks(ctx)
	assert.Equal(t, []int{0, 1, 2}, order, "hooks run FIFO in registration order")
}

func TestRegisterAfterCommit_OutsideTxPanics(t *testing.T) {
	assert.Panics(t, func() {
		RegisterAfterCommit(context.Background(), func(context.Context) {})
	}, "registering without an active tx registry is a programmer error")
}

func TestRegisterAfterCommit_NilHookIgnored(t *testing.T) {
	ctx, _ := WithAfterCommitRegistry(context.Background())
	require.NotPanics(t, func() { RegisterAfterCommit(ctx, nil) })

	ran := false
	RegisterAfterCommit(ctx, func(context.Context) { ran = true })
	RunAfterCommitHooks(ctx)
	assert.True(t, ran, "nil hook is skipped without disturbing real hooks")
}

func TestRunAfterCommitHooks_PanicIsolation(t *testing.T) {
	ctx, _ := WithAfterCommitRegistry(context.Background())

	var ran []int
	RegisterAfterCommit(ctx, func(context.Context) { ran = append(ran, 0) })
	RegisterAfterCommit(ctx, func(context.Context) { panic("boom") })
	RegisterAfterCommit(ctx, func(context.Context) { ran = append(ran, 2) })

	require.NotPanics(t, func() { RunAfterCommitHooks(ctx) },
		"a panicking hook must not propagate to the committing goroutine")
	assert.Equal(t, []int{0, 2}, ran, "hooks before and after a panic still run")
}

func TestRunAfterCommitHooks_StripsAmbientTx(t *testing.T) {
	// Install the registry on a ctx that already carries a tx carrier.
	base := context.WithValue(context.Background(), TxCtxKey, sentinelTx{id: 7})
	ctx, _ := WithAfterCommitRegistry(base)

	// Sanity: the tx is reachable inside the tx scope.
	_, ok := TxFromContext[sentinelTx](ctx)
	require.True(t, ok, "tx is reachable within the tx scope")

	var hookSawTx bool
	RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		_, present := TxFromContext[sentinelTx](hookCtx)
		hookSawTx = present
	})
	RunAfterCommitHooks(ctx)
	assert.False(t, hookSawTx,
		"the committed tx must be unreachable through the ctx passed to a hook")
}

func TestRunAfterCommitHooks_SurvivesParentCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, _ := WithAfterCommitRegistry(parent)

	var hookCtxErr error
	RegisterAfterCommit(ctx, func(hookCtx context.Context) { hookCtxErr = hookCtx.Err() })

	cancel() // parent canceled before drain (e.g. HTTP request returned)
	RunAfterCommitHooks(ctx)
	assert.NoError(t, hookCtxErr, "hooks run under a non-cancelable ctx")
}

func TestRegisterAfterCommit_DuringDrainIsIgnored(t *testing.T) {
	ctx, _ := WithAfterCommitRegistry(context.Background())

	reentrantRan := false
	RegisterAfterCommit(ctx, func(context.Context) {
		// Mirrors spring-tx snapshot semantics: a hook registered while hooks
		// are draining is not run this round (there is no next round).
		RegisterAfterCommit(ctx, func(context.Context) { reentrantRan = true })
	})

	RunAfterCommitHooks(ctx)
	assert.False(t, reentrantRan, "re-registration during drain is ignored")
}

func TestWithAfterCommitRegistry_NestedReturnsNotInstalled(t *testing.T) {
	ctx, top1 := WithAfterCommitRegistry(context.Background())
	require.True(t, top1)

	nested, top2 := WithAfterCommitRegistry(ctx)
	require.False(t, top2, "a registry already present must report installed=false")

	// Hooks registered via the nested ctx accumulate into the same registry and
	// fire when the outermost installer drains.
	var ran []string
	RegisterAfterCommit(ctx, func(context.Context) { ran = append(ran, "outer") })
	RegisterAfterCommit(nested, func(context.Context) { ran = append(ran, "inner") })
	RunAfterCommitHooks(ctx)
	assert.Equal(t, []string{"outer", "inner"}, ran)
}

func TestRunAfterCommitHooks_NoRegistryIsNoop(t *testing.T) {
	require.NotPanics(t, func() { RunAfterCommitHooks(context.Background()) },
		"draining a ctx with no registry is a safe no-op")
}
