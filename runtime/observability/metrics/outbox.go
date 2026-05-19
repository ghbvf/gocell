package metrics

import (
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
)

// OutboxRejectCollector registers outbox_consumer_rejected_total{cell,topic,reason}.
//
// This collector is multi-cell-shared: the cell label flows from the
// ObserveReject call-site argument, NOT from construction time. There is
// therefore no cellID constructor argument. One instance covers all cells.
//
// Metric registered:
//   - outbox_consumer_rejected_total{cell,topic,reason}: total terminal Reject
//     dispositions observed by ConsumerBase. consumerGroup is excluded from the
//     label set to keep time-series cardinality bounded; one cell may have
//     multiple consumer groups sharing the same counter family.
//
// ref: Watermill router metrics middleware — per-handler counters mirroring
// the router's router_messages_processed_total pattern.
type OutboxRejectCollector struct {
	rejected kernelmetrics.CounterVec // outbox_consumer_rejected_total{cell,topic,reason}
}

// compile-time interface check.
var _ outbox.ConsumerObserver = (*OutboxRejectCollector)(nil)

// NewOutboxRejectCollector registers outbox_consumer_rejected_total on the
// given provider. Returns an error if registration fails.
//
// No cellID argument: cell label flows from ObserveReject's call-site argument.
func NewOutboxRejectCollector(p kernelmetrics.Provider) (*OutboxRejectCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: OutboxRejectCollector Provider is required")
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

	return &OutboxRejectCollector{rejected: rejected}, nil
}

// ObserveReject implements kernel/outbox.ConsumerObserver.
// consumerGroup is received but not used as a label to keep cardinality bounded;
// the label set is {cell, topic, reason}.
//
// Caller contract: topic MUST be a static contract identifier (ContractSpec.Topic).
// Passing runtime-derived values (message IDs, tenant IDs) causes unbounded cardinality.
func (c *OutboxRejectCollector) ObserveReject(cellID, topic, _ /* consumerGroup */, reason string) {
	if c == nil {
		return // nil-receiver safe: observability must never panic on startup
	}
	c.rejected.With(kernelmetrics.Labels{
		"cell":   cellID,
		"topic":  topic,
		"reason": reason,
	}).Inc()
}

// ---------------------------------------------------------------------------

// OutboxPendingDepthCollector registers outbox_pending_depth{cell} scoped to
// the owner cell supplied at construction. One collector per relay.
//
// Metric registered:
//   - outbox_pending_depth{cell}: current ELIGIBLE pending outbox entry depth
//     — rows with status=pending AND (next_retry_at IS NULL OR next_retry_at
//     <= now()). Rows still in retry backoff are EXCLUDED, matching the
//     ClaimPending eligibility predicate (Store.CountPending). Set on each
//     Relay reclaim tick.
//
// Sampling cadence is bound to Relay.ReclaimInterval (default minutes), not
// Prometheus scrape interval. SREs should treat this Gauge as "eligible
// depth at last reclaim tick", not real-time. A growing retry backlog will
// not inflate this value — sustained retry backlog must be diagnosed via
// outbox_consumer_rejected_total or the reclaim-budget readyz probe.
//
// PR #593 review fix-up (P2#5) aligned the help text and dashboards with
// the eligibility semantics introduced when Store.CountPending narrowed
// to claimable rows.
//
// ref: Watermill router metrics middleware — gauge mirroring router queue depth.
type OutboxPendingDepthCollector struct {
	cellID  string
	pending kernelmetrics.GaugeVec // outbox_pending_depth{cell}
}

// compile-time interface check.
var _ runtimeoutbox.PendingDepthObserver = (*OutboxPendingDepthCollector)(nil)

// NewOutboxPendingDepthCollector registers outbox_pending_depth on the given
// provider. Returns an error if registration fails.
//
// cellID empty string returns error (no fallback to _runtime sentinel): this
// collector is per-cell and must be constructed with a real cell ID.
func NewOutboxPendingDepthCollector(p kernelmetrics.Provider, cellID string) (*OutboxPendingDepthCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: OutboxPendingDepthCollector Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: OutboxPendingDepthCollector cellID is required")
	}

	pending, err := p.GaugeVec(kernelmetrics.GaugeOpts{
		Name: "outbox_pending_depth",
		Help: "Current eligible pending outbox entries for the owner cell " +
			"(status=pending AND next_retry_at IS NULL OR <= now(); excludes rows in retry backoff). " +
			"Set on each Relay reclaim tick.",
		LabelNames: []string{"cell"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register outbox_pending_depth: %w", err)
	}

	return &OutboxPendingDepthCollector{cellID: cellID, pending: pending}, nil
}

// ObservePendingDepth implements runtimeoutbox.PendingDepthObserver.
// Records the current pending outbox depth for the owner cell.
func (c *OutboxPendingDepthCollector) ObservePendingDepth(n int64) {
	if c == nil {
		return
	}
	c.pending.With(kernelmetrics.Labels{"cell": c.cellID}).Set(float64(n))
}
