package metrics_test

import (
	"errors"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func TestProviderCollector_RejectsMissingDeps(t *testing.T) {
	if _, err := metrics.NewProviderCollector(nil, metrics.ProviderCollectorConfig{CellID: "x"}); err == nil {
		t.Fatal("nil Provider must be rejected")
	}
	if _, err := metrics.NewProviderCollector(kernelmetrics.NopProvider{}, metrics.ProviderCollectorConfig{}); err == nil {
		t.Fatal("empty CellID must be rejected")
	}
}

func TestProviderCollector_NopProviderNoPanic(t *testing.T) {
	c, err := metrics.NewProviderCollector(kernelmetrics.NopProvider{}, metrics.ProviderCollectorConfig{CellID: "dev"})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	// Recording through the Nop provider must not panic.
	c.RecordRequest("GET", "/api/v1/users", 200, 0.05)
	c.RecordRequest("POST", "/api/v1/users", 201, 0.12)
}

func TestProviderCollector_EmitsExpectedLabels(t *testing.T) {
	p := newSpyProvider()
	c, err := metrics.NewProviderCollector(p, metrics.ProviderCollectorConfig{CellID: "access-core"})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	c.RecordRequest("GET", "/api/v1/sessions", 200, 0.01)

	ops := p.counterOps["http_requests_total"]
	if len(ops) != 1 {
		t.Fatalf("want 1 counter op, got %d", len(ops))
	}
	got := ops[0].labels
	wants := map[string]string{
		"method": "GET",
		"route":  "/api/v1/sessions",
		"status": "200",
		"cell":   "access-core",
	}
	for k, v := range wants {
		if got[k] != v {
			t.Errorf("label %s = %q, want %q (all=%v)", k, got[k], v, got)
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

type spyCounterVec struct {
	parent *spyProvider
	name   string
	labels []string
}

func (v spyCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return spyCounter{parent: v.parent, name: v.name, labels: l}
}

type spyHistogramVec struct {
	parent *spyProvider
	name   string
	labels []string
}

func (v spyHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return spyHistogram{parent: v.parent, name: v.name, labels: l}
}

type spyCounter struct {
	parent *spyProvider
	name   string
	labels kernelmetrics.Labels
}

func (c spyCounter) Inc()            { c.Add(1) }
func (c spyCounter) Add(d float64)   { c.parent.counterOps[c.name] = append(c.parent.counterOps[c.name], spyOp{labels: c.labels, value: d}) }

type spyHistogram struct {
	parent *spyProvider
	name   string
	labels kernelmetrics.Labels
}

func (h spyHistogram) Observe(v float64) {
	h.parent.histogramOps[h.name] = append(h.parent.histogramOps[h.name], spyOp{labels: h.labels, value: v})
}

// silence unused import if toolchain introduces new helpers during refactors
var _ = errors.New
