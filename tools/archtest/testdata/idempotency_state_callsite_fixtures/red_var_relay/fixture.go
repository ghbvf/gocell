//go:build archtest_fixture

// Package idemstateredvarrelay is a RED fixture for the
// IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 assignment guard: a variable
// whose initializer is an inline string literal of RequestState type bypasses
// the callsite guard because at the callsite tv.Value == nil (var, not const).
// The assignment guard must flag the assignment site instead. Expect 1
// diagnostic (the var initializer; the callsite passes a non-constant var and
// must NOT double-flag).
package idemstateredvarrelay

import (
	"context"

	idem "github.com/ghbvf/gocell/runtime/http/idempotency"
)

func stateSink(_ context.Context, _ idem.RequestState) {}

func caller(ctx context.Context) {
	var x idem.RequestState = "typo" // assignment of literal to RequestState var — MUST be flagged
	stateSink(ctx, x)                // callsite has tv.Value==nil (var), must NOT double-flag
}
