//go:build archtest_fixture

package redsaferunninruntintx

import (
	"context"
)

// fakeTxRunner mimics the persistence.TxRunner shape (method name "RunInTx").
// A2 matches the method name structurally, not via type resolution — any
// receiver with a RunInTx method satisfies the syntactic match.
type fakeTxRunner struct{}

func (fakeTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

// safeRun mimics the saga.safeRun shape so A2's identifier match fires.
func safeRun(_ context.Context) error { return nil }

// violatesA2 calls safeRun() directly inside the RunInTx closure body.
// SAGA-STEP-RUN-OUTSIDE-TX-01 A2 must flag the inner safeRun callsite.
func violatesA2(ctx context.Context, tx fakeTxRunner) error {
	return tx.RunInTx(ctx, func(txCtx context.Context) error {
		// Direct safeRun invocation inside the RunInTx closure — violates A2.
		return safeRun(txCtx)
	})
}
