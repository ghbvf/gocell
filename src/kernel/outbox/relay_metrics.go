package outbox

import "time"

// PollCycleResult captures the outcome of a single relay poll cycle.
// Used by RelayCollector.RecordPollCycle to avoid a long parameter list
// and to support future extensions without breaking the interface.
type PollCycleResult struct {
	Published, Retried, Dead, Skipped int
	ClaimDur, PublishDur, WriteBackDur time.Duration
}

// RelayCollector records outbox relay operational metrics.
// Implementations must be safe for concurrent use.
// Zero counts are valid inputs; implementations should handle them gracefully
// (e.g. skip counter increments for zero values).
//
// The interface is intentionally in kernel/outbox (not runtime/) so that
// adapters/postgres can depend on it without pulling in runtime/ packages.
//
// ref: Temporal client.Options{MetricsHandler} — inject-at-construction pattern
// ref: Watermill components/metrics — publish_time_seconds, subscriber_messages_received_total
// ref: Debezium JMX — MilliSecondsBehindSource, max.batch.size, DLQ count
type RelayCollector interface {
	// RecordPollCycle records a completed poll cycle with outcome counts and
	// per-phase durations. Called once per pollOnce invocation after writeBack.
	RecordPollCycle(result PollCycleResult)

	// RecordBatchSize records the number of entries claimed in a poll cycle.
	// Called even when the batch is empty (size=0) to capture idle cycles.
	RecordBatchSize(size int)

	// RecordReclaim records the number of stale entries reclaimed back to
	// pending (or dead-lettered). Called once per reclaimStale invocation.
	RecordReclaim(count int64)

	// RecordCleanup records the number of entries removed during periodic
	// cleanup, split by original status (published vs dead-lettered).
	RecordCleanup(publishedDeleted, deadDeleted int64)
}

// NoopRelayCollector is a no-op implementation of RelayCollector.
// Used when metrics collection is disabled (nil Metrics in RelayConfig).
type NoopRelayCollector struct{}

func (NoopRelayCollector) RecordPollCycle(_ PollCycleResult) { /* no-op: metrics disabled */ }
func (NoopRelayCollector) RecordBatchSize(_ int)             { /* no-op: metrics disabled */ }
func (NoopRelayCollector) RecordReclaim(_ int64)             { /* no-op: metrics disabled */ }
func (NoopRelayCollector) RecordCleanup(_, _ int64)          { /* no-op: metrics disabled */ }
