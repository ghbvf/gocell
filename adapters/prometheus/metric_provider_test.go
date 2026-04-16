package prometheus_test

import (
	"errors"
	"strings"
	"testing"

	gcprom "github.com/ghbvf/gocell/adapters/prometheus"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newTestProvider(t *testing.T) (metrics.Provider, *prom.Registry) {
	t.Helper()
	reg := prom.NewRegistry()
	p, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{
		Registry:  reg,
		Namespace: "gocelltest",
	})
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}
	return p, reg
}

func TestMetricProvider_CounterInc(t *testing.T) {
	p, reg := newTestProvider(t)

	cv, err := p.CounterVec(metrics.CounterOpts{
		Name:       "events_total",
		Help:       "Total events.",
		LabelNames: []string{"outcome"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	cv.With(metrics.Labels{"outcome": "success"}).Inc()
	cv.With(metrics.Labels{"outcome": "success"}).Inc()
	cv.With(metrics.Labels{"outcome": "failure"}).Add(3)

	got := testutil.CollectAndCount(reg, "gocelltest_events_total")
	if got != 2 {
		t.Fatalf("expected 2 series (success, failure), got %d", got)
	}

	if v := testutil.ToFloat64(collect(t, reg, "gocelltest_events_total", prom.Labels{"outcome": "success"})); v != 2 {
		t.Fatalf("success counter = %v, want 2", v)
	}
	if v := testutil.ToFloat64(collect(t, reg, "gocelltest_events_total", prom.Labels{"outcome": "failure"})); v != 3 {
		t.Fatalf("failure counter = %v, want 3", v)
	}
}

func TestMetricProvider_HistogramObserve(t *testing.T) {
	p, reg := newTestProvider(t)

	hv, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "hook_duration_seconds",
		Help:       "Hook duration.",
		LabelNames: []string{"phase"},
		Buckets:    []float64{0.1, 1, 10},
	})
	if err != nil {
		t.Fatalf("HistogramVec: %v", err)
	}
	hv.With(metrics.Labels{"phase": "start"}).Observe(0.05)
	hv.With(metrics.Labels{"phase": "start"}).Observe(2.5)

	count := testutil.CollectAndCount(reg, "gocelltest_hook_duration_seconds")
	if count != 1 {
		t.Fatalf("expected 1 histogram series, got %d", count)
	}
}

func TestMetricProvider_RegisterDuplicateReturnsError(t *testing.T) {
	p, _ := newTestProvider(t)
	opts := metrics.CounterOpts{Name: "dup_total", Help: "h", LabelNames: []string{"a"}}
	if _, err := p.CounterVec(opts); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, err := p.CounterVec(opts)
	if err == nil {
		t.Fatal("duplicate register must fail")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Code != gcprom.ErrAdapterPromRegister {
		t.Fatalf("expected code %s, got %v", gcprom.ErrAdapterPromRegister, err)
	}
}

func TestMetricProvider_LabelMismatchPanics(t *testing.T) {
	p, _ := newTestProvider(t)
	cv, err := p.CounterVec(metrics.CounterOpts{
		Name:       "mismatch_total",
		LabelNames: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on label mismatch")
		}
		recErr, ok := r.(error)
		if !ok || !errors.Is(recErr, metrics.ErrLabelMismatch) {
			t.Fatalf("panic must wrap metrics.ErrLabelMismatch, got %v", r)
		}
	}()
	cv.With(metrics.Labels{"a": "x", "c": "y"})
}

func TestMetricProvider_NilRegistryRejected(t *testing.T) {
	_, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{Registry: nil})
	if err == nil {
		t.Fatal("nil Registry must be rejected")
	}
	if !strings.Contains(err.Error(), "Registry") {
		t.Fatalf("error should mention Registry, got %v", err)
	}
}

// collect fetches a single labeled Counter/Histogram from the registry for
// testutil.ToFloat64. prom.Collector must be obtained indirectly; easiest is
// to reflect via testutil.GatherAndCount for histogram bucket sums, but for
// counter values with specific labels we need a hand-rolled helper.
func collect(t *testing.T, reg *prom.Registry, name string, labels prom.Labels) prom.Collector {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			match := true
			for _, lp := range m.GetLabel() {
				if v, ok := labels[lp.GetName()]; ok && v != lp.GetValue() {
					match = false
					break
				}
			}
			if match {
				return singletonCounter{val: m.GetCounter().GetValue()}
			}
		}
	}
	t.Fatalf("no metric %s with labels %v", name, labels)
	return nil
}

type singletonCounter struct{ val float64 }

func (s singletonCounter) Describe(ch chan<- *prom.Desc) {
	ch <- prom.NewDesc("singleton", "test helper", nil, nil)
}
func (s singletonCounter) Collect(ch chan<- prom.Metric) {
	ch <- prom.MustNewConstMetric(prom.NewDesc("singleton", "test helper", nil, nil), prom.CounterValue, s.val)
}
