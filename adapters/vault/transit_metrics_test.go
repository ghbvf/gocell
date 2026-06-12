package vault

import (
	"context"
	"errors"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// buildTestMetrics is a test helper that builds a TransitMetrics using a
// fresh Prometheus registry via promadapter.NewMetricProvider. It returns
// both the registry (for scraping) and the constructed *TransitMetrics.
// Use this when the test needs to scrape metric values from the registry.
func buildTestMetrics(t *testing.T) (*prom.Registry, *TransitMetrics) {
	t.Helper()
	reg := prom.NewRegistry()
	provider, err := promadapter.NewMetricProvider(promadapter.MetricProviderConfig{
		Registry:  reg,
		Namespace: "gocell",
	})
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}
	m, err := NewTransitMetrics(provider)
	if err != nil {
		t.Fatalf("NewTransitMetrics: %v", err)
	}
	if m == nil {
		t.Fatal("NewTransitMetrics returned nil metrics on success")
	}
	return reg, m
}

// newTestTransitMetrics is a test helper that builds a TransitMetrics using a
// fresh Prometheus registry. Use this when the test only needs a valid
// *TransitMetrics and does not need to scrape metric values from the registry.
func newTestTransitMetrics(t *testing.T) *TransitMetrics {
	t.Helper()
	_, m := buildTestMetrics(t)
	return m
}

// TestNewTransitMetrics_NilProvider_ReturnsError verifies that NewTransitMetrics
// rejects a nil metrics.Provider with a non-nil error carrying errcode.ErrInternal.
// A nil provider would silently skip registration and panic on the first metric
// write; the nil-guard must fire before any instrument construction.
func TestNewTransitMetrics_NilProvider_ReturnsError(t *testing.T) {
	var nilProvider metrics.Provider // typed nil

	m, err := NewTransitMetrics(nilProvider)

	if err == nil {
		t.Fatal("expected error for nil metrics.Provider, got nil")
	}
	if m != nil {
		t.Errorf("expected nil *TransitMetrics on error, got non-nil")
	}
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		t.Logf("error is not *errcode.Error (acceptable); message: %v", err)
	} else if ecErr.Code != errcode.ErrInternal {
		t.Errorf("errcode.Code = %v, want %v", ecErr.Code, errcode.ErrInternal)
	}
}

func TestNewTransitMetrics_RegistersAllCollectors(t *testing.T) {
	reg, m := buildTestMetrics(t)

	// Seed loginOutcome so its CounterVec family appears in Gather() (CounterVec
	// families are absent until at least one labeled child is observed).
	m.recordLoginOutcome(context.Background(), "token", "success", "none")

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("registry Gather: %v", err)
	}

	want := []string{
		"gocell_vault_token_renew_success_total",
		"gocell_vault_token_renew_failure_total",
		"gocell_vault_token_auth_healthy",
		"gocell_vault_auth_login_total",
		"gocell_vault_cached_key_version",
	}
	got := make(map[string]bool, len(families))
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("expected metric %q to be registered; got families %v", name, got)
		}
	}

	// authHealthy must default to 0 (worker transitions 0→1 after start);
	// constructing TransitMetrics without ever starting a renewal worker
	// must not produce a false-green healthy signal.
	if got := scrapeGauge(t, reg, "gocell_vault_token_auth_healthy"); got != 0 {
		t.Errorf("authHealthy at construction = %v, want 0 (worker starts at 0; transitions to 1 only after Start)", got)
	}
}

func TestTransitMetrics_StoreCachedVersion_GaugeReflectsValue(t *testing.T) {
	reg, m := buildTestMetrics(t)

	cases := []struct {
		name string
		set  int64
	}{
		{"initial-zero", 0},
		{"first-version", 7},
		{"rotated-up", 12},
		{"invalidated", 0},
		{"reseed", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m.StoreCachedVersion(tc.set)
			if got := m.LoadCachedVersion(); got != tc.set {
				t.Errorf("LoadCachedVersion = %d, want %d", got, tc.set)
			}
			if got := scrapeGauge(t, reg, "gocell_vault_cached_key_version"); got != float64(tc.set) {
				t.Errorf("cached_key_version scrape = %v, want %v", got, float64(tc.set))
			}
		})
	}
}

// TestTransitMetrics_CountersAccumulateAcrossProviderReplacement is the
// regression-lock for #879: counters / gauge persist across "provider
// replacement" because the metric ownership lives at the registry scope, not
// at provider scope. We do not need a real TransitKeyProvider — incrementing
// the metric set directly models exactly the writes that the worker performs,
// and the registry observation is the same shape as a Prometheus scrape.
func TestTransitMetrics_CountersAccumulateAcrossProviderReplacement(t *testing.T) {
	reg, m := buildTestMetrics(t)
	ctx := context.Background()

	// Provider A activity.
	m.renewSuccess.Inc(ctx)
	m.renewSuccess.Inc(ctx)
	m.renewSuccess.Inc(ctx)
	m.renewFailure.Inc(ctx)
	m.recordLoginOutcome(ctx, "approle", "success", "none")
	m.StoreCachedVersion(5)

	// "Replace provider" — nothing happens at the registry level; the new
	// provider simply receives the same *TransitMetrics pointer. Modeled by
	// continuing to drive the same metric set.
	m.renewSuccess.Inc(ctx)
	m.renewSuccess.Inc(ctx)
	m.recordLoginOutcome(ctx, "approle", "success", "none")
	m.recordLoginOutcome(ctx, "approle", "failure", "transient")
	m.StoreCachedVersion(9)

	if got := scrapeCounter(t, reg, "gocell_vault_token_renew_success_total"); got != 5 {
		t.Errorf("token_renew_success_total = %v, want 5 (cumulative across provider replacement)", got)
	}
	if got := scrapeCounter(t, reg, "gocell_vault_token_renew_failure_total"); got != 1 {
		t.Errorf("token_renew_failure_total = %v, want 1", got)
	}
	if got := scrapeGauge(t, reg, "gocell_vault_cached_key_version"); got != 9 {
		t.Errorf("cached_key_version = %v, want 9 (latest set wins)", got)
	}

	// loginOutcome CounterVec by label set.
	const metric = "gocell_vault_auth_login_total"
	successVal := scrapeCounterVec(t, reg, metric, map[string]string{"method": "approle", "result": "success", "reason": "none"})
	failureVal := scrapeCounterVec(t, reg, metric, map[string]string{"method": "approle", "result": "failure", "reason": "transient"})
	if successVal != 2 {
		t.Errorf("auth_login_total{success} = %v, want 2", successVal)
	}
	if failureVal != 1 {
		t.Errorf("auth_login_total{failure} = %v, want 1", failureVal)
	}
}

// TestNewTransitMetrics_ReuseOnDuplicateProvider is the regression lock for the
// reconstruction-safety fix: calling NewTransitMetrics twice on the SAME
// MetricProvider must (a) succeed without error and (b) NOT clobber the
// registry-observable state a running worker already wrote through the first
// instance. Construction performs no explicit Set(0); GaugeVec/CounterVec reuse
// the registered collectors, so counters accumulate and gauges retain their
// last-written value across a composition rebuild (Module().Provide re-run).
//
// If construction were to force point-in-time gauges back to 0, the asserts
// below would catch it — proving the funnel preserves worker state across
// reconstruction.
func TestNewTransitMetrics_ReuseOnDuplicateProvider(t *testing.T) {
	reg := prom.NewRegistry()
	provider, err := promadapter.NewMetricProvider(promadapter.MetricProviderConfig{
		Registry:  reg,
		Namespace: "gocell",
	})
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}
	ctx := context.Background()

	m1, err := NewTransitMetrics(provider)
	if err != nil {
		t.Fatalf("first NewTransitMetrics: %v", err)
	}
	// Drive worker-like writes through the first instance: a healthy renewal
	// worker (authHealthy=1), a cached key version, accumulated renew successes,
	// and a login outcome.
	m1.authHealthy.Set(ctx, 1)
	m1.StoreCachedVersion(7)
	m1.renewSuccess.Inc(ctx)
	m1.renewSuccess.Inc(ctx)
	m1.recordLoginOutcome(ctx, "approle", "success", "none")

	// Reconstruct on the SAME provider (models a composition rebuild re-running
	// Module().Provide). This must be non-destructive to the state above.
	m2, err := NewTransitMetrics(provider)
	if err != nil {
		t.Fatalf("second NewTransitMetrics on same provider: %v", err)
	}
	if m1 == nil || m2 == nil {
		t.Fatal("expected non-nil *TransitMetrics from both calls")
	}

	if got := scrapeGauge(t, reg, "gocell_vault_token_auth_healthy"); got != 1 {
		t.Errorf("authHealthy after reconstruction = %v, want 1 (reconstruction must not clobber a healthy worker's gauge back to 0)", got)
	}
	if got := scrapeGauge(t, reg, "gocell_vault_cached_key_version"); got != 7 {
		t.Errorf("cached_key_version after reconstruction = %v, want 7 (gauge is the durable point-in-time value)", got)
	}
	if got := scrapeCounter(t, reg, "gocell_vault_token_renew_success_total"); got != 2 {
		t.Errorf("token_renew_success_total after reconstruction = %v, want 2 (counters accumulate, never reset)", got)
	}
	if got := scrapeCounterVec(t, reg, "gocell_vault_auth_login_total",
		map[string]string{"method": "approle", "result": "success", "reason": "none"}); got != 1 {
		t.Errorf("auth_login_total{approle,success} after reconstruction = %v, want 1", got)
	}
}

// scrapeCounter returns the value of a Counter family by metric name.
func scrapeCounter(t *testing.T, reg *prom.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("registry Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		metrics := f.GetMetric()
		if len(metrics) == 0 {
			t.Fatalf("counter %q registered but has no samples", name)
		}
		return metrics[0].GetCounter().GetValue()
	}
	t.Fatalf("counter %q not found; available: %v", name, familyNames(families))
	return 0
}

// scrapeGauge returns the value of a single-sample Gauge / GaugeFunc family
// by metric name.
func scrapeGauge(t *testing.T, reg *prom.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("registry Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		metrics := f.GetMetric()
		if len(metrics) == 0 {
			t.Fatalf("gauge %q registered but has no samples", name)
		}
		return metrics[0].GetGauge().GetValue()
	}
	t.Fatalf("gauge %q not found; available: %v", name, familyNames(families))
	return 0
}

// scrapeCounterVec returns the value of a CounterVec sample matching the given
// label set exactly. Fatals if the family exists but the label set is not found.
// Fatals if the family does not exist (use tryGatherCounterVec for polling loops).
// name is kept explicit for parity with scrapeCounter/scrapeGauge and call-site
// readability even though vault exposes a single CounterVec today.
//
//nolint:unparam // metric name parameterised for parity with scrapeCounter/scrapeGauge
func scrapeCounterVec(t *testing.T, reg *prom.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("registry Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if matchLabels(m.GetLabel(), labels) {
				return m.GetCounter().GetValue()
			}
		}
		t.Fatalf("counter vec %q has no sample with labels %v", name, labels)
	}
	t.Fatalf("counter vec %q not found; available: %v", name, familyNames(families))
	return 0
}

// tryGatherCounterVec is like scrapeCounterVec but returns 0 (no fatal) when
// the metric family or label set is not yet present. Use this in polling
// condition lambdas where the metric may not exist on the first few polls.
func tryGatherCounterVec(reg *prom.Registry, name string, labels map[string]string) float64 {
	families, err := reg.Gather()
	if err != nil {
		return 0
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if matchLabels(m.GetLabel(), labels) {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// tryGatherGauge is like scrapeGauge but returns 0 (no fatal) when the metric
// family is not yet present. Use this in polling condition lambdas.
func tryGatherGauge(reg *prom.Registry, name string) float64 {
	families, err := reg.Gather()
	if err != nil {
		return 0
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		m := f.GetMetric()
		if len(m) == 0 {
			return 0
		}
		return m[0].GetGauge().GetValue()
	}
	return 0
}

// tryGatherCounter is like scrapeCounter but returns 0 (no fatal) when the
// metric family is not yet present. Use this in polling condition lambdas.
func tryGatherCounter(reg *prom.Registry, name string) float64 {
	families, err := reg.Gather()
	if err != nil {
		return 0
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		m := f.GetMetric()
		if len(m) == 0 {
			return 0
		}
		return m[0].GetCounter().GetValue()
	}
	return 0
}

func matchLabels(pairs []*dto.LabelPair, want map[string]string) bool {
	if len(pairs) != len(want) {
		return false
	}
	for _, p := range pairs {
		if want[p.GetName()] != p.GetValue() {
			return false
		}
	}
	return true
}

func familyNames(families []*dto.MetricFamily) []string {
	names := make([]string, 0, len(families))
	for _, f := range families {
		names = append(names, f.GetName())
	}
	return names
}
