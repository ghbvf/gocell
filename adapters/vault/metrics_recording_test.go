package vault

// metrics_recording_test.go — adapter-local in-memory metrics.Provider for vault
// unit tests. Records counter/gauge values so tests assert recorded values without
// importing adapters/prometheus (#1909). Embeds NopProvider for the
// HistogramVec/Unregister methods vault never records through. Vecs are stateless
// handles over a shared sample store keyed by fully-qualified name + sorted-label
// blob, so a 2nd NewTransitMetrics on the SAME provider reuses the same sample
// keys (register-once parity) — counters keep accumulating and gauges retain their
// last value across a composition rebuild (regression lock for #879/#885).

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
)

// recordingProvider is an in-memory, concurrency-safe metrics.Provider for vault
// unit tests. It records counter/gauge values so tests assert recorded values
// without importing adapters/prometheus (#1909). It embeds NopProvider for the
// HistogramVec/Unregister methods vault never records through. Vecs are stateless
// handles over a shared sample store keyed by fully-qualified name + sorted-label
// blob, so a 2nd NewTransitMetrics on the SAME provider reuses the same sample
// keys (register-once parity) — counters keep accumulating and gauges retain
// their last value across a composition rebuild (regression lock for #879/#885).
type recordingProvider struct {
	metrics.NopProvider
	namespace string
	mu        sync.Mutex
	samples   map[string]float64
	families  map[string]bool
}

var _ metrics.Provider = (*recordingProvider)(nil)

func newRecordingProvider(namespace string) *recordingProvider {
	return &recordingProvider{namespace: namespace, samples: map[string]float64{}, families: map[string]bool{}}
}

func (p *recordingProvider) full(name string) string {
	if p.namespace == "" {
		return name
	}
	return p.namespace + "_" + name
}

func (p *recordingProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	full := p.full(opts.Name)
	p.mu.Lock()
	p.families[full] = true
	p.mu.Unlock()
	return recCounterVec{p: p, full: full, labels: append([]string(nil), opts.LabelNames...)}, nil
}

func (p *recordingProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	full := p.full(opts.Name)
	p.mu.Lock()
	p.families[full] = true
	p.mu.Unlock()
	return recGaugeVec{p: p, full: full, labels: append([]string(nil), opts.LabelNames...)}, nil
}

// ensure materializes the sample for (full,labels) at 0 if absent — mirrors a
// Prometheus child created by .With before any write, so a registered-but-unwritten
// metric reads 0 rather than "absent".
func (p *recordingProvider) ensure(full string, l metrics.Labels) string {
	key := sampleKey(full, l)
	p.mu.Lock()
	if _, ok := p.samples[key]; !ok {
		p.samples[key] = 0
	}
	p.mu.Unlock()
	return key
}

func (p *recordingProvider) add(key string, d float64) {
	p.mu.Lock()
	p.samples[key] += d
	p.mu.Unlock()
}

func (p *recordingProvider) set(key string, v float64) {
	p.mu.Lock()
	p.samples[key] = v
	p.mu.Unlock()
}

// value reports the recorded sample for the fully-qualified name + labels.
func (p *recordingProvider) value(full string, l map[string]string) (float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.samples[sampleKey(full, metrics.Labels(l))]
	return v, ok
}

// registered reports whether a vec with the fully-qualified name was registered.
func (p *recordingProvider) registered(full string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.families[full]
}

func sampleKey(full string, l metrics.Labels) string {
	if len(l) == 0 {
		return full
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(full)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(l[k])
	}
	return b.String()
}

type recCounterVec struct {
	p      *recordingProvider
	full   string
	labels []string
}

func (recCounterVec) Registered() bool { return true }
func (v recCounterVec) With(l metrics.Labels) metrics.Counter {
	// Enforce the same label-set contract as every real Provider (Nop/Prometheus/
	// OTel all call MustValidateLabels in With), so the fake cannot silently pass a
	// test whose labels drifted from the registered LabelNames.
	metrics.MustValidateLabels(v.labels, l)
	return recPoint{p: v.p, key: v.p.ensure(v.full, l)}
}

type recGaugeVec struct {
	p      *recordingProvider
	full   string
	labels []string
}

func (recGaugeVec) Registered() bool { return true }
func (v recGaugeVec) With(l metrics.Labels) metrics.Gauge {
	metrics.MustValidateLabels(v.labels, l)
	return recPoint{p: v.p, key: v.p.ensure(v.full, l)}
}

// recPoint satisfies both metrics.Counter and metrics.Gauge over one sample key.
type recPoint struct {
	p   *recordingProvider
	key string
}

func (c recPoint) Inc(ctx context.Context) { c.Add(ctx, 1) }

// Dec satisfies metrics.Gauge; metrics.Counter has no Dec, so it is unreachable on the counter path.
func (c recPoint) Dec(ctx context.Context)          { c.Add(ctx, -1) }
func (c recPoint) Add(_ context.Context, d float64) { c.p.add(c.key, d) }
func (c recPoint) Set(_ context.Context, v float64) { c.p.set(c.key, v) }
