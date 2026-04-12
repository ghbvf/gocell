package prometheus

import (
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

// Compile-time check: RelayCollector implements outbox.RelayCollector.
var _ outbox.RelayCollector = (*RelayCollector)(nil)

// DefaultPollBuckets are histogram buckets for poll phase duration (5ms–10s).
var DefaultPollBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// DefaultBatchBuckets are histogram buckets for batch size (1–500).
var DefaultBatchBuckets = []float64{1, 5, 10, 25, 50, 100, 200, 500}

// RelayCollectorConfig configures the Prometheus relay metrics collector.
type RelayCollectorConfig struct {
	// CellID identifies the cell that owns these metrics. Required.
	CellID string

	// Namespace is the Prometheus metric namespace prefix. Default: "gocell".
	Namespace string

	// Registry is the Prometheus registry to use. If nil, a new registry is
	// created. Using an isolated registry avoids collisions with the global default.
	Registry *prom.Registry

	// PollBuckets configures histogram bucket boundaries for poll durations.
	// Default: DefaultPollBuckets.
	PollBuckets []float64

	// BatchBuckets configures histogram bucket boundaries for batch sizes.
	// Default: DefaultBatchBuckets.
	BatchBuckets []float64
}

// defaults fills zero-valued fields with sensible defaults.
func (c *RelayCollectorConfig) defaults() {
	if c.Namespace == "" {
		c.Namespace = "gocell"
	}
	if c.Registry == nil {
		c.Registry = prom.NewRegistry()
	}
	if len(c.PollBuckets) == 0 {
		c.PollBuckets = DefaultPollBuckets
	}
	if len(c.BatchBuckets) == 0 {
		c.BatchBuckets = DefaultBatchBuckets
	}
}

// RelayCollector implements outbox.RelayCollector using Prometheus metrics.
//
// All metrics use dynamic labels (no ConstLabels) for consistency and to
// support multiple cells sharing a single registry without conflicts.
//
// Registered metrics:
//   - gocell_outbox_relayed_total        (counter, labels: cell, outcome)
//   - gocell_outbox_poll_duration_seconds (histogram, labels: cell, phase)
//   - gocell_outbox_batch_size           (histogram, labels: cell)
//   - gocell_outbox_reclaimed_total      (counter, labels: cell)
//   - gocell_outbox_cleaned_total        (counter, labels: cell, status)
type RelayCollector struct {
	cellID   string
	registry *prom.Registry

	relayed      *prom.CounterVec
	pollDuration *prom.HistogramVec
	batchSize    *prom.HistogramVec
	reclaimed    *prom.CounterVec
	cleaned      *prom.CounterVec
}

// NewRelayCollector creates a Prometheus-backed relay metrics collector.
// ref: Watermill PrometheusMetricsBuilder — register with AlreadyRegistered tolerance
// ref: Temporal MetricsHandler — inject at construction
func NewRelayCollector(cfg RelayCollectorConfig) (*RelayCollector, error) {
	cfg.defaults()

	if cfg.CellID == "" {
		return nil, errcode.New(ErrAdapterPromConfig, "prometheus: relay CellID is required")
	}

	relayed := prom.NewCounterVec(prom.CounterOpts{
		Namespace: cfg.Namespace,
		Subsystem: "outbox",
		Name:      "relayed_total",
		Help:      "Total number of outbox entries processed by the relay, by outcome.",
	}, []string{"cell", "outcome"})

	pollDuration := prom.NewHistogramVec(prom.HistogramOpts{
		Namespace: cfg.Namespace,
		Subsystem: "outbox",
		Name:      "poll_duration_seconds",
		Help:      "Duration of each relay poll phase in seconds.",
		Buckets:   cfg.PollBuckets,
	}, []string{"cell", "phase"})

	batchSize := prom.NewHistogramVec(prom.HistogramOpts{
		Namespace: cfg.Namespace,
		Subsystem: "outbox",
		Name:      "batch_size",
		Help:      "Number of entries claimed per relay poll cycle.",
		Buckets:   cfg.BatchBuckets,
	}, []string{"cell"})

	reclaimed := prom.NewCounterVec(prom.CounterOpts{
		Namespace: cfg.Namespace,
		Subsystem: "outbox",
		Name:      "reclaimed_total",
		Help:      "Total number of stale entries reclaimed by the relay.",
	}, []string{"cell"})

	cleaned := prom.NewCounterVec(prom.CounterOpts{
		Namespace: cfg.Namespace,
		Subsystem: "outbox",
		Name:      "cleaned_total",
		Help:      "Total number of entries cleaned up (deleted) by the relay.",
	}, []string{"cell", "status"})

	for _, c := range []prom.Collector{relayed, pollDuration, batchSize, reclaimed, cleaned} {
		if err := cfg.Registry.Register(c); err != nil {
			return nil, errcode.Wrap(ErrAdapterPromRegister, "prometheus: relay metric registration failed", err)
		}
	}

	return &RelayCollector{
		cellID:       cfg.CellID,
		registry:     cfg.Registry,
		relayed:      relayed,
		pollDuration: pollDuration,
		batchSize:    batchSize,
		reclaimed:    reclaimed,
		cleaned:      cleaned,
	}, nil
}

// RecordPollCycle records a completed poll cycle.
func (c *RelayCollector) RecordPollCycle(r outbox.PollCycleResult) {
	cell := c.cellID

	if r.Published > 0 {
		c.relayed.With(prom.Labels{"cell": cell, "outcome": "published"}).Add(float64(r.Published))
	}
	if r.Retried > 0 {
		c.relayed.With(prom.Labels{"cell": cell, "outcome": "retried"}).Add(float64(r.Retried))
	}
	if r.Dead > 0 {
		c.relayed.With(prom.Labels{"cell": cell, "outcome": "dead"}).Add(float64(r.Dead))
	}
	if r.Skipped > 0 {
		c.relayed.With(prom.Labels{"cell": cell, "outcome": "skipped"}).Add(float64(r.Skipped))
	}

	c.pollDuration.With(prom.Labels{"cell": cell, "phase": "claim"}).Observe(r.ClaimDur.Seconds())
	c.pollDuration.With(prom.Labels{"cell": cell, "phase": "publish"}).Observe(r.PublishDur.Seconds())
	c.pollDuration.With(prom.Labels{"cell": cell, "phase": "write_back"}).Observe(r.WriteBackDur.Seconds())
	c.pollDuration.With(prom.Labels{"cell": cell, "phase": "total"}).Observe((r.ClaimDur + r.PublishDur + r.WriteBackDur).Seconds())
}

// RecordBatchSize records the number of entries claimed.
func (c *RelayCollector) RecordBatchSize(size int) {
	c.batchSize.With(prom.Labels{"cell": c.cellID}).Observe(float64(size))
}

// RecordReclaim records stale entries reclaimed.
func (c *RelayCollector) RecordReclaim(count int64) {
	if count > 0 {
		c.reclaimed.With(prom.Labels{"cell": c.cellID}).Add(float64(count))
	}
}

// RecordCleanup records entries cleaned up.
func (c *RelayCollector) RecordCleanup(publishedDeleted, deadDeleted int64) {
	cell := c.cellID
	if publishedDeleted > 0 {
		c.cleaned.With(prom.Labels{"cell": cell, "status": "published"}).Add(float64(publishedDeleted))
	}
	if deadDeleted > 0 {
		c.cleaned.With(prom.Labels{"cell": cell, "status": "dead"}).Add(float64(deadDeleted))
	}
}
