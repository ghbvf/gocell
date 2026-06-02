//go:build archtest_fixture

// Package sagaenumredvarrelay is a RED fixture for F2 of the
// SAGA-METRIC-LABEL-VALUES-FROZEN-01 callsite guard: a variable whose
// initializer is an inline string literal of enum type bypasses the callsite
// guard because at the callsite tv.Value == nil (var, not const).  The fixed
// guard must flag the assignment site instead.  Expect 1 diagnostic.
package sagaenumredvarrelay

import (
	"context"

	"github.com/ghbvf/gocell/runtime/saga/executor"
)

func leaderSink(_ context.Context, _ executor.LeaderSkipReason) {}

func caller(ctx context.Context) {
	var x executor.LeaderSkipReason = "typo" // assignment of literal to enum var — MUST be flagged (F2)
	leaderSink(ctx, x)                       // callsite has tv.Value==nil (var), must NOT double-flag
}
