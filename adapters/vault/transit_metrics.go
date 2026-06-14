package vault

// Package-level: TransitMetrics is the SOLE vault-transit metric construction
// site. New transit-key-provider metric instruments MUST be added here, NOT in
// transit_provider.go or other adapter files.
//
// All instruments are built through the kernel-neutral
// github.com/ghbvf/gocell/framework/kernel/observability/metrics.Provider — the vault
// adapter no longer imports github.com/prometheus/client_golang directly (#885).
// The bare (dimensionless) counters/gauge are modeled as zero-label vecs whose
// single child is pre-bound once here via With(empty Labels); call sites then use
// the bound Counter/Gauge directly. The kernel Provider is deliberately vec-only
// (Prometheus-style: label names at registration, label map at record), so a
// zero-label vec is its representation of an unlabeled metric.
//
// Lifetime invariant: every TransitMetrics instrument is registered with the
// owning Provider exactly once, at construction time, before any provider uses
// it. This eliminates the per-Provide unregister/re-register dance that broke
// counter accumulation in multi-Provide integration-test scenarios (#879).
// Provider replacement does NOT touch the registry — the new transit provider
// shares the same *TransitMetrics via reference, so renew counters keep
// accumulating across rebuilds and the cached-version gauge reads from a single
// physical atomic shared between the metric and whichever provider currently
// writes it.
//
// Reconstruction (a second NewTransitMetrics on the SAME provider, e.g. a
// composition rebuild that re-runs Module().Provide) is non-destructive to
// registry-observable state: GaugeVec/CounterVec reuse the already-registered
// collector, construction performs NO explicit Set(0) (see NewTransitMetrics),
// so counters keep accumulating and gauges retain their last-written value. The
// per-call cachedVersion atomic is an instance-local read cache, not the source
// of truth — the new instance re-seeds it from the worker's first key read; the
// gauge is the durable point-in-time value. In production Provide runs exactly
// once, so a single instance owns the lifecycle.
//
// ref: opentelemetry-specification metrics/api.md — subscribe-to-change value
// → synchronous (push) gauge (cachedVersion).

import (
	"context"
	"sync/atomic"

	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// errMsgRegisterTransitMetric is the errcode.Wrap message used by every
// NewTransitMetrics instrument registration failure. MESSAGE-CONST-LITERAL-01
// permits a const identifier referencing a string literal.
const errMsgRegisterTransitMetric = "vault: register transit metric"

// loginOutcomeLabels is the ordered label set of the gocell_vault_auth_login_total
// CounterVec. Record sites build a metrics.Labels map keyed by exactly these names.
var loginOutcomeLabels = []string{"method", "result", "reason"}

// TransitMetrics owns every metric instrument exposed by the Vault transit key
// provider. Construct once per metrics.Provider (composition-root scope); share
// the pointer across every TransitKeyProvider built against the same provider.
// Replacing the provider does NOT re-register instruments.
//
// Concurrency: all fields are safe for concurrent read/write from worker
// goroutines (the kernel metric instruments are sync; cachedVersion is atomic).
//
// Single-mount invariant: cached_key_version exposes the active provider's cache
// value with no labels (one process owns one Vault Transit mount + key today).
// Multi-key support would need a labeled gauge keyed by mount_path / key_name;
// that's an explicit schema bump, not an implicit migration.
type TransitMetrics struct {
	renewSuccess       metrics.Counter
	renewFailure       metrics.Counter
	authHealthy        metrics.Gauge
	loginOutcome       metrics.CounterVec
	cachedVersionGauge metrics.Gauge // push-updated by StoreCachedVersion (subscribe-to-change → sync gauge)
	cachedVersion      atomic.Int64
}

// NewTransitMetrics constructs and registers all five vault-transit instruments
// on provider. Returns a *TransitMetrics for callers to pass into
// NewTransitKeyProvider / NewTransitKeyProviderFromEnv.
//
// Metric names are unchanged from the pre-#885 raw-prometheus construction: the
// provider applies the "gocell" namespace, and each Name folds in the former
// "vault" subsystem (e.g. "vault_token_renew_success_total" →
// gocell_vault_token_renew_success_total).
//
// authHealthy starts at 0 (not 1) — the renewal worker transitions it to 1 after
// a successful initial Start. Non-renewable token deployments never start the
// worker, so the gauge correctly remains 0 (a true "no healthy renewer" signal,
// replacing the prior false-green where construction set 1 unconditionally).
//
// cachedVersionGauge starts at 0 on first construction (cache miss; the
// .With(empty Labels) call materializes the zero-label child at 0 — no explicit
// Set is performed) and is then push-updated by StoreCachedVersion. This
// replaces the former scrape-time GaugeFunc callback: vault owns the write
// funnel (StoreCachedVersion), so per the OTel metrics API guidance a
// subscribe-to-change value is a synchronous (push) gauge, not an accessor-read
// async/callback gauge.
//
// Returns an error if provider is nil or any instrument fails to register.
func NewTransitMetrics(provider metrics.Provider) (*TransitMetrics, error) {
	if validation.IsNilInterface(provider) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"vault: NewTransitMetrics requires a non-nil metrics.Provider")
	}

	renewSuccessVec, err := provider.CounterVec(metrics.CounterOpts{
		Name: "vault_token_renew_success_total",
		Help: "Number of successful Vault token renewals.",
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			errMsgRegisterTransitMetric, err)
	}
	renewFailureVec, err := provider.CounterVec(metrics.CounterOpts{
		Name: "vault_token_renew_failure_total",
		Help: "Number of Vault token renewal failures.",
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			errMsgRegisterTransitMetric, err)
	}
	authHealthyVec, err := provider.GaugeVec(metrics.GaugeOpts{
		Name: "vault_token_auth_healthy",
		Help: "1 when the Vault token renewal worker is healthy; 0 before the worker starts " +
			"or while re-authenticating after a terminal renewal failure.",
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			errMsgRegisterTransitMetric, err)
	}
	loginOutcome, err := provider.CounterVec(metrics.CounterOpts{
		Name:       "vault_auth_login_total",
		Help:       "Count of Vault auth Login attempts.",
		LabelNames: loginOutcomeLabels,
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			errMsgRegisterTransitMetric, err)
	}
	cachedVersionVec, err := provider.GaugeVec(metrics.GaugeOpts{
		Name: "vault_cached_key_version",
		Help: "Latest Vault Transit key version cached by this process; 0 means cache miss.",
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			errMsgRegisterTransitMetric, err)
	}

	// .With(empty Labels) materializes the single zero-label child of each
	// vec at its default value (0), so a scrape before the first worker
	// transition / cache fill sees an explicit 0 rather than an absent series —
	// no explicit Set(0) is needed. Crucially, NOT re-setting 0 here keeps a
	// second NewTransitMetrics on the SAME provider non-destructive: GaugeVec
	// reuses the already-registered collector and .With returns the existing
	// child, so a running worker's authHealthy=1 / cached version are preserved
	// across reconstruction instead of being clobbered back to 0.
	m := &TransitMetrics{
		renewSuccess:       renewSuccessVec.With(metrics.Labels{}),
		renewFailure:       renewFailureVec.With(metrics.Labels{}),
		authHealthy:        authHealthyVec.With(metrics.Labels{}),
		loginOutcome:       loginOutcome,
		cachedVersionGauge: cachedVersionVec.With(metrics.Labels{}),
	}
	return m, nil
}

// recordLoginOutcome increments the gocell_vault_auth_login_total counter for the
// given method/result/reason tuple. It is the single funnel for the labeled
// login metric so the label-key set lives in exactly one place (loginOutcomeLabels).
func (m *TransitMetrics) recordLoginOutcome(ctx context.Context, method, result, reason string) {
	m.loginOutcome.With(metrics.Labels{
		"method": method,
		"result": result,
		"reason": reason,
	}).Inc(ctx)
}

// StoreCachedVersion writes v to the shared cached-version atomic that backs
// LoadCachedVersion, and mirrors it to the gocell_vault_cached_key_version gauge
// (push model). Provider write sites go through this single funnel so "version
// cache" and "metric value" stay one physical source — no double-write to a
// separate mirror is needed. Background ctx: cache writes happen on worker /
// refresh paths with no request correlation.
func (m *TransitMetrics) StoreCachedVersion(v int64) {
	m.cachedVersion.Store(v)
	m.cachedVersionGauge.Set(context.Background(), float64(v))
}

// LoadCachedVersion returns the current value of the cached-version atomic.
// Provider read sites use this in place of a separate per-instance cache.
func (m *TransitMetrics) LoadCachedVersion() int64 { return m.cachedVersion.Load() }
