package outbox_test

import (
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

type spyCounterVec struct {
	parent *spyProvider
	name   string
	labels []string
}

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
