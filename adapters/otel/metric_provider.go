package otel

import (
	"context"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	// attrCacheMaxSize is the cap injected into each CounterVec /
	// HistogramVec's attrCache. Always defaults to defaultAttrCacheMaxSize
	// in NewMetricProvider; only same-package _test.go is permitted to
	// overwrite it (unexported field — packages outside adapters/otel
	// cannot reach it, so production misuse is a type-system error rather
	// than a convention).
	attrCacheMaxSize int
}

// Compile-time check: MetricProvider satisfies metrics.Provider.
var _ metrics.Provider = (*MetricProvider)(nil)

// defaultAttrCacheMaxSize caps the per-instrument attribute set cardinality
// to prevent unbounded memory growth when a caller emits with high-cardinality
// labels. 2000 matches the OTel SDK's own defaultCardinalityLimit so a stream
// of high-cardinality writes degrades into the overflow bucket at the same
// threshold the SDK would impose downstream.
//
// ref: opentelemetry-go sdk/metric/config.go@main defaultCardinalityLimit
// ref: opentelemetry-go sdk/metric/internal/aggregate/limit.go@main —
// overflow bucket pattern (vs LRU eviction, which would silently produce
// wrong-attribute reports on subsequent lookup of an evicted key).
const defaultAttrCacheMaxSize = 2000

// overflowAttrKey is the attribute key OTel SDK uses to mark data points
// produced past a cardinality cap (sourced from
// sdk/metric/internal/aggregate/limit.go private const `overflowAttrKey`
// in opentelemetry-go v1.43.0). Matching the SDK's key keeps GoCell's
// overflow data points indistinguishable from SDK-side overflow at the
// collector. If a future OTel release renames this key, update here and
// re-verify TestMetricProvider_OverflowDataPointEmitted.
const overflowAttrKey = "otel.metric.overflow"

// overflowOpt is the single MeasurementOption returned by attrCache.lookup
// when the cache is full. Emitting overflow under this sentinel collapses
// the unbounded high-cardinality tail into one data point tagged
// otel.metric.overflow=true, matching the OTel SDK's overflow attribute.
var overflowOpt = otelmetric.WithAttributes(attribute.Bool(overflowAttrKey, true))

// NewMetricProvider returns a Provider that registers instruments on the
// supplied Meter. Caller owns the MeterProvider (and exporter) lifecycle;
// this constructor does not spin up OTLP connections.
//
// Errors:
//   - ErrAdapterOTelConfig when meter is nil.
func NewMetricProvider(meter otelmetric.Meter) (*MetricProvider, error) {
	if meter == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterOTelConfig, "otel metric provider: Meter is required")
	}
	return &MetricProvider{
		meter:            meter,
		attrCacheMaxSize: defaultAttrCacheMaxSize,
	}, nil
}

// CounterVec creates a Float64Counter instrument. OTel counters are
// monotonic and support fractional increments (Add(delta)); the float
// choice matches metrics.Counter.Add(float64).
func (p *MetricProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	c, err := p.meter.Float64Counter(opts.Name, otelmetric.WithDescription(opts.Help))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			"otel metric provider: create counter failed", err,
			errcode.WithDetails(slog.String("metric", opts.Name)))
	}
	return &otelCounterVec{
		inner:  c,
		labels: append([]string(nil), opts.LabelNames...),
		cache:  newAttrCache(p.attrCacheMaxSize),
	}, nil
}

// Unregister is a no-op for the OTel provider. OTel instruments are
// registered with the MeterProvider at SDK level; individual instrument
// deregistration is not part of the OTel API. Returns nil (idempotent,
// per the Unregister contract).
func (p *MetricProvider) Unregister(_ metrics.Collector) error { return nil }

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
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			"otel metric provider: create histogram failed", err,
			errcode.WithDetails(slog.String("metric", opts.Name)))
	}
	return &otelHistogramVec{
		inner:  h,
		labels: append([]string(nil), opts.LabelNames...),
		cache:  newAttrCache(p.attrCacheMaxSize),
	}, nil
}

// attrCache memoises MeasurementOption per canonical label key so that
// repeat emission paths (pool collector loop, hook dispatcher) avoid
// per-call []attribute.KeyValue allocation.
//
// The cache is bounded by maxSize. Once at cap, subsequent distinct keys
// receive the package-level overflowOpt sentinel (otel.metric.overflow=true)
// instead of being inserted — this is the cap-and-overflow pattern the OTel
// SDK uses internally for view aggregation. Eviction (LRU) is deliberately
// not used: an evicted key, on later re-lookup, would receive a fresh
// MeasurementOption indistinguishable from the original, masking the
// cardinality issue from operators.
type attrCache struct {
	mu      sync.RWMutex
	m       map[string]otelmetric.MeasurementOption
	maxSize int
}

func newAttrCache(maxSize int) *attrCache {
	return &attrCache{
		m:       make(map[string]otelmetric.MeasurementOption, maxSize),
		maxSize: maxSize,
	}
}

// key builds the canonical cache key. LabelNames are ordered at
// registration, so we render values in that order. Separator "|" is safe
// because metrics.MustValidateLabels (called from CounterVec.With /
// HistogramVec.With before reaching this cache) rejects label values
// containing "|" or "=" via ErrLabelValueIllegal — collision via
// separator injection is statically impossible at the cache boundary.
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
	defer c.mu.Unlock()
	// Double-check: another goroutine may have populated the entry between
	// our RUnlock and Lock; return its result rather than racing it.
	if existing, ok := c.m[key]; ok {
		return existing
	}
	if len(c.m) >= c.maxSize {
		return overflowOpt
	}
	c.m[key] = opt
	return opt
}

type otelCounterVec struct {
	inner  otelmetric.Float64Counter
	labels []string
	cache  *attrCache
}

func (v *otelCounterVec) Registered() bool { return true }
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

func (v *otelHistogramVec) Registered() bool { return true }
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
//
// See METRICS-CTX-FUNNEL-01 in docs/backlog/cap-13-observability.md — the
// ctx-bearing alignment to OTel exemplar/baggage semantics is an open
// cross-layer refactor (kernel metrics interface + adapters/{prometheus,otel}
// + all emission sites). Background() here is bounded by the kernel interface
// shape, not by this adapter; the funnel ID points readers to the open work.
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
