//go:build archtest_fixture

// Package redcellwriter is a RED fixture for AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
// A2 (F3): the hook writes to an outbox.CellWriter — the sealed marker that
// EMBEDS outbox.Writer but is named differently. Exact-name matching misses it;
// the types.Implements check must flag it.
package redcellwriter

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
)

func register(ctx context.Context, cw outbox.CellWriter) {
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		_ = cw.Write(hookCtx, outbox.Entry{})
	})
}
