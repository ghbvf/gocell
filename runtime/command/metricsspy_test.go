package command

import (
	"sort"
	"sync"
	"sync/atomic"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// testProvider is a spy that accumulates counter observations in memory so
// tests can assert counts after the fact. It implements only the CounterVec
// surface SweeperLifecycle exercises (C.3 observability); Histogram/Unregister
// are intentionally omitted because no test needs them.
//
// Test-only: this is a _test.go helper local to package command — the single
// consumer is lifecycle_test.go. (Previously
// kernel/observability/metrics/testprovider.go; moved here so it stops dragging
// that production package below the kernel ≥90% coverage gate — an unexercised
// non-_test.go file counted against it. Promote to a shared *test subpackage
// only if a second consumer appears, adding the gate awk exclusion then.)
type testProvider struct {
	mu       sync.Mutex
	counters map[string]*testCounterVec // key: metric name
}

func newTestProvider() *testProvider {
	return &testProvider{counters: make(map[string]*testCounterVec)}
}

// CounterVec registers (or retrieves) a counter family by name.
func (p *testProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
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

// testProviderCounterValue returns the accumulated count for the named counter
// with the given label set. Returns 0 if no observations were recorded.
func testProviderCounterValue(p *testProvider, name string, labels map[string]string) float64 {
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

type testCounterVec struct {
	labels []string
	mu     sync.Mutex
	obs    map[string]*atomic.Int64 // key: labelsKey
}

func (v *testCounterVec) Registered() bool { return true }

func (v *testCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labels, l)
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

// labelsKey produces a deterministic string key for a label set (sorted keys).
func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
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
