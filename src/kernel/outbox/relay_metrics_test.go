package outbox

import (
	"testing"
	"time"
)

// Compile-time interface check.
var _ RelayCollector = NoopRelayCollector{}

func TestNoopRelayCollector_DoesNotPanic(t *testing.T) {
	var c NoopRelayCollector
	// All methods must be safe to call with any arguments.
	c.RecordPollCycle(PollCycleResult{
		Published: 1, Retried: 2, Dead: 3, Skipped: 4,
		ClaimDur: time.Millisecond, PublishDur: time.Second, WriteBackDur: time.Microsecond,
	})
	c.RecordBatchSize(0)
	c.RecordBatchSize(100)
	c.RecordReclaim(0)
	c.RecordReclaim(42)
	c.RecordCleanup(0, 0)
	c.RecordCleanup(100, 50)
}

func TestNoopRelayCollector_ZeroValue_Safe(t *testing.T) {
	// Zero value must be usable without initialization.
	var c NoopRelayCollector
	c.RecordPollCycle(PollCycleResult{})
	c.RecordBatchSize(0)
	c.RecordReclaim(0)
	c.RecordCleanup(0, 0)
}
