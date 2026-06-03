//go:build archtest_fixture

// Package idemstateredvarrelay is a RED fixture for the
// IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 assignment guard: a variable
// whose initializer is an inline RequestState constant bypasses the callsite
// guard because at the callsite tv.Value == nil (var, not const). The assignment
// guard must flag the assignment site instead — across ALL three forms whose LHS
// is typed RequestState:
//   - explicit-type var declaration  (`var x RequestState = "typo"`)
//   - inferred-type var declaration   (`var y = RequestState("rogue")`)
//   - short variable declaration       (`z := RequestState("bad")`)
//
// The inferred + `:=` forms are the regression coverage for the LHS-type
// resolution fix: a defining ident's type lives in info.Defs, not info.Types, so
// resolving via info.ObjectOf is what makes them detectable. Expect 3
// diagnostics (the three initializers; the three callsites pass non-constant
// vars and must NOT double-flag).
package idemstateredvarrelay

import (
	"context"

	idem "github.com/ghbvf/gocell/runtime/http/idempotency"
)

func stateSink(_ context.Context, _ idem.RequestState) {}

func caller(ctx context.Context) {
	var x idem.RequestState = "typo" // explicit-type literal — MUST be flagged
	stateSink(ctx, x)
	var y = idem.RequestState("rogue") // inferred-type conversion — MUST be flagged
	stateSink(ctx, y)
	z := idem.RequestState("bad") // short var decl conversion — MUST be flagged
	stateSink(ctx, z)
}
