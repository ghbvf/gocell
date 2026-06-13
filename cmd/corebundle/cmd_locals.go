package main

import (
	"net/http"

	prom "github.com/prometheus/client_golang/prometheus"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
)

// cmdLocals holds cmd-private wiring that is NOT on composition.SharedDeps —
// prometheus adapter types, pool MR, and metrics HTTP handler. Platform cell
// modules read exclusively from composition.SharedDeps.
//
// Fields that the composition layer needs (MetricsProvider, JWTIssuer/Verifier,
// InternalHMACRing, PG, Redis, ConsumerClaimer, etc.) are on composition.SharedDeps.
// Fields that only cmd-layer functions (defaultRuntimeOptions, buildAssembly,
// runCorebundle defers, log functions) need are here.
//
// The configcore key provider (formerly vault-metrics factory + sync.Once here)
// moved to cellmodules/configcore.Module.Provide in #1413/#885: cmd no longer
// imports adapters/vault.
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

	// brokerResources are the event-transport broker resources (the RabbitMQ
	// connection in postgres mode; empty in demo mode) resolved by
	// eventtransport.Resolve. Registered among the FIRST ManagedResources (right
	// after poolMR) so LIFO teardown closes them LATE — after the relay and every
	// consumer that publishes/subscribes through the broker drain (registered later
	// via cell opts → close first), and before the PG pool closes (#1940).
	brokerResources []kernellifecycle.ManagedResource

	// metricsHandler is the Prometheus HTTP handler.
	metricsHandler http.Handler

	// redisClient holds the raw redis client constructed in LoadSharedDepsFromEnv
	// (buildSharedReplayDeps). Used by provisionRedis to wrap into Redis capability
	// provider. Unexported composition-root plumbing — public consumers use
	// composition.SharedDeps.Redis.Client().
	redisClient *adapterredis.Client
}
