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

// TestMetricProvider_Unregister_RemovesAndAllowsReregister verifies the K2
// atomic-registration rollback contract: Unregister removes a previously
// registered Collector from both the Provider's internal map and the
// underlying Prometheus registry, allowing the same name to be registered
// again without "duplicate collector" error.
//
// Without this behaviour, the NewProviderRelayCollector rollback loop would
// leak orphan Prometheus collectors on partial failure and refuse retry.
func TestMetricProvider_Unregister_RemovesAndAllowsReregister(t *testing.T) {
	p, reg := newTestProvider(t)

	cv, err := p.CounterVec(metrics.CounterOpts{
		Name:       "unreg_demo_total",
		Help:       "demo",
		LabelNames: []string{"label"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}

	// Registering the same name again must fail — baseline for the rollback
	// contract (without Unregister, rollback would be useless).
	if _, err := p.CounterVec(metrics.CounterOpts{
		Name:       "unreg_demo_total",
		Help:       "demo",
		LabelNames: []string{"label"},
	}); err == nil {
		t.Fatal("duplicate CounterVec without Unregister should fail")
	}

	// Unregister the first vec. Same name must now be re-registrable.
	if err := p.Unregister(cv); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	cv2, err := p.CounterVec(metrics.CounterOpts{
		Name:       "unreg_demo_total",
		Help:       "demo",
		LabelNames: []string{"label"},
	})
	if err != nil {
		t.Fatalf("re-register after Unregister: %v", err)
	}

	// Touch the new vec so Prometheus Gather emits a sample, then confirm
	// exactly one family — the registry is in sync with no stale entries.
	cv2.With(metrics.Labels{"label": "v"}).Inc()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var seen int
	for _, f := range families {
		if strings.HasSuffix(f.GetName(), "unreg_demo_total") {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("expected exactly 1 unreg_demo_total metric family after re-register, got %d", seen)
	}
}

// TestMetricProvider_Unregister_IdempotentOnUnknown verifies Unregister
// returns nil when called with a Collector never registered with this
// Provider — required by the Provider.Unregister contract (idempotent,
// nil-safe for double-unregister and orphan collectors).
func TestMetricProvider_Unregister_IdempotentOnUnknown(t *testing.T) {
	p, _ := newTestProvider(t)

	cv, err := p.CounterVec(metrics.CounterOpts{
		Name: "known_total", Help: "h", LabelNames: []string{"x"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	if err := p.Unregister(cv); err != nil {
		t.Fatalf("first Unregister: %v", err)
	}
	// Second call must also return nil (idempotent).
	if err := p.Unregister(cv); err != nil {
		t.Fatalf("double Unregister must be idempotent, got %v", err)
	}
}

// TestMetricProvider_Registered_AlwaysTrue locks in the documented marker
// semantics of Collector.Registered: the method is a compile-time type-
// membership marker and always returns true for vecs issued by the Provider,
// even after Unregister. It is not a runtime state probe.
func TestMetricProvider_Registered_AlwaysTrue(t *testing.T) {
	p, _ := newTestProvider(t)

	cv, err := p.CounterVec(metrics.CounterOpts{
		Name: "marker_counter_total", Help: "h", LabelNames: []string{"x"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	hv, err := p.HistogramVec(metrics.HistogramOpts{
		Name: "marker_hist_seconds", Help: "h", LabelNames: []string{"x"},
	})
	if err != nil {
		t.Fatalf("HistogramVec: %v", err)
	}

	if !cv.Registered() {
		t.Error("counter vec Registered() must be true before Unregister")
	}
	if !hv.Registered() {
		t.Error("histogram vec Registered() must be true before Unregister")
	}

	// Per the marker contract, Registered remains true post-Unregister; the
	// registry state changes but the vec value identity does not.
	_ = p.Unregister(cv)
	_ = p.Unregister(hv)
	if !cv.Registered() {
		t.Error("counter vec Registered() must still be true after Unregister (marker semantics)")
	}
	if !hv.Registered() {
		t.Error("histogram vec Registered() must still be true after Unregister (marker semantics)")
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
