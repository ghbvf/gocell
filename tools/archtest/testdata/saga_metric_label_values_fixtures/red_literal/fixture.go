//go:build archtest_fixture

// Package sagaenumred is a RED fixture for the SAGA-METRIC-LABEL-VALUES-FROZEN-01
// callsite guard: inline string literals and a LeaderSkipReason("x") conversion
// both reach an enum-typed parameter and must each be flagged — the residual hole
// the sealed `type X string` enum cannot close (an untyped string constant is
// assignable to a defined string type). Expect 5 diagnostics:
// 4 inline literals (one per enum) + 1 T("x") conversion.
package sagaenumred

import (
	"context"

	"github.com/ghbvf/gocell/framework/runtime/saga/executor"
)

// One sink per frozen executor label enum, so the A2 guard's type-binding is
// exercised across all four (not just LeaderSkipReason).
func leaderSink(_ context.Context, _ executor.LeaderSkipReason)   {}
func tickSink(_ context.Context, _ executor.TickResult)           {}
func driveSink(_ context.Context, _ executor.DriveResult)         {}
func hbSink(_ context.Context, _ executor.HeartbeatFailureReason) {}

func caller(ctx context.Context) {
	leaderSink(ctx, "typo")                               // inline literal — MUST be flagged
	leaderSink(ctx, executor.LeaderSkipReason("backend")) // conversion of literal — MUST be flagged
	tickSink(ctx, "nope")                                 // TickResult literal — MUST be flagged
	driveSink(ctx, "bad")                                 // DriveResult literal — MUST be flagged
	hbSink(ctx, "wat")                                    // HeartbeatFailureReason literal — MUST be flagged
}
