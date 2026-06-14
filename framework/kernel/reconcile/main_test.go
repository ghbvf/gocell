package reconcile

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs goleak.VerifyTestMain once after the whole package's tests
// (covering both `package reconcile` and external `package reconcile_test`
// files, which share one test binary). This replaces per-test
// goleak.VerifyNone(IgnoreCurrent()): the Loop's fire-and-forget Trigger
// goroutine and watchDrain tail drain asynchronously on ctx cancel (mirroring
// controller-runtime's ctx-bound source goroutines), so a per-test
// IgnoreCurrent snapshot under -race could misattribute a not-yet-drained
// goroutine to the wrong test. A single package-level verify lets goleak's
// retry budget absorb the async drain. See gh #1568.
//
// Tradeoff: leak attribution is now per-package, not per-test. When a real leak
// is introduced, VerifyTestMain fails the whole binary; re-run the suspect test
// in isolation (go test -run=TestName -race -count=1) to localize the source.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
