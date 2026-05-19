package metrics

import (
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// OutboxConsumerCollector registers outbox consumer-side metrics and provides
// the ConsumerObserver interface required by ConsumerBase as well as a
// ObservePendingDepth method used by runtime/outbox.Relay.
//
// OutboxConsumerCollector implements BOTH outbox.ConsumerObserver (consumed by
// ConsumerBase.AttachObserver for outbox_consumer_rejected_total) AND exposes
// ObservePendingDepth(int64) (consumed by Relay.WithPendingDepthObserver for
// outbox_pending_depth). Bootstrap.autoWireOutboxConsumerCollector wires both
// paths from the same instance.
//
// Metrics registered:
//   - outbox_consumer_rejected_total{cell,topic,reason}: total terminal Reject
//     dispositions observed by ConsumerBase. The consumerGroup argument from
//     ObserveReject is intentionally not included as a label to keep time-series
//     cardinality bounded; one cell may have multiple consumer groups sharing
//     the same counter family.
//   - outbox_pending_depth{cell}: current pending outbox entry depth, set on
//     each Relay reclaim tick. Scoped to the cellID supplied at construction.
//
// Label set is {cell, topic, reason}; reason is a closed set of 2 values
// (handler_reject + retry_exhausted); cardinality bound = num_cells ×
// num_topics × 2. For deployments with >100 unique topics monitor /metrics
// time-series count.
//
// ref: Watermill router metrics middleware — per-handler counters + gauges
// mirroring the router's router_messages_processed_total pattern.
type OutboxConsumerCollector struct {
	cellID   string
	rejected kernelmetrics.CounterVec // outbox_consumer_rejected_total{cell,topic,reason}
	pending  kernelmetrics.GaugeVec   // outbox_pending_depth{cell}
}

// compile-time interface check.
var _ outbox.ConsumerObserver = (*OutboxConsumerCollector)(nil)

// NewOutboxConsumerCollector registers outbox_consumer_rejected_total and
// outbox_pending_depth on the given provider. Returns an error if any
// registration fails (rolls back the first successful registration LIFO on
// second failure).
//
// cellID is required (non-empty). Metric observations for this collector
// instance are scoped to a single cell; the cell label is automatically applied
// when calling ObservePendingDepth.
func NewOutboxConsumerCollector(p kernelmetrics.Provider, cellID string) (*OutboxConsumerCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: outbox Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: outbox cellID is required")
	}

	rejected, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name: "outbox_consumer_rejected_total",
		Help: "Total number of terminal Reject dispositions from outbox ConsumerBase. " +
			"consumerGroup is not included as a label to bound time-series cardinality.",
		LabelNames: []string{"cell", "topic", "reason"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register outbox_consumer_rejected_total: %w", err)
	}

	pending, err := p.GaugeVec(kernelmetrics.GaugeOpts{
		Name:       "outbox_pending_depth",
		Help:       "Current number of pending outbox entries for the owner cell, set on each Relay reclaim tick.",
		LabelNames: []string{"cell"},
	})
	if err != nil {
		// rollback first registration LIFO
		_ = p.Unregister(rejected)
		return nil, fmt.Errorf("runtime/observability/metrics: register outbox_pending_depth: %w", err)
	}

	return &OutboxConsumerCollector{
		cellID:   cellID,
		rejected: rejected,
		pending:  pending,
	}, nil
}

// ObserveReject implements kernel/outbox.ConsumerObserver.
// consumerGroup is received but not used as a label to keep cardinality
// bounded; the label set is {cell, topic, reason}.
//
// Caller contract: topic MUST be a static contract identifier (ContractSpec.Topic).
// Passing runtime-derived values (message IDs, tenant IDs) would cause unbounded
// time-series cardinality. consumerGroup is deliberately excluded from the label
// set for the same reason.
func (c *OutboxConsumerCollector) ObserveReject(cellID, topic, _ /* consumerGroup */, reason string) {
	if c == nil {
		return // nil-receiver safe: observability must never panic on startup
	}
	c.rejected.With(kernelmetrics.Labels{
		"cell":   cellID,
		"topic":  topic,
		"reason": reason,
	}).Inc()
}

// ObservePendingDepth records the current pending outbox depth for the owner
// cell. Called by Relay on each reclaim tick.
//
// Sampling cadence is bound to Relay.ReclaimInterval (default minutes), not
// Prometheus scrape interval. SREs should treat this Gauge as "depth at last
// reclaim tick", not real-time. For tighter sampling, decrease ReclaimInterval.
func (c *OutboxConsumerCollector) ObservePendingDepth(n int64) {
	if c == nil {
		return
	}
	c.pending.With(kernelmetrics.Labels{"cell": c.cellID}).Set(float64(n))
}
