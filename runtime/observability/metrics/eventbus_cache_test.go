package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

func TestProviderEventbusCacheCollector_RejectsNilProvider(t *testing.T) {
	collector, err := obmetrics.NewProviderEventbusCacheCollector(nil)
	require.Error(t, err)
	assert.Nil(t, collector)
}

func TestProviderEventbusCacheCollector_NopProviderNoPanic(t *testing.T) {
	collector, err := obmetrics.NewProviderEventbusCacheCollector(kernelmetrics.NopProvider{})
	require.NoError(t, err)

	collector.RecordTombstoneEvicted("configcore", "configsubscribe")
}

func TestProviderEventbusCacheCollector_ReturnsRegistrationError(t *testing.T) {
	collector, err := obmetrics.NewProviderEventbusCacheCollector(failingCounterProvider{})
	require.Error(t, err)
	assert.Nil(t, collector)
	assert.Contains(t, err.Error(), "register eventbus_cache_tombstone_evicted_total")
}

func TestProviderEventbusCacheCollector_EmitsExpectedMetricAndLabels(t *testing.T) {
	p := newSpyProvider()
	collector, err := obmetrics.NewProviderEventbusCacheCollector(p)
	require.NoError(t, err)

	collector.RecordTombstoneEvicted("configcore", "configsubscribe")

	ops := p.counterOps["eventbus_cache_tombstone_evicted_total"]
	require.Len(t, ops, 1)
	assert.Equal(t, kernelmetrics.Labels{"cell": "configcore", "slice": "configsubscribe"}, ops[0].labels)
	assert.Equal(t, 1.0, ops[0].value)

	collector.RecordTombstoneEvicted("configcore", "configsubscribe")

	ops = p.counterOps["eventbus_cache_tombstone_evicted_total"]
	require.Len(t, ops, 2)
	assert.Equal(t, 1.0, ops[1].value)
}

func TestNoopEventbusCacheCollector_NoPanic(t *testing.T) {
	obmetrics.NoopEventbusCacheCollector{}.RecordTombstoneEvicted("a", "b")
}
