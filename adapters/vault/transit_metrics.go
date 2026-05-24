package vault

// Package-level: TransitMetrics is the SOLE allowed callsite of
// adapters/prometheus.{NewCounter,NewCounterVec,NewGauge,NewGaugeFunc} within
// the vault adapter. New transit-key-provider metric instruments MUST be added
// here, NOT in transit_provider.go or other adapter files. Enforced by
// METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 (tools/archtest/observability_metrics_test.go).
//
// Lifetime invariant: every TransitMetrics field is registered with the owning
// Registerer exactly once, at construction time, before any provider uses it.
// This eliminates the per-Provide unregister/re-register dance that broke
// counter accumulation in multi-Provide integration-test scenarios (#879).
// Provider replacement does NOT touch the registry — the new provider shares
// the same *TransitMetrics via reference, so renew counters keep accumulating
// across rebuilds and the cached-version Gauge reads from a single physical
// atomic shared between the metric and whichever provider currently writes it.
//
// Funnel rating per .claude/rules/gocell/ai-robust.md §"Funnel 双向锁评级":
//   - Downstream Hard via METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 (form
//     uniqueness on (pkgPath, name) via *types.Info; alias/dot-import collapse).
//   - Upstream Medium (file-path allowlist is archtest-bound, not type-system).
//     Hard upgrade tracked by gh issue #885 (vault migrates loginOutcome to
//     kernel/observability/metrics.Provider).

import (
	"fmt"
	"sync/atomic"

	prom "github.com/prometheus/client_golang/prometheus"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
)

// TransitMetrics owns every Prometheus collector exposed by the Vault transit
// key provider. Construct once per *prom.Registerer (composition-root scope);
// share the pointer across every TransitKeyProvider built against the same
// SharedDeps. Replacing the provider does NOT re-register collectors.
//
// Concurrency: all fields are safe for concurrent read/write from worker
// goroutines (Counter / Gauge / CounterVec are sync; cachedVersion is atomic).
type TransitMetrics struct {
	renewSuccess  prom.Counter
	renewFailure  prom.Counter
	authHealthy   prom.Gauge
	loginOutcome  *prom.CounterVec
	cachedVersion atomic.Int64
}

// NewTransitMetrics constructs and registers all five vault-transit collectors
// with reg. Returns a *TransitMetrics for callers to pass into
// NewTransitKeyProvider / NewTransitKeyProviderFromEnv.
//
// authHealthy starts at 0 (not 1) — the renewal worker transitions it to 1
// after a successful initial Start. Non-renewable token deployments never
// start the worker, so the gauge correctly remains 0 (a true "no healthy
// renewer" signal, replacing the prior false-green where construction set 1
// unconditionally).
//
// Returns an error if any collector fails to register (typically
// AlreadyRegisteredError when the same metric name was previously registered
// on reg by another path).
func NewTransitMetrics(reg prom.Registerer) (*TransitMetrics, error) {
	m := &TransitMetrics{
		renewSuccess: promadapter.NewCounter(prom.CounterOpts{
			Namespace: "gocell",
			Subsystem: "vault",
			Name:      "token_renew_success_total",
			Help:      "Number of successful Vault token renewals.",
		}),
		renewFailure: promadapter.NewCounter(prom.CounterOpts{
			Namespace: "gocell",
			Subsystem: "vault",
			Name:      "token_renew_failure_total",
			Help:      "Number of Vault token renewal failures.",
		}),
		authHealthy: promadapter.NewGauge(prom.GaugeOpts{
			Namespace: "gocell",
			Subsystem: "vault",
			Name:      "token_auth_healthy",
			Help:      "1 when the Vault token renewal worker is healthy; 0 before the worker starts or while re-authenticating after a terminal renewal failure.",
		}),
		loginOutcome: promadapter.NewCounterVec(prom.CounterOpts{
			Namespace: "gocell",
			Subsystem: "vault",
			Name:      "auth_login_total",
			Help:      "Count of Vault auth Login attempts.",
		}, []string{"method", "result", "reason"}),
	}
	cachedVersionGauge := promadapter.NewGaugeFunc(prom.GaugeOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "cached_key_version",
		Help:      "Latest Vault Transit key version cached by this process; 0 means cache miss.",
	}, func() float64 { return float64(m.cachedVersion.Load()) })

	for _, c := range []prom.Collector{
		m.renewSuccess, m.renewFailure, m.authHealthy, m.loginOutcome, cachedVersionGauge,
	} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("vault: register transit metric: %w", err)
		}
	}
	return m, nil
}

// StoreCachedVersion writes v to the shared cached-version atomic that the
// gocell_vault_cached_key_version GaugeFunc reads. Provider write sites for
// the cache go through this funnel to keep "version cache" and "metric read"
// as a single physical variable — no double-write to mirror is needed.
func (m *TransitMetrics) StoreCachedVersion(v int64) { m.cachedVersion.Store(v) }

// LoadCachedVersion returns the current value of the cached-version atomic.
// Provider read sites use this in place of a separate per-instance cache.
func (m *TransitMetrics) LoadCachedVersion() int64 { return m.cachedVersion.Load() }
