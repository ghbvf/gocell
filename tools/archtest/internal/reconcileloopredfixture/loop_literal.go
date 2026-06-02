//go:build archtest_fixture

// Package reconcileloopredfixture contains an intentionally-violating form for
// the RECONCILE-BUILDER-FUNNEL-01 archtest detector. Gated by the
// archtest_fixture build tag (must agree with the literal value of the
// unexported fixtureBuildTag const declared in tools/archtest/fixture.go; Go
// build-directive syntax cannot reference a Go constant, so this file hard-codes
// the tag literal).
//
// It exercises the RED shape: a bare reconcile.Loop{} composite literal in a
// package outside the allowlist (not kernel/reconcile), which sidesteps the
// Builder wiring (metric / leader / backoff).
package reconcileloopredfixture

import "github.com/ghbvf/gocell/kernel/reconcile"

// VIOLATION: reconcile.Loop{} — forbidden composite literal outside
// kernel/reconcile. Production code must construct the Loop via
// reconcile.New(...).Build(); Loop fields are private (Builder is the sole
// public constructor).
//
//nolint:all // intentional violation for archtest RED fixture
var redLoopLiteral = &reconcile.Loop{} //nolint:unused
