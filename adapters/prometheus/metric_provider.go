package prometheus

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
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
// registry. If the same metric name has already been registered (e.g. when
// multiple cells share a single MetricProvider), the existing collector is
// returned — matching the standard prometheus AlreadyRegisteredError pattern.
// Any other registration error surfaces as ErrAdapterPromRegister.
//
// Note: structurally mirrors HistogramVec but operates on a different prom type
// (*prom.CounterVec vs *prom.HistogramVec); type assertion + label-lookup paths
// cannot share a helper without unsafe generic conversion or full reflection.
//

func (p *MetricProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	cv := prom.NewCounterVec(prom.CounterOpts{
		Namespace: p.cfg.Namespace,
		Name:      opts.Name,
		Help:      opts.Help,
	}, opts.LabelNames)
	if err := p.cfg.Registry.Register(cv); err != nil {
		var are prom.AlreadyRegisteredError
		if ok := errors.As(err, &are); ok {
			// Return the previously registered collector so callers sharing a
			// provider (e.g. accesscore + auditcore in the same assembly) get
			// back a valid CounterVec without failing init.
			existing, castOK := are.ExistingCollector.(*prom.CounterVec)
			if !castOK {
				return nil, errcode.Wrap(ErrAdapterPromRegister,
					"prometheus metric provider: existing collector type mismatch for counter "+opts.Name, err)
			}
			// Validate that the re-used collector's label set matches the requested one.
			// A label-set mismatch causes a panic at With() call time — reject it here.
			// We find the previously-registered wrapper in our vecs map to retrieve
			// the original label names.
			if existingLabels := p.lookupCounterVecLabels(existing); existingLabels != nil {
				if !slices.Equal(existingLabels, opts.LabelNames) {
					return nil, errcode.New(ErrAdapterPromRegister,
						"prometheus metric provider: label name mismatch for counter "+opts.Name+
							": existing="+join(existingLabels)+" requested="+join(opts.LabelNames))
				}
			}
			slog.Warn("prometheus metric provider: reusing already-registered collector",
				slog.String("name", opts.Name))
			return &promCounterVec{inner: existing, labels: append([]string(nil), opts.LabelNames...)}, nil
		}
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
// registry. Empty Buckets uses Prometheus default (DefBuckets). If the same
// metric name has already been registered, the existing collector is returned
// (same AlreadyRegisteredError pattern as CounterVec).
//

func (p *MetricProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	hv := prom.NewHistogramVec(prom.HistogramOpts{
		Namespace: p.cfg.Namespace,
		Name:      opts.Name,
		Help:      opts.Help,
		Buckets:   opts.Buckets,
	}, opts.LabelNames)
	if err := p.cfg.Registry.Register(hv); err != nil {
		var are prom.AlreadyRegisteredError
		if ok := errors.As(err, &are); ok {
			existing, castOK := are.ExistingCollector.(*prom.HistogramVec)
			if !castOK {
				return nil, errcode.Wrap(ErrAdapterPromRegister,
					"prometheus metric provider: existing collector type mismatch for histogram "+opts.Name, err)
			}
			// Validate label set consistency to prevent delayed With() panics.
			if existingLabels := p.lookupHistogramVecLabels(existing); existingLabels != nil {
				if !slices.Equal(existingLabels, opts.LabelNames) {
					return nil, errcode.New(ErrAdapterPromRegister,
						"prometheus metric provider: label name mismatch for histogram "+opts.Name+
							": existing="+join(existingLabels)+" requested="+join(opts.LabelNames))
				}
			}
			slog.Warn("prometheus metric provider: reusing already-registered collector",
				slog.String("name", opts.Name))
			return &promHistogramVec{inner: existing, labels: append([]string(nil), opts.LabelNames...)}, nil
		}
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

// lookupCounterVecLabels returns the label names for a previously registered
// *prom.CounterVec by finding its wrapper in our vecs map. Returns nil when
// the collector was not registered through this provider instance (safe to reuse).
func (p *MetricProvider) lookupCounterVecLabels(cv *prom.CounterVec) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for wrapper := range p.vecs {
		if w, ok := wrapper.(*promCounterVec); ok && w.inner == cv {
			return w.labels
		}
	}
	return nil
}

// lookupHistogramVecLabels returns the label names for a previously registered
// *prom.HistogramVec by finding its wrapper in our vecs map.
func (p *MetricProvider) lookupHistogramVecLabels(hv *prom.HistogramVec) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for wrapper := range p.vecs {
		if w, ok := wrapper.(*promHistogramVec); ok && w.inner == hv {
			return w.labels
		}
	}
	return nil
}

// join produces a compact comma-separated string for error messages.
func join(ss []string) string {
	var out strings.Builder
	out.WriteString("[")
	for i, s := range ss {
		if i > 0 {
			out.WriteString(",")
		}
		out.WriteString(s)
	}
	return out.String() + "]"
}

type promCounter struct{ inner prom.Counter }

func (c promCounter) Inc()          { c.inner.Inc() }
func (c promCounter) Add(d float64) { c.inner.Add(d) }

type promHistogram struct{ inner prom.Observer }

func (h promHistogram) Observe(v float64) { h.inner.Observe(v) }
