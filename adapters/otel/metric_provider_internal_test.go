package otel

import (
	"context"
	"strconv"
	"sync"
	"testing"

	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
)

// hasCacheEntry inspects the unexported map under read lock — used by tests
// to assert which keys ended up cached vs collapsed into overflow.
func (c *attrCache) hasCacheEntry(order []string, l metrics.Labels) bool {
	key := c.key(order, l)
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.m[key]
	return ok
}

// B2-R-09: attrCache must cap distinct entries at maxSize and route
// overflow lookups through the package-level overflowOpt sentinel (without
// inserting into the cache). The cap matches the OTel SDK's cardinality
// limit pattern: high-cardinality callers collapse into one overflow data
// point, the cache stays bounded.
//
// Detection: we cannot compare MeasurementOption interface values for
// identity (the concrete impl holds a slice; `==` panics). Instead we
// inspect the unexported cache map: keys looked up before cap are stored,
// keys looked up after cap are NOT stored (they hit the overflow branch).
func TestAttrCache_OverflowSentinelAtCap(t *testing.T) {
	const capSize = 4
	c := newAttrCache(capSize)
	order := []string{"k"}

	// Fill to exactly capSize with distinct keys.
	for i := 0; i < capSize; i++ {
		opt := c.lookup(order, metrics.Labels{"k": "v" + strconv.Itoa(i)})
		require.NotNil(t, opt)
	}
	assert.Equal(t, capSize, len(c.m), "cache must be exactly at cap after fill")
	for i := 0; i < capSize; i++ {
		assert.True(t, c.hasCacheEntry(order, metrics.Labels{"k": "v" + strconv.Itoa(i)}),
			"pre-cap key v%d must be cached", i)
	}

	// One more distinct key triggers overflow — NOT stored in cache.
	opt := c.lookup(order, metrics.Labels{"k": "overflow-1"})
	require.NotNil(t, opt)
	assert.Equal(t, capSize, len(c.m),
		"cache must not grow past cap when handling overflow keys")
	assert.False(t, c.hasCacheEntry(order, metrics.Labels{"k": "overflow-1"}),
		"overflow key must not be inserted into the cache")

	// Existing key still maps to its cached option (no eviction).
	c.lookup(order, metrics.Labels{"k": "v0"})
	assert.True(t, c.hasCacheEntry(order, metrics.Labels{"k": "v0"}),
		"already-cached key must not be evicted by overflow traffic")
	assert.Equal(t, capSize, len(c.m),
		"cache size must remain at cap after re-lookup of an existing key")
}

// B2-R-09: many concurrent goroutines hammering the cache across a
// cap*2 keyspace must not race or panic; cache must end bounded by cap.
// Runs with -race in CI; the assertion below is for behavior
// (boundedness), races themselves are caught by the race detector.
func TestAttrCache_ConcurrentLookupRaceSafe(t *testing.T) {
	const (
		capSize    = 32
		goroutines = 50
		iterations = 200
	)
	c := newAttrCache(capSize)
	order := []string{"k"}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := strconv.Itoa((seed + i) % (capSize * 2))
				_ = c.lookup(order, metrics.Labels{"k": key})
			}
		}(g)
	}
	wg.Wait()

	c.mu.RLock()
	size := len(c.m)
	c.mu.RUnlock()
	assert.LessOrEqualf(t, size, capSize,
		"cache must remain bounded by cap under concurrent load; size=%d cap=%d", size, capSize)
}

// B2-R-09: repeat lookup of the same overflow key must keep returning
// without inserting (no map growth, deterministic).
func TestAttrCache_OverflowIsStableAcrossLookups(t *testing.T) {
	const capSize = 2
	c := newAttrCache(capSize)
	order := []string{"k"}
	c.lookup(order, metrics.Labels{"k": "a"})
	c.lookup(order, metrics.Labels{"k": "b"})
	require.Equal(t, capSize, len(c.m))

	for i := 0; i < 5; i++ {
		c.lookup(order, metrics.Labels{"k": "overflow"})
	}
	assert.Equal(t, capSize, len(c.m),
		"overflow lookups must not grow the cache regardless of repeat count")
	assert.False(t, c.hasCacheEntry(order, metrics.Labels{"k": "overflow"}),
		"overflow key must remain uninserted across repeats")
}

// TestMetricProvider_GaugeVec_OverflowGaugeSlotIsShared pins the F8 finding:
// the otelGaugeVec.gauges map must not grow past attrCacheMaxSize + 1 even
// when many distinct label sets are used. Overflow label sets must share a
// single overflowGauge slot (the +1) rather than each getting their own entry.
func TestMetricProvider_GaugeVec_OverflowGaugeSlotIsShared(t *testing.T) {
	const cap = 3

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	provider, err := NewMetricProvider(mp.Meter("gocell.test"))
	require.NoError(t, err)
	provider.attrCacheMaxSize = cap

	gvIface, err := provider.GaugeVec(metrics.GaugeOpts{
		Name:       "gocell_test_gauge_overflow",
		Help:       "Test gauge overflow.",
		LabelNames: []string{"k"},
	})
	require.NoError(t, err)

	gv, ok := gvIface.(*otelGaugeVec)
	require.True(t, ok, "GaugeVec must return *otelGaugeVec")

	// Emit cap distinct label sets — all should land in gauges map.
	for i := 0; i < cap; i++ {
		gvIface.With(metrics.Labels{"k": strconv.Itoa(i)}).Set(context.Background(), 1)
	}
	gv.gaugesMu.Lock()
	sizeAtCap := len(gv.gauges)
	gv.gaugesMu.Unlock()
	require.Equal(t, cap, sizeAtCap, "gauges map must equal cap after filling to cap")

	// Emit 10 more distinct overflow label sets — gauges must not grow beyond cap.
	for i := cap; i < cap+10; i++ {
		gvIface.With(metrics.Labels{"k": strconv.Itoa(i)}).Set(context.Background(), float64(i))
	}
	gv.gaugesMu.Lock()
	sizeAfterOverflow := len(gv.gauges)
	overflowGauge := gv.overflowGauge
	gv.gaugesMu.Unlock()

	assert.Equal(t, cap, sizeAfterOverflow,
		"gauges map must not grow past cap (%d) for overflow label sets; got %d", cap, sizeAfterOverflow)
	assert.NotNil(t, overflowGauge,
		"overflowGauge must be lazily created for the shared overflow slot")
}

// B2-R-09 end-to-end: emit through a real CounterVec past cap and verify
// the downstream OTel reader sees a data point carrying
// otel.metric.overflow=true — confirms the package-level overflowOpt
// sentinel is wired correctly through the full instrument → reader pipeline.
//
// Overrides the per-provider attrCacheMaxSize via the unexported field
// (test lives in package otel, so the field is reachable here but invisible
// to any caller outside adapters/otel — production code cannot set a small
// cap because the field is not part of the public API). This replaces an
// earlier var-save/restore pattern around a package-level defaultAttrCacheMaxSize.
func TestMetricProvider_OverflowDataPointEmitted(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	provider, err := NewMetricProvider(mp.Meter("gocell.test"))
	require.NoError(t, err)
	provider.attrCacheMaxSize = 3

	cv, err := provider.CounterVec(metrics.CounterOpts{
		Name:       "gocell_test_overflow_total",
		LabelNames: []string{"k"},
	})
	require.NoError(t, err)

	// Emit 5 distinct values past cap=3 → 3 distinct cached + 2 overflow.
	for i := 0; i < 5; i++ {
		cv.With(metrics.Labels{"k": strconv.Itoa(i)}).Inc(context.Background())
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	var sawOverflow bool
	var totalDataPoints int
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gocell_test_overflow_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[float64])
			require.True(t, ok, "metric is not Sum[float64], got %T", m.Data)
			totalDataPoints = len(sum.DataPoints)
			for _, dp := range sum.DataPoints {
				if v, has := dp.Attributes.Value("otel.metric.overflow"); has && v.AsBool() {
					sawOverflow = true
				}
			}
		}
	}
	assert.True(t, sawOverflow,
		"a data point with otel.metric.overflow=true must appear past cap")
	assert.LessOrEqual(t, totalDataPoints, 4,
		"data points must collapse high-cardinality tail; got %d (cap=3 + 1 overflow expected)", totalDataPoints)
}

// ---------------------------------------------------------------------------
// ctx-passthrough tests (METRICS-CTX-FUNNEL-01)
//
// These tests assert that every otelCounter / otelHistogram / otelGauge method
// forwards the *caller-supplied* ctx to the underlying OTel instrument — not
// context.Background(). Fake implementations of Float64Counter /
// Float64Histogram / Float64Gauge capture the ctx argument so we can assert
// sentinel value propagation.
// ---------------------------------------------------------------------------

type ctxSentinelKey struct{}

// fakeFloat64Counter captures the ctx and delta from Add calls.
type fakeFloat64Counter struct {
	otelmetric.Float64Counter
	gotCtx   context.Context
	gotDelta float64
}

func (f *fakeFloat64Counter) Add(ctx context.Context, incr float64, _ ...otelmetric.MeasurementOption) {
	f.gotCtx = ctx
	f.gotDelta = incr
}

// fakeFloat64Histogram captures the ctx and value from Record calls.
type fakeFloat64Histogram struct {
	otelmetric.Float64Histogram
	gotCtx   context.Context
	gotValue float64
}

func (f *fakeFloat64Histogram) Record(ctx context.Context, value float64, _ ...otelmetric.MeasurementOption) {
	f.gotCtx = ctx
	f.gotValue = value
}

// fakeFloat64Gauge captures the ctx and value from Record calls.
type fakeFloat64Gauge struct {
	otelmetric.Float64Gauge
	gotCtx   context.Context
	gotValue float64
}

func (f *fakeFloat64Gauge) Record(ctx context.Context, value float64, _ ...otelmetric.MeasurementOption) {
	f.gotCtx = ctx
	f.gotValue = value
}

// sentinelCtx creates a context containing a sentinel value for identity checks.
func sentinelCtx() context.Context {
	return context.WithValue(context.Background(), ctxSentinelKey{}, "sentinel")
}

// assertCtxSentinel fails the test if ctx does not carry the sentinel value.
func assertCtxSentinel(t *testing.T, ctx context.Context, method string) {
	t.Helper()
	if ctx == nil {
		t.Errorf("%s: captured ctx is nil — method must forward ctx, not drop it", method)
		return
	}
	if ctx.Value(ctxSentinelKey{}) != "sentinel" {
		t.Errorf("%s: ctx does not carry sentinel — method forwarded context.Background() instead of caller ctx", method)
	}
}

// TestOtelCounter_CtxPassthrough verifies Inc and Add forward the caller's ctx.
// RED: if the body uses context.Background(), ctx.Value(ctxSentinelKey{}) == nil.
// GREEN: when the body uses the ctx parameter, the sentinel value is present.
func TestOtelCounter_CtxPassthrough(t *testing.T) {
	fake := &fakeFloat64Counter{}
	c := &otelCounter{inner: fake, attrs: overflowOpt}
	ctx := sentinelCtx()

	c.Inc(ctx)
	assertCtxSentinel(t, fake.gotCtx, "otelCounter.Inc")
	if fake.gotDelta != 1 {
		t.Errorf("Inc: expected delta=1, got %v", fake.gotDelta)
	}

	c.Add(ctx, 7.5)
	assertCtxSentinel(t, fake.gotCtx, "otelCounter.Add")
	if fake.gotDelta != 7.5 {
		t.Errorf("Add: expected delta=7.5, got %v", fake.gotDelta)
	}
}

// TestOtelHistogram_CtxPassthrough verifies Observe forwards the caller's ctx.
func TestOtelHistogram_CtxPassthrough(t *testing.T) {
	fake := &fakeFloat64Histogram{}
	h := &otelHistogram{inner: fake, attrs: overflowOpt}
	ctx := sentinelCtx()

	h.Observe(ctx, 3.14)
	assertCtxSentinel(t, fake.gotCtx, "otelHistogram.Observe")
	if fake.gotValue != 3.14 {
		t.Errorf("Observe: expected value=3.14, got %v", fake.gotValue)
	}
}

// TestOtelGauge_CtxPassthrough verifies Set, Inc, Dec, Add each forward the caller's ctx.
func TestOtelGauge_CtxPassthrough(t *testing.T) {
	ctx := sentinelCtx()

	t.Run("Set", func(t *testing.T) {
		fake := &fakeFloat64Gauge{}
		g := &otelGauge{inner: fake, attrs: overflowOpt}
		g.Set(ctx, 42.0)
		assertCtxSentinel(t, fake.gotCtx, "otelGauge.Set")
		if fake.gotValue != 42.0 {
			t.Errorf("Set: expected value=42.0, got %v", fake.gotValue)
		}
	})

	t.Run("Inc", func(t *testing.T) {
		fake := &fakeFloat64Gauge{}
		g := &otelGauge{inner: fake, attrs: overflowOpt}
		g.Inc(ctx)
		assertCtxSentinel(t, fake.gotCtx, "otelGauge.Inc")
		if fake.gotValue != 1.0 {
			t.Errorf("Inc: expected value=1.0, got %v", fake.gotValue)
		}
	})

	t.Run("Dec", func(t *testing.T) {
		fake := &fakeFloat64Gauge{}
		g := &otelGauge{inner: fake, attrs: overflowOpt}
		g.last = 5.0
		g.Dec(ctx)
		assertCtxSentinel(t, fake.gotCtx, "otelGauge.Dec")
		if fake.gotValue != 4.0 {
			t.Errorf("Dec: expected value=4.0, got %v", fake.gotValue)
		}
	})

	t.Run("Add", func(t *testing.T) {
		fake := &fakeFloat64Gauge{}
		g := &otelGauge{inner: fake, attrs: overflowOpt}
		g.last = 10.0
		g.Add(ctx, 3.0)
		assertCtxSentinel(t, fake.gotCtx, "otelGauge.Add")
		if fake.gotValue != 13.0 {
			t.Errorf("Add: expected value=13.0, got %v", fake.gotValue)
		}
	})
}
