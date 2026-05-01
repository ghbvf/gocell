// Package runtime_gosched_passes verifies that bare `runtime.Gosched()` calls
// — used in poll-with-deadline barriers driven by FakeClock — are not flagged
// by TEST-TIME-LITERAL-01 because they take no Duration argument.
// 0 violations expected.
package runtime_gosched_passes

import (
	"runtime"
	"testing"
	"time"
)

const pollDeadline = 2 * time.Second

func TestPollWithGosched(t *testing.T) {
	deadline := time.Now().Add(pollDeadline)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("never became ready")
		}
		runtime.Gosched()
	}
}

func ready() bool { return true }
