//go:build archtest_fixture

// Package reconcilegoleakredfixture holds intentionally-violating and
// sanctioned forms for the RECONCILE-GOLEAK-TESTMAIN-FUNNEL-01 archtest
// detector (A1: per-test goleak.VerifyNone ban). Gated by the archtest_fixture
// build tag (must agree with the literal value of the unexported
// fixtureBuildTag const in tools/archtest/fixture.go; Go build-directive syntax
// cannot reference a Go constant, so this file hard-codes the tag literal).
//
// It is the non-vacuity proof for gh #1568: the A1 detector
// (goleakVerifyNoneViolations) must flag redPerTestVerifyNone's
// goleak.VerifyNone(IgnoreCurrent()) and must NOT flag greenVerifyTestMain's
// package-level goleak.VerifyTestMain.
package reconcilegoleakredfixture

import (
	"testing"

	"go.uber.org/goleak"
)

// redPerTestVerifyNone is RED bait: the per-test goleak.VerifyNone(IgnoreCurrent())
// idiom. The flake (gh #1568) lives in exactly this shape — A1 must flag the
// VerifyNone call. The nested IgnoreCurrent is inert on its own and is NOT the
// match target.
//
//nolint:all // intentional violation for archtest RED fixture
func redPerTestVerifyNone(t *testing.T) { //nolint:unused
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
}

// greenVerifyTestMain is the SANCTIONED form: package-level VerifyTestMain. A1
// must NOT flag it — VerifyTestMain is the funnel that per-test VerifyNone is
// banned in favor of.
//
//nolint:all // green control for archtest fixture
func greenVerifyTestMain(m *testing.M) { //nolint:unused
	goleak.VerifyTestMain(m)
}
