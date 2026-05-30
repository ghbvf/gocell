package projection

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// TestRegisterProjectionMetrics_WiresThree asserts RegisterMetrics registers
// all three instruments with their canonical names and label sets.
func TestRegisterProjectionMetrics_WiresThree(t *testing.T) {
	t.Parallel()
	p := newProjectionRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	if m == nil {
		t.Fatal("RegisterMetrics returned nil *Metrics")
	}
	if m.ReplayLag == nil {
		t.Error("ReplayLag is nil")
	}
	if m.RebuildDuration == nil {
		t.Error("RebuildDuration is nil")
	}
	if m.PendingEvents == nil {
		t.Error("PendingEvents is nil")
	}

	// Verify label sets.
	if labels := p.gaugeLabels(metricProjectionReplayLag); !labelsMatch(labels, []string{"cell", "projection"}) {
		t.Errorf("ReplayLag labels = %v, want [cell projection]", labels)
	}
	if labels := p.histogramLabels(metricProjectionRebuildDuration); !labelsMatch(labels, []string{"cell", "projection"}) {
		t.Errorf("RebuildDuration labels = %v, want [cell projection]", labels)
	}
	if labels := p.gaugeLabels(metricProjectionPendingEvents); !labelsMatch(labels, []string{"cell", "projection"}) {
		t.Errorf("PendingEvents labels = %v, want [cell projection]", labels)
	}
}

// TestProjectionMetrics_Preflight_GoodLabelsPasses asserts preflight with
// valid labels passes cleanly.
func TestProjectionMetrics_Preflight_GoodLabelsPasses(t *testing.T) {
	t.Parallel()
	m, err := RegisterMetrics(newProjectionRecordingProvider())
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	if err := m.preflight("mycell", "myproj"); err != nil {
		t.Errorf("preflight: unexpected error: %v", err)
	}
}

// TestProjectionMetrics_Preflight_BadLabelsFails asserts a hand-built Metrics
// with wrong label set is caught by preflight.
func TestProjectionMetrics_Preflight_BadLabelsFails(t *testing.T) {
	t.Parallel()
	p := newProjectionRecordingProvider()
	bad, err := p.GaugeVec(kernelmetrics.GaugeOpts{
		Name:       metricProjectionReplayLag,
		LabelNames: []string{"wrong"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}
	m := &Metrics{ReplayLag: bad}
	gotErr := m.preflight("cell", "proj")
	if gotErr == nil {
		t.Fatal("preflight with bad labels: expected error, got nil")
	}
	if !containsStr(gotErr.Error(), "metrics label set invalid") {
		t.Errorf("error %q does not mention 'metrics label set invalid'", gotErr.Error())
	}
}

// TestProjectionMetrics_NilSafe asserts all helpers are nil-safe when *Metrics is nil.
func TestProjectionMetrics_NilSafe(t *testing.T) {
	t.Parallel()
	var m *Metrics
	ctx := context.Background()
	// None of these must panic.
	m.setReplayLag(ctx, "c", "p", 1.0)
	m.observeRebuildDuration(ctx, "c", "p", 0.5)
	m.setPendingEvents(ctx, "c", "p", 3.0)
}

// TestProjectionMetrics_RecordsReplayLag asserts setReplayLag writes the gauge.
func TestProjectionMetrics_RecordsReplayLag(t *testing.T) {
	t.Parallel()
	p := newProjectionRecordingProvider()
	m, _ := RegisterMetrics(p)
	ctx := context.Background()
	m.setReplayLag(ctx, "mycell", "myproj", 42.5)
	v := p.gaugeValue(metricProjectionReplayLag, kernelmetrics.Labels{"cell": "mycell", "projection": "myproj"})
	if v != 42.5 {
		t.Errorf("ReplayLag gauge = %f, want 42.5", v)
	}
}

// TestProjectionMetrics_RecordsRebuildDuration asserts observeRebuildDuration
// increments the histogram observation count.
func TestProjectionMetrics_RecordsRebuildDuration(t *testing.T) {
	t.Parallel()
	p := newProjectionRecordingProvider()
	m, _ := RegisterMetrics(p)
	ctx := context.Background()
	m.observeRebuildDuration(ctx, "mycell", "myproj", 1.2)
	m.observeRebuildDuration(ctx, "mycell", "myproj", 2.3)
	count := p.histogramCount(metricProjectionRebuildDuration, kernelmetrics.Labels{"cell": "mycell", "projection": "myproj"})
	if count != 2 {
		t.Errorf("RebuildDuration observe count = %d, want 2", count)
	}
}

// TestProjectionMetrics_RecordsPendingEvents asserts setPendingEvents writes the gauge.
func TestProjectionMetrics_RecordsPendingEvents(t *testing.T) {
	t.Parallel()
	p := newProjectionRecordingProvider()
	m, _ := RegisterMetrics(p)
	ctx := context.Background()
	m.setPendingEvents(ctx, "mycell", "myproj", 7.0)
	v := p.gaugeValue(metricProjectionPendingEvents, kernelmetrics.Labels{"cell": "mycell", "projection": "myproj"})
	if v != 7.0 {
		t.Errorf("PendingEvents gauge = %f, want 7.0", v)
	}
}

// ---------------------------------------------------------------------------
// recordingProvider — spy metrics.Provider for projection metrics tests.
// Cloned from kernel/reconcile/metrics_test.go recordingProvider.
// ---------------------------------------------------------------------------

type projRecordingProvider struct {
	mu         sync.Mutex
	histograms map[string]*projRecordingHistogramVec
	gauges     map[string]*projRecordingGaugeVec
}

func newProjectionRecordingProvider() *projRecordingProvider {
	return &projRecordingProvider{
		histograms: map[string]*projRecordingHistogramVec{},
		gauges:     map[string]*projRecordingGaugeVec{},
	}
}

func (p *projRecordingProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	// projection metrics has no CounterVec, return nop
	return kernelmetrics.NopProvider{}.CounterVec(opts)
}

func (p *projRecordingProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.histograms[opts.Name]; ok {
		return v, nil
	}
	v := &projRecordingHistogramVec{labels: append([]string(nil), opts.LabelNames...), obs: map[string]*atomic.Int64{}}
	p.histograms[opts.Name] = v
	return v, nil
}

func (p *projRecordingProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.gauges[opts.Name]; ok {
		return v, nil
	}
	v := &projRecordingGaugeVec{labels: append([]string(nil), opts.LabelNames...), vals: map[string]float64{}}
	p.gauges[opts.Name] = v
	return v, nil
}

func (p *projRecordingProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

func (p *projRecordingProvider) gaugeLabels(name string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.gauges[name]; ok {
		return v.labels
	}
	return nil
}

func (p *projRecordingProvider) histogramLabels(name string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.histograms[name]; ok {
		return v.labels
	}
	return nil
}

func (p *projRecordingProvider) histogramCount(name string, l kernelmetrics.Labels) int64 {
	p.mu.Lock()
	v, ok := p.histograms[name]
	p.mu.Unlock()
	if !ok {
		return 0
	}
	return v.count(l)
}

func (p *projRecordingProvider) gaugeValue(name string, l kernelmetrics.Labels) float64 {
	p.mu.Lock()
	v, ok := p.gauges[name]
	p.mu.Unlock()
	if !ok {
		return 0
	}
	return v.value(l)
}

type projRecordingHistogramVec struct {
	labels []string
	mu     sync.Mutex
	obs    map[string]*atomic.Int64
}

func (v *projRecordingHistogramVec) Registered() bool { return true }
func (v *projRecordingHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	kernelmetrics.MustValidateLabels(v.labels, l)
	v.mu.Lock()
	c, ok := v.obs[projLabelsKey(l)]
	if !ok {
		c = &atomic.Int64{}
		v.obs[projLabelsKey(l)] = c
	}
	v.mu.Unlock()
	return &projRecordingHistogram{n: c}
}

func (v *projRecordingHistogramVec) count(l kernelmetrics.Labels) int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if c, ok := v.obs[projLabelsKey(l)]; ok {
		return c.Load()
	}
	return 0
}

type projRecordingHistogram struct{ n *atomic.Int64 }

func (h *projRecordingHistogram) Observe(context.Context, float64) { h.n.Add(1) }

type projRecordingGaugeVec struct {
	labels []string
	mu     sync.Mutex
	vals   map[string]float64
}

func (v *projRecordingGaugeVec) Registered() bool { return true }
func (v *projRecordingGaugeVec) With(l kernelmetrics.Labels) kernelmetrics.Gauge {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return &projRecordingGauge{vec: v, key: projLabelsKey(l)}
}

func (v *projRecordingGaugeVec) value(l kernelmetrics.Labels) float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.vals[projLabelsKey(l)]
}

type projRecordingGauge struct {
	vec *projRecordingGaugeVec
	key string
}

func (g *projRecordingGauge) Set(_ context.Context, val float64) {
	g.vec.mu.Lock()
	g.vec.vals[g.key] = val
	g.vec.mu.Unlock()
}
func (g *projRecordingGauge) Inc(ctx context.Context) { g.Add(ctx, 1) }
func (g *projRecordingGauge) Dec(ctx context.Context) { g.Add(ctx, -1) }
func (g *projRecordingGauge) Add(_ context.Context, d float64) {
	g.vec.mu.Lock()
	g.vec.vals[g.key] += d
	g.vec.mu.Unlock()
}

func projLabelsKey(l kernelmetrics.Labels) string {
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

// labelsMatch checks two label slices have the same sorted elements.
func labelsMatch(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	sortedGot := append([]string(nil), got...)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedGot)
	sort.Strings(sortedWant)
	for i := range sortedGot {
		if sortedGot[i] != sortedWant[i] {
			return false
		}
	}
	return true
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
