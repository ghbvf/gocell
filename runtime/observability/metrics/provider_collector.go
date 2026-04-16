package metrics

import (
	"fmt"
	"strconv"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// DefaultDurationBuckets are the histogram buckets previously hardcoded in
// adapters/prometheus.Collector. Matching them keeps existing Grafana
// dashboards valid after the migration to the provider-neutral surface.
var DefaultDurationBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// ProviderCollectorConfig configures NewProviderCollector. Zero-value
// defaults produce the equivalent of the legacy Prometheus collector:
// same metric names ("http_requests_total", "http_request_duration_seconds")
// and the same DefaultDurationBuckets.
type ProviderCollectorConfig struct {
	// CellID labels every recorded observation (cell="{id}"). Required.
	CellID string
	// DurationBuckets overrides DefaultDurationBuckets; zero value uses defaults.
	DurationBuckets []float64
}

// providerCollector implements Collector on top of a provider-neutral
// metrics.Provider. The Provider owns registry ownership; this collector
// only records observations.
type providerCollector struct {
	cellID   string
	requests kernelmetrics.CounterVec
	duration kernelmetrics.HistogramVec
}

var _ Collector = (*providerCollector)(nil)

// NewProviderCollector builds an HTTP Collector that records through a
// kernel-level metrics.Provider (Prometheus-backed, OTel-backed, Nop, …).
//
// ref: kernel/observability/metrics — provider neutrality pattern.
// ref: pre-migration adapters/prometheus/collector.go — metric names and
// labels preserved so operators do not need to re-write their dashboards.
func NewProviderCollector(p kernelmetrics.Provider, cfg ProviderCollectorConfig) (Collector, error) {
	if p == nil {
		return nil, errcode.New(errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: Provider is required")
	}
	if cfg.CellID == "" {
		return nil, errcode.New(errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: CellID is required")
	}
	if len(cfg.DurationBuckets) == 0 {
		cfg.DurationBuckets = DefaultDurationBuckets
	}

	reqs, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "http_requests_total",
		Help:       "Total number of HTTP requests.",
		LabelNames: []string{"method", "route", "status", "cell"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register http_requests_total: %w", err)
	}
	dur, err := p.HistogramVec(kernelmetrics.HistogramOpts{
		Name:       "http_request_duration_seconds",
		Help:       "HTTP request duration in seconds.",
		LabelNames: []string{"method", "route", "status", "cell"},
		Buckets:    cfg.DurationBuckets,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register http_request_duration_seconds: %w", err)
	}

	return &providerCollector{cellID: cfg.CellID, requests: reqs, duration: dur}, nil
}

// RecordRequest emits an increment on http_requests_total and a sample on
// http_request_duration_seconds, labelled identically. Status is stringified
// once per call because Provider.CounterVec/HistogramVec expect string
// labels uniformly (avoids adapter-specific type conversions).
func (c *providerCollector) RecordRequest(method, route string, status int, durationSeconds float64) {
	labels := kernelmetrics.Labels{
		"method": method,
		"route":  route,
		"status": strconv.Itoa(status),
		"cell":   c.cellID,
	}
	c.requests.With(labels).Inc()
	c.duration.With(labels).Observe(durationSeconds)
}
