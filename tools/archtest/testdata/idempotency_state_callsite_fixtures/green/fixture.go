//go:build archtest_fixture

// Package idemstategreen is the GREEN over-fire guard for the
// IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 callsite guard. The following
// are all allowed and must NOT produce diagnostics:
//   - a declared idempotency.State* const referenced via qualified selector (it
//     lives in runtime/http/idempotency, so the foreign-package check passes)
//   - a non-constant typed RequestState var (param relay / classify-like return)
//   - a var passed to a sink (callsite tv.Value==nil)
//   - string(state) conversion (non-enum type at the callsite — documented blind spot)
package idemstategreen

import (
	"context"

	idem "github.com/ghbvf/gocell/framework/runtime/http/idempotency"
)

func stateSink(_ context.Context, _ idem.RequestState) {}
func stringSink(_ string)                              {}

// classifyLike simulates a non-constant producer whose return value is
// RequestState but carries no compile-time constant.
func classifyLike(busy bool) idem.RequestState {
	if busy {
		return idem.StateBusy
	}
	return idem.StateAcquired
}

func caller(ctx context.Context, r idem.RequestState) {
	stateSink(ctx, idem.StateReplayed) // declared idempotency const — allowed
	stateSink(ctx, r)                  // non-constant parameter var — allowed

	// var initialised from a function call (not a literal) must NOT be flagged
	// even though it is RequestState-typed.
	y := classifyLike(true) // non-constant RHS — allowed
	stateSink(ctx, y)       // non-constant var at callsite — allowed

	// var initialised from a declared const — allowed (explicit + inferred + :=).
	var z idem.RequestState = idem.StateOversize
	stateSink(ctx, z)
	var z2 = idem.StateStoreError // inferred-type declared const — allowed
	stateSink(ctx, z2)
	w := idem.StateBusy // short var decl declared const — allowed
	stateSink(ctx, w)
	z = idem.StateAcquired // reassignment to declared const — allowed
	stateSink(ctx, z)

	// Documented blind spot: string(state) carries a non-constant arg of a
	// non-enum type (string), so the guard must NOT flag it.
	stringSink(string(r))
}
