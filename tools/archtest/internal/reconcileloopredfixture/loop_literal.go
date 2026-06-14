//go:build archtest_fixture

// Package reconcileloopredfixture contains intentionally-violating forms for
// the RECONCILE-BUILDER-FUNNEL-01 archtest detector. Gated by the
// archtest_fixture build tag (must agree with the literal value of the
// unexported fixtureBuildTag const declared in tools/archtest/fixture.go; Go
// build-directive syntax cannot reference a Go constant, so this file hard-codes
// the tag literal).
//
// It exercises RED shapes: bare reconcile.Loop construction in a package outside
// the allowlist (not kernel/reconcile), which sidesteps the Builder wiring
// (metric / leader / backoff).
package reconcileloopredfixture

import "github.com/ghbvf/gocell/framework/kernel/reconcile"

//nolint:all // intentional violations for archtest RED fixture
var (
	// VIOLATION: reconcile.Loop{} — forbidden composite literal outside
	// kernel/reconcile. Production code must construct the Loop via
	// reconcile.New(...).Build(); Loop fields are private (Builder is the sole
	// public constructor).
	redLoopLiteral = &reconcile.Loop{} //nolint:unused

	// VIOLATION: new(reconcile.Loop) — forbidden builtin new() construction outside
	// kernel/reconcile. Loop fields are private; use reconcile.New(...).Build().
	redLoopViaNew = new(reconcile.Loop) //nolint:unused

	// VIOLATION: var x reconcile.Loop — forbidden value zero-var declaration outside
	// kernel/reconcile. Loop fields are private; use reconcile.New(...).Build().
	// Note: var x *reconcile.Loop (pointer holder) is NOT flagged — only the value
	// form constitutes zero-value construction.
	redLoopVar reconcile.Loop //nolint:unused
)
