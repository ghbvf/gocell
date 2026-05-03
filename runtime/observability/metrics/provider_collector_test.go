package metrics_test

import (
	"errors"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func TestProviderCollector_RejectsNilProvider(t *testing.T) {
	if _, err := metrics.NewProviderCollector(nil, metrics.ProviderCollectorConfig{}); err == nil {
		t.Fatal("nil Provider must be rejected")
	}
}

func TestProviderCollector_NopProviderNoPanic(t *testing.T) {
	c, err := metrics.NewProviderCollector(kernelmetrics.NopProvider{}, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	// Recording through the Nop provider must not panic.
	c.RecordRequest("dev", "GET", "/api/v1/users", 200, 0.05)
	c.RecordRequest("dev", "POST", "/api/v1/users", 201, 0.12)
}

func TestProviderCollector_EmitsCellLabelFromArg(t *testing.T) {
	p := newSpyProvider()
	c, err := metrics.NewProviderCollector(p, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	c.RecordRequest("accesscore", "GET", "/api/v1/sessions", 200, 0.01)

	ops := p.counterOps["http_requests_total"]
	if len(ops) != 1 {
		t.Fatalf("want 1 counter op, got %d", len(ops))
	}
	got := ops[0].labels
	wants := map[string]string{
		"method": "GET",
		"route":  "/api/v1/sessions",
		"status": "200",
		"cell":   "accesscore",
	}
	for k, v := range wants {
		if got[k] != v {
			t.Errorf("label %s = %q, want %q (all=%v)", k, got[k], v, got)
		}
	}
}

func TestProviderCollector_PerCallCellLabel(t *testing.T) {
	// Two calls with different cellID values must yield two distinct label sets;
	// no global / cached cellID can leak between calls.
	p := newSpyProvider()
	c, err := metrics.NewProviderCollector(p, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	c.RecordRequest("accesscore", "GET", "/api/v1/sessions", 200, 0.01)
	c.RecordRequest("auditcore", "GET", "/api/v1/audit", 200, 0.02)
	c.RecordRequest("_runtime", "GET", "/healthz", 200, 0.001)

	ops := p.counterOps["http_requests_total"]
	if len(ops) != 3 {
		t.Fatalf("want 3 counter ops, got %d", len(ops))
	}
	cells := map[string]bool{}
	for _, op := range ops {
		cells[op.labels["cell"]] = true
	}
	for _, want := range []string{"accesscore", "auditcore", "_runtime"} {
		if !cells[want] {
			t.Errorf("missing cell label %q in %v", want, cells)
		}
	}
}

// spyProvider captures Counter/Histogram emissions for label-set assertions.
// Defined local to this test file to keep runtime/observability/metrics free
// of cross-test dependencies (the package does not export spy types).
type spyProvider struct {
	counterOps   map[string][]spyOp
	histogramOps map[string][]spyOp
}

type spyOp struct {
	labels kernelmetrics.Labels
	value  float64
}

func newSpyProvider() *spyProvider {
	return &spyProvider{
		counterOps:   map[string][]spyOp{},
		histogramOps: map[string][]spyOp{},
	}
}

func (s *spyProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return spyCounterVec{parent: s, name: opts.Name, labels: opts.LabelNames}, nil
}

func (s *spyProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return spyHistogramVec{parent: s, name: opts.Name, labels: opts.LabelNames}, nil
}

func (s *spyProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

type spyCounterVec struct {
	parent *spyProvider
	name   string
	labels []string
}

func (v spyCounterVec) Registered() bool { return true }
func (v spyCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return spyCounter{parent: v.parent, name: v.name, labels: l}
}

type spyHistogramVec struct {
	parent *spyProvider
	name   string
	labels []string
}

func (v spyHistogramVec) Registered() bool { return true }
func (v spyHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return spyHistogram{parent: v.parent, name: v.name, labels: l}
}

type spyCounter struct {
	parent *spyProvider
	name   string
	labels kernelmetrics.Labels
}

func (c spyCounter) Inc() { c.Add(1) }
func (c spyCounter) Add(d float64) {
	c.parent.counterOps[c.name] = append(c.parent.counterOps[c.name], spyOp{labels: c.labels, value: d})
}

type spyHistogram struct {
	parent *spyProvider
	name   string
	labels kernelmetrics.Labels
}

func (h spyHistogram) Observe(v float64) {
	h.parent.histogramOps[h.name] = append(h.parent.histogramOps[h.name], spyOp{labels: h.labels, value: v})
}

// silence unused import if toolchain introduces new helpers during refactors.
var _ = errors.New
