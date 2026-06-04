package reconciletest_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs goleak.VerifyTestMain once after the whole package's tests.
// RunConformance drives Loops with the same fire-and-forget Trigger as the
// reconcile package, so per-test goleak.VerifyNone(IgnoreCurrent()) is equally
// flake-prone; the package-level verify absorbs async goroutine drain within
// goleak's retry budget. See gh #1568.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
