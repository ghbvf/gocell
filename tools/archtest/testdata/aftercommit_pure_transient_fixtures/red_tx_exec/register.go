//go:build archtest_fixture

// Package redtxexec is a RED fixture for AFTERCOMMIT-HOOK-PURE-TRANSIENT-01 A2:
// the hook literal calls a pgx.Tx method (captured from the enclosing scope).
// Writing to the committed tx is a persistent side effect; A2 must flag it.
package redtxexec

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/kernel/persistence"
)

func register(ctx context.Context, tx pgx.Tx) {
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		_, _ = tx.Exec(hookCtx, "UPDATE t SET x = 1")
	})
}
