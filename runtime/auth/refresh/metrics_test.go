package refresh

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gcMetricProvider struct {
	failName   string
	registered map[string]metrics.Collector
}

func newGCMetricProvider(failName string) *gcMetricProvider {
	return &gcMetricProvider{failName: failName, registered: make(map[string]metrics.Collector)}
}

func (p *gcMetricProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	if opts.Name == p.failName {
		return nil, fmt.Errorf("forced failure")
	}
	v := gcCounterVec{name: opts.Name}
	p.registered[opts.Name] = v
	return v, nil
}

func (p *gcMetricProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	if opts.Name == p.failName {
		return nil, fmt.Errorf("forced failure")
	}
	v := gcHistogramVec{name: opts.Name}
	p.registered[opts.Name] = v
	return v, nil
}

func (p *gcMetricProvider) Unregister(c metrics.Collector) error {
	switch v := c.(type) {
	case gcCounterVec:
		delete(p.registered, v.name)
	case gcHistogramVec:
		delete(p.registered, v.name)
	}
	return nil
}

type gcCounterVec struct {
	name string
}

func (gcCounterVec) Registered() bool                    { return true }
func (gcCounterVec) With(metrics.Labels) metrics.Counter { return gcCounter{} }

type gcHistogramVec struct {
	name string
}

func (gcHistogramVec) Registered() bool                      { return true }
func (gcHistogramVec) With(metrics.Labels) metrics.Histogram { return gcHistogram{} }

type gcCounter struct{}

func (gcCounter) Inc()              {}
func (gcCounter) Add(delta float64) {}

type gcHistogram struct{}

func (gcHistogram) Observe(float64) {}

func TestNewProviderGCCollector_CleansUpPartialRegistration(t *testing.T) {
	p := newGCMetricProvider("auth_refresh_gc_removed_total")

	collector, err := NewProviderGCCollector(p)
	require.Error(t, err)
	assert.Nil(t, collector)
	assert.Empty(t, p.registered, "partial metric registration must be unregistered on failure")
}

func TestNewProviderGCCollector_RegistersAndObserves(t *testing.T) {
	p := newGCMetricProvider("")

	collector, err := NewProviderGCCollector(p)
	require.NoError(t, err)
	require.NotNil(t, collector)
	assert.Len(t, p.registered, 3)
	assert.Contains(t, p.registered, "auth_refresh_gc_runs_total")
	assert.Contains(t, p.registered, "auth_refresh_gc_removed_total")
	assert.Contains(t, p.registered, "auth_refresh_gc_duration_seconds")

	require.NotPanics(t, func() {
		collector.ObserveRefreshGC(context.Background(), "success", 7, 1500*time.Millisecond)
	})
}

func TestNewProviderGCCollector_RejectsNilProviderAndCleansHistogramFailure(t *testing.T) {
	collector, err := NewProviderGCCollector(nil)
	require.Error(t, err)
	assert.Nil(t, collector)

	p := newGCMetricProvider("auth_refresh_gc_duration_seconds")
	collector, err = NewProviderGCCollector(p)
	require.Error(t, err)
	assert.Nil(t, collector)
	assert.Empty(t, p.registered, "histogram registration failure must clean up counters")
}

func TestGCCollectors_NoopsAreSafe(t *testing.T) {
	require.NotPanics(t, func() {
		NoopGCCollector{}.ObserveRefreshGC(context.Background(), "success", 0, 0)
	})
	var collector *ProviderGCCollector
	require.NotPanics(t, func() {
		collector.ObserveRefreshGC(context.Background(), "failure", 0, time.Millisecond)
	})
}
