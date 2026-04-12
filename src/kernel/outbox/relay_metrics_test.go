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
	c.RecordPollCycle(1, 2, 3, 4, time.Millisecond, time.Second, time.Microsecond)
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
	c.RecordPollCycle(0, 0, 0, 0, 0, 0, 0)
	c.RecordBatchSize(0)
	c.RecordReclaim(0)
	c.RecordCleanup(0, 0)
}
