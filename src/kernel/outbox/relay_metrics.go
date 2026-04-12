package outbox

import "time"

// RelayCollector records outbox relay operational metrics.
// Implementations must be safe for concurrent use.
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
	RecordPollCycle(published, retried, dead, skipped int, claimDur, publishDur, writeBackDur time.Duration)

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

func (NoopRelayCollector) RecordPollCycle(_, _, _, _ int, _, _, _ time.Duration) {}
func (NoopRelayCollector) RecordBatchSize(_ int)                                {}
func (NoopRelayCollector) RecordReclaim(_ int64)                                {}
func (NoopRelayCollector) RecordCleanup(_, _ int64)                             {}
