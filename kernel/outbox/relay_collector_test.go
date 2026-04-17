package outbox_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
)

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

	c.RecordPollCycle(outbox.PollCycleResult{
		Published:    3,
		Retried:      1,
		Dead:         0,
		Skipped:      2,
		ClaimDur:     10 * time.Millisecond,
		PublishDur:   50 * time.Millisecond,
		WriteBackDur: 5 * time.Millisecond,
	})
	c.RecordBatchSize(6)
	c.RecordReclaim(4)
	c.RecordCleanup(10, 2)

	// Zero counts must not panic; skipped counter increment for zero values.
	c.RecordPollCycle(outbox.PollCycleResult{})
	c.RecordReclaim(0)
	c.RecordCleanup(0, 0)
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

func (s *spyProvider) Unregister(_ metrics.Collector) error { return nil }

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

func (c spyCounter) Inc() { c.Add(1) }
func (c spyCounter) Add(d float64) {
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

func (h spyHistogram) Observe(v float64) {
	// Histogram observations are recorded as counter-like ops under the name
	// "hist:{metric}" so the spy can remain a single map.
	h.parent.counterOps["hist:"+h.name] = append(h.parent.counterOps["hist:"+h.name], spyCounterOp{labels: h.labels, op: "observe", value: v})
}

func TestProviderRelayCollector_PollCycleEmitsPerOutcome(t *testing.T) {
	p := newSpyProvider()
	c, err := outbox.NewProviderRelayCollector(p, "access-core")
	if err != nil {
		t.Fatalf("NewProviderRelayCollector: %v", err)
	}

	c.RecordPollCycle(outbox.PollCycleResult{
		Published: 4, Retried: 1, Dead: 0, Skipped: 2,
		ClaimDur: time.Millisecond, PublishDur: 2 * time.Millisecond, WriteBackDur: 500 * time.Microsecond,
	})

	relayed := p.counterOps["outbox_relayed_total"]
	// 3 non-zero outcomes: published=4, retried=1, skipped=2
	if len(relayed) != 3 {
		t.Fatalf("want 3 relayed entries (zero-skip dead), got %d: %+v", len(relayed), relayed)
	}

	hist := p.counterOps["hist:outbox_poll_duration_seconds"]
	if len(hist) != 4 { // claim + publish + write_back + total
		t.Fatalf("want 4 poll_duration observations, got %d", len(hist))
	}
}

func TestProviderRelayCollector_ZeroBatchSizeStillObserved(t *testing.T) {
	p := newSpyProvider()
	c, err := outbox.NewProviderRelayCollector(p, "access-core")
	if err != nil {
		t.Fatalf("NewProviderRelayCollector: %v", err)
	}
	c.RecordBatchSize(0)
	c.RecordBatchSize(5)
	obs := p.counterOps["hist:outbox_batch_size"]
	if len(obs) != 2 {
		t.Fatalf("want 2 batch_size observations (including zero), got %d", len(obs))
	}
}

// TestNewProviderRelayCollector_PartialFailure_RollbackAll verifies that when
// metric registration fails mid-way (e.g., 3rd metric conflicts), all
// previously registered metrics are unregistered, preserving Provider clean
// state for retry or redeployment.
//
// Regression test for K2 (OBS-RELAY-REGISTER-ATOMIC-01): sequential
// registration left orphan metrics on partial failure. The rollback loop in
// NewProviderRelayCollector must unregister previously registered collectors
// in LIFO order on any partial failure.
func TestNewProviderRelayCollector_PartialFailure_RollbackAll(t *testing.T) {
	tests := []struct {
		name        string
		failOnCall  int // 1-based: which registration call (counter+histogram combined) fails
		wantRollCnt int // how many Unregister calls expected
	}{
		{name: "fail_on_1st", failOnCall: 1, wantRollCnt: 0},
		{name: "fail_on_2nd", failOnCall: 2, wantRollCnt: 1},
		{name: "fail_on_3rd", failOnCall: 3, wantRollCnt: 2},
		{name: "fail_on_4th", failOnCall: 4, wantRollCnt: 3},
		{name: "fail_on_5th", failOnCall: 5, wantRollCnt: 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newFailingProvider(tc.failOnCall)
			_, err := outbox.NewProviderRelayCollector(p, "rollback-cell")
			if err == nil {
				t.Fatal("expected error from partial registration, got nil")
			}

			// All prior registrations must have been rolled back via Unregister.
			if got := p.unregisteredCount(); got != tc.wantRollCnt {
				t.Fatalf("want %d Unregister calls, got %d", tc.wantRollCnt, got)
			}

			// Provider must be in a clean state: re-registering with a
			// non-failing provider for the same cell must succeed.
			p2 := newSpyProvider()
			c2, err := outbox.NewProviderRelayCollector(p2, "rollback-cell")
			if err != nil {
				t.Fatalf("re-register after rollback failed: %v", err)
			}
			if c2 == nil {
				t.Fatal("collector must not be nil after clean registration")
			}
		})
	}
}

// TestNewProviderRelayCollector_UnregisterLIFOOrder verifies that when
// registration fails, previously registered collectors are unregistered in
// reverse (LIFO) order to mirror standard stack-unwinding semantics.
func TestNewProviderRelayCollector_UnregisterLIFOOrder(t *testing.T) {
	// Fail on 4th call; first 3 succeeded (relayed, pollDuration, batchSize).
	p := newFailingProvider(4)
	_, err := outbox.NewProviderRelayCollector(p, "lifo-cell")
	if err == nil {
		t.Fatal("expected error")
	}

	names := p.unregisteredNames()
	// Registered order: outbox_relayed_total(1), outbox_poll_duration_seconds(2),
	// outbox_batch_size(3). Unregister must be reverse: 3, 2, 1.
	want := []string{
		"outbox_batch_size",
		"outbox_poll_duration_seconds",
		"outbox_relayed_total",
	}
	if len(names) != len(want) {
		t.Fatalf("want %d unregistered, got %d: %v", len(want), len(names), names)
	}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("unregister[%d]: want %q got %q", i, w, names[i])
		}
	}
}

// failingProvider is a test Provider that succeeds for the first N-1
// registration calls, then returns an error on the Nth call, and never
// errors again. It records which collectors were Unregistered and in what
// order.
type failingProvider struct {
	failOnCall   int
	callCount    int
	registered   []failingCollector
	unregistered []failingCollector
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

func (p *failingProvider) Unregister(c metrics.Collector) error {
	p.unregistered = append(p.unregistered, failingCollector{vec: c, name: collectorName(c)})
	return nil
}

func (p *failingProvider) unregisteredCount() int {
	return len(p.unregistered)
}

func (p *failingProvider) unregisteredNames() []string {
	names := make([]string, len(p.unregistered))
	for i, fc := range p.unregistered {
		names[i] = fc.name
	}
	return names
}

// collectorName extracts the metric name from a registered Collector for
// assertion purposes. It relies on the NamedCollector interface that the
// failing spy vecs implement.
func collectorName(c metrics.Collector) string {
	type named interface {
		MetricName() string
	}
	if n, ok := c.(named); ok {
		return n.MetricName()
	}
	return "<unknown>"
}

// MetricName exposes the name so collectorName can extract it in tests.
func (v *spyCounterVec) MetricName() string   { return v.name }
func (v *spyHistogramVec) MetricName() string { return v.name }
