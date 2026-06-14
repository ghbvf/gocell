package tailer

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a package-wide goleak guard, bringing the tailer package to
// parity with its siblings runtime/saga (integration_test.go) and
// runtime/saga/executor (main_test.go), which both already verify no goroutines
// leak across the test binary.
//
// This matters most for safeObserve (tailer.go): when an observer call exceeds
// observerCallDeadline, safeObserve abandons the spawned observer goroutine and
// returns — by design (Go cannot kill a goroutine). Tests such as
// TestTailer_BlockingObserverBounded deliberately exercise that abandonment;
// goleak.VerifyTestMain asserts every such goroutine is released before the
// binary exits, so a regression that leaves an observer pinned indefinitely
// surfaces as a failing test rather than silently rotting.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
