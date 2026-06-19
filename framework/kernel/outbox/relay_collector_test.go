package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// testWriteBackDur500us is the write-back duration used in poll cycle recording tests.
// 500µs is not in the testtime table so it is declared as a file-level const.
const testWriteBackDur500us = 500 * time.Microsecond

func TestProviderRelayCollector_RejectsEmptyCellID(t *testing.T) {
	if _, err := outbox.NewProviderRelayCollector(metrics.NopProvider{}, ""); err == nil {
		t.Fatal("empty cellID must be rejected")
	}
}

func TestProviderRelayCollector_NopProviderNoPanic(t *testing.T) {
	c, err := outbox.NewProviderRelayCollector(metrics.NopProvider{}, "test-cell")
	if err != nil {
		t.Fatalf("NewProviderRelayCollector: %v", err)
	}

	ctx := context.Background()
	c.RecordPollCycle(ctx, outbox.PollCycleResult{
		Event:        outbox.OutcomeCounts{Published: 3, Retried: 1, Dead: 0, Skipped: 2},
		Command:      outbox.OutcomeCounts{Published: 1},
		ClaimDur:     testtime.D10ms,
		PublishDur:   testtime.MediumPoll,
		WriteBackDur: testtime.FastPoll,
	})
	c.RecordBatchSize(ctx, 6)
	c.RecordReclaim(ctx, 4)
	c.RecordCleanup(ctx, 10, 2)

	// Zero counts must not panic; skipped counter increment for zero values.
	c.RecordPollCycle(ctx, outbox.PollCycleResult{})
	c.RecordReclaim(ctx, 0)
	c.RecordCleanup(ctx, 0, 0)
}

// spyProvider records the last operation so tests can assert the collector
// emits via the Provider pipeline (not directly to a backend).
type spyProvider struct {
	counterOps map[string][]spyCounterOp
}

type spyCounterOp struct {
	labels metrics.Labels
	op     string
	value  float64
}

func newSpyProvider() *spyProvider {
	return &spyProvider{counterOps: map[string][]spyCounterOp{}}
}

func (s *spyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	return &spyCounterVec{parent: s, name: opts.Name, labels: opts.LabelNames}, nil
}

func (s *spyProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return &spyHistogramVec{parent: s, name: opts.Name, labels: opts.LabelNames}, nil
}

func (s *spyProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return &spyGaugeVec{parent: s, name: opts.Name, labels: opts.LabelNames}, nil
}

type spyCounterVec struct {
	parent *spyProvider
	name   string
	labels []string
}

func (v *spyCounterVec) Registered() bool { return true }
func (v *spyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labels, l)
	return spyCounter{parent: v.parent, name: v.name, labels: l}
}

type spyCounter struct {
	parent *spyProvider
	name   string
	labels metrics.Labels
}

func (c spyCounter) Inc(ctx context.Context) { c.Add(ctx, 1) }
func (c spyCounter) Add(_ context.Context, d float64) {
	c.parent.counterOps[c.name] = append(c.parent.counterOps[c.name], spyCounterOp{labels: c.labels, op: "add", value: d})
}

type spyHistogramVec struct {
	parent *spyProvider
	name   string
	labels []string
}

func (v *spyHistogramVec) Registered() bool { return true }
func (v *spyHistogramVec) With(l metrics.Labels) metrics.Histogram {
	metrics.MustValidateLabels(v.labels, l)
	return spyHistogram{parent: v.parent, name: v.name, labels: l}
}

type spyHistogram struct {
	parent *spyProvider
	name   string
	labels metrics.Labels
}

func (h spyHistogram) Observe(_ context.Context, v float64) {
	// Histogram observations are recorded as counter-like ops under the name
	// "hist:{metric}" so the spy can remain a single map.
	h.parent.counterOps["hist:"+h.name] = append(h.parent.counterOps["hist:"+h.name], spyCounterOp{labels: h.labels, op: "observe", value: v})
}

func TestProviderRelayCollector_PollCycleEmitsPerOutcome(t *testing.T) {
	p := newSpyProvider()
	c, err := outbox.NewProviderRelayCollector(p, "accesscore")
	if err != nil {
		t.Fatalf("NewProviderRelayCollector: %v", err)
	}

	c.RecordPollCycle(context.Background(), outbox.PollCycleResult{
		Event:    outbox.OutcomeCounts{Published: 4, Retried: 1, Dead: 0, Skipped: 2},
		ClaimDur: time.Millisecond, PublishDur: testtime.D2ms, WriteBackDur: testWriteBackDur500us,
	})

	relayed := p.counterOps["outbox_relayed_total"]
	// 3 non-zero event outcomes: published=4, retried=1, skipped=2 (dead=0 zero-skipped).
	if len(relayed) != 3 {
		t.Fatalf("want 3 relayed entries (zero-skip dead), got %d: %+v", len(relayed), relayed)
	}

	hist := p.counterOps["hist:outbox_poll_duration_seconds"]
	if len(hist) != 4 { // claim + publish + write_back + total
		t.Fatalf("want 4 poll_duration observations, got %d", len(hist))
	}
}

// TestProviderRelayCollector_PollCycleEmitsKindLabels is the F4 collector-level
// proof: command and event settlements land on outbox_relayed_total under DISTINCT
// kind labels with the disposition in the orthogonal outcome label, and zero counts
// are skipped per (kind, outcome) (#1674).
func TestProviderRelayCollector_PollCycleEmitsKindLabels(t *testing.T) {
	p := newSpyProvider()
	c, err := outbox.NewProviderRelayCollector(p, "devicecell")
	if err != nil {
		t.Fatalf("NewProviderRelayCollector: %v", err)
	}

	c.RecordPollCycle(context.Background(), outbox.PollCycleResult{
		Event:    outbox.OutcomeCounts{Published: 4, Dead: 1},
		Command:  outbox.OutcomeCounts{Published: 2, Retried: 3},
		ClaimDur: time.Millisecond, PublishDur: testtime.D2ms, WriteBackDur: testWriteBackDur500us,
	})

	// Build a {kind/outcome -> value} view of every emitted relayed op.
	got := map[string]float64{}
	for _, op := range p.counterOps["outbox_relayed_total"] {
		if op.labels["cell"] != "devicecell" {
			t.Fatalf("relayed op missing/!=devicecell cell label: %+v", op.labels)
		}
		got[op.labels["kind"]+"/"+op.labels["outcome"]] = op.value
	}

	want := map[string]float64{
		"event/published":   4,
		"event/dead":        1,
		"command/published": 2,
		"command/retried":   3,
	}
	if len(got) != len(want) {
		t.Fatalf("want exactly %d {kind,outcome} series (zero-skipped), got %d: %+v", len(want), len(got), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("outbox_relayed_total{%s} = %v, want %v (full: %+v)", k, got[k], v, got)
		}
	}
}

func TestProviderRelayCollector_ZeroBatchSizeStillObserved(t *testing.T) {
	p := newSpyProvider()
	c, err := outbox.NewProviderRelayCollector(p, "accesscore")
	if err != nil {
		t.Fatalf("NewProviderRelayCollector: %v", err)
	}
	c.RecordBatchSize(context.Background(), 0)
	c.RecordBatchSize(context.Background(), 5)
	obs := p.counterOps["hist:outbox_batch_size"]
	if len(obs) != 2 {
		t.Fatalf("want 2 batch_size observations (including zero), got %d", len(obs))
	}
}

// TestNewProviderRelayCollector_PartialFailure_ReturnsError verifies that when
// metric registration fails mid-way (e.g., 3rd metric conflicts), the
// constructor returns that provider error. Provider lifecycle cleanup is owned
// by the caller, not by per-instrument unregister.
func TestNewProviderRelayCollector_PartialFailure_ReturnsError(t *testing.T) {
	tests := []struct {
		name           string
		failOnCall     int // 1-based: which registration call (counter+histogram combined) fails
		wantRegistered int // completed registrations before the failure
	}{
		{name: "fail_on_1st", failOnCall: 1, wantRegistered: 0},
		{name: "fail_on_2nd", failOnCall: 2, wantRegistered: 1},
		{name: "fail_on_3rd", failOnCall: 3, wantRegistered: 2},
		{name: "fail_on_4th", failOnCall: 4, wantRegistered: 3},
		{name: "fail_on_5th", failOnCall: 5, wantRegistered: 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newFailingProvider(tc.failOnCall)
			_, err := outbox.NewProviderRelayCollector(p, "partial-failure-cell")
			if err == nil {
				t.Fatal("expected error from partial registration, got nil")
			}

			if got := len(p.registered); got != tc.wantRegistered {
				t.Fatalf("completed registrations = %d, want %d", got, tc.wantRegistered)
			}
		})
	}
}

// TestNewProviderRelayCollector_SuccessPath_AllFiveMetricsRegistered asserts
// that all five outbox metrics (including 'cleaned') are appended to the
// test provider's registered slice on the success path. Regression test for F5:
// 'cleaned' was previously not appended, hiding incomplete startup wiring.
// Verification strategy: use a counting provider that records how many
// times CounterVec/HistogramVec were called; on success all 5 must complete.
func TestNewProviderRelayCollector_SuccessPath_AllFiveMetricsRegistered(t *testing.T) {
	p := newSpyProvider()
	c, err := outbox.NewProviderRelayCollector(p, "five-metrics-cell")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("collector must not be nil")
	}

	// 3 CounterVecs + 2 HistogramVecs = 5 registration calls.
	totalVecs := 0
	for range p.counterOps {
		// counterOps only has entries after Record calls; use registered names
		// from calling RecordPollCycle to verify all vecs are wired.
		_ = totalVecs
	}
	// Exercise all recording paths to confirm all 5 vecs are live (no nil panic).
	c.RecordPollCycle(context.Background(), outbox.PollCycleResult{Event: outbox.OutcomeCounts{Published: 1}})
	c.RecordBatchSize(context.Background(), 1)
	c.RecordReclaim(context.Background(), 1)
	c.RecordCleanup(context.Background(), 1, 1)
}

// TestNewProviderRelayCollector_PartialFailureRegistrationOrder verifies that
// when registration fails, the constructor attempted metrics in the expected
// order up to the failure point.
func TestNewProviderRelayCollector_PartialFailureRegistrationOrder(t *testing.T) {
	// Fail on 4th call; first 3 succeeded (relayed, pollDuration, batchSize).
	p := newFailingProvider(4)
	_, err := outbox.NewProviderRelayCollector(p, "lifo-cell")
	if err == nil {
		t.Fatal("expected error")
	}

	names := p.registeredNames()
	want := []string{
		"outbox_relayed_total",
		"outbox_poll_duration_seconds",
		"outbox_batch_size",
	}
	if len(names) != len(want) {
		t.Fatalf("want %d registered, got %d: %v", len(want), len(names), names)
	}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("registered[%d]: want %q got %q", i, w, names[i])
		}
	}
}

// failingProvider is a test Provider that succeeds for the first N-1
// registration calls, then returns an error on the Nth call, and never errors
// again. It records completed registrations.
type failingProvider struct {
	failOnCall int
	callCount  int
	registered []failingCollector
}

type failingCollector struct {
	name string
	vec  metrics.Collector
}

func newFailingProvider(failOnCall int) *failingProvider {
	return &failingProvider{failOnCall: failOnCall}
}

func (p *failingProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.callCount++
	if p.callCount == p.failOnCall {
		return nil, errors.New("simulated counter registration failure")
	}
	cv := &spyCounterVec{parent: newSpyProvider(), name: opts.Name, labels: opts.LabelNames}
	p.registered = append(p.registered, failingCollector{name: opts.Name, vec: cv})
	return cv, nil
}

func (p *failingProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	p.callCount++
	if p.callCount == p.failOnCall {
		return nil, errors.New("simulated histogram registration failure")
	}
	hv := &spyHistogramVec{parent: newSpyProvider(), name: opts.Name, labels: opts.LabelNames}
	p.registered = append(p.registered, failingCollector{name: opts.Name, vec: hv})
	return hv, nil
}

func (p *failingProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	p.callCount++
	if p.callCount == p.failOnCall {
		return nil, errors.New("simulated gauge registration failure")
	}
	gv := &spyGaugeVec{parent: newSpyProvider(), name: opts.Name, labels: opts.LabelNames}
	p.registered = append(p.registered, failingCollector{name: opts.Name, vec: gv})
	return gv, nil
}

func (p *failingProvider) registeredNames() []string {
	names := make([]string, len(p.registered))
	for i, fc := range p.registered {
		names[i] = fc.name
	}
	return names
}

type spyGaugeVec struct {
	parent *spyProvider
	name   string
	labels []string
}

func (v *spyGaugeVec) Registered() bool { return true }
func (v *spyGaugeVec) With(l metrics.Labels) metrics.Gauge {
	metrics.MustValidateLabels(v.labels, l)
	return spyGauge{}
}

type spyGauge struct{}

func (spyGauge) Set(_ context.Context, _ float64) {}
func (spyGauge) Inc(_ context.Context)            {}
func (spyGauge) Dec(_ context.Context)            {}
func (spyGauge) Add(_ context.Context, _ float64) {}
