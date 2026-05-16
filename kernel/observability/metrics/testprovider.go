package metrics

import (
	"sync"
	"sync/atomic"
)

// TestProvider is a spy Provider for use in unit tests. It accumulates counter
// observations in memory so callers can assert counts after the fact.
//
// Intended for test packages in kernel/ and runtime/; do NOT use in production
// code (there is no archtest gate, rely on code review to enforce).
type TestProvider struct {
	mu       sync.Mutex
	counters map[string]*testCounterVec // key: metric name
}

// NewTestProvider returns a fresh TestProvider with an empty registry.
func NewTestProvider() *TestProvider {
	return &TestProvider{
		counters: make(map[string]*testCounterVec),
	}
}

// CounterVec registers (or retrieves) a counter family by name.
func (p *TestProvider) CounterVec(opts CounterOpts) (CounterVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.counters[opts.Name]; ok {
		return v, nil
	}
	v := &testCounterVec{
		labels: append([]string(nil), opts.LabelNames...),
		obs:    make(map[string]*atomic.Int64),
	}
	p.counters[opts.Name] = v
	return v, nil
}

// HistogramVec returns a no-op HistogramVec (histograms are not tracked by
// TestProvider; use NopProvider if histogram recording is needed).
func (p *TestProvider) HistogramVec(opts HistogramOpts) (HistogramVec, error) {
	return nopHistogramVec{labels: append([]string(nil), opts.LabelNames...)}, nil
}

// Unregister is a no-op for the TestProvider.
func (p *TestProvider) Unregister(_ Collector) error { return nil }

// TestProviderCounterValue returns the accumulated float64 count for the
// named counter with the given label set. Returns 0.0 if no observations
// have been recorded yet.
//
// This function is a test-only helper; it must not be called from
// production code.
func TestProviderCounterValue(p *TestProvider, name string, labels map[string]string) float64 {
	p.mu.Lock()
	v, ok := p.counters[name]
	p.mu.Unlock()
	if !ok {
		return 0
	}
	key := labelsKey(labels)
	v.mu.Lock()
	obs, ok := v.obs[key]
	v.mu.Unlock()
	if !ok {
		return 0
	}
	return float64(obs.Load())
}

// ---------------------------------------------------------------------------
// internal implementation
// ---------------------------------------------------------------------------

type testCounterVec struct {
	labels []string
	mu     sync.Mutex
	obs    map[string]*atomic.Int64 // key: labelsKey
}

func (v *testCounterVec) Registered() bool { return true }

func (v *testCounterVec) With(l Labels) Counter {
	MustValidateLabels(v.labels, l)
	key := labelsKey(l)
	v.mu.Lock()
	obs, ok := v.obs[key]
	if !ok {
		obs = &atomic.Int64{}
		v.obs[key] = obs
	}
	v.mu.Unlock()
	return &testCounter{obs: obs}
}

type testCounter struct {
	obs *atomic.Int64
}

func (c *testCounter) Inc()              { c.obs.Add(1) }
func (c *testCounter) Add(delta float64) { c.obs.Add(int64(delta)) }

// labelsKey produces a canonical string key for a label set. The key is
// stable and deterministic for a given label map.
func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	// Use sorted keys (same order as sortedKeys) for determinism.
	keys := sortedKeys(labels)
	b := make([]byte, 0, 64)
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, k...)
		b = append(b, '=')
		b = append(b, labels[k]...)
	}
	return string(b)
}
