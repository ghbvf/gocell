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
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
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
}

// buildSharedMetricsDeps assembles framework-level always-on metric collectors
// (config events, eventbus cache). Provider-conditional metrics (vault) live
// in dedicated lazy methods on cmdLocals (initVaultMetricsFactory) so deployments
// that don't use the corresponding backend don't pollute their scrape footprint
// with always-zero series.
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
	eventbusCacheCollector, err := obmetrics.NewProviderEventbusCacheCollector(ps.metricProvider)
	if err != nil {
		return sharedMetricsDeps{}, fmt.Errorf("build eventbus cache metrics collector: %w", err)
	}
	return sharedMetricsDeps{
		PromStack:              ps,
		ConfigEventCollector:   configEventCollector,
		EventbusCacheCollector: eventbusCacheCollector,
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

func adapterInfoForSharedDeps(shared *composition.SharedDeps, locals *cmdLocals) map[string]string {
	info := shared.Topology.AdapterInfo()
	redisState := "not-configured"
	if locals.redisClient != nil {
		redisState = "configured"
	}
	nonceStoreKind := string(kauth.NonceStoreKindNoop)
	if locals.internalGuard != nil && locals.internalGuard.NonceStore() != nil {
		nonceStoreKind = string(locals.internalGuard.NonceStore().Kind())
	}
	claimerKind := string(locals.consumerClaimerKind)
	if claimerKind == "" {
		claimerKind = string(consumerClaimerKindUnknown)
	}
	info["redis"] = redisState
	info["service_token_nonce_store"] = nonceStoreKind
	info["outbox_consumer_claimer"] = claimerKind
	return info
}
