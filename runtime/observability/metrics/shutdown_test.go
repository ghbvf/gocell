package metrics

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// ---------------------------------------------------------------------------
// fakeMetricsProvider — records all metric registrations and observations
// (local to this test file; bootstrap integration tests carry their own copy)
// ---------------------------------------------------------------------------

type shutdownFakeCounterVec struct {
	mu      sync.Mutex
	labels  []string
	records []shutdownFakeCounterRecord
}

type shutdownFakeCounterRecord struct {
	labels kernelmetrics.Labels
	delta  float64
}

func (v *shutdownFakeCounterVec) Registered() bool { return true }
func (v *shutdownFakeCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return &shutdownFakeCounter{vec: v, labels: l}
}

type shutdownFakeCounter struct {
	vec    *shutdownFakeCounterVec
	labels kernelmetrics.Labels
}

func (c *shutdownFakeCounter) Inc() { c.Add(1) }
func (c *shutdownFakeCounter) Add(delta float64) {
	c.vec.mu.Lock()
	defer c.vec.mu.Unlock()
	c.vec.records = append(c.vec.records, shutdownFakeCounterRecord{labels: c.labels, delta: delta})
}

type shutdownFakeHistogramVec struct {
	mu      sync.Mutex
	labels  []string
	records []shutdownFakeHistogramRecord
}

type shutdownFakeHistogramRecord struct {
	labels kernelmetrics.Labels
	value  float64
}

func (v *shutdownFakeHistogramVec) Registered() bool { return true }
func (v *shutdownFakeHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return &shutdownFakeHistogram{vec: v, labels: l}
}

type shutdownFakeHistogram struct {
	vec    *shutdownFakeHistogramVec
	labels kernelmetrics.Labels
}

func (h *shutdownFakeHistogram) Observe(value float64) {
	h.vec.mu.Lock()
	defer h.vec.mu.Unlock()
	h.vec.records = append(h.vec.records, shutdownFakeHistogramRecord{labels: h.labels, value: value})
}

type shutdownFakeProvider struct {
	mu         sync.Mutex
	counters   map[string]*shutdownFakeCounterVec
	histograms map[string]*shutdownFakeHistogramVec
}

func newShutdownFakeProvider() *shutdownFakeProvider {
	return &shutdownFakeProvider{
		counters:   make(map[string]*shutdownFakeCounterVec),
		histograms: make(map[string]*shutdownFakeHistogramVec),
	}
}

func (p *shutdownFakeProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := &shutdownFakeCounterVec{labels: append([]string(nil), opts.LabelNames...)}
	p.counters[opts.Name] = v
	return v, nil
}

func (p *shutdownFakeProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := &shutdownFakeHistogramVec{labels: append([]string(nil), opts.LabelNames...)}
	p.histograms[opts.Name] = v
	return v, nil
}

func (p *shutdownFakeProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return kernelmetrics.NopProvider{}.GaugeVec(opts)
}

func (p *shutdownFakeProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

var _ kernelmetrics.Provider = (*shutdownFakeProvider)(nil)

// ---------------------------------------------------------------------------
// Test 6: nil-safety of ShutdownCollector methods (unit level)
// ---------------------------------------------------------------------------

// TestShutdownCollector_NilSafe verifies that all ShutdownCollector methods are
// nil-safe and do not panic when called on a nil receiver.
func TestShutdownCollector_NilSafe(t *testing.T) {
	var m *ShutdownCollector
	require.NotPanics(t, func() {
		m.RecordPhaseEntry(ShutdownPhaseReadinessFlip)
		m.ObservePhaseDuration("readiness_flip", testtime.D1ms)
		m.CountOutcome("success")
	})
}

// ---------------------------------------------------------------------------
// Test 7: NewShutdownCollector with nil provider returns disabled collector
// ---------------------------------------------------------------------------

func TestNewShutdownCollector_NilProvider(t *testing.T) {
	m, err := NewShutdownCollector(nil)
	require.NoError(t, err)
	require.NotNil(t, m, "nil provider must return a disabled ShutdownCollector")
	assert.True(t, m.disabled)
}

// ---------------------------------------------------------------------------
// Test 8: concurrent observations do not race
// ---------------------------------------------------------------------------

// TestShutdownCollector_ConcurrentObserve verifies that concurrent calls to
// ShutdownCollector methods do not cause data races (exercised via -race).
func TestShutdownCollector_ConcurrentObserve(t *testing.T) {
	p := newShutdownFakeProvider()
	m, err := NewShutdownCollector(p)
	require.NoError(t, err)
	require.NotNil(t, m)

	var wg sync.WaitGroup
	var panicked atomic.Bool
	for range 10 {
		wg.Go(func() {
			defer func() {
				if recover() != nil {
					panicked.Store(true)
				}
			}()
			m.RecordPhaseEntry(ShutdownPhaseReadinessFlip)
			m.ObservePhaseDuration("readiness_flip", time.Millisecond)
			m.CountOutcome("success")
		})
	}
	wg.Wait()
	assert.False(t, panicked.Load(), "concurrent calls must not panic")
}
