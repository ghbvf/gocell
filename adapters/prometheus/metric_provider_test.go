package prometheus_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	gcprom "github.com/ghbvf/gocell/adapters/prometheus"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics/metricstest"
)

// promNamespace mirrors newTestProvider's MetricProviderConfig.Namespace; the
// registered metric name is "<namespace>_<opts.Name>".
const promNamespace = "gocelltest_"

// TestMetricProvider_CanceledCtxConformance enrolls prometheus.MetricProvider
// in the no-skip-on-cancel conformance harness (METRICS-CANCEL-CTX-CONFORMANCE-01).
// The Prometheus adapter discards ctx entirely, so a canceled ctx can never
// suppress a write; the readback confirms the measured values regardless.
//
// The provider is constructed concretely (not via newTestProvider, which widens
// to metrics.Provider) so the archtest's per-impl enrollment scan can resolve
// the argument to *prometheus.MetricProvider.
func TestMetricProvider_CanceledCtxConformance(t *testing.T) {
	reg := prom.NewRegistry()
	provider, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{
		Registry:  reg,
		Namespace: "gocelltest",
	})
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}
	labels := prom.Labels(metricstest.Labels())
	metricstest.RunCanceledCtxConformance(t, provider, metricstest.Readback{
		Counter: func() float64 {
			return testutil.ToFloat64(collect(t, reg, promNamespace+metricstest.CounterName, labels))
		},
		Histogram: func() uint64 {
			return histogramSampleCount(t, reg, promNamespace+metricstest.HistogramName, labels)
		},
		Gauge: func() float64 {
			return testutil.ToFloat64(collectGauge(t, reg, promNamespace+metricstest.GaugeName, labels))
		},
	})
}

// histogramSampleCount gathers reg and returns the observation count for the
// histogram series matching name+labels. The shared collect helper only reads
// counters, so the cancel-ctx conformance needs this histogram-specific reader.
func histogramSampleCount(t *testing.T, reg *prom.Registry, name string, labels prom.Labels) uint64 {
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
			if promLabelsMatch(labels, m.GetLabel()) {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	t.Fatalf("no histogram %s with labels %v", name, labels)
	return 0
}

// raceConcurrency mirrors the constant in hook_observer_test (50). Kept as
// a separate const here because the two test files live in different
// packages (prometheus_test vs prometheus).
const raceConcurrency = 50

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
	cv.With(metrics.Labels{"outcome": "success"}).Inc(context.Background())
	cv.With(metrics.Labels{"outcome": "success"}).Inc(context.Background())
	cv.With(metrics.Labels{"outcome": "failure"}).Add(context.Background(), 3)

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
	hv.With(metrics.Labels{"phase": "start"}).Observe(context.Background(), 0.05)
	hv.With(metrics.Labels{"phase": "start"}).Observe(context.Background(), 2.5)

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
	cv1.With(metrics.Labels{"a": "x"}).Inc(context.Background())
	cv2.With(metrics.Labels{"a": "x"}).Inc(context.Background())
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
	cv2.With(metrics.Labels{"label": "v"}).Inc(context.Background())
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
	hv1.With(metrics.Labels{"phase": "start"}).Observe(context.Background(), 0.05)
	hv2.With(metrics.Labels{"phase": "start"}).Observe(context.Background(), 0.50)
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
	cv1.With(metrics.Labels{"l": "v"}).Inc(context.Background())
	cv2.With(metrics.Labels{"l": "v"}).Inc(context.Background())
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
	hv1.With(metrics.Labels{"l": "v"}).Observe(context.Background(), 1.0)
	hv2.With(metrics.Labels{"l": "v"}).Observe(context.Background(), 2.0)
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
			if promLabelsMatch(labels, m.GetLabel()) {
				return singletonCounter{val: m.GetCounter().GetValue()}
			}
		}
	}
	t.Fatalf("no metric %s with labels %v", name, labels)
	return nil
}

// promLabelsMatch reports whether all entries in want are present in got with
// matching values. Extra labels in got are allowed (subset match). Extracted
// from collect to reduce cognitive complexity (S3776).
func promLabelsMatch(want prom.Labels, got []*dto.LabelPair) bool {
	// Build a name→value index from got for O(1) lookup.
	gotIndex := make(map[string]string, len(got))
	for _, lp := range got {
		gotIndex[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if gotIndex[k] != v {
			return false
		}
	}
	return true
}

// TestPromLabelsMatch verifies subset-match semantics: all want entries must be
// present in got with matching values; extra labels in got are allowed.
func TestPromLabelsMatch(t *testing.T) {
	lp := func(name, value string) *dto.LabelPair {
		return &dto.LabelPair{Name: &name, Value: &value}
	}
	tests := []struct {
		name string
		want prom.Labels
		got  []*dto.LabelPair
		ok   bool
	}{
		{
			name: "exact match",
			want: prom.Labels{"k": "v"},
			got:  []*dto.LabelPair{lp("k", "v")},
			ok:   true,
		},
		{
			name: "extra label in got allowed",
			want: prom.Labels{"k": "v"},
			got:  []*dto.LabelPair{lp("k", "v"), lp("extra", "x")},
			ok:   true,
		},
		{
			name: "want label absent from got",
			want: prom.Labels{"k": "v"},
			got:  []*dto.LabelPair{lp("other", "v")},
			ok:   false,
		},
		{
			name: "empty got does not satisfy non-empty want",
			want: prom.Labels{"k": "v"},
			got:  nil,
			ok:   false,
		},
		{
			name: "empty want always matches",
			want: prom.Labels{},
			got:  []*dto.LabelPair{lp("k", "v")},
			ok:   true,
		},
		{
			name: "value mismatch",
			want: prom.Labels{"k": "v"},
			got:  []*dto.LabelPair{lp("k", "wrong")},
			ok:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := promLabelsMatch(tc.want, tc.got)
			if got != tc.ok {
				t.Errorf("promLabelsMatch(%v, %v) = %v, want %v", tc.want, tc.got, got, tc.ok)
			}
		})
	}
}

type singletonCounter struct{ val float64 }

func (s singletonCounter) Describe(ch chan<- *prom.Desc) {
	ch <- prom.NewDesc("singleton", "test helper", nil, nil)
}

func (s singletonCounter) Collect(ch chan<- prom.Metric) {
	ch <- prom.MustNewConstMetric(prom.NewDesc("singleton", "test helper", nil, nil), prom.CounterValue, s.val)
}

// TestMetricProvider_ConcurrentCounterVec_RaceDetector verifies that N
// goroutines concurrently calling CounterVec with the same opts is safe.
// Exercises registerOrReuse's AlreadyRegisteredError branch under contention:
// only the first call actually registers a new collector; subsequent calls
// return the existing one. Both paths must be race-free.
//
// Run with `go test -race`. No t.Parallel() — see golden reference
// adapters/redis/race_stress_integration_test.go.
func TestMetricProvider_ConcurrentCounterVec_RaceDetector(t *testing.T) {
	p, reg := newTestProvider(t)

	opts := metrics.CounterOpts{
		Name:       "race_total",
		Help:       "race test counter",
		LabelNames: []string{"k"},
	}

	var wg sync.WaitGroup
	var firstErr atomic.Value // error
	wg.Add(raceConcurrency)
	for i := 0; i < raceConcurrency; i++ {
		go func() {
			defer wg.Done()
			cv, err := p.CounterVec(opts)
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			cv.With(metrics.Labels{"k": "v"}).Inc(context.Background())
		}()
	}
	wg.Wait()

	if err := firstErr.Load(); err != nil {
		t.Fatalf("concurrent CounterVec returned error: %v", err)
	}

	// The N concurrent .Inc() calls share the same underlying *prom.CounterVec
	// (registerOrReuse idempotent path): exactly 1 series, sum = N.
	got := testutil.CollectAndCount(reg, "gocelltest_race_total")
	if got != 1 {
		t.Fatalf("expected exactly 1 series after concurrent register, got %d", got)
	}
	// Verify sum so a regression where Inc dropped writes (e.g. a future
	// per-call register/unregister bug) does not pass with series-count
	// alone. Gather() walks the registry directly — the abstracted
	// metrics.Counter does not implement prom.Collector so testutil.ToFloat64
	// is not directly applicable here.
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var sum float64
	for _, mf := range gathered {
		if mf.GetName() != "gocelltest_race_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
	}
	if sum != float64(raceConcurrency) {
		t.Fatalf("counter sum = %v, want %d", sum, raceConcurrency)
	}
}

// ---------------------------------------------------------------------------
// GaugeVec
// ---------------------------------------------------------------------------

// TestMetricProvider_GaugeVec_Register verifies that a fresh GaugeVec
// registration lands exactly one metric family in the registry.
func TestMetricProvider_GaugeVec_Register(t *testing.T) {
	p, reg := newTestProvider(t)

	_, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "queue_depth",
		Help:       "Queue depth.",
		LabelNames: []string{"queue"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	// No observations yet — family exists but no series until With is called.
	// CollectAndCount returns 0 for an empty GaugeVec; this is correct Prometheus
	// behavior. Registration is asserted by the absence of an error above and the
	// successful CollectAndCount call. Trigger a With() so the series is emitted.
	_ = testutil.CollectAndCount(reg, "gocelltest_queue_depth")
	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "queue_depth",
		Help:       "Queue depth.",
		LabelNames: []string{"queue"},
	})
	if err != nil {
		t.Fatalf("second GaugeVec (reuse): %v", err)
	}
	gv.With(metrics.Labels{"queue": "main"}).Set(context.Background(), 1)
	if n := testutil.CollectAndCount(reg, "gocelltest_queue_depth"); n != 1 {
		t.Fatalf("expected 1 series after Set, got %d", n)
	}
}

// TestMetricProvider_GaugeVec_SetInc verifies that Set/Inc/Dec/Add are
// forwarded to the underlying Prometheus gauge and produce the expected value.
func TestMetricProvider_GaugeVec_SetInc(t *testing.T) {
	p, reg := newTestProvider(t)

	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "workers_active",
		Help:       "Active workers.",
		LabelNames: []string{"pool"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	g := gv.With(metrics.Labels{"pool": "default"})
	g.Set(context.Background(), 10) // 10
	g.Inc(context.Background())     // 11
	g.Dec(context.Background())     // 10
	g.Add(context.Background(), 5)  // 15
	g.Add(context.Background(), -3) // 12

	if v := testutil.ToFloat64(collectGauge(t, reg, "gocelltest_workers_active", prom.Labels{"pool": "default"})); v != 12 {
		t.Fatalf("gauge value = %v, want 12", v)
	}
}

// TestMetricProvider_GaugeVec_AlreadyRegistered_Reuse verifies that a second
// GaugeVec registration with the same name returns the existing collector
// (AlreadyRegisteredError reuse path) and that writes from both handles share
// the same underlying series.
func TestMetricProvider_GaugeVec_AlreadyRegistered_Reuse(t *testing.T) {
	p, reg := newTestProvider(t)
	opts := metrics.GaugeOpts{Name: "dup_gauge", Help: "h", LabelNames: []string{"a"}}

	gv1, err := p.GaugeVec(opts)
	if err != nil {
		t.Fatalf("first GaugeVec: %v", err)
	}
	gv2, err := p.GaugeVec(opts)
	if err != nil {
		t.Fatalf("duplicate GaugeVec must succeed (return existing), got error: %v", err)
	}
	// Both write to the same underlying series; last write wins (Set semantics).
	gv1.With(metrics.Labels{"a": "x"}).Set(context.Background(), 5)
	gv2.With(metrics.Labels{"a": "x"}).Inc(context.Background()) // 6
	if v := testutil.ToFloat64(collectGauge(t, reg, "gocelltest_dup_gauge", prom.Labels{"a": "x"})); v != 6 {
		t.Fatalf("shared gauge = %v, want 6", v)
	}
}

// TestMetricProvider_GaugeVec_LabelMismatch_RegisterError verifies that a
// GaugeVec re-registration with different label names (descriptor conflict)
// returns an ErrAdapterPromRegister error — mirroring the Counter/Histogram
// descriptor-conflict path.
func TestMetricProvider_GaugeVec_LabelMismatch_RegisterError(t *testing.T) {
	p, _ := newTestProvider(t)
	_, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "label_conflict_gauge",
		Help:       "h",
		LabelNames: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("first GaugeVec: %v", err)
	}
	// Different label names → Prometheus descriptor conflict.
	_, err = p.GaugeVec(metrics.GaugeOpts{
		Name:       "label_conflict_gauge",
		Help:       "h",
		LabelNames: []string{"x", "y"},
	})
	if err == nil {
		t.Fatal("expected error for conflicting gauge descriptor, got nil")
	}
	if !strings.Contains(err.Error(), "ERR_ADAPTER_PROM_REGISTER") {
		t.Fatalf("error should be ErrAdapterPromRegister, got: %v", err)
	}
}

// TestMetricProvider_GaugeVec_Unregister verifies that Unregister removes a
// GaugeVec from both the provider's internal map and the Prometheus registry,
// allowing the same name to be re-registered without conflict.
func TestMetricProvider_GaugeVec_Unregister(t *testing.T) {
	p, reg := newTestProvider(t)

	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "unreg_gauge",
		Help:       "h",
		LabelNames: []string{"k"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}
	if err := p.Unregister(gv); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	// Re-register: must succeed (no AlreadyRegisteredError from the prom registry).
	gv2, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "unreg_gauge",
		Help:       "h",
		LabelNames: []string{"k"},
	})
	if err != nil {
		t.Fatalf("re-register after Unregister: %v", err)
	}
	gv2.With(metrics.Labels{"k": "v"}).Set(context.Background(), 1)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var seen int
	for _, f := range families {
		if strings.HasSuffix(f.GetName(), "unreg_gauge") {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("expected exactly 1 unreg_gauge metric family after re-register, got %d", seen)
	}
}

// TestMetricProvider_ConcurrentGaugeVec_RaceDetector verifies that N goroutines
// concurrently calling GaugeVec + With + Set are race-free under the -race
// detector. Exercises the registerOrReuse AlreadyRegisteredError path under
// high contention.
//
// Run with `go test -race`.
func TestMetricProvider_ConcurrentGaugeVec_RaceDetector(t *testing.T) {
	p, _ := newTestProvider(t)

	opts := metrics.GaugeOpts{
		Name:       "race_gauge",
		Help:       "race test gauge",
		LabelNames: []string{"k"},
	}

	var wg sync.WaitGroup
	var firstErr atomic.Value
	wg.Add(raceConcurrency)
	for i := 0; i < raceConcurrency; i++ {
		go func() {
			defer wg.Done()
			gv, err := p.GaugeVec(opts)
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			gv.With(metrics.Labels{"k": "v"}).Set(context.Background(), 1)
		}()
	}
	wg.Wait()

	if err := firstErr.Load(); err != nil {
		t.Fatalf("concurrent GaugeVec returned error: %v", err)
	}
}

// matchLabelPairs reports whether all label pairs in labelPairs match the wanted
// labels map. A pair is considered matching if its name is not present in labels,
// or if its value equals the wanted value.
//
// labelPairs is typed as []interface{ GetName() string; GetValue() string } to
// avoid importing the dto package in the test binary; the concrete slice element
// type is *dto.LabelPair from Gather().
func matchLabelPairs[LP interface {
	GetName() string
	GetValue() string
}](labelPairs []LP, labels prom.Labels) bool {
	for _, lp := range labelPairs {
		if v, ok := labels[lp.GetName()]; ok && v != lp.GetValue() {
			return false
		}
	}
	return true
}

// collectGauge fetches a single labeled Gauge from the registry for use with
// testutil.ToFloat64. Mirrors collect() but reads GetGauge().GetValue() instead
// of GetCounter().GetValue().
func collectGauge(t *testing.T, reg *prom.Registry, name string, labels prom.Labels) prom.Collector {
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
			if matchLabelPairs(m.GetLabel(), labels) {
				return singletonGauge{val: m.GetGauge().GetValue()}
			}
		}
	}
	t.Fatalf("no metric %s with labels %v", name, labels)
	return nil
}

type singletonGauge struct{ val float64 }

func (s singletonGauge) Describe(ch chan<- *prom.Desc) {
	ch <- prom.NewDesc("singleton_gauge", "test helper", nil, nil)
}

func (s singletonGauge) Collect(ch chan<- prom.Metric) {
	ch <- prom.MustNewConstMetric(prom.NewDesc("singleton_gauge", "test helper", nil, nil), prom.GaugeValue, s.val)
}

// ---------------------------------------------------------------------------
// TestMetricProvider_ConcurrentRegisterAndUnregister_RaceDetector (counter)
// ---------------------------------------------------------------------------

// TestMetricProvider_ConcurrentRegisterAndUnregister_RaceDetector verifies
// that interleaved CounterVec / Unregister calls do not race on the
// provider's internal vecs map (the RWMutex contract). Each goroutine
// registers a uniquely-named CounterVec and then unregisters it, with N
// goroutines running concurrently. The race detector must observe no data
// race on the vecs map's read/write boundary.
//
// Run with `go test -race`.
func TestMetricProvider_ConcurrentRegisterAndUnregister_RaceDetector(t *testing.T) {
	p, reg := newTestProvider(t)

	var wg sync.WaitGroup
	var firstErr atomic.Value // error
	wg.Add(raceConcurrency)
	for i := 0; i < raceConcurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("unique_total_%d", idx)
			cv, err := p.CounterVec(metrics.CounterOpts{
				Name:       name,
				Help:       "h",
				LabelNames: []string{"k"},
			})
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			cv.With(metrics.Labels{"k": "v"}).Inc(context.Background())
			if err := p.Unregister(cv); err != nil {
				firstErr.CompareAndSwap(nil, err)
			}
		}(i)
	}
	wg.Wait()

	if err := firstErr.Load(); err != nil {
		t.Fatalf("concurrent register/unregister returned error: %v", err)
	}

	// After all unregisters complete, Gather must succeed without panicking.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather after concurrent unregister: %v", err)
	}
	// All unique_total_* series should be gone.
	for _, mf := range families {
		if strings.HasPrefix(mf.GetName(), "gocelltest_unique_total_") {
			t.Fatalf("unique_total series leaked after unregister: %s", mf.GetName())
		}
	}
}
