//go:build archtest_fixture

package redsaferunwrapperinrunintx

import (
	"context"
)

// fakeTxRunner mimics the persistence.TxRunner shape (method name "RunInTx").
type fakeTxRunner struct{}

func (fakeTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

// safeRun is the taint seed (matched by name, mirroring executor.safeRun).
func safeRun(_ context.Context) error { return nil }

// wrap is a NAMED helper that transitively reaches safeRun. The old direct
// A2 scan does not chase wrap's body, so a wrap() callsite inside a RunInTx
// closure was missed; transitive A2 taints wrap and flags the callsite.
func wrap(ctx context.Context) error {
	return safeRun(ctx)
}

// vwrap is a package-level FuncLit-valued var reaching safeRun (the cx-1
// var-indirection case): calling vwrap() reaches safeRun without any named
// wrapper func. Transitive A2 taints the var and flags its callsite.
var vwrap = func(ctx context.Context) error {
	return safeRun(ctx)
}

// violatesViaNamedWrapper calls wrap() inside the RunInTx closure body.
func violatesViaNamedWrapper(ctx context.Context, tx fakeTxRunner) error {
	return tx.RunInTx(ctx, func(txCtx context.Context) error {
		return wrap(txCtx)
	})
}

// violatesViaFuncLitVar calls vwrap() inside the RunInTx closure body.
func violatesViaFuncLitVar(ctx context.Context, tx fakeTxRunner) error {
	return tx.RunInTx(ctx, func(txCtx context.Context) error {
		return vwrap(txCtx)
	})
}
