//go:build archtest_fixture

// Package rednonliteral is a RED fixture for AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
// A1: the hook argument is a named func, not a func literal. A1 must flag it
// (blind spot B1 is closed by construction).
package rednonliteral

import (
	"context"

	"github.com/ghbvf/gocell/kernel/persistence"
)

func hook(context.Context) {}

func register(ctx context.Context) {
	persistence.RegisterAfterCommit(ctx, hook)
}
