package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

type sharedReplayDeps struct {
	RedisClient         *adapterredis.Client
	NonceStore          kauth.NonceStore
	ConsumerClaimer     idempotency.Claimer
	ConsumerClaimerKind consumerClaimerKind
}

type sharedMetricsDeps struct {
	PromStack              promStack
	ConfigEventCollector   obmetrics.ConfigEventCollector
	EventbusCacheCollector obmetrics.EventbusCacheCollector
	VaultTransitMetrics    *adaptervault.TransitMetrics
}

// buildSharedMetricsDeps assembles the per-process metric set. Failure here
// is composition-root fatal (the parent caller, LoadSharedDepsFromEnv, returns
// the error and the process exits), so no LIFO rollback is needed — every
// constructed sub-resource holds only in-memory state (promStack wraps a
// *prom.Registry value; collectors register on it but the registry itself has
// no Close). The contrast with buildSharedReplayDeps is intentional: that
// function owns a Redis client whose connection pool requires explicit close
// on partial failure, whereas no resource here outlives the process exit.
func buildSharedMetricsDeps() (sharedMetricsDeps, error) {
	ps, err := buildPromStack()
	if err != nil {
		return sharedMetricsDeps{}, err
	}
	configEventCollector, err := obmetrics.NewProviderConfigEventCollector(ps.metricProvider)
	if err != nil {
		return sharedMetricsDeps{}, fmt.Errorf("build config event metrics collector: %w", err)
	}
	eventbusCacheCollector, err := obmetrics.NewProviderEventbusCacheCollector(ps.metricProvider)
	if err != nil {
		return sharedMetricsDeps{}, fmt.Errorf("build eventbus cache metrics collector: %w", err)
	}
	vtm, err := adaptervault.NewTransitMetrics(ps.registry)
	if err != nil {
		return sharedMetricsDeps{}, fmt.Errorf("build vault transit metrics: %w", err)
	}
	return sharedMetricsDeps{
		PromStack:              ps,
		ConfigEventCollector:   configEventCollector,
		EventbusCacheCollector: eventbusCacheCollector,
		VaultTransitMetrics:    vtm,
	}, nil
}

func buildSharedReplayDeps(ctx context.Context, topo bootstrap.Topology, clk clock.Clock) (sharedReplayDeps, error) {
	redisResult, err := buildRedisClient(ctx, topo)
	if err != nil {
		return sharedReplayDeps{}, err
	}
	redisClient := redisResult.Client
	loaded := false
	defer func() {
		if !loaded {
			closeRedisClientAfterFailedLoad(ctx, redisClient)
		}
	}()

	nonceStore, err := buildServiceNonceStore(topo, redisClient, clk)
	if err != nil {
		return sharedReplayDeps{}, err
	}
	claimer, claimerKind, err := buildConsumerClaimer(topo, redisClient, clk)
	if err != nil {
		return sharedReplayDeps{}, err
	}

	loaded = true
	return sharedReplayDeps{
		RedisClient:         redisClient,
		NonceStore:          nonceStore,
		ConsumerClaimer:     claimer,
		ConsumerClaimerKind: claimerKind,
	}, nil
}

// resolveListenerAddrs returns primary / internal / health bind addresses,
// applying default ports when the matching env var is unset:
//
//   - primary  → `:8080`
//   - internal → `127.0.0.1:9090` (loopback by default; service-token gated
//     in every mode; operators binding to a VPC interface must set
//     GOCELL_HTTP_INTERNAL_ADDR explicitly)
//   - health   → `127.0.0.1:9091` (separate loopback port; real-mode
//     PodIP/Service probes must set a Pod-reachable bind such as `:9091`,
//     or explicitly opt into same-netns access with GOCELL_HTTP_HEALTH_LOCAL_ONLY=1)
//
// See docs/ops/listener-topology.md for the deployment topology, threat boundaries,
// and single-listener migration guide that consume these envvars.
func resolveListenerAddrs() (primary, internal, health string) {
	primary = os.Getenv("GOCELL_HTTP_PRIMARY_ADDR")
	if primary == "" {
		primary = ":8080"
	}
	internal = os.Getenv("GOCELL_HTTP_INTERNAL_ADDR")
	if internal == "" {
		internal = "127.0.0.1:9090"
	}
	health = os.Getenv("GOCELL_HTTP_HEALTH_ADDR")
	if health == "" {
		health = "127.0.0.1:9091"
	}
	return
}

// closeRedisClientAfterFailedLoad is the single source of truth for "close
// Redis with nil-safe + slog warn". Two callers, both following the same
// `if !loaded { close }` defer pattern (one inside buildSharedReplayDeps for
// inner-construction failure, one in LoadSharedDepsFromEnv for
// outer-composition failure after replay deps are already attached). The
// structure is mirrored at both sites so the two scopes can be visually
// compared in one read.
func closeRedisClientAfterFailedLoad(ctx context.Context, client *adapterredis.Client) {
	if client == nil {
		return
	}
	if closeErr := client.Close(ctx); closeErr != nil {
		slog.Warn("corebundle: failed to close Redis client after startup validation failure",
			slog.Any("error", closeErr))
	}
}

func adapterInfoForSharedDeps(shared *SharedDeps) map[string]string {
	info := shared.Topology.AdapterInfo()
	redisState := "not-configured"
	if shared.RedisClient != nil {
		redisState = "configured"
	}
	nonceStoreKind := string(kauth.NonceStoreKindNoop)
	if shared.InternalGuard != nil && shared.InternalGuard.NonceStore() != nil {
		nonceStoreKind = string(shared.InternalGuard.NonceStore().Kind())
	}
	claimerKind := string(shared.ConsumerClaimerKind)
	if claimerKind == "" {
		claimerKind = string(consumerClaimerKindUnknown)
	}
	info["redis"] = redisState
	info["service_token_nonce_store"] = nonceStoreKind
	info["outbox_consumer_claimer"] = claimerKind
	return info
}
