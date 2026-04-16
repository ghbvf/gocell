package otel

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// MetricProvider implements metrics.Provider backed by an OTel Meter.
//
// ref: opentelemetry-go metric/meter.go@main — Int64Counter / Float64Histogram
// are the underlying instruments; our Counter/Histogram bind pre-computed
// label attributes and call Add / Record at record time. We expose a
// label-map abstraction on top because the kernel wants label *drift
// detection* (see kernel/observability/metrics.MustValidateLabels); OTel's
// native variadic attribute.KeyValue makes drift silent.
type MetricProvider struct {
	meter otelmetric.Meter
}

// Compile-time check: MetricProvider satisfies metrics.Provider.
var _ metrics.Provider = (*MetricProvider)(nil)

// NewMetricProvider returns a Provider that registers instruments on the
// supplied Meter. Caller owns the MeterProvider (and exporter) lifecycle;
// this constructor does not spin up OTLP connections.
//
// Errors:
//   - ErrAdapterOTelConfig when meter is nil.
func NewMetricProvider(meter otelmetric.Meter) (*MetricProvider, error) {
	if meter == nil {
		return nil, errcode.New(ErrAdapterOTelConfig, "otel metric provider: Meter is required")
	}
	return &MetricProvider{meter: meter}, nil
}

// CounterVec creates a Float64Counter instrument. OTel counters are
// monotonic and support fractional increments (Add(delta)); the float
// choice matches metrics.Counter.Add(float64).
func (p *MetricProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	c, err := p.meter.Float64Counter(opts.Name, otelmetric.WithDescription(opts.Help))
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOTelInit,
			"otel metric provider: create counter "+opts.Name, err)
	}
	return &otelCounterVec{
		inner:  c,
		labels: append([]string(nil), opts.LabelNames...),
		cache:  &attrCache{m: map[string]otelmetric.MeasurementOption{}},
	}, nil
}

// HistogramVec creates a Float64Histogram. Explicit Buckets propagate
// to OTel as aggregation preferences; callers that want richer aggregation
// (exponential, quantile) must build their MeterProvider with the relevant
// views before handing a Meter to us.
func (p *MetricProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	hOpts := []otelmetric.Float64HistogramOption{
		otelmetric.WithDescription(opts.Help),
	}
	if len(opts.Buckets) > 0 {
		hOpts = append(hOpts, otelmetric.WithExplicitBucketBoundaries(opts.Buckets...))
	}
	h, err := p.meter.Float64Histogram(opts.Name, hOpts...)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOTelInit,
			"otel metric provider: create histogram "+opts.Name, err)
	}
	return &otelHistogramVec{
		inner:  h,
		labels: append([]string(nil), opts.LabelNames...),
		cache:  &attrCache{m: map[string]otelmetric.MeasurementOption{}},
	}, nil
}

// attrCache memoises MeasurementOption per canonical label key so that
// repeat emission paths (pool_collector loop, hook dispatcher) avoid
// per-call []attribute.KeyValue allocation. Cache grows with cardinality.
type attrCache struct {
	mu sync.RWMutex
	m  map[string]otelmetric.MeasurementOption
}

// key builds the canonical cache key. LabelNames are ordered at
// registration, so we render values in that order — stable and collision
// resistant for this use (labels are strings, "|" cannot appear in typical
// snake_case values; for extreme edge cases the adapter would deliver
// wrong attributes silently, which is acceptable for internal metric
// labels controlled by GoCell itself).
func (c *attrCache) key(order []string, l metrics.Labels) string {
	n := 0
	for _, name := range order {
		n += len(name) + len(l[name]) + 2
	}
	buf := make([]byte, 0, n)
	for i, name := range order {
		if i > 0 {
			buf = append(buf, '|')
		}
		buf = append(buf, name...)
		buf = append(buf, '=')
		buf = append(buf, l[name]...)
	}
	return string(buf)
}

func (c *attrCache) lookup(order []string, l metrics.Labels) otelmetric.MeasurementOption {
	key := c.key(order, l)

	c.mu.RLock()
	if opt, ok := c.m[key]; ok {
		c.mu.RUnlock()
		return opt
	}
	c.mu.RUnlock()

	attrs := make([]attribute.KeyValue, 0, len(order))
	for _, name := range order {
		attrs = append(attrs, attribute.String(name, l[name]))
	}
	opt := otelmetric.WithAttributes(attrs...)

	c.mu.Lock()
	c.m[key] = opt
	c.mu.Unlock()
	return opt
}

type otelCounterVec struct {
	inner  otelmetric.Float64Counter
	labels []string
	cache  *attrCache
}

func (v *otelCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labels, l)
	return &otelCounter{
		inner: v.inner,
		attrs: v.cache.lookup(v.labels, l),
	}
}

type otelHistogramVec struct {
	inner  otelmetric.Float64Histogram
	labels []string
	cache  *attrCache
}

func (v *otelHistogramVec) With(l metrics.Labels) metrics.Histogram {
	metrics.MustValidateLabels(v.labels, l)
	return &otelHistogram{
		inner: v.inner,
		attrs: v.cache.lookup(v.labels, l),
	}
}

type otelCounter struct {
	inner otelmetric.Float64Counter
	attrs otelmetric.MeasurementOption
}

// Inc records 1. Uses context.Background() because the Counter interface
// deliberately omits context (kernel modules emit from hot paths where
// passing ctx everywhere would be noise). OTel tolerates Background.
func (c *otelCounter) Inc() { c.inner.Add(context.Background(), 1, c.attrs) }

func (c *otelCounter) Add(delta float64) {
	c.inner.Add(context.Background(), delta, c.attrs)
}

type otelHistogram struct {
	inner otelmetric.Float64Histogram
	attrs otelmetric.MeasurementOption
}

func (h *otelHistogram) Observe(v float64) {
	h.inner.Record(context.Background(), v, h.attrs)
}
