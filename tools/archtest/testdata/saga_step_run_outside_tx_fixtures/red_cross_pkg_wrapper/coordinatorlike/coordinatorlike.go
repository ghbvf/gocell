//go:build archtest_fixture

// Package coordinatorlike mimics runtime/saga (the package that owns RunInTx):
// it imports executorlike and calls the exported Wrapper — which transitively
// reaches the OTHER package's unexported safeRun — inside a RunInTx closure.
// Proves A2's taint set spans packages by *types.Func object identity within
// one packages.Load (gh #1998 review F3).
package coordinatorlike

import (
	"context"

	"github.com/ghbvf/gocell/tools/archtest/testdata/saga_step_run_outside_tx_fixtures/red_cross_pkg_wrapper/executorlike"
)

type fakeTxRunner struct{}

func (fakeTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

// violatesCrossPkg calls executorlike.Wrapper (reaching safeRun in the OTHER
// package) inside the RunInTx closure — must be flagged by transitive A2.
func violatesCrossPkg(ctx context.Context, tx fakeTxRunner) error {
	return tx.RunInTx(ctx, func(txCtx context.Context) error {
		return executorlike.Wrapper(txCtx)
	})
}
