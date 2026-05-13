package otel

import (
	"context"
	"strconv"
	"sync"
	"testing"

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
	assert.LessOrEqual(t, size, capSize,
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

// B2-R-09 end-to-end: emit through a real CounterVec past cap and verify
// the downstream OTel reader sees a data point carrying
// otel.metric.overflow=true — confirms the package-level overflowOpt
// sentinel is wired correctly through the full instrument → reader pipeline.
//
// Uses defaultAttrCacheMaxSize save/restore to keep the emission loop small.
func TestMetricProvider_OverflowDataPointEmitted(t *testing.T) {
	orig := defaultAttrCacheMaxSize
	defaultAttrCacheMaxSize = 3
	t.Cleanup(func() { defaultAttrCacheMaxSize = orig })

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	provider, err := NewMetricProvider(mp.Meter("gocell.test"))
	require.NoError(t, err)

	cv, err := provider.CounterVec(metrics.CounterOpts{
		Name:       "gocell_test_overflow_total",
		LabelNames: []string{"k"},
	})
	require.NoError(t, err)

	// Emit 5 distinct values past cap=3 → 3 distinct cached + 2 overflow.
	for i := 0; i < 5; i++ {
		cv.With(metrics.Labels{"k": strconv.Itoa(i)}).Inc()
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
