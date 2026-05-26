package otel

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/ghbvf/gocell/adapters/otel/internal/otelwrap"
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
	c, err := otelwrap.Float64Counter(p.meter, opts.Name, otelmetric.WithDescription(opts.Help))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			"otel metric provider: create counter failed", err,
			errcode.WithDetails(errcode.PublicAttr("metric", opts.Name)))
	}
	return &otelCounterVec{
		inner:  c,
		labels: append([]string(nil), opts.LabelNames...),
		cache:  newAttrCache(p.attrCacheMaxSize),
	}, nil
}

// GaugeVec creates a Float64Gauge instrument (OTel v1.33+ synchronous gauge,
// LastValue / metricdata.Gauge export semantics) and wraps it in otelGaugeVec.
//
// Set(v) calls Float64Gauge.Record(v) directly — Record takes an absolute
// value, so no delta arithmetic is needed for Set. Inc/Dec/Add maintain a
// per-label-set last-value slot (protected by sync.Mutex) to compute the new
// absolute value before calling Record:
//
//	Inc:  last++; Record(ctx, last)
//	Dec:  last--; Record(ctx, last)
//	Add:  last += delta; Record(ctx, last)
//
// Each call to With() returns the *same* otelGauge for a given label set
// so that concurrent callers sharing a label set operate on the same
// last-value slot.
//
// ref: opentelemetry-go metric.Meter.Float64Gauge (v1.33+)
func (p *MetricProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	c, err := otelwrap.Float64Gauge(p.meter, opts.Name, otelmetric.WithDescription(opts.Help))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			"otel metric provider: create gauge failed", err,
			errcode.WithDetails(errcode.PublicAttr("metric", opts.Name)))
	}
	return &otelGaugeVec{
		inner:  c,
		labels: append([]string(nil), opts.LabelNames...),
		cache:  newAttrCache(p.attrCacheMaxSize),
		gauges: make(map[string]*otelGauge),
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
	h, err := otelwrap.Float64Histogram(p.meter, opts.Name, hOpts...)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			"otel metric provider: create histogram failed", err,
			errcode.WithDetails(errcode.PublicAttr("metric", opts.Name)))
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

// Inc records 1. ctx is forwarded to the OTel instrument to support exemplar
// and baggage correlation.
func (c *otelCounter) Inc(ctx context.Context) { c.inner.Add(ctx, 1, c.attrs) }

// Add records delta. ctx is forwarded to the OTel instrument to support exemplar
// and baggage correlation.
func (c *otelCounter) Add(ctx context.Context, delta float64) {
	c.inner.Add(ctx, delta, c.attrs)
}

type otelHistogram struct {
	inner otelmetric.Float64Histogram
	attrs otelmetric.MeasurementOption
}

// Observe records v. ctx is forwarded to the OTel instrument to support exemplar
// and baggage correlation.
func (h *otelHistogram) Observe(ctx context.Context, v float64) {
	h.inner.Record(ctx, v, h.attrs)
}

// otelGaugeVec wraps Float64Gauge and provides per-label-set last-value
// semantics. With() is the hot path; it returns a stable *otelGauge per
// label-set so that concurrent callers sharing the same label tuple operate
// on the same last-value slot.
//
// gaugesMu guards the gauges map; attrCache.mu guards the attribute cache.
// The two locks are independent and never held simultaneously to avoid
// lock-ordering deadlocks.
//
// Cardinality defense: when attrCache reaches its cap, lookup returns the
// package-level overflowOpt sentinel. In that case With() reuses a single
// shared overflowGauge (created lazily) instead of inserting into gauges,
// keeping len(gauges) ≤ attrCacheMaxSize + 1 (the +1 being the overflow slot).
type otelGaugeVec struct {
	inner         otelmetric.Float64Gauge
	labels        []string
	cache         *attrCache
	gaugesMu      sync.Mutex
	gauges        map[string]*otelGauge
	overflowGauge *otelGauge // lazily created; guarded by gaugesMu
}

func (v *otelGaugeVec) Registered() bool { return true }

// With returns the otelGauge for the given label set, creating it on first
// use. The same *otelGauge is returned on every call for a given label tuple
// so that Set(val) on one caller reflects the correct last value when another
// caller calls Set() later for the same label set.
//
// When the attrCache is at capacity, lookup returns the overflowOpt sentinel;
// With detects this via pointer identity (attrs == overflowOpt) and returns
// a single shared overflowGauge instead of growing gauges unboundedly.
func (v *otelGaugeVec) With(l metrics.Labels) metrics.Gauge {
	metrics.MustValidateLabels(v.labels, l)
	attrs := v.cache.lookup(v.labels, l)

	// Overflow path: attrCache is at cap; reuse the single shared overflow slot.
	if attrs == overflowOpt {
		v.gaugesMu.Lock()
		if v.overflowGauge == nil {
			v.overflowGauge = &otelGauge{inner: v.inner, attrs: overflowOpt}
		}
		g := v.overflowGauge
		v.gaugesMu.Unlock()
		return g
	}

	key := v.cache.key(v.labels, l)

	v.gaugesMu.Lock()
	g, ok := v.gauges[key]
	if !ok {
		g = &otelGauge{inner: v.inner, attrs: attrs}
		v.gauges[key] = g
	}
	v.gaugesMu.Unlock()
	return g
}

// otelGauge is a single label-set binding to a Float64Gauge.
// Float64Gauge.Record takes an absolute value (LastValue semantics), so
// Set(v) calls Record(v) directly. Inc/Dec/Add need read-modify-write
// semantics because they must compute the new absolute value from the
// previous one; last + mu provide that RMW slot.
//
// mu guards last; all four methods acquire it as a write lock so that
// concurrent Set / Inc / Dec / Add calls are serialized on the same slot.
// The OTel SDK's Record call itself is goroutine-safe; we only need mu to
// make the read-modify-write atomic for Inc/Dec/Add.
type otelGauge struct {
	inner otelmetric.Float64Gauge
	attrs otelmetric.MeasurementOption
	mu    sync.Mutex
	last  float64
}

// Set records the gauge as absolute value v (Float64Gauge.Record semantics).
// ctx is forwarded to the OTel instrument to support exemplar and baggage correlation.
func (g *otelGauge) Set(ctx context.Context, v float64) {
	g.mu.Lock()
	g.last = v
	g.mu.Unlock()
	g.inner.Record(ctx, v, g.attrs)
}

// Inc increments the gauge by 1.
// ctx is forwarded to the OTel instrument to support exemplar and baggage correlation.
func (g *otelGauge) Inc(ctx context.Context) {
	g.mu.Lock()
	g.last++
	v := g.last
	g.mu.Unlock()
	g.inner.Record(ctx, v, g.attrs)
}

// Dec decrements the gauge by 1.
// ctx is forwarded to the OTel instrument to support exemplar and baggage correlation.
func (g *otelGauge) Dec(ctx context.Context) {
	g.mu.Lock()
	g.last--
	v := g.last
	g.mu.Unlock()
	g.inner.Record(ctx, v, g.attrs)
}

// Add adds delta to the gauge.
// ctx is forwarded to the OTel instrument to support exemplar and baggage correlation.
func (g *otelGauge) Add(ctx context.Context, delta float64) {
	g.mu.Lock()
	g.last += delta
	v := g.last
	g.mu.Unlock()
	g.inner.Record(ctx, v, g.attrs)
}
