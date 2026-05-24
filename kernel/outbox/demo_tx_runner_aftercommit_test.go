package outbox_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/persistence/persistencetest"
)

// TestDemoTxRunner_AfterCommitConformance asserts the demo (no-real-tx) runner
// still honors the after-commit hook contract — hooks fire after fn succeeds,
// not when it errors — so demo assemblies behave identically to durable ones.
func TestDemoTxRunner_AfterCommitConformance(t *testing.T) {
	persistencetest.RunAfterCommitConformance(t, outbox.DemoTxRunner{})
}

// TestDemoTxRunner_AfterCommit_NestedFiresOnceAfterOutermost covers the
// outermost-only semantics for a pass-through runner (the savepoint analog is
// covered in adapters/postgres): a hook registered inside a nested RunInTx must
// not fire when the inner call returns — only when the outermost call drains.
func TestDemoTxRunner_AfterCommit_NestedFiresOnceAfterOutermost(t *testing.T) {
	r := outbox.DemoTxRunner{}
	var order []string
	err := r.RunInTx(context.Background(), func(outer context.Context) error {
		persistence.RegisterAfterCommit(outer, func(context.Context) { order = append(order, "outer") })
		innerErr := r.RunInTx(outer, func(inner context.Context) error {
			persistence.RegisterAfterCommit(inner, func(context.Context) { order = append(order, "inner") })
			return nil
		})
		require.NoError(t, innerErr)
		require.Empty(t, order, "no hook fires when the nested RunInTx returns; only the outermost drains")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"outer", "inner"}, order,
		"both hooks fire exactly once, after the outermost commit, in registration order")
}
