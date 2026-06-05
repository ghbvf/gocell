//go:build archtest_fixture

// Package green is a GREEN fixture for AFTERCOMMIT-HOOK-PURE-TRANSIENT-01:
// a func-literal hook performing only transient work. Expect 0 diagnostics.
//
// Regression for the A2 package-qualified-call guard: this package imports
// kernel/outbox so outbox.Writer IS in the banned-interface set that
// collectBannedInterfaces resolves from the import closure (the `w outbox.Writer`
// param keeps it referenced without calling any method on it). The hook calls
// package-qualified functions (slog.InfoContext / fmt.Sprintf); before the guard,
// info.TypeOf on those package idents yields Typ[Invalid], which spuriously
// "implements" outbox.Writer and falsely flagged A2. The guard skips
// package-qualified calls, so this stays GREEN.
package green

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
)

type cache interface{ Invalidate(key string) }

func register(ctx context.Context, c cache, _ outbox.Writer) {
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		c.Invalidate("k")                                         // transient: cache invalidation
		slog.InfoContext(hookCtx, fmt.Sprintf("committed %d", 1)) // transient: package-qualified calls
	})
}
