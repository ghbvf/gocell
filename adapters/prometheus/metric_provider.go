package prometheus

import (
	"sync"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

// MetricProviderConfig configures a Prometheus-backed metrics.Provider.
//
// ref: prometheus/client_golang prometheus/registry.go@main — NewRegistry is
// preferred over the global default so multiple providers can coexist in
// one process (e.g. one per test, or one per sub-system exposition).
type MetricProviderConfig struct {
	// Registry is the destination for all CounterVec/HistogramVec. Required.
	Registry *prom.Registry

	// Namespace prefixes every metric name (e.g. "gocell" → "gocell_foo_total").
	// Empty namespace emits bare names; useful when the surrounding exposition
	// already groups by job.
	Namespace string
}

// MetricProvider implements metrics.Provider by wrapping a Prometheus
// *prom.Registry. Every CounterVec/HistogramVec returned is registered on
// the configured registry; duplicate registration surfaces as an
// ErrAdapterPromRegister error, not a panic.
//
// Unregister is safe for concurrent use: it uses a RWMutex to protect the
// internal registry-to-collector map so rollback from NewProviderRelayCollector
// can be called from any goroutine.
type MetricProvider struct {
	cfg  MetricProviderConfig
	mu   sync.RWMutex
	vecs map[metrics.Collector]prom.Collector // kernel vec handle → prom Collector
}

// Compile-time check: MetricProvider satisfies metrics.Provider.
var _ metrics.Provider = (*MetricProvider)(nil)

// NewMetricProvider constructs a MetricProvider.
//
// Errors:
//   - ErrAdapterPromConfig if Registry is nil (required).
func NewMetricProvider(cfg MetricProviderConfig) (*MetricProvider, error) {
	if cfg.Registry == nil {
		return nil, errcode.New(ErrAdapterPromConfig, "prometheus metric provider: Registry is required")
	}
	return &MetricProvider{
		cfg:  cfg,
		vecs: make(map[metrics.Collector]prom.Collector),
	}, nil
}

// CounterVec registers and returns a CounterVec bound to the provider's
// registry. A duplicate Name (after namespace prefixing) results in
// ErrAdapterPromRegister.
func (p *MetricProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	cv := prom.NewCounterVec(prom.CounterOpts{
		Namespace: p.cfg.Namespace,
		Name:      opts.Name,
		Help:      opts.Help,
	}, opts.LabelNames)
	if err := p.cfg.Registry.Register(cv); err != nil {
		return nil, errcode.Wrap(ErrAdapterPromRegister,
			"prometheus metric provider: register counter "+opts.Name, err)
	}
	vec := &promCounterVec{inner: cv, labels: append([]string(nil), opts.LabelNames...)}
	p.mu.Lock()
	p.vecs[vec] = cv
	p.mu.Unlock()
	return vec, nil
}

// HistogramVec registers and returns a HistogramVec bound to the provider's
// registry. Empty Buckets uses Prometheus default (DefBuckets).
func (p *MetricProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	hv := prom.NewHistogramVec(prom.HistogramOpts{
		Namespace: p.cfg.Namespace,
		Name:      opts.Name,
		Help:      opts.Help,
		Buckets:   opts.Buckets,
	}, opts.LabelNames)
	if err := p.cfg.Registry.Register(hv); err != nil {
		return nil, errcode.Wrap(ErrAdapterPromRegister,
			"prometheus metric provider: register histogram "+opts.Name, err)
	}
	vec := &promHistogramVec{inner: hv, labels: append([]string(nil), opts.LabelNames...)}
	p.mu.Lock()
	p.vecs[vec] = hv
	p.mu.Unlock()
	return vec, nil
}

// Unregister removes a previously registered collector from the Prometheus
// registry. It is idempotent — passing an unknown Collector (or one already
// unregistered) returns nil. Concurrent calls are safe.
//
// ref: prometheus/client_golang prometheus/registry.go — Registry.Unregister
// returns bool; we convert "not found" to nil so callers treat it as a no-op.
func (p *MetricProvider) Unregister(c metrics.Collector) error {
	p.mu.Lock()
	promColl, ok := p.vecs[c]
	if ok {
		delete(p.vecs, c)
	}
	p.mu.Unlock()

	if !ok {
		// Idempotent: collector was never registered or already removed.
		return nil
	}
	p.cfg.Registry.Unregister(promColl)
	return nil
}

type promCounterVec struct {
	inner  *prom.CounterVec
	labels []string // Expected label-name set, validated on every With().
}

func (v *promCounterVec) Registered() bool { return true }
func (v *promCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labels, l)
	return promCounter{inner: v.inner.With(prom.Labels(l))}
}

type promHistogramVec struct {
	inner  *prom.HistogramVec
	labels []string
}

func (v *promHistogramVec) Registered() bool { return true }
func (v *promHistogramVec) With(l metrics.Labels) metrics.Histogram {
	metrics.MustValidateLabels(v.labels, l)
	return promHistogram{inner: v.inner.With(prom.Labels(l))}
}

type promCounter struct{ inner prom.Counter }

func (c promCounter) Inc()          { c.inner.Inc() }
func (c promCounter) Add(d float64) { c.inner.Add(d) }

type promHistogram struct{ inner prom.Observer }

func (h promHistogram) Observe(v float64) { h.inner.Observe(v) }
