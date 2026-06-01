//go:build archtest_fixture

// Package sagaenumred is a RED fixture for the SAGA-METRIC-LABEL-VALUES-FROZEN-01
// callsite guard: an inline string literal and a LeaderSkipReason("x") conversion
// both reach an enum-typed parameter and must each be flagged — the residual hole
// the sealed `type X string` enum cannot close (an untyped string constant is
// assignable to a defined string type). Expect 2 diagnostics.
package sagaenumred

import (
	"context"

	"github.com/ghbvf/gocell/runtime/saga/executor"
)

func leaderSink(_ context.Context, _ executor.LeaderSkipReason) {}

func caller(ctx context.Context) {
	leaderSink(ctx, "typo")                               // inline literal — MUST be flagged
	leaderSink(ctx, executor.LeaderSkipReason("backend")) // conversion of literal — MUST be flagged
}
