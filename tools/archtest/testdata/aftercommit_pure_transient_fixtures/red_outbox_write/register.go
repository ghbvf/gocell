//go:build archtest_fixture

// Package redoutboxwrite is a RED fixture for AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
// A2: the hook literal writes to an outbox.Writer. Outbox writes belong inside
// the tx body, not in a post-commit hook; A2 must flag it.
package redoutboxwrite

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
)

func register(ctx context.Context, w outbox.Writer) {
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		_ = w.Write(hookCtx, outbox.Entry{})
	})
}
