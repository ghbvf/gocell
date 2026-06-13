package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

type sharedReplayDeps struct {
	RedisClient     *adapterredis.Client
	NonceStore      kauth.NonceStore
	ConsumerClaimer idempotency.Claimer
}

type sharedMetricsDeps struct {
	PromStack            promStack
	ConfigEventCollector obmetrics.ConfigEventCollector
}

// buildSharedMetricsDeps assembles framework-level always-on metric collectors
// (config events). The eventbus-cache and stale-cipher collectors moved to
// cellmodules/configcore (#1413) — they are configcore-specific and route
// through the kernel MetricsProvider, so configcore self-builds them. The
// vault-transit metrics also moved to cellmodules/configcore (#1413/#885):
// adapters/vault.NewTransitMetrics now accepts a kernel MetricsProvider
// (client_golang-free in non-test code), so cmd no longer needs a lazy
// factory for them.
//
// Failure here is composition-root fatal (LoadSharedDepsFromEnv returns the
// error and the process exits); no LIFO rollback needed because every
// constructed sub-resource is in-memory state with no Close contract.
func buildSharedMetricsDeps() (sharedMetricsDeps, error) {
	ps, err := buildPromStack()
	if err != nil {
		return sharedMetricsDeps{}, err
	}
	configEventCollector, err := obmetrics.NewProviderConfigEventCollector(ps.metricProvider)
	if err != nil {
		return sharedMetricsDeps{}, fmt.Errorf("build config event metrics collector: %w", err)
	}
	return sharedMetricsDeps{
		PromStack:            ps,
		ConfigEventCollector: configEventCollector,
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
	claimer, err := buildConsumerClaimer(topo, redisClient, clk)
	if err != nil {
		return sharedReplayDeps{}, err
	}

	loaded = true
	return sharedReplayDeps{
		RedisClient:     redisClient,
		NonceStore:      nonceStore,
		ConsumerClaimer: claimer,
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
// Redis with nil-safe + slog warn". Three callers, all following the same
// `if !ok { close }` defer pattern: one inside buildSharedReplayDeps for
// inner-construction failure, one in LoadSharedDepsFromEnv for outer-composition
// failure after replay deps are already attached, and one in runCorebundle's
// startup-abort defer (covering the window from a successful Load to bootstrap.Run
// taking ownership — provisionCapabilities / composition.Build / option wiring).
// The structure is mirrored at every site so the scopes can be visually compared.
func closeRedisClientAfterFailedLoad(ctx context.Context, client *adapterredis.Client) {
	if client == nil {
		return
	}
	if closeErr := client.Close(ctx); closeErr != nil {
		slog.Warn("corebundle: failed to close Redis client after startup validation failure",
			slog.Any("error", closeErr))
	}
}

// closeBrokerResourcesAfterFailedLoad best-effort closes any event-transport
// broker resources (e.g. the RabbitMQ connection, which dials eagerly in its
// constructor) opened by eventtransport.Resolve, when LoadSharedDepsFromEnv fails
// after the transport was resolved. Mirrors closeRedisClientAfterFailedLoad — the
// broker connection holds an open socket that must not leak on a failed startup.
// Errors are logged, not propagated (the load already failed).
func closeBrokerResourcesAfterFailedLoad(ctx context.Context, resources []kernellifecycle.ManagedResource) {
	for _, r := range resources {
		if r == nil {
			continue
		}
		if closeErr := r.Close(ctx); closeErr != nil {
			slog.Warn("corebundle: failed to close event-transport broker resource after startup validation failure",
				slog.Any("error", closeErr))
		}
	}
}

func adapterInfoForSharedDeps(shared *composition.SharedDeps, locals *cmdLocals) map[string]string {
	info := shared.Topology.AdapterInfo()
	redisState := "not-configured"
	if locals.redisClient != nil {
		redisState = "configured"
	}
	nonceStoreKind := string(kauth.NonceStoreKindNoop)
	if shared.NonceStore != nil {
		nonceStoreKind = string(shared.NonceStore.Kind())
	}
	claimerKind := "unknown"
	if shared.ConsumerClaimer != nil {
		claimerKind = string(shared.ConsumerClaimer.Kind())
	}
	// HTTP idempotency store is wired default-ON when Redis is present
	// (no env toggle); the gate in defaultRuntimeOptions is locals.redisClient != nil,
	// which is the same condition as redisState above.
	httpIdempotencyStore := "inactive"
	if locals.redisClient != nil {
		httpIdempotencyStore = "redis-backed"
	}
	info["redis"] = redisState
	info["service_token_nonce_store"] = nonceStoreKind
	info["outbox_consumer_claimer"] = claimerKind
	info["http_idempotency_store"] = httpIdempotencyStore
	return info
}
