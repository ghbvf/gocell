package prometheus

import (
	"net/http"
	"strconv"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Compile-time check: Collector implements metrics.Collector.
var _ metrics.Collector = (*Collector)(nil)

// Collector implements metrics.Collector using Prometheus CounterVec and HistogramVec.
type Collector struct {
	cellID   string
	registry *prom.Registry
	requests *prom.CounterVec
	duration *prom.HistogramVec
}

// NewCollector creates a Prometheus-backed metrics Collector. It registers
// two metric families (requests_total and request_duration_seconds) on the
// provided or default registry.
func NewCollector(cfg CollectorConfig) (*Collector, error) {
	cfg.defaults()

	if cfg.CellID == "" {
		return nil, errcode.New(ErrAdapterPromConfig, "prometheus: CellID is required")
	}

	labels := []string{"method", "path", "status", "cell"}

	requests := prom.NewCounterVec(prom.CounterOpts{
		Namespace: cfg.Namespace,
		Name:      "http_requests_total",
		Help:      "Total number of HTTP requests.",
	}, labels)

	duration := prom.NewHistogramVec(prom.HistogramOpts{
		Namespace: cfg.Namespace,
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request duration in seconds.",
		Buckets:   cfg.DurationBuckets,
	}, labels)

	if err := cfg.Registry.Register(requests); err != nil {
		return nil, errcode.Wrap(ErrAdapterPromRegister,
			"prometheus: register requests_total", err)
	}
	if err := cfg.Registry.Register(duration); err != nil {
		return nil, errcode.Wrap(ErrAdapterPromRegister,
			"prometheus: register request_duration_seconds", err)
	}

	return &Collector{
		cellID:   cfg.CellID,
		registry: cfg.Registry,
		requests: requests,
		duration: duration,
	}, nil
}

// RecordRequest records a completed HTTP request with the given labels.
func (c *Collector) RecordRequest(method, path string, status int, durationSeconds float64) {
	lbls := prom.Labels{
		"method": method,
		"path":   path,
		"status": strconv.Itoa(status),
		"cell":   c.cellID,
	}
	c.requests.With(lbls).Inc()
	c.duration.With(lbls).Observe(durationSeconds)
}

// Handler returns an http.Handler that serves Prometheus metrics in
// exposition format. Mount this on /metrics.
func (c *Collector) Handler() http.Handler {
	return promhttp.HandlerFor(c.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
