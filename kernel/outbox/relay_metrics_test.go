package outbox

import (
	"context"
	"testing"
	"time"
)

// Compile-time interface check.
var _ RelayCollector = NoopRelayCollector{}

func TestNoopRelayCollector_DoesNotPanic(t *testing.T) {
	var c NoopRelayCollector
	ctx := context.Background()
	// All methods must be safe to call with any arguments.
	c.RecordPollCycle(ctx, PollCycleResult{
		Event:    OutcomeCounts{Published: 1, Retried: 2, Dead: 3, Skipped: 4},
		Command:  OutcomeCounts{Published: 5, Lost: 1},
		ClaimDur: time.Millisecond, PublishDur: time.Second, WriteBackDur: time.Microsecond,
	})
	c.RecordBatchSize(ctx, 0)
	c.RecordBatchSize(ctx, 100)
	c.RecordReclaim(ctx, 0)
	c.RecordReclaim(ctx, 42)
	c.RecordCleanup(ctx, 0, 0)
	c.RecordCleanup(ctx, 100, 50)
}

func TestNoopRelayCollector_ZeroValue_Safe(t *testing.T) {
	// Zero value must be usable without initialization.
	var c NoopRelayCollector
	ctx := context.Background()
	c.RecordPollCycle(ctx, PollCycleResult{})
	c.RecordBatchSize(ctx, 0)
	c.RecordReclaim(ctx, 0)
	c.RecordCleanup(ctx, 0, 0)
}
