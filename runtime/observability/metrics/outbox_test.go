package metrics_test

import (
	"errors"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// ---------------------------------------------------------------------------
// OutboxConsumerCollector tests
// ---------------------------------------------------------------------------

func TestNewOutboxConsumerCollector_RegistersMetrics(t *testing.T) {
	c, err := obmetrics.NewOutboxConsumerCollector(kernelmetrics.NopProvider{}, "accesscore")
	if err != nil {
		t.Fatalf("NewOutboxConsumerCollector: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil collector")
	}
}

func TestNewOutboxConsumerCollector_RejectsEmptyCellID(t *testing.T) {
	_, err := obmetrics.NewOutboxConsumerCollector(kernelmetrics.NopProvider{}, "")
	if err == nil {
		t.Fatal("expected error for empty cellID, got nil")
	}
}

func TestNewOutboxConsumerCollector_RejectsNilProvider(t *testing.T) {
	_, err := obmetrics.NewOutboxConsumerCollector(nil, "accesscore")
	if err == nil {
		t.Fatal("expected error for nil provider, got nil")
	}
}

func TestOutboxConsumerCollector_ObserveReject_IncrementsCounterWithLabels(t *testing.T) {
	p := newOutboxSpyProvider()
	c, err := obmetrics.NewOutboxConsumerCollector(p, "accesscore")
	if err != nil {
		t.Fatalf("NewOutboxConsumerCollector: %v", err)
	}

	c.ObserveReject("accesscore", "event.session.created.v1", "cg-accesscore-session", "handler_reject")

	ops := p.counterOps["outbox_consumer_rejected_total"]
	if len(ops) != 1 {
		t.Fatalf("want 1 counter op, got %d", len(ops))
	}
	got := ops[0].labels
	wants := map[string]string{
		"cell":   "accesscore",
		"topic":  "event.session.created.v1",
		"reason": "handler_reject",
	}
	for k, v := range wants {
		if got[k] != v {
			t.Errorf("label %s = %q, want %q (all=%v)", k, got[k], v, got)
		}
	}
	// consumerGroup must NOT appear as a label (kept out of label set to bound cardinality)
	if _, ok := got["consumerGroup"]; ok {
		t.Error("consumerGroup must not be a label on outbox_consumer_rejected_total")
	}
}

func TestOutboxConsumerCollector_ObserveReject_NilReceiverDoesNotPanic(t *testing.T) {
	var c *obmetrics.OutboxConsumerCollector
	// must not panic
	c.ObserveReject("accesscore", "event.session.created.v1", "cg", "handler_reject")
}

func TestOutboxConsumerCollector_ObservePendingDepth_SetsGauge(t *testing.T) {
	p := newOutboxSpyProvider()
	c, err := obmetrics.NewOutboxConsumerCollector(p, "accesscore")
	if err != nil {
		t.Fatalf("NewOutboxConsumerCollector: %v", err)
	}

	c.ObservePendingDepth(42)

	ops := p.gaugeOps["outbox_pending_depth"]
	if len(ops) != 1 {
		t.Fatalf("want 1 gauge op, got %d", len(ops))
	}
	if ops[0].value != 42 {
		t.Errorf("gauge value = %v, want 42", ops[0].value)
	}
	if ops[0].labels["cell"] != "accesscore" {
		t.Errorf("cell label = %q, want %q", ops[0].labels["cell"], "accesscore")
	}
}

func TestOutboxConsumerCollector_ObservePendingDepth_NilReceiverDoesNotPanic(t *testing.T) {
	var c *obmetrics.OutboxConsumerCollector
	c.ObservePendingDepth(7)
}

func TestNewOutboxConsumerCollector_RollbackOnPartialFailure(t *testing.T) {
	// failAfterFirstGaugeProvider succeeds the CounterVec registration but
	// fails the GaugeVec registration so we can assert that the first
	// registration is rolled back (Unregister called).
	p := &outboxPartialFailProvider{}
	_, err := obmetrics.NewOutboxConsumerCollector(p, "accesscore")
	if err == nil {
		t.Fatal("expected error from partial failure, got nil")
	}
	if !p.unregisterCalled {
		t.Error("expected Unregister to be called on rollback, but it was not")
	}
}

// ---------------------------------------------------------------------------
// outboxSpyProvider — tracks counter + gauge emissions for label assertions.
// Independent of the spyProvider in provider_collector_test.go to avoid
// coupling through the shared struct.
// ---------------------------------------------------------------------------

type outboxSpyRecord struct {
	labels kernelmetrics.Labels
	value  float64
}

type outboxSpyProvider struct {
	counterOps map[string][]outboxSpyRecord
	gaugeOps   map[string][]outboxSpyRecord
}

func newOutboxSpyProvider() *outboxSpyProvider {
	return &outboxSpyProvider{
		counterOps: map[string][]outboxSpyRecord{},
		gaugeOps:   map[string][]outboxSpyRecord{},
	}
}

func (p *outboxSpyProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return &outboxSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *outboxSpyProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return kernelmetrics.NopProvider{}.HistogramVec(opts)
}

func (p *outboxSpyProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return &outboxSpyGaugeVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *outboxSpyProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

type outboxSpyCounterVec struct {
	parent     *outboxSpyProvider
	name       string
	labelNames []string
}

func (v *outboxSpyCounterVec) Registered() bool { return true }
func (v *outboxSpyCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &outboxSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type outboxSpyGaugeVec struct {
	parent     *outboxSpyProvider
	name       string
	labelNames []string
}

func (v *outboxSpyGaugeVec) Registered() bool { return true }
func (v *outboxSpyGaugeVec) With(l kernelmetrics.Labels) kernelmetrics.Gauge {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &outboxSpyGauge{parent: v.parent, name: v.name, labels: l}
}

type outboxSpyCounter struct {
	parent *outboxSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (c *outboxSpyCounter) Inc() { c.Add(1) }
func (c *outboxSpyCounter) Add(d float64) {
	c.parent.counterOps[c.name] = append(c.parent.counterOps[c.name], outboxSpyRecord{labels: c.labels, value: d})
}

type outboxSpyGauge struct {
	parent *outboxSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (g *outboxSpyGauge) Set(v float64) {
	g.parent.gaugeOps[g.name] = append(g.parent.gaugeOps[g.name], outboxSpyRecord{labels: g.labels, value: v})
}
func (g *outboxSpyGauge) Inc()          { g.Add(1) }
func (g *outboxSpyGauge) Dec()          { g.Add(-1) }
func (g *outboxSpyGauge) Add(d float64) { g.Set(d) }

// ---------------------------------------------------------------------------
// outboxPartialFailProvider: succeeds CounterVec, fails GaugeVec, tracks Unregister.
// ---------------------------------------------------------------------------

type outboxPartialFailProvider struct {
	kernelmetrics.NopProvider
	unregisterCalled bool
}

func (p *outboxPartialFailProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return &outboxSpyCounterVec{parent: newOutboxSpyProvider(), name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *outboxPartialFailProvider) GaugeVec(_ kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return nil, errors.New("gauge registration failed")
}

func (p *outboxPartialFailProvider) Unregister(_ kernelmetrics.Collector) error {
	p.unregisterCalled = true
	return nil
}
