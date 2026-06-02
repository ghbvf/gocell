package bootstrap

// metrics_autowire_test.go — direct unit tests for the single-source metric
// auto-wire helper autoWireCachedCollector[T]. The four bootstrap auto-wire
// functions (HTTP / event-router / outbox-reject / projection) all route
// through this helper, so pinning its skip/cache/fail-fast contract here is the
// single-source guarantee that #1399's warn-then-degrade regression cannot recur.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// fakeCachedCollector is an arbitrary cached value type for exercising the
// generic helper without depending on a real collector.
type fakeCachedCollector struct{ id int }

const testConflictMsg = "bootstrap: test metrics auto-wire conflict: " +
	"WithMetricsProvider constructs the test collector; do not also register foo. Remove one side"

func TestAutoWireCachedCollector_NilProvider_Skips(t *testing.T) {
	t.Parallel()
	b := New(clock.Real())
	b.metricsProvider = nil // explicit-nil: never wire, never construct

	var cache *fakeCachedCollector
	constructed := 0
	val, wired, err := autoWireCachedCollector(b, &cache,
		func(kernelmetrics.Provider) (*fakeCachedCollector, error) {
			constructed++
			return &fakeCachedCollector{}, nil
		}, testConflictMsg)

	require.NoError(t, err)
	assert.False(t, wired, "nil provider must not wire")
	assert.Nil(t, val)
	assert.Nil(t, cache, "cache must stay nil on skip")
	assert.Zero(t, constructed, "construct must not run on skip")
}

func TestAutoWireCachedCollector_NopProvider_Skips(t *testing.T) {
	t.Parallel()
	b := New(clock.Real()) // default provider is NopProvider
	_, isNop := b.metricsProvider.(kernelmetrics.NopProvider)
	require.True(t, isNop, "default must be NopProvider for this test to be meaningful")

	var cache *fakeCachedCollector
	constructed := 0
	val, wired, err := autoWireCachedCollector(b, &cache,
		func(kernelmetrics.Provider) (*fakeCachedCollector, error) {
			constructed++
			return &fakeCachedCollector{}, nil
		}, testConflictMsg)

	require.NoError(t, err)
	assert.False(t, wired, "Nop provider must not wire")
	assert.Nil(t, val)
	assert.Nil(t, cache)
	assert.Zero(t, constructed, "construct must not run on Nop")
}

func TestAutoWireCachedCollector_RealProvider_ConstructsAndCachesIdempotently(t *testing.T) {
	t.Parallel()
	spy := &registrationSpy{}
	b := New(clock.Real(), WithMetricsProvider(spy))

	var cache *fakeCachedCollector
	constructed := 0
	mk := func(kernelmetrics.Provider) (*fakeCachedCollector, error) {
		constructed++
		return &fakeCachedCollector{id: constructed}, nil
	}

	v1, w1, err := autoWireCachedCollector(b, &cache, mk, testConflictMsg)
	require.NoError(t, err)
	assert.True(t, w1)
	require.NotNil(t, v1)
	assert.Equal(t, 1, constructed)
	assert.Same(t, v1, cache, "first call must populate the cache pointer")

	// Second call: cache hit — same value, no re-construction.
	v2, w2, err := autoWireCachedCollector(b, &cache, mk, testConflictMsg)
	require.NoError(t, err)
	assert.True(t, w2)
	assert.Same(t, v1, v2, "cached value must be returned unchanged")
	assert.Equal(t, 1, constructed, "construct must run exactly once across repeated calls")
}

func TestAutoWireCachedCollector_ConstructError_FailFastWrapped(t *testing.T) {
	t.Parallel()
	spy := &registrationSpy{}
	b := New(clock.Real(), WithMetricsProvider(spy))

	var cache *fakeCachedCollector
	_, wired, err := autoWireCachedCollector(b, &cache,
		func(kernelmetrics.Provider) (*fakeCachedCollector, error) {
			return nil, errors.New("duplicate registration boom")
		}, testConflictMsg)

	require.Error(t, err, "registration conflict must be startup-fatal, never degraded")
	assert.False(t, wired)
	assert.Nil(t, cache, "cache must stay nil when construct fails")
	assert.Contains(t, err.Error(), "test metrics auto-wire conflict")
	assert.Contains(t, err.Error(), "Remove one side")
	assert.Contains(t, err.Error(), "duplicate registration boom", "underlying error must be wrapped")
}
