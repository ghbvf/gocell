package metrics_test

// Wave 1 RED tests for P1#2/#3 — collector split.
//
// These tests assert the TARGET semantics after the split:
//   - OutboxRejectCollector: only outbox_consumer_rejected_total, no cellID ctor arg
//   - OutboxPendingDepthCollector: only outbox_pending_depth, cellID required
//
// All tests in this file FAIL against the current production code because
// OutboxRejectCollector / OutboxPendingDepthCollector do not exist yet (stubs
// added in outbox_stubs.go to allow compilation). When Wave 2 replaces the stubs
// with real implementations, these tests turn GREEN.

import (
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
)

// ---------------------------------------------------------------------------
// Compile-time interface checks
// ---------------------------------------------------------------------------

// OutboxRejectCollector must implement outbox.ConsumerObserver.
var _ outbox.ConsumerObserver = (*obmetrics.OutboxRejectCollector)(nil)

// OutboxPendingDepthCollector must implement runtimeoutbox.PendingDepthObserver.
var _ runtimeoutbox.PendingDepthObserver = (*obmetrics.OutboxPendingDepthCollector)(nil)

// ---------------------------------------------------------------------------
// OutboxRejectCollector tests
// ---------------------------------------------------------------------------

// TestNewOutboxRejectCollector_ConstructorTakesNoCell asserts that
// NewOutboxRejectCollector takes ONLY a Provider (no cellID argument).
// The reject counter is shared across all cells; cell label comes from
// ObserveReject call-site args.
func TestNewOutboxRejectCollector_ConstructorTakesNoCell(t *testing.T) {
	c, err := obmetrics.NewOutboxRejectCollector(kernelmetrics.NopProvider{})
	if err != nil {
		t.Fatalf("NewOutboxRejectCollector: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil OutboxRejectCollector")
	}
}

// TestNewOutboxRejectCollector_RejectsNilProvider verifies fail-fast on nil.
func TestNewOutboxRejectCollector_RejectsNilProvider(t *testing.T) {
	_, err := obmetrics.NewOutboxRejectCollector(nil)
	if err == nil {
		t.Fatal("expected error for nil provider, got nil")
	}
}

// TestOutboxRejectCollector_RegistersOnlyRejectedCounter asserts that the
// collector registers outbox_consumer_rejected_total but NOT outbox_pending_depth.
//
// Wave 1 RED: the current stub wraps OutboxConsumerCollector which registers
// BOTH metrics, so the "does NOT register outbox_pending_depth" assertion fails.
func TestOutboxRejectCollector_RegistersOnlyRejectedCounter(t *testing.T) {
	p := newOutboxSpyProvider()
	_, err := obmetrics.NewOutboxRejectCollector(p)
	if err != nil {
		t.Fatalf("NewOutboxRejectCollector: %v", err)
	}

	if _, ok := p.counterNames["outbox_consumer_rejected_total"]; !ok {
		t.Error("OutboxRejectCollector must register outbox_consumer_rejected_total")
	}
	if _, ok := p.gaugeNames["outbox_pending_depth"]; ok {
		t.Error("OutboxRejectCollector must NOT register outbox_pending_depth — " +
			"that belongs to OutboxPendingDepthCollector (P1#3 split)")
	}
}

// TestOutboxRejectCollector_ObserveReject_IncrementsCounterWithLabels verifies
// the label set {cell, topic, reason} without consumerGroup.
func TestOutboxRejectCollector_ObserveReject_IncrementsCounterWithLabels(t *testing.T) {
	p := newOutboxSpyProvider()
	c, err := obmetrics.NewOutboxRejectCollector(p)
	if err != nil {
		t.Fatalf("NewOutboxRejectCollector: %v", err)
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
	if _, ok := got["consumerGroup"]; ok {
		t.Error("consumerGroup must not be a label on outbox_consumer_rejected_total")
	}
}

// TestOutboxRejectCollector_NilReceiver_DoesNotPanic verifies nil-safe call.
func TestOutboxRejectCollector_NilReceiver_DoesNotPanic(t *testing.T) {
	var c *obmetrics.OutboxRejectCollector
	c.ObserveReject("cell", "topic", "cg", "handler_reject") // must not panic
}

// ---------------------------------------------------------------------------
// OutboxPendingDepthCollector tests
// ---------------------------------------------------------------------------

// TestNewOutboxPendingDepthCollector_RequiresCellID verifies that empty cellID
// returns an error (fail-fast at construction).
func TestNewOutboxPendingDepthCollector_RequiresCellID(t *testing.T) {
	_, err := obmetrics.NewOutboxPendingDepthCollector(kernelmetrics.NopProvider{}, "")
	if err == nil {
		t.Fatal("expected error for empty cellID, got nil — " +
			"OutboxPendingDepthCollector must fail-fast on empty cellID")
	}
}

// TestNewOutboxPendingDepthCollector_RejectsNilProvider verifies fail-fast on nil provider.
func TestNewOutboxPendingDepthCollector_RejectsNilProvider(t *testing.T) {
	_, err := obmetrics.NewOutboxPendingDepthCollector(nil, "configcore")
	if err == nil {
		t.Fatal("expected error for nil provider, got nil")
	}
}

// TestNewOutboxPendingDepthCollector_Success verifies happy path.
func TestNewOutboxPendingDepthCollector_Success(t *testing.T) {
	c, err := obmetrics.NewOutboxPendingDepthCollector(kernelmetrics.NopProvider{}, "configcore")
	if err != nil {
		t.Fatalf("NewOutboxPendingDepthCollector: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil OutboxPendingDepthCollector")
	}
}

// TestOutboxPendingDepthCollector_RegistersOnlyPendingDepthGauge asserts that the
// collector registers outbox_pending_depth but NOT outbox_consumer_rejected_total.
//
// Wave 1 RED: the current stub wraps OutboxConsumerCollector which registers
// BOTH metrics, so the "does NOT register outbox_consumer_rejected_total" assertion fails.
func TestOutboxPendingDepthCollector_RegistersOnlyPendingDepthGauge(t *testing.T) {
	p := newOutboxSpyProvider()
	_, err := obmetrics.NewOutboxPendingDepthCollector(p, "configcore")
	if err != nil {
		t.Fatalf("NewOutboxPendingDepthCollector: %v", err)
	}

	if _, ok := p.gaugeNames["outbox_pending_depth"]; !ok {
		t.Error("OutboxPendingDepthCollector must register outbox_pending_depth")
	}
	if _, ok := p.counterNames["outbox_consumer_rejected_total"]; ok {
		t.Error("OutboxPendingDepthCollector must NOT register outbox_consumer_rejected_total — " +
			"that belongs to OutboxRejectCollector (P1#2 split)")
	}
}

// TestOutboxPendingDepthCollector_ObservePendingDepth_UsesConstructedCellID verifies
// that ObservePendingDepth records with cell={constructed cellID}, NOT "_runtime".
//
// Wave 1 RED: the stub delegates to OutboxConsumerCollector("_stub"), so the
// cell label will be "_stub" (or "_runtime" in the auto-wire path), not "configcore".
func TestOutboxPendingDepthCollector_ObservePendingDepth_UsesConstructedCellID(t *testing.T) {
	p := newOutboxSpyProvider()
	c, err := obmetrics.NewOutboxPendingDepthCollector(p, "configcore")
	if err != nil {
		t.Fatalf("NewOutboxPendingDepthCollector: %v", err)
	}

	c.ObservePendingDepth(42)

	ops := p.gaugeOps["outbox_pending_depth"]
	if len(ops) != 1 {
		t.Fatalf("want 1 gauge op, got %d", len(ops))
	}
	if ops[0].value != 42 {
		t.Errorf("gauge value = %v, want 42", ops[0].value)
	}
	// The cell label MUST be the cellID passed at construction ("configcore"),
	// NOT the "_runtime" sentinel or the "_stub" placeholder used in the stub.
	if ops[0].labels["cell"] != "configcore" {
		t.Errorf("cell label = %q, want %q — OutboxPendingDepthCollector must use "+
			"the constructed cellID, not a runtime sentinel",
			ops[0].labels["cell"], "configcore")
	}
}

// TestOutboxPendingDepthCollector_NilReceiver_DoesNotPanic verifies nil-safe call.
func TestOutboxPendingDepthCollector_NilReceiver_DoesNotPanic(t *testing.T) {
	var c *obmetrics.OutboxPendingDepthCollector
	c.ObservePendingDepth(7) // must not panic
}

// ---------------------------------------------------------------------------
// outboxSpyProvider — tracks counter + gauge registrations and emissions.
// Independent of provider_collector_test.go to avoid coupling.
// ---------------------------------------------------------------------------

type outboxSpyRecord struct {
	labels kernelmetrics.Labels
	value  float64
}

type outboxSpyProvider struct {
	counterNames map[string]struct{}
	gaugeNames   map[string]struct{}
	counterOps   map[string][]outboxSpyRecord
	gaugeOps     map[string][]outboxSpyRecord
}

func newOutboxSpyProvider() *outboxSpyProvider {
	return &outboxSpyProvider{
		counterNames: make(map[string]struct{}),
		gaugeNames:   make(map[string]struct{}),
		counterOps:   make(map[string][]outboxSpyRecord),
		gaugeOps:     make(map[string][]outboxSpyRecord),
	}
}

func (p *outboxSpyProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	p.counterNames[opts.Name] = struct{}{}
	return &outboxSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *outboxSpyProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return kernelmetrics.NopProvider{}.HistogramVec(opts)
}

func (p *outboxSpyProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	p.gaugeNames[opts.Name] = struct{}{}
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
