package metrics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// testReadyWaitDuration is a representative ready-wait observation used by
// TestEventRouterCollector_ObserveReadyWait. The exact value is incidental
// (a non-zero duration that's neither at zero nor at the histogram tail).
const testReadyWaitDuration = 75 * time.Millisecond

// ---------------------------------------------------------------------------
// EventRouterCollector tests
// ---------------------------------------------------------------------------

func TestNewEventRouterCollector_RegistersMetrics(t *testing.T) {
	c, err := obmetrics.NewEventRouterCollector(kernelmetrics.NopProvider{})
	if err != nil {
		t.Fatalf("NewEventRouterCollector: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil collector")
	}
}

func TestNewEventRouterCollector_RejectsNilProvider(t *testing.T) {
	_, err := obmetrics.NewEventRouterCollector(nil)
	if err == nil {
		t.Fatal("expected error for nil provider, got nil")
	}
}

func TestEventRouterCollector_IncSubscriptionActive_EmitsGaugeLabel(t *testing.T) {
	p := newEventSpyProvider()
	c, err := obmetrics.NewEventRouterCollector(p)
	if err != nil {
		t.Fatalf("NewEventRouterCollector: %v", err)
	}

	c.IncSubscriptionActive(context.Background(), "accesscore")

	ops := p.gaugeOps["event_router_subscriptions_active"]
	if len(ops) != 1 {
		t.Fatalf("want 1 gauge op, got %d", len(ops))
	}
	if ops[0].labels["cell"] != "accesscore" {
		t.Errorf("cell label = %q, want %q", ops[0].labels["cell"], "accesscore")
	}
}

func TestEventRouterCollector_DecSubscriptionActive_EmitsGaugeLabel(t *testing.T) {
	p := newEventSpyProvider()
	c, err := obmetrics.NewEventRouterCollector(p)
	if err != nil {
		t.Fatalf("NewEventRouterCollector: %v", err)
	}

	c.DecSubscriptionActive(context.Background(), "configcore")

	ops := p.gaugeOps["event_router_subscriptions_active"]
	if len(ops) != 1 {
		t.Fatalf("want 1 gauge op, got %d", len(ops))
	}
	if ops[0].labels["cell"] != "configcore" {
		t.Errorf("cell label = %q, want %q", ops[0].labels["cell"], "configcore")
	}
}

func TestEventRouterCollector_RecordSetupError_EmitsCounterLabels(t *testing.T) {
	p := newEventSpyProvider()
	c, err := obmetrics.NewEventRouterCollector(p)
	if err != nil {
		t.Fatalf("NewEventRouterCollector: %v", err)
	}

	c.RecordSetupError(context.Background(), "auditcore", "event.audit.appended.v1", "dial_timeout")

	ops := p.counterOps["event_router_setup_errors_total"]
	if len(ops) != 1 {
		t.Fatalf("want 1 counter op, got %d", len(ops))
	}
	wants := map[string]string{
		"cell":   "auditcore",
		"topic":  "event.audit.appended.v1",
		"reason": "dial_timeout",
	}
	for k, v := range wants {
		if ops[0].labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, ops[0].labels[k], v)
		}
	}
}

func TestEventRouterCollector_ObserveReadyWait_EmitsHistogramLabel(t *testing.T) {
	p := newEventSpyProvider()
	c, err := obmetrics.NewEventRouterCollector(p)
	if err != nil {
		t.Fatalf("NewEventRouterCollector: %v", err)
	}

	c.ObserveReadyWait(context.Background(), "accesscore", testReadyWaitDuration)

	ops := p.histogramOps["event_router_ready_wait_seconds"]
	if len(ops) != 1 {
		t.Fatalf("want 1 histogram op, got %d", len(ops))
	}
	if ops[0].labels["cell"] != "accesscore" {
		t.Errorf("cell label = %q, want %q", ops[0].labels["cell"], "accesscore")
	}
	const want = 0.075
	const epsilon = 1e-9
	if ops[0].value < want-epsilon || ops[0].value > want+epsilon {
		t.Errorf("histogram value = %v, want ~%v", ops[0].value, want)
	}
}

func TestEventRouterCollector_NilReceiverDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	var c *obmetrics.EventRouterCollector
	c.IncSubscriptionActive(ctx, "cell")
	c.DecSubscriptionActive(ctx, "cell")
	c.RecordSetupError(ctx, "cell", "topic", "reason")
	c.ObserveReadyWait(ctx, "cell", time.Second)
}

func TestNewEventRouterCollector_RollbackOnPartialFailure(t *testing.T) {
	// failAfterFirstEventProvider succeeds GaugeVec registration, fails on
	// CounterVec, so the first Gauge registration must be rolled back.
	p := &eventPartialFailProvider{failOnCounter: true}
	_, err := obmetrics.NewEventRouterCollector(p)
	if err == nil {
		t.Fatal("expected error from partial failure, got nil")
	}
	if !p.unregisterCalled {
		t.Error("expected Unregister to be called on rollback, but it was not")
	}
}

func TestNewEventRouterCollector_RollbackOnHistogramFailure(t *testing.T) {
	// failAfterTwoEventProvider succeeds GaugeVec + CounterVec but fails on
	// HistogramVec — both prior registrations must be rolled back.
	p := &eventPartialFailProvider{failOnHistogram: true}
	_, err := obmetrics.NewEventRouterCollector(p)
	if err == nil {
		t.Fatal("expected error from histogram failure, got nil")
	}
	if p.unregisterCount < 2 {
		t.Errorf("expected at least 2 Unregister calls on rollback, got %d", p.unregisterCount)
	}
}

// ---------------------------------------------------------------------------
// eventSpyProvider — tracks counter + gauge + histogram emissions for label
// assertions. Fully independent to avoid coupling with the shared spyProvider.
// ---------------------------------------------------------------------------

type eventSpyRecord struct {
	labels kernelmetrics.Labels
	value  float64
}

type eventSpyProvider struct {
	counterOps   map[string][]eventSpyRecord
	histogramOps map[string][]eventSpyRecord
	gaugeOps     map[string][]eventSpyRecord
}

func newEventSpyProvider() *eventSpyProvider {
	return &eventSpyProvider{
		counterOps:   map[string][]eventSpyRecord{},
		histogramOps: map[string][]eventSpyRecord{},
		gaugeOps:     map[string][]eventSpyRecord{},
	}
}

func (p *eventSpyProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return &eventSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *eventSpyProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return &eventSpyHistogramVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *eventSpyProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return &eventSpyGaugeVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *eventSpyProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

type eventSpyCounterVec struct {
	parent     *eventSpyProvider
	name       string
	labelNames []string
}

func (v *eventSpyCounterVec) Registered() bool { return true }
func (v *eventSpyCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &eventSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type eventSpyHistogramVec struct {
	parent     *eventSpyProvider
	name       string
	labelNames []string
}

func (v *eventSpyHistogramVec) Registered() bool { return true }
func (v *eventSpyHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &eventSpyHistogram{parent: v.parent, name: v.name, labels: l}
}

type eventSpyGaugeVec struct {
	parent     *eventSpyProvider
	name       string
	labelNames []string
}

func (v *eventSpyGaugeVec) Registered() bool { return true }
func (v *eventSpyGaugeVec) With(l kernelmetrics.Labels) kernelmetrics.Gauge {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &eventSpyGauge{parent: v.parent, name: v.name, labels: l}
}

type eventSpyCounter struct {
	parent *eventSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (c *eventSpyCounter) Inc(ctx context.Context) { c.Add(ctx, 1) }
func (c *eventSpyCounter) Add(_ context.Context, d float64) {
	c.parent.counterOps[c.name] = append(c.parent.counterOps[c.name], eventSpyRecord{labels: c.labels, value: d})
}

type eventSpyHistogram struct {
	parent *eventSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (h *eventSpyHistogram) Observe(_ context.Context, v float64) {
	h.parent.histogramOps[h.name] = append(h.parent.histogramOps[h.name], eventSpyRecord{labels: h.labels, value: v})
}

type eventSpyGauge struct {
	parent *eventSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (g *eventSpyGauge) Set(_ context.Context, v float64) {
	g.parent.gaugeOps[g.name] = append(g.parent.gaugeOps[g.name], eventSpyRecord{labels: g.labels, value: v})
}
func (g *eventSpyGauge) Inc(ctx context.Context)            { g.Add(ctx, 1) }
func (g *eventSpyGauge) Dec(ctx context.Context)            { g.Add(ctx, -1) }
func (g *eventSpyGauge) Add(ctx context.Context, d float64) { g.Set(ctx, d) }

// ---------------------------------------------------------------------------
// eventPartialFailProvider: configurable partial failure for rollback tests.
// ---------------------------------------------------------------------------

type eventPartialFailProvider struct {
	kernelmetrics.NopProvider
	failOnCounter    bool
	failOnHistogram  bool
	unregisterCalled bool
	unregisterCount  int
}

func (p *eventPartialFailProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	nop, err := p.NopProvider.GaugeVec(opts)
	return nop, err
}

func (p *eventPartialFailProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	if p.failOnCounter {
		return nil, errors.New("counter registration failed")
	}
	return p.NopProvider.CounterVec(opts)
}

func (p *eventPartialFailProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	if p.failOnHistogram {
		return nil, errors.New("histogram registration failed")
	}
	return p.NopProvider.HistogramVec(opts)
}

func (p *eventPartialFailProvider) Unregister(_ kernelmetrics.Collector) error {
	p.unregisterCalled = true
	p.unregisterCount++
	return nil
}
