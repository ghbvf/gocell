package prometheus_test

import (
	"errors"
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	gcprom "github.com/ghbvf/gocell/adapters/prometheus"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
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

func TestMetricProvider_RegisterDuplicateReturnsExisting(t *testing.T) {
	// CounterVec is idempotent: a second registration with the same name
	// returns the existing collector rather than an error. This lets multiple
	// cells share a single MetricProvider and register the same outbox counter
	// without failing init (e.g. accesscore + auditcore in one assembly).
	p, reg := newTestProvider(t)
	opts := metrics.CounterOpts{Name: "dup_total", Help: "h", LabelNames: []string{"a"}}
	cv1, err := p.CounterVec(opts)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	cv2, err := p.CounterVec(opts)
	if err != nil {
		t.Fatalf("duplicate register must succeed (return existing), got error: %v", err)
	}
	// Both vecs must be functional and share the same underlying collector.
	cv1.With(metrics.Labels{"a": "x"}).Inc()
	cv2.With(metrics.Labels{"a": "x"}).Inc()
	// The shared collector should report 2 increments.
	if v := testutil.ToFloat64(collect(t, reg, "gocelltest_dup_total", prom.Labels{"a": "x"})); v != 2 {
		t.Fatalf("shared counter = %v, want 2", v)
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
// Without this behavior, the NewProviderRelayCollector rollback loop would
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

	// Registering the same name again returns the existing collector (idempotent),
	// not an error. This is by design — see TestMetricProvider_RegisterDuplicateReturnsExisting.
	if _, err := p.CounterVec(metrics.CounterOpts{
		Name:       "unreg_demo_total",
		Help:       "demo",
		LabelNames: []string{"label"},
	}); err != nil {
		t.Fatalf("duplicate CounterVec should return existing collector, got error: %v", err)
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

// ---------------------------------------------------------------------------
// HistogramVec AlreadyRegisteredError branches
// ---------------------------------------------------------------------------

// TestMetricProvider_HistogramVec_DuplicateReturnsExisting verifies that a
// second HistogramVec registration with the same name returns the existing
// collector (AlreadyRegisteredError reuse path) without error — mirroring the
// CounterVec idempotent pattern.
func TestMetricProvider_HistogramVec_DuplicateReturnsExisting(t *testing.T) {
	p, reg := newTestProvider(t)
	opts := metrics.HistogramOpts{
		Name:       "dup_hist_seconds",
		Help:       "h",
		LabelNames: []string{"phase"},
		Buckets:    []float64{0.1, 1.0},
	}
	hv1, err := p.HistogramVec(opts)
	if err != nil {
		t.Fatalf("first HistogramVec: %v", err)
	}
	hv2, err := p.HistogramVec(opts)
	if err != nil {
		t.Fatalf("duplicate HistogramVec must succeed (return existing), got error: %v", err)
	}
	// Both vecs must be functional and share the same underlying collector.
	hv1.With(metrics.Labels{"phase": "start"}).Observe(0.05)
	hv2.With(metrics.Labels{"phase": "start"}).Observe(0.50)
	// Exactly 1 series since both writes go to the same underlying histogram.
	if cnt := testutil.CollectAndCount(reg, "gocelltest_dup_hist_seconds"); cnt != 1 {
		t.Fatalf("expected 1 series after shared histogram writes, got %d", cnt)
	}
}

// TestMetricProvider_HistogramVec_DifferentLabelNamesErrors verifies that
// re-registering a HistogramVec with a different label set (different names)
// returns an ErrAdapterPromRegister error. With different label names Prometheus
// returns a descriptor conflict error (not AlreadyRegisteredError), so the
// provider surfaces it as ErrAdapterPromRegister directly.
func TestMetricProvider_HistogramVec_DifferentLabelNamesErrors(t *testing.T) {
	p, _ := newTestProvider(t)
	_, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "label_conflict_hist_seconds",
		Help:       "h",
		LabelNames: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("first HistogramVec: %v", err)
	}
	// Different label names → Prometheus descriptor conflict, not AlreadyRegisteredError.
	_, err = p.HistogramVec(metrics.HistogramOpts{
		Name:       "label_conflict_hist_seconds",
		Help:       "h",
		LabelNames: []string{"x", "y"},
	})
	if err == nil {
		t.Fatal("expected error for conflicting histogram descriptor, got nil")
	}
	if !strings.Contains(err.Error(), "ERR_ADAPTER_PROM_REGISTER") {
		t.Fatalf("error should be ErrAdapterPromRegister, got: %v", err)
	}
}

// TestMetricProvider_CounterVec_DifferentLabelNamesErrors verifies that
// re-registering a CounterVec with different label names returns
// ErrAdapterPromRegister (Prometheus descriptor conflict path).
func TestMetricProvider_CounterVec_DifferentLabelNamesErrors(t *testing.T) {
	p, _ := newTestProvider(t)
	_, err := p.CounterVec(metrics.CounterOpts{
		Name:       "label_conflict_counter_total",
		Help:       "h",
		LabelNames: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("first CounterVec: %v", err)
	}
	// Different label names → Prometheus descriptor conflict.
	_, err = p.CounterVec(metrics.CounterOpts{
		Name:       "label_conflict_counter_total",
		Help:       "h",
		LabelNames: []string{"x", "y"},
	})
	if err == nil {
		t.Fatal("expected error for conflicting counter descriptor, got nil")
	}
	if !strings.Contains(err.Error(), "ERR_ADAPTER_PROM_REGISTER") {
		t.Fatalf("error should be ErrAdapterPromRegister, got: %v", err)
	}
}

// TestMetricProvider_CounterVec_ExistingCollectorTypeMismatch verifies that
// re-registering a name that was registered as a HistogramVec (not CounterVec)
// returns an ErrAdapterPromRegister cast-fail error.
func TestMetricProvider_CounterVec_ExistingCollectorTypeMismatch(t *testing.T) {
	// Use an isolated registry so we can register the same name as histogram first.
	reg := prom.NewRegistry()
	p1, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{
		Registry:  reg,
		Namespace: "typemismatch",
	})
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}
	// Register as histogram via the underlying prom registry directly so that
	// the provider's CounterVec call encounters an existing *prom.HistogramVec.
	hv := prom.NewHistogramVec(prom.HistogramOpts{
		Namespace: "typemismatch",
		Name:      "shared_name_total",
		Help:      "h",
	}, []string{"l"})
	if err := reg.Register(hv); err != nil {
		t.Fatalf("pre-register histogram: %v", err)
	}
	// Now CounterVec on the same name must encounter type mismatch.
	_, err = p1.CounterVec(metrics.CounterOpts{
		Name:       "shared_name_total",
		Help:       "h",
		LabelNames: []string{"l"},
	})
	if err == nil {
		t.Fatal("expected type mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("error should mention type mismatch, got: %v", err)
	}
}

// TestMetricProvider_HistogramVec_ExistingCollectorTypeMismatch mirrors
// TestMetricProvider_CounterVec_ExistingCollectorTypeMismatch for HistogramVec:
// pre-register a CounterVec, then attempt HistogramVec on the same name.
func TestMetricProvider_HistogramVec_ExistingCollectorTypeMismatch(t *testing.T) {
	reg := prom.NewRegistry()
	p1, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{
		Registry:  reg,
		Namespace: "histtypemismatch",
	})
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}
	cv := prom.NewCounterVec(prom.CounterOpts{
		Namespace: "histtypemismatch",
		Name:      "shared_hist_total",
		Help:      "h",
	}, []string{"l"})
	if err := reg.Register(cv); err != nil {
		t.Fatalf("pre-register counter: %v", err)
	}
	_, err = p1.HistogramVec(metrics.HistogramOpts{
		Name:       "shared_hist_total",
		Help:       "h",
		LabelNames: []string{"l"},
	})
	if err == nil {
		t.Fatal("expected type mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("error should mention type mismatch, got: %v", err)
	}
}

// TestMetricProvider_CounterVec_CrossProvider_ReuseWithoutLabelCheck verifies
// that when a second provider encounters an AlreadyRegisteredError for a name
// registered by a DIFFERENT provider (i.e., the existing *prom.CounterVec is
// NOT in the second provider's vecs map), lookupCounterVecLabels returns nil
// and the collector is reused without label validation — the safe fallback.
// This exercises the "return nil" branch in lookupCounterVecLabels.
func TestMetricProvider_CounterVec_CrossProvider_ReuseWithoutLabelCheck(t *testing.T) {
	reg := prom.NewRegistry()
	p1, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{Registry: reg, Namespace: "cross"})
	if err != nil {
		t.Fatalf("p1: %v", err)
	}
	p2, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{Registry: reg, Namespace: "cross"})
	if err != nil {
		t.Fatalf("p2: %v", err)
	}
	opts := metrics.CounterOpts{Name: "shared_counter_total", Help: "h", LabelNames: []string{"l"}}
	cv1, err := p1.CounterVec(opts)
	if err != nil {
		t.Fatalf("p1.CounterVec: %v", err)
	}
	// p2 encounters AlreadyRegisteredError; existing is not in p2's vecs map
	// → lookupCounterVecLabels returns nil → reuse without label check.
	cv2, err := p2.CounterVec(opts)
	if err != nil {
		t.Fatalf("p2.CounterVec (cross-provider reuse) must succeed, got: %v", err)
	}
	// Both vecs must share the same underlying collector.
	cv1.With(metrics.Labels{"l": "v"}).Inc()
	cv2.With(metrics.Labels{"l": "v"}).Inc()
	if cnt := testutil.CollectAndCount(reg, "cross_shared_counter_total"); cnt != 1 {
		t.Fatalf("expected 1 series, got %d", cnt)
	}
}

// TestMetricProvider_HistogramVec_CrossProvider_ReuseWithoutLabelCheck mirrors
// the counter version for HistogramVec, exercising the "return nil" branch in
// lookupHistogramVecLabels.
func TestMetricProvider_HistogramVec_CrossProvider_ReuseWithoutLabelCheck(t *testing.T) {
	reg := prom.NewRegistry()
	p1, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{Registry: reg, Namespace: "crosshist"})
	if err != nil {
		t.Fatalf("p1: %v", err)
	}
	p2, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{Registry: reg, Namespace: "crosshist"})
	if err != nil {
		t.Fatalf("p2: %v", err)
	}
	opts := metrics.HistogramOpts{Name: "shared_hist_seconds", Help: "h", LabelNames: []string{"l"}}
	hv1, err := p1.HistogramVec(opts)
	if err != nil {
		t.Fatalf("p1.HistogramVec: %v", err)
	}
	hv2, err := p2.HistogramVec(opts)
	if err != nil {
		t.Fatalf("p2.HistogramVec (cross-provider reuse) must succeed, got: %v", err)
	}
	hv1.With(metrics.Labels{"l": "v"}).Observe(1.0)
	hv2.With(metrics.Labels{"l": "v"}).Observe(2.0)
	if cnt := testutil.CollectAndCount(reg, "crosshist_shared_hist_seconds"); cnt != 1 {
		t.Fatalf("expected 1 series, got %d", cnt)
	}
}

// TestMetricProvider_HistogramVec_DifferentLabelNames_DescriptorConflict verifies
// that conflicting HistogramVec descriptor produces an ErrAdapterPromRegister error.
func TestMetricProvider_HistogramVec_DifferentLabelNames_DescriptorConflict(t *testing.T) {
	p, _ := newTestProvider(t)
	_, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "desc_conflict_hist_seconds",
		Help:       "h",
		LabelNames: []string{"cat", "dog"},
	})
	if err != nil {
		t.Fatalf("first HistogramVec: %v", err)
	}
	// Different label count → Prometheus descriptor conflict.
	_, err = p.HistogramVec(metrics.HistogramOpts{
		Name:       "desc_conflict_hist_seconds",
		Help:       "h",
		LabelNames: []string{"x"},
	})
	if err == nil {
		t.Fatal("expected descriptor conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "ERR_ADAPTER_PROM_REGISTER") {
		t.Fatalf("error should be ErrAdapterPromRegister, got: %v", err)
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
