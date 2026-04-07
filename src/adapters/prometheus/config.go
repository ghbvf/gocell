package prometheus

import prom "github.com/prometheus/client_golang/prometheus"

// DefaultDurationBuckets are the default histogram buckets for request duration.
var DefaultDurationBuckets = prom.DefBuckets

// CollectorConfig holds settings for the Prometheus metrics collector.
type CollectorConfig struct {
	// CellID identifies the cell that owns these metrics.
	CellID string

	// Namespace is the Prometheus metric namespace prefix (e.g. "gocell").
	// Default: "gocell".
	Namespace string

	// DurationBuckets configures histogram bucket boundaries in seconds.
	// Default: prometheus.DefBuckets.
	DurationBuckets []float64

	// Registry is the Prometheus registry to use. If nil, a new registry is
	// created. Using an isolated registry avoids collisions with the global
	// default registry.
	Registry *prom.Registry
}

// defaults fills zero-valued fields with sensible defaults.
func (c *CollectorConfig) defaults() {
	if c.Namespace == "" {
		c.Namespace = "gocell"
	}
	if len(c.DurationBuckets) == 0 {
		c.DurationBuckets = DefaultDurationBuckets
	}
	if c.Registry == nil {
		c.Registry = prom.NewRegistry()
	}
}
