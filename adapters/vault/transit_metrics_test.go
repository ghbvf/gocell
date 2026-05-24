package vault

import (
	"errors"
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestNewTransitMetrics_RegistersAllCollectors(t *testing.T) {
	reg := prom.NewRegistry()
	m, err := NewTransitMetrics(reg)
	if err != nil {
		t.Fatalf("NewTransitMetrics: %v", err)
	}
	if m == nil {
		t.Fatal("NewTransitMetrics returned nil metrics on success")
	}

	// Seed loginOutcome so its CounterVec family appears in Gather() (CounterVec
	// families are absent until at least one labelled child is observed).
	m.loginOutcome.WithLabelValues("token", "success", "none").Add(0)

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
	if v := testutil.ToFloat64(m.authHealthy); v != 0 {
		t.Errorf("authHealthy at construction = %v, want 0 (worker starts at 0; transitions to 1 only after Start)", v)
	}
}

func TestNewTransitMetrics_DuplicateRegistrationFails(t *testing.T) {
	reg := prom.NewRegistry()
	if _, err := NewTransitMetrics(reg); err != nil {
		t.Fatalf("first NewTransitMetrics: %v", err)
	}
	_, err := NewTransitMetrics(reg)
	if err == nil {
		t.Fatal("second NewTransitMetrics on same registry: want error, got nil")
	}
	var are prom.AlreadyRegisteredError
	if !errors.As(err, &are) {
		t.Errorf("want AlreadyRegisteredError in chain; got %v (%T)", err, err)
	}
	if !strings.Contains(err.Error(), "register transit metric") {
		t.Errorf("error message should identify vault transit metric registration site; got %q", err.Error())
	}
}

func TestTransitMetrics_StoreCachedVersion_GaugeReflectsValue(t *testing.T) {
	reg := prom.NewRegistry()
	m, err := NewTransitMetrics(reg)
	if err != nil {
		t.Fatalf("NewTransitMetrics: %v", err)
	}

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
	reg := prom.NewRegistry()
	m, err := NewTransitMetrics(reg)
	if err != nil {
		t.Fatalf("NewTransitMetrics: %v", err)
	}

	// Provider A activity.
	m.renewSuccess.Inc()
	m.renewSuccess.Inc()
	m.renewSuccess.Inc()
	m.renewFailure.Inc()
	m.loginOutcome.WithLabelValues("approle", "success", "none").Inc()
	m.StoreCachedVersion(5)

	// "Replace provider" — nothing happens at the registry level; the new
	// provider simply receives the same *TransitMetrics pointer. Modelled by
	// continuing to drive the same metric set.
	m.renewSuccess.Inc()
	m.renewSuccess.Inc()
	m.loginOutcome.WithLabelValues("approle", "success", "none").Inc()
	m.loginOutcome.WithLabelValues("approle", "failure", "transient").Inc()
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

// scrapeGauge returns the value of a single-sample Gauge / GaugeFunc family.
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
// label set exactly. Used by tests that need to inspect labelled counters
// without depending on textfile format helpers.
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
