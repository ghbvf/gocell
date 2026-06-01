//go:build archtest_fixture

// Package sagaenumgreen is the GREEN over-fire guard for the
// SAGA-METRIC-LABEL-VALUES-FROZEN-01 callsite guard: a declared executor enum
// const (qualified selector) and a non-constant typed var are both allowed —
// only inline literals / conversions of literals are banned.
package sagaenumgreen

import (
	"context"

	"github.com/ghbvf/gocell/runtime/saga/executor"
)

func leaderSink(_ context.Context, _ executor.LeaderSkipReason) {}
func tickSink(_ context.Context, _ executor.TickResult)         {}

func caller(ctx context.Context, r executor.LeaderSkipReason) {
	leaderSink(ctx, executor.LeaderSkipContended) // declared const — allowed
	leaderSink(ctx, r)                            // non-constant var — allowed
	tickSink(ctx, executor.TickClaimed)           // declared const — allowed
}
