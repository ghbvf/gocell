package outbox

import (
	"context"
	"time"
)

// OutcomeCounts holds the per-disposition settled-entry counts for ONE entry kind
// (event or command) within a single poll cycle. It is the shared shape used by both
// PollCycleResult (this package) and the relay's internal pollStats (runtime/outbox)
// so the kind→outcome attribution is single-sourced and copies one-to-one with no
// hand mapping (#1674).
//
// Published / Retried / Dead are the canonical outcomes from the publish and
// writeback phases. Skipped covers `MarkPublished updated=false` (the entry was
// reclaimed mid-flight before MarkPublished could win). Lost covers the same
// condition for failure writebacks (Mark{Retry,Dead} updated=false): the lease lost
// mid-flight while the publisher was reporting an error, so the failure must NOT be
// counted as retried/dead — the new lease owner will report the canonical outcome.
type OutcomeCounts struct {
	Published, Retried, Dead, Skipped, Lost int
}

// PollCycleResult captures the outcome of a single relay poll cycle, split by entry
// KIND so command in-process dispatch and event broker publish are distinguishable
// in outbox_relayed_total{kind,outcome} (#1674). Event and Command carry the same
// per-disposition shape (OutcomeCounts); the relay attributes each settled entry to
// exactly one bucket via its command-dispatch discriminator (publishResult.isCommand).
// Used by RelayCollector.RecordPollCycle to avoid a long parameter list and to support
// future extensions without breaking the interface.
type PollCycleResult struct {
	Event   OutcomeCounts
	Command OutcomeCounts

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
	RecordPollCycle(ctx context.Context, r PollCycleResult)

	// RecordBatchSize records the number of entries claimed in a poll cycle.
	// Called even when the batch is empty (size=0) to capture idle cycles.
	RecordBatchSize(ctx context.Context, size int)

	// RecordReclaim records the number of stale entries reclaimed back to
	// pending (or dead-lettered). Called once per reclaimStale tick **only
	// when count > 0** — idle reclaim sweeps are not observed (the metric
	// is a recovery counter, not a tick frequency gauge). This contrasts
	// with RecordBatchSize which is also called on size==0 cycles so
	// dashboards can detect a totally idle relay.
	RecordReclaim(ctx context.Context, count int64)

	// RecordCleanup records the number of entries removed during periodic
	// cleanup, split by original status (published vs dead-lettered).
	RecordCleanup(ctx context.Context, publishedDeleted, deadDeleted int64)
}

// NoopRelayCollector is a no-op implementation of RelayCollector.
// Used when metrics collection is disabled (nil Metrics in RelayConfig).
type NoopRelayCollector struct{}

func (NoopRelayCollector) RecordPollCycle(_ context.Context, _ PollCycleResult) {
	/* no-op: metrics disabled */
}

func (NoopRelayCollector) RecordBatchSize(_ context.Context, _ int) {
	/* no-op: metrics disabled */
}

func (NoopRelayCollector) RecordReclaim(_ context.Context, _ int64) {
	/* no-op: metrics disabled */
}

func (NoopRelayCollector) RecordCleanup(_ context.Context, _, _ int64) {
	/* no-op: metrics disabled */
}
