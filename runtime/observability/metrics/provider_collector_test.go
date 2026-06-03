package metrics_test

import (
	"context"
	"errors"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/metrics/metricstest"
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
	ctx := context.Background()
	c.RecordRequest(ctx, metricstest.Label("dev"), "GET", "/api/v1/users", 200, 0.05)
	c.RecordRequest(ctx, metricstest.Label("dev"), "POST", "/api/v1/users", 201, 0.12)
}

func TestProviderCollector_EmitsCellLabelFromArg(t *testing.T) {
	p := newSpyProvider()
	c, err := metrics.NewProviderCollector(p, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	c.RecordRequest(context.Background(), metricstest.Label("accesscore"), "GET", "/api/v1/sessions", 200, 0.01)

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

// ctxKey is a private type for the sentinel ctx value used to prove the middle
// layer forwards the caller's ctx (not context.Background()) to the instruments.
type ctxKey struct{}

// TestProviderCollector_ForwardsCallerCtx asserts RecordRequest threads its ctx
// argument into both the counter (Inc) and histogram (Observe) calls — F5: the
// provider_collector middle layer must not substitute context.Background(),
// otherwise OTel exemplar/baggage correlation is silently lost.
func TestProviderCollector_ForwardsCallerCtx(t *testing.T) {
	p := newSpyProvider()
	c, err := metrics.NewProviderCollector(p, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}

	want := "sentinel-ctx-value"
	ctx := context.WithValue(context.Background(), ctxKey{}, want)
	c.RecordRequest(ctx, metricstest.Label("accesscore"), "GET", "/api/v1/sessions", 200, 0.01)

	assertForwardedCtx := func(label string, ops []spyOp) {
		if len(ops) != 1 {
			t.Fatalf("%s: want 1 op, got %d", label, len(ops))
		}
		if ops[0].ctx == nil {
			t.Fatalf("%s: forwarded ctx is nil (middle layer dropped it)", label)
		}
		if got, _ := ops[0].ctx.Value(ctxKey{}).(string); got != want {
			t.Errorf("%s: forwarded ctx value = %q, want %q "+
				"(provider_collector substituted a different ctx — exemplar/baggage lost)", label, got, want)
		}
	}
	assertForwardedCtx("http_requests_total", p.counterOps["http_requests_total"])
	assertForwardedCtx("http_request_duration_seconds", p.histogramOps["http_request_duration_seconds"])
}

func TestProviderCollector_RecordBodyLimitRejection_NopProviderNoPanic(t *testing.T) {
	c, err := metrics.NewProviderCollector(kernelmetrics.NopProvider{}, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	// Must not panic.
	ctx := context.Background()
	c.RecordBodyLimitRejection(ctx, metricstest.Label("accesscore"), "/api/v1/upload")
}

func TestProviderCollector_RecordBodyLimitRejection_EmitsLabels(t *testing.T) {
	p := newSpyProvider()
	c, err := metrics.NewProviderCollector(p, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	c.RecordBodyLimitRejection(context.Background(), metricstest.Label("configcore"), "/api/v1/config")

	ops := p.counterOps["http_request_body_limit_rejections_total"]
	if len(ops) != 1 {
		t.Fatalf("want 1 body-limit counter op, got %d", len(ops))
	}
	got := ops[0].labels
	wants := map[string]string{
		"cell":  "configcore",
		"route": "/api/v1/config",
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
	ctx := context.Background()
	c.RecordRequest(ctx, metricstest.Label("accesscore"), "GET", "/api/v1/sessions", 200, 0.01)
	c.RecordRequest(ctx, metricstest.Label("auditcore"), "GET", "/api/v1/audit", 200, 0.02)
	c.RecordRequest(ctx, metricstest.RuntimeLabel(), "GET", "/healthz", 200, 0.001)

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

// spyProvider captures Counter/Histogram/Gauge emissions for label-set
// assertions. Defined local to this test file to keep
// runtime/observability/metrics free of cross-test dependencies (the package
// does not export spy types).
type spyProvider struct {
	counterOps   map[string][]spyOp
	histogramOps map[string][]spyOp
	gaugeOps     map[string][]spyOp
}

type spyOp struct {
	labels kernelmetrics.Labels
	value  float64
	ctx    context.Context //nolint:containedctx // test spy captures the ctx the middle layer forwarded, to assert ctx passthrough (F5)
}

func newSpyProvider() *spyProvider {
	return &spyProvider{
		counterOps:   map[string][]spyOp{},
		histogramOps: map[string][]spyOp{},
		gaugeOps:     map[string][]spyOp{},
	}
}

func (s *spyProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return spyCounterVec{parent: s, name: opts.Name, labels: opts.LabelNames}, nil
}

func (s *spyProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return spyHistogramVec{parent: s, name: opts.Name, labels: opts.LabelNames}, nil
}

func (s *spyProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return spyGaugeVec{parent: s, name: opts.Name, labels: opts.LabelNames}, nil
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

func (c spyCounter) Inc(ctx context.Context) { c.Add(ctx, 1) }
func (c spyCounter) Add(ctx context.Context, d float64) {
	c.parent.counterOps[c.name] = append(c.parent.counterOps[c.name], spyOp{labels: c.labels, value: d, ctx: ctx})
}

type spyHistogram struct {
	parent *spyProvider
	name   string
	labels kernelmetrics.Labels
}

func (h spyHistogram) Observe(ctx context.Context, v float64) {
	h.parent.histogramOps[h.name] = append(h.parent.histogramOps[h.name], spyOp{labels: h.labels, value: v, ctx: ctx})
}

type spyGaugeVec struct {
	parent *spyProvider
	name   string
	labels []string
}

func (v spyGaugeVec) Registered() bool { return true }
func (v spyGaugeVec) With(l kernelmetrics.Labels) kernelmetrics.Gauge {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return spyGauge{parent: v.parent, name: v.name, labels: l}
}

type spyGauge struct {
	parent *spyProvider
	name   string
	labels kernelmetrics.Labels
}

func (g spyGauge) Set(ctx context.Context, v float64) {
	g.parent.gaugeOps[g.name] = append(g.parent.gaugeOps[g.name], spyOp{labels: g.labels, value: v, ctx: ctx})
}
func (g spyGauge) Inc(ctx context.Context) { g.Add(ctx, 1) }
func (g spyGauge) Dec(ctx context.Context) { g.Add(ctx, -1) }
func (g spyGauge) Add(ctx context.Context, d float64) {
	g.parent.gaugeOps[g.name] = append(g.parent.gaugeOps[g.name], spyOp{labels: g.labels, value: d, ctx: ctx})
}

// silence unused import if toolchain introduces new helpers during refactors.
var _ = errors.New
