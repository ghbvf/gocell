//go:build archtest_fixture

// Package green is a GREEN fixture for AFTERCOMMIT-HOOK-PURE-TRANSIENT-01:
// a func-literal hook performing only transient work. Expect 0 diagnostics.
package green

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/persistence"
)

type cache interface{ Invalidate(key string) }

func register(ctx context.Context, c cache) {
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		c.Invalidate("k")                          // transient: cache invalidation
		slog.InfoContext(hookCtx, "committed")     // transient: log
	})
}
