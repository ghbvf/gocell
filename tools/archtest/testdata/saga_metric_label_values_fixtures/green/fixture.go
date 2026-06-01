//go:build archtest_fixture

// Package sagaenumgreen is the GREEN over-fire guard for the
// SAGA-METRIC-LABEL-VALUES-FROZEN-01 callsite guard.  The following are all
// allowed and must NOT produce diagnostics:
//   - a declared executor enum const referenced via qualified selector (F1: it
//     lives in runtime/saga/executor, so the foreign-package check passes)
//   - a non-constant typed var (F2: assignment from a function call, not a literal)
//   - a var passed to a sink (callsite tv.Value==nil)
//   - string(reason) conversion (non-enum type at the callsite)
package sagaenumgreen

import (
	"context"
	"errors"

	"github.com/ghbvf/gocell/runtime/saga/executor"
)

func leaderSink(_ context.Context, _ executor.LeaderSkipReason) {}
func tickSink(_ context.Context, _ executor.TickResult)         {}
func stringSink(_ string)                                       {}

// classifyLike simulates a non-constant producer (like classifyLeaderSkip)
// whose return value is an enum type but carries no compile-time constant.
func classifyLike(err error) executor.LeaderSkipReason {
	if errors.Is(err, context.Canceled) {
		return executor.LeaderSkipCtxCanceled
	}
	return executor.LeaderSkipContended
}

func caller(ctx context.Context, r executor.LeaderSkipReason) {
	leaderSink(ctx, executor.LeaderSkipContended) // declared executor const — allowed (F1)
	leaderSink(ctx, r)                            // non-constant parameter var — allowed
	tickSink(ctx, executor.TickClaimed)           // declared executor const — allowed (F1)

	// F2 GREEN: var initialised from a function call (not a literal) must NOT
	// be flagged even though it is enum-typed.
	y := classifyLike(nil) // non-constant RHS — allowed
	leaderSink(ctx, y)     // non-constant var at callsite — allowed

	// Documented blind spot: string(reason) carries a non-constant arg of a
	// non-enum type (string), so the guard must NOT flag it.
	stringSink(string(r))
}
