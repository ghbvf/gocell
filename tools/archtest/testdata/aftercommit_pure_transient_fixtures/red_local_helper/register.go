//go:build archtest_fixture

// Package redlocalhelper is a RED fixture for AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
// A2 blind spot B4: the hook literal does not touch the tx directly, but calls a
// same-package unexported helper that does. A2's one-level heuristic must flag
// the helper call. (Deeper chains remain uncaught — documented Medium.)
package redlocalhelper

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/framework/kernel/persistence"
)

func touchTx(ctx context.Context, tx pgx.Tx) {
	_, _ = tx.Exec(ctx, "UPDATE t SET x = 1")
}

func register(ctx context.Context, tx pgx.Tx) {
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		touchTx(hookCtx, tx)
	})
}
