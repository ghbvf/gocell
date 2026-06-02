//go:build archtest_fixture

// Package sagaenumredforeignconst is a RED fixture for F1 of the
// SAGA-METRIC-LABEL-VALUES-FROZEN-01 callsite guard: a const declared OUTSIDE
// the runtime/saga/executor package (here, in this fixture package) but typed as
// one of the frozen executor enums must be flagged.  It launders an arbitrary
// value into the frozen label set — the original isSagaDeclaredConstRef accepted
// any *types.Const, making it trivially bypassable.  Expect 1 diagnostic.
package sagaenumredforeignconst

import (
	"context"

	"github.com/ghbvf/gocell/runtime/saga/executor"
)

// rogueSkip is a const declared in the fixture package (NOT in
// runtime/saga/executor).  Because it is typed executor.LeaderSkipReason it can
// reach any sink that accepts that type, yet its value "rogue" is not in the
// frozen set.  The fixed guard must reject it.
const rogueSkip executor.LeaderSkipReason = "rogue"

func leaderSink(_ context.Context, _ executor.LeaderSkipReason) {}

func caller(ctx context.Context) {
	leaderSink(ctx, rogueSkip) // foreign-package const — MUST be flagged (F1)
}
