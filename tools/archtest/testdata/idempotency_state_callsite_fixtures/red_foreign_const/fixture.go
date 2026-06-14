//go:build archtest_fixture

// Package idemstateredforeignconst is a RED fixture for the
// IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 callsite guard: a const
// declared OUTSIDE runtime/http/idempotency (here, in this fixture package) but
// typed as RequestState must be flagged. It launders an arbitrary value into the
// frozen label set — accepting any *types.Const would be trivially bypassable.
// Expect 1 diagnostic.
package idemstateredforeignconst

import (
	"context"

	idem "github.com/ghbvf/gocell/framework/runtime/http/idempotency"
)

// rogueState is a const declared in the fixture package (NOT in
// runtime/http/idempotency). Because it is typed idem.RequestState it can reach
// any sink that accepts that type, yet its value "rogue" is not in the frozen
// set. The guard must reject it.
const rogueState idem.RequestState = "rogue"

func stateSink(_ context.Context, _ idem.RequestState) {}

func caller(ctx context.Context) {
	stateSink(ctx, rogueState) // foreign-package const — MUST be flagged
}
