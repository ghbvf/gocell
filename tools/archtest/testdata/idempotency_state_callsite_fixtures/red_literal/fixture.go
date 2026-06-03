//go:build archtest_fixture

// Package idemstatered is a RED fixture for the
// IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 callsite guard: an inline
// string literal and a RequestState("x") conversion both reach a RequestState
// parameter and must each be flagged — the residual hole the sealed
// `type RequestState string` cannot close (an untyped string constant is
// assignable to a defined string type). Expect 2 diagnostics:
// 1 inline literal + 1 RequestState("x") conversion.
package idemstatered

import (
	"context"

	idem "github.com/ghbvf/gocell/runtime/http/idempotency"
)

func stateSink(_ context.Context, _ idem.RequestState) {}

func caller(ctx context.Context) {
	stateSink(ctx, "typo")                     // inline literal — MUST be flagged
	stateSink(ctx, idem.RequestState("rogue")) // conversion of literal — MUST be flagged
}
