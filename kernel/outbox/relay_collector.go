package outbox

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// DefaultRelayPollBuckets are histogram buckets for poll phase duration (5ms–10s).
// Matches the range the original adapters/prometheus impl used before
// migration; preserved here so Grafana dashboards continue to look natural.
var DefaultRelayPollBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// DefaultRelayBatchBuckets are histogram buckets for batch size (1–500).
var DefaultRelayBatchBuckets = []float64{1, 5, 10, 25, 50, 100, 200, 500}

// providerRelayCollector implements RelayCollector via a provider-neutral
// metrics.Provider. Callers supply the Provider at wire time (prom in prod,
// OTel in future deployments, Nop in tests); the collector itself has no
// backend knowledge.
//
// Metrics (subsystem=outbox):
//
//	outbox_relayed_total         (counter, labels: cell, outcome)
//	outbox_poll_duration_seconds (histogram, labels: cell, phase)
//	outbox_batch_size            (histogram, labels: cell)
//	outbox_reclaimed_total       (counter, labels: cell)
//	outbox_cleaned_total         (counter, labels: cell, status)
//
// ref: Temporal MetricsHandler — inject-at-construction pattern
// ref: Watermill components/metrics — publish_time_seconds semantics
type providerRelayCollector struct {
	cellID       string
	relayed      metrics.CounterVec
	pollDuration metrics.HistogramVec
	batchSize    metrics.HistogramVec
	reclaimed    metrics.CounterVec
	cleaned      metrics.CounterVec
}

var _ RelayCollector = (*providerRelayCollector)(nil)

// ProviderRelayCollectorConfig customises metric naming / bucketing.
// Zero value is acceptable and produces defaults.
type ProviderRelayCollectorConfig struct {
	// PollBuckets overrides DefaultRelayPollBuckets; zero value uses defaults.
	PollBuckets []float64
	// BatchBuckets overrides DefaultRelayBatchBuckets; zero value uses defaults.
	BatchBuckets []float64
}

// NewProviderRelayCollector registers outbox relay metrics on p and returns
// a RelayCollector that records through them. Returns error when cellID is
// empty or when the Provider reports registration failure (typically
// duplicate metric names).
func NewProviderRelayCollector(p metrics.Provider, cellID string, opts ...ProviderRelayCollectorConfig) (RelayCollector, error) {
	if cellID == "" {
		return nil, errcode.New(errcode.ErrObservabilityConfigInvalid, "outbox: cellID is required for provider relay collector")
	}
	if p == nil {
		return nil, errcode.New(errcode.ErrObservabilityConfigInvalid, "outbox: metrics.Provider is required")
	}
	cfg := ProviderRelayCollectorConfig{}
	if len(opts) > 0 {
		cfg = opts[0]
	}
	if len(cfg.PollBuckets) == 0 {
		cfg.PollBuckets = DefaultRelayPollBuckets
	}
	if len(cfg.BatchBuckets) == 0 {
		cfg.BatchBuckets = DefaultRelayBatchBuckets
	}

	relayed, err := p.CounterVec(metrics.CounterOpts{
		Name:       "outbox_relayed_total",
		Help:       "Total number of outbox entries processed by the relay, by outcome.",
		LabelNames: []string{"cell", "outcome"},
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: register outbox_relayed_total: %w", err)
	}
	pollDuration, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "outbox_poll_duration_seconds",
		Help:       "Duration of each relay poll phase in seconds.",
		LabelNames: []string{"cell", "phase"},
		Buckets:    cfg.PollBuckets,
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: register outbox_poll_duration_seconds: %w", err)
	}
	batchSize, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "outbox_batch_size",
		Help:       "Number of entries claimed per relay poll cycle.",
		LabelNames: []string{"cell"},
		Buckets:    cfg.BatchBuckets,
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: register outbox_batch_size: %w", err)
	}
	reclaimed, err := p.CounterVec(metrics.CounterOpts{
		Name:       "outbox_reclaimed_total",
		Help:       "Total number of stale entries reclaimed by the relay.",
		LabelNames: []string{"cell"},
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: register outbox_reclaimed_total: %w", err)
	}
	cleaned, err := p.CounterVec(metrics.CounterOpts{
		Name:       "outbox_cleaned_total",
		Help:       "Total number of entries cleaned up (deleted) by the relay.",
		LabelNames: []string{"cell", "status"},
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: register outbox_cleaned_total: %w", err)
	}

	return &providerRelayCollector{
		cellID:       cellID,
		relayed:      relayed,
		pollDuration: pollDuration,
		batchSize:    batchSize,
		reclaimed:    reclaimed,
		cleaned:      cleaned,
	}, nil
}

// RecordPollCycle emits one relayed_total increment per non-zero outcome
// and four poll_duration observations (claim, publish, write_back, total).
// Zero-count outcomes are skipped to keep time-series cardinality clean:
// a persistent zero counter fragment would otherwise appear in Grafana
// topology for dead-lettered cells that never actually dead-letter anything.
func (c *providerRelayCollector) RecordPollCycle(r PollCycleResult) {
	if r.Published > 0 {
		c.relayed.With(metrics.Labels{"cell": c.cellID, "outcome": "published"}).Add(float64(r.Published))
	}
	if r.Retried > 0 {
		c.relayed.With(metrics.Labels{"cell": c.cellID, "outcome": "retried"}).Add(float64(r.Retried))
	}
	if r.Dead > 0 {
		c.relayed.With(metrics.Labels{"cell": c.cellID, "outcome": "dead"}).Add(float64(r.Dead))
	}
	if r.Skipped > 0 {
		c.relayed.With(metrics.Labels{"cell": c.cellID, "outcome": "skipped"}).Add(float64(r.Skipped))
	}

	c.pollDuration.With(metrics.Labels{"cell": c.cellID, "phase": "claim"}).Observe(r.ClaimDur.Seconds())
	c.pollDuration.With(metrics.Labels{"cell": c.cellID, "phase": "publish"}).Observe(r.PublishDur.Seconds())
	c.pollDuration.With(metrics.Labels{"cell": c.cellID, "phase": "write_back"}).Observe(r.WriteBackDur.Seconds())
	c.pollDuration.With(metrics.Labels{"cell": c.cellID, "phase": "total"}).Observe((r.ClaimDur + r.PublishDur + r.WriteBackDur).Seconds())
}

// RecordBatchSize observes the claim count of each poll, including zero to
// capture idle cycles (useful for relay liveness panels).
func (c *providerRelayCollector) RecordBatchSize(size int) {
	c.batchSize.With(metrics.Labels{"cell": c.cellID}).Observe(float64(size))
}

// RecordReclaim emits only when count > 0; dropping zero avoids a noisy
// counter increment every cleanup interval on a healthy relay.
func (c *providerRelayCollector) RecordReclaim(count int64) {
	if count > 0 {
		c.reclaimed.With(metrics.Labels{"cell": c.cellID}).Add(float64(count))
	}
}

// RecordCleanup splits increments by status so dashboards can track
// published-vs-dead cleanup separately.
func (c *providerRelayCollector) RecordCleanup(publishedDeleted, deadDeleted int64) {
	if publishedDeleted > 0 {
		c.cleaned.With(metrics.Labels{"cell": c.cellID, "status": "published"}).Add(float64(publishedDeleted))
	}
	if deadDeleted > 0 {
		c.cleaned.With(metrics.Labels{"cell": c.cellID, "status": "dead"}).Add(float64(deadDeleted))
	}
}
