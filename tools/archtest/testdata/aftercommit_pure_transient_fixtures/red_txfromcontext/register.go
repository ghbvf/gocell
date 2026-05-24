//go:build archtest_fixture

// Package redtxfromcontext is a RED fixture for AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
// blind spot B2 (F4): the hook reaches for the committed tx via the GENERIC
// persistence.TxFromContext[pgx.Tx]. The callee is an *ast.IndexExpr, so the
// escape scan must unwrap the generic instantiation to detect it.
package redtxfromcontext

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/kernel/persistence"
)

func register(ctx context.Context) {
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		_, _ = persistence.TxFromContext[pgx.Tx](hookCtx)
	})
}
