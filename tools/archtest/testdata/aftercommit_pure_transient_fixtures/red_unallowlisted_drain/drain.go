//go:build archtest_fixture

// Package redunallowlisteddrain is a RED fixture for
// AFTERCOMMIT-HOOK-PURE-TRANSIENT-01 A3: a file outside the TxRunner allowlist
// calls the registry plumbing funcs. A3 must flag both call sites — only
// TxRunner implementations may install/drain the after-commit registry.
package redunallowlisteddrain

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/persistence"
)

func drain(ctx context.Context) {
	ctx, _ = persistence.WithAfterCommitRegistry(ctx)
	persistence.RunAfterCommitHooks(ctx)
}
