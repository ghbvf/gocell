package vault

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// buildTestMetrics is a test helper that builds a TransitMetrics using an
// adapter-local recordingProvider (no adapters/prometheus import; #1909).
// It returns both the provider (for scraping) and the constructed *TransitMetrics.
// Use this when the test needs to read back metric values.
func buildTestMetrics(t *testing.T) (*recordingProvider, *TransitMetrics) {
	t.Helper()
	rec := newRecordingProvider("gocell")
	m, err := NewTransitMetrics(rec)
	if err != nil {
		t.Fatalf("NewTransitMetrics: %v", err)
	}
	if m == nil {
		t.Fatal("NewTransitMetrics returned nil metrics on success")
	}
	return rec, m
}

// newTestTransitMetrics is a test helper that builds a TransitMetrics using a
// fresh recordingProvider. Use this when the test only needs a valid
// *TransitMetrics and does not need to read back metric values.
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
	rec, m := buildTestMetrics(t)

	// All families are registered at NewTransitMetrics construction (the fake
	// records the family name in CounterVec/GaugeVec); this call additionally
	// exercises the labeled counter path.
	m.recordLoginOutcome(context.Background(), "token", "success", "none")

	want := []string{
		"gocell_vault_token_renew_success_total",
		"gocell_vault_token_renew_failure_total",
		"gocell_vault_token_auth_healthy",
		"gocell_vault_auth_login_total",
		"gocell_vault_cached_key_version",
	}
	for _, name := range want {
		if !rec.registered(name) {
			t.Errorf("expected metric %q to be registered", name)
		}
	}

	// authHealthy must default to 0 (worker transitions 0→1 after start);
	// constructing TransitMetrics without ever starting a renewal worker
	// must not produce a false-green healthy signal.
	if got := scrapeGauge(t, rec, "gocell_vault_token_auth_healthy"); got != 0 {
		t.Errorf("authHealthy at construction = %v, want 0 (worker starts at 0; transitions to 1 only after Start)", got)
	}
}

func TestTransitMetrics_StoreCachedVersion_GaugeReflectsValue(t *testing.T) {
	rec, m := buildTestMetrics(t)

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
			if got := scrapeGauge(t, rec, "gocell_vault_cached_key_version"); got != float64(tc.set) {
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
// and the recorded observation is the same shape as a Prometheus scrape.
func TestTransitMetrics_CountersAccumulateAcrossProviderReplacement(t *testing.T) {
	rec, m := buildTestMetrics(t)
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

	if got := scrapeCounter(t, rec, "gocell_vault_token_renew_success_total"); got != 5 {
		t.Errorf("token_renew_success_total = %v, want 5 (cumulative across provider replacement)", got)
	}
	if got := scrapeCounter(t, rec, "gocell_vault_token_renew_failure_total"); got != 1 {
		t.Errorf("token_renew_failure_total = %v, want 1", got)
	}
	if got := scrapeGauge(t, rec, "gocell_vault_cached_key_version"); got != 9 {
		t.Errorf("cached_key_version = %v, want 9 (latest set wins)", got)
	}

	// loginOutcome CounterVec by label set.
	const metric = "gocell_vault_auth_login_total"
	successVal := scrapeCounterVec(t, rec, metric, map[string]string{"method": "approle", "result": "success", "reason": "none"})
	failureVal := scrapeCounterVec(t, rec, metric, map[string]string{"method": "approle", "result": "failure", "reason": "transient"})
	if successVal != 2 {
		t.Errorf("auth_login_total{success} = %v, want 2", successVal)
	}
	if failureVal != 1 {
		t.Errorf("auth_login_total{failure} = %v, want 1", failureVal)
	}
}

// TestNewTransitMetrics_ReuseOnDuplicateProvider is the regression lock for the
// reconstruction-safety fix: calling NewTransitMetrics twice on the SAME
// recordingProvider must (a) succeed without error and (b) NOT clobber the
// observable state a running worker already wrote through the first instance.
// Construction performs no explicit Set(0); GaugeVec/CounterVec reuse the same
// sample entries, so counters accumulate and gauges retain their last-written
// value across a composition rebuild (Module().Provide re-run).
//
// If construction were to force point-in-time gauges back to 0, the asserts
// below would catch it — proving the funnel preserves worker state across
// reconstruction.
func TestNewTransitMetrics_ReuseOnDuplicateProvider(t *testing.T) {
	rec := newRecordingProvider("gocell")
	ctx := context.Background()

	m1, err := NewTransitMetrics(rec)
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
	m2, err := NewTransitMetrics(rec)
	if err != nil {
		t.Fatalf("second NewTransitMetrics on same provider: %v", err)
	}
	if m1 == nil || m2 == nil {
		t.Fatal("expected non-nil *TransitMetrics from both calls")
	}

	if got := scrapeGauge(t, rec, "gocell_vault_token_auth_healthy"); got != 1 {
		t.Errorf("authHealthy after reconstruction = %v, want 1 (reconstruction must not clobber a healthy worker's gauge back to 0)", got)
	}
	if got := scrapeGauge(t, rec, "gocell_vault_cached_key_version"); got != 7 {
		t.Errorf("cached_key_version after reconstruction = %v, want 7 (gauge is the durable point-in-time value)", got)
	}
	if got := scrapeCounter(t, rec, "gocell_vault_token_renew_success_total"); got != 2 {
		t.Errorf("token_renew_success_total after reconstruction = %v, want 2 (counters accumulate, never reset)", got)
	}
	if got := scrapeCounterVec(t, rec, "gocell_vault_auth_login_total",
		map[string]string{"method": "approle", "result": "success", "reason": "none"}); got != 1 {
		t.Errorf("auth_login_total{approle,success} after reconstruction = %v, want 1", got)
	}
}

// scrapeCounter returns the recorded value of a Counter family by metric name.
func scrapeCounter(t *testing.T, rec *recordingProvider, name string) float64 {
	t.Helper()
	v, ok := rec.value(name, nil)
	if !ok {
		t.Fatalf("counter %q not found", name)
	}
	return v
}

// scrapeGauge returns the recorded value of a Gauge family by metric name.
func scrapeGauge(t *testing.T, rec *recordingProvider, name string) float64 {
	t.Helper()
	v, ok := rec.value(name, nil)
	if !ok {
		t.Fatalf("gauge %q not found", name)
	}
	return v
}

// scrapeCounterVec returns the recorded value of a CounterVec sample matching the
// given label set exactly. Fatals if the label set is not found.
func scrapeCounterVec(t *testing.T, rec *recordingProvider, name string, labels map[string]string) float64 {
	t.Helper()
	v, ok := rec.value(name, labels)
	if !ok {
		t.Fatalf("counter vec %q has no sample with labels %v", name, labels)
	}
	return v
}

// tryGatherCounter is like scrapeCounter but returns 0 (no fatal) when the
// metric family is not yet present. Use this in polling condition lambdas.
func tryGatherCounter(rec *recordingProvider, name string) float64 {
	v, _ := rec.value(name, nil)
	return v
}

// tryGatherGauge is like scrapeGauge but returns 0 (no fatal) when the metric
// family is not yet present. Use this in polling condition lambdas.
func tryGatherGauge(rec *recordingProvider, name string) float64 {
	v, _ := rec.value(name, nil)
	return v
}

// tryGatherCounterVec is like scrapeCounterVec but returns 0 (no fatal) when the
// metric family or label set is not yet present. Use this in polling condition lambdas.
func tryGatherCounterVec(rec *recordingProvider, name string, labels map[string]string) float64 {
	v, _ := rec.value(name, labels)
	return v
}

// TestRecordingProvider_With_RejectsMismatchedLabels asserts the adapter-local
// recording fake enforces the same label-set contract as every real Provider:
// Vec.With panics (wrapping metrics.ErrLabelMismatch) when the supplied labels do
// not exactly cover the registered LabelNames. Without this, a test could pass
// despite label drift the production Prometheus/OTel Provider would reject.
func TestRecordingProvider_With_RejectsMismatchedLabels(t *testing.T) {
	// A non-"gocell" namespace also exercises the fake's namespace-agnostic
	// prefixing (the production callers above all use "gocell").
	rec := newRecordingProvider("test")
	cv, err := rec.CounterVec(metrics.CounterOpts{
		Name:       "labeled_total",
		LabelNames: []string{"method", "result"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	gv, err := rec.GaugeVec(metrics.GaugeOpts{
		Name:       "labeled_gauge",
		LabelNames: []string{"zone"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	cases := []struct {
		name string
		call func()
	}{
		{"counter missing key", func() { cv.With(metrics.Labels{"method": "token"}) }},
		{"counter extra key", func() { cv.With(metrics.Labels{"method": "t", "result": "ok", "x": "y"}) }},
		{"counter wrong key", func() { cv.With(metrics.Labels{"method": "t", "reason": "x"}) }},
		{"gauge missing labels", func() { gv.With(metrics.Labels{}) }},
		{"gauge wrong key", func() { gv.With(metrics.Labels{"region": "us"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected With to panic on label mismatch, got none")
				}
				rerr, ok := r.(error)
				if !ok || !errors.Is(rerr, metrics.ErrLabelMismatch) {
					t.Fatalf("panic = %v, want a value wrapping metrics.ErrLabelMismatch", r)
				}
			}()
			tc.call()
		})
	}
}

// TestRecordingProvider_With_AcceptsMatchingLabels is the anti-vacuity companion:
// the exact registered label set must NOT panic and must record through, proving
// the validation in TestRecordingProvider_With_RejectsMismatchedLabels rejects
// only genuine mismatches.
func TestRecordingProvider_With_AcceptsMatchingLabels(t *testing.T) {
	rec := newRecordingProvider("test")
	cv, err := rec.CounterVec(metrics.CounterOpts{
		Name:       "labeled_total",
		LabelNames: []string{"method", "result"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	cv.With(metrics.Labels{"method": "token", "result": "ok"}).Inc(context.Background())
	if got := scrapeCounterVec(t, rec, "test_labeled_total",
		map[string]string{"method": "token", "result": "ok"}); got != 1 {
		t.Fatalf("recorded = %v, want 1", got)
	}
}
