package reconcile

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// TestRegisterMetrics_WiresFour asserts RegisterMetrics registers all four
// instruments with their canonical names + label sets (FR-010).
func TestRegisterMetrics_WiresFour(t *testing.T) {
	t.Parallel()
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	require.NotNil(t, m.Total)
	require.NotNil(t, m.Duration)
	require.NotNil(t, m.InFlight)
	require.NotNil(t, m.Leader)

	assert.ElementsMatch(t, []string{labelReconciler, labelResult}, p.counterLabels(metricReconcileTotal))
	assert.ElementsMatch(t, []string{labelReconciler}, p.histogramLabels(metricReconcileDuration))
	assert.ElementsMatch(t, []string{labelReconciler}, p.gaugeLabels(metricReconcileInFlight))
	assert.ElementsMatch(t, []string{labelReconciler}, p.gaugeLabels(metricReconcileLeader))
}

// TestMetrics_Preflight_GoodLabelsPasses asserts a Metrics built by
// RegisterMetrics preflights cleanly (the canonical label sets validate).
func TestMetrics_Preflight_GoodLabelsPasses(t *testing.T) {
	t.Parallel()
	m, err := RegisterMetrics(newRecordingProvider())
	require.NoError(t, err)
	require.NoError(t, m.preflight("certrotation"))
}

// TestMetrics_Preflight_BadLabelsFails asserts a hand-built Metrics whose
// counter has the wrong label set is caught by preflight (recover → error)
// rather than crashing a worker on first record.
func TestMetrics_Preflight_BadLabelsFails(t *testing.T) {
	t.Parallel()
	p := newRecordingProvider()
	// Register the Total counter with an intentionally-wrong label set.
	bad, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       metricReconcileTotal,
		LabelNames: []string{"wrong"},
	})
	require.NoError(t, err)
	m := Metrics{Total: bad}
	gotErr := m.preflight("certrotation")
	require.Error(t, gotErr)
	assert.Contains(t, gotErr.Error(), "metrics label set invalid")
}

// -----------------------------------------------------------------------------
// recordingProvider — an in-memory metrics.Provider spy for reconcile tests.
// Mirrors runtime/command's testProvider but covers all three vec kinds.
// -----------------------------------------------------------------------------

type recordingProvider struct {
	mu         sync.Mutex
	counters   map[string]*recordingCounterVec
	histograms map[string]*recordingHistogramVec
	gauges     map[string]*recordingGaugeVec
}

func newRecordingProvider() *recordingProvider {
	return &recordingProvider{
		counters:   map[string]*recordingCounterVec{},
		histograms: map[string]*recordingHistogramVec{},
		gauges:     map[string]*recordingGaugeVec{},
	}
}

func (p *recordingProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.counters[opts.Name]; ok {
		return v, nil
	}
	v := &recordingCounterVec{labels: append([]string(nil), opts.LabelNames...), obs: map[string]*atomic.Int64{}}
	p.counters[opts.Name] = v
	return v, nil
}

func (p *recordingProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.histograms[opts.Name]; ok {
		return v, nil
	}
	v := &recordingHistogramVec{labels: append([]string(nil), opts.LabelNames...), obs: map[string]*atomic.Int64{}}
	p.histograms[opts.Name] = v
	return v, nil
}

func (p *recordingProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.gauges[opts.Name]; ok {
		return v, nil
	}
	v := &recordingGaugeVec{labels: append([]string(nil), opts.LabelNames...), vals: map[string]float64{}}
	p.gauges[opts.Name] = v
	return v, nil
}

func (p *recordingProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

func (p *recordingProvider) counterLabels(name string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.counters[name]; ok {
		return v.labels
	}
	return nil
}

func (p *recordingProvider) histogramLabels(name string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.histograms[name]; ok {
		return v.labels
	}
	return nil
}

func (p *recordingProvider) gaugeLabels(name string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.gauges[name]; ok {
		return v.labels
	}
	return nil
}

func (p *recordingProvider) counterValue(l kernelmetrics.Labels) int64 {
	p.mu.Lock()
	v, ok := p.counters[metricReconcileTotal]
	p.mu.Unlock()
	if !ok {
		return 0
	}
	return v.value(l)
}

// signalWhenCounterReaches returns a channel that is closed once the
// reconcile_total counter for the given labels reaches or exceeds threshold.
// The channel is closed at most once (sync.Once). Callers MUST register this
// before starting the loop (or before sending requests) so no increment is
// missed. The returned channel is safe to pass to testwait.Deterministic.
//
// Thread safety: the notify callback is installed under vec.mu and is read
// under vec.mu in Inc/Add, so there is no data race between registration and
// concurrent counter increments.
func (p *recordingProvider) signalWhenCounterReaches(l kernelmetrics.Labels, threshold int64) <-chan struct{} {
	ch := make(chan struct{})
	var once sync.Once
	fire := func() { once.Do(func() { close(ch) }) }

	// Install the notify callback on the reconcile_total counter vec.
	// RegisterMetrics must be called before signalWhenCounterReaches so the vec
	// already exists; callers that violate this ordering will get a clear panic.
	p.mu.Lock()
	vec, ok := p.counters[metricReconcileTotal]
	p.mu.Unlock()
	if !ok {
		panic("signalWhenCounterReaches: RegisterMetrics must be called before registering a signal")
	}

	labelKey := labelsKeyR(l)
	vec.mu.Lock()
	vec.notify = func(labels kernelmetrics.Labels, newVal int64) {
		if labelsKeyR(labels) == labelKey && newVal >= threshold {
			fire()
		}
	}
	// Check if threshold is already reached (race-free: vec.mu held).
	if cur, exists := vec.obs[labelKey]; exists && cur.Load() >= threshold {
		fire()
	}
	vec.mu.Unlock()

	return ch
}

func (p *recordingProvider) histogramCount(name string, l kernelmetrics.Labels) int64 {
	p.mu.Lock()
	v, ok := p.histograms[name]
	p.mu.Unlock()
	if !ok {
		return 0
	}
	return v.count(l)
}

func (p *recordingProvider) gaugeValue(name string, l kernelmetrics.Labels) float64 {
	p.mu.Lock()
	v, ok := p.gauges[name]
	p.mu.Unlock()
	if !ok {
		return 0
	}
	return v.value(l)
}

type recordingCounterVec struct {
	labels []string
	mu     sync.Mutex
	obs    map[string]*atomic.Int64
	// notify is an optional per-increment callback set by tests. It is called
	// after every Inc/Add with the label set and the new counter value.
	// The callback is invoked outside recordingCounterVec.mu to avoid inversion
	// with any mutex the callback itself may acquire.
	notify func(labels kernelmetrics.Labels, newVal int64)
}

func (v *recordingCounterVec) Registered() bool { return true }
func (v *recordingCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labels, l)
	// Snapshot labels for the counter so the notify callback can reference them
	// without holding v.mu.
	labelsCopy := make(kernelmetrics.Labels, len(l))
	for k, val := range l {
		labelsCopy[k] = val
	}
	v.mu.Lock()
	c, ok := v.obs[labelsKeyR(l)]
	if !ok {
		c = &atomic.Int64{}
		v.obs[labelsKeyR(l)] = c
	}
	v.mu.Unlock()
	// Pass a pointer to the vec so Inc/Add always read the latest notify
	// callback, even if it is installed after With is first called.
	return &recordingCounter{n: c, labels: labelsCopy, vec: v}
}

func (v *recordingCounterVec) value(l kernelmetrics.Labels) int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if c, ok := v.obs[labelsKeyR(l)]; ok {
		return c.Load()
	}
	return 0
}

type recordingCounter struct {
	n      *atomic.Int64
	labels kernelmetrics.Labels
	// vec is a back-reference to the parent vec so Inc/Add always read the
	// latest notify callback, even if it is installed after With is first called.
	vec *recordingCounterVec
}

func (c *recordingCounter) Inc(_ context.Context) {
	newVal := c.n.Add(1)
	c.vec.mu.Lock()
	fn := c.vec.notify
	c.vec.mu.Unlock()
	if fn != nil {
		fn(c.labels, newVal)
	}
}

func (c *recordingCounter) Add(_ context.Context, d float64) {
	newVal := c.n.Add(int64(d))
	c.vec.mu.Lock()
	fn := c.vec.notify
	c.vec.mu.Unlock()
	if fn != nil {
		fn(c.labels, newVal)
	}
}

type recordingHistogramVec struct {
	labels []string
	mu     sync.Mutex
	obs    map[string]*atomic.Int64
}

func (v *recordingHistogramVec) Registered() bool { return true }
func (v *recordingHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	kernelmetrics.MustValidateLabels(v.labels, l)
	v.mu.Lock()
	c, ok := v.obs[labelsKeyR(l)]
	if !ok {
		c = &atomic.Int64{}
		v.obs[labelsKeyR(l)] = c
	}
	v.mu.Unlock()
	return &recordingHistogram{n: c}
}

func (v *recordingHistogramVec) count(l kernelmetrics.Labels) int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if c, ok := v.obs[labelsKeyR(l)]; ok {
		return c.Load()
	}
	return 0
}

type recordingHistogram struct{ n *atomic.Int64 }

func (h *recordingHistogram) Observe(context.Context, float64) { h.n.Add(1) }

type recordingGaugeVec struct {
	labels []string
	mu     sync.Mutex
	vals   map[string]float64
}

func (v *recordingGaugeVec) Registered() bool { return true }
func (v *recordingGaugeVec) With(l kernelmetrics.Labels) kernelmetrics.Gauge {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return &recordingGauge{vec: v, key: labelsKeyR(l)}
}

func (v *recordingGaugeVec) value(l kernelmetrics.Labels) float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.vals[labelsKeyR(l)]
}

type recordingGauge struct {
	vec *recordingGaugeVec
	key string
}

func (g *recordingGauge) Set(_ context.Context, val float64) {
	g.vec.mu.Lock()
	g.vec.vals[g.key] = val
	g.vec.mu.Unlock()
}
func (g *recordingGauge) Inc(ctx context.Context) { g.Add(ctx, 1) }
func (g *recordingGauge) Dec(ctx context.Context) { g.Add(ctx, -1) }
func (g *recordingGauge) Add(_ context.Context, d float64) {
	g.vec.mu.Lock()
	g.vec.vals[g.key] += d
	g.vec.mu.Unlock()
}

func labelsKeyR(l kernelmetrics.Labels) string {
	if len(l) == 0 {
		return ""
	}
	keys := make([]string, 0, len(l))
	for k := range l {
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
		b = append(b, l[k]...)
	}
	return string(b)
}
