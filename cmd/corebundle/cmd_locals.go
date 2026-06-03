package main

import (
	"net/http"
	"sync"

	prom "github.com/prometheus/client_golang/prometheus"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
)

// cmdLocals holds cmd-private wiring that is NOT on composition.SharedDeps —
// prometheus adapter types, internal guard, claimer kind, pool MR, and metrics
// HTTP handler. Platform cell modules read exclusively from composition.SharedDeps.
//
// Fields that the composition layer needs (MetricsProvider, JWTIssuer/Verifier,
// InternalHMACRing, PG, Redis, ConsumerClaimer, etc.) are on composition.SharedDeps.
// Fields that only cmd-layer functions (defaultRuntimeOptions, buildAssembly,
// runCorebundle defers, log functions) need are here.
type cmdLocals struct {
	// registry is the isolated per-run prometheus registry.
	registry *prom.Registry

	// hookObserver records cell lifecycle hook latencies.
	hookObserver *promadapter.HookObserver

	// metricProvider is the prometheus-backed metrics.Provider; also stored in
	// composition.SharedDeps.MetricsProvider as the interface value.
	metricProvider *promadapter.MetricProvider

	// poolMR is the postgres pool as a ManagedResource, registered first by
	// runtimeBaseOptions for LIFO last-close.
	poolMR kernellifecycle.ManagedResource

	// metricsHandler is the Prometheus HTTP handler.
	metricsHandler http.Handler

	// redisClient holds the raw redis client constructed in LoadSharedDepsFromEnv
	// (buildSharedReplayDeps). Used by provisionRedis to wrap into Redis capability
	// provider. Unexported composition-root plumbing — public consumers use
	// composition.SharedDeps.Redis.Client().
	redisClient *adapterredis.Client

	// vaultTransitMetrics is a sync.Once-guarded factory for the vault-transit
	// metric set. Used by buildConfigCoreKeyProvider in configcore_key_provider.go
	// so that local-aes / memory deployments never register gocell_vault_* series.
	vaultTransitMetrics func() (*adaptervault.TransitMetrics, error)

	// vaultMetricsOnce / vaultMetrics / vaultMetricsErr implement the lazy
	// once-guarded pattern for the vault-transit metric set. Kept here (cmd)
	// alongside the *prom.Registry so the registry never leaks into
	// composition.SharedDeps.
	vaultMetricsOnce sync.Once
	vaultMetrics     *adaptervault.TransitMetrics
	vaultMetricsErr  error
}

// initVaultMetricsFactory sets up the lazy vault-transit metrics factory on
// locals. Must be called after locals.registry is assigned.
func (l *cmdLocals) initVaultMetricsFactory() {
	l.vaultTransitMetrics = func() (*adaptervault.TransitMetrics, error) {
		l.vaultMetricsOnce.Do(func() {
			m, err := adaptervault.NewTransitMetrics(l.registry)
			if err != nil {
				l.vaultMetricsErr = err
				return
			}
			l.vaultMetrics = m
		})
		return l.vaultMetrics, l.vaultMetricsErr
	}
}
