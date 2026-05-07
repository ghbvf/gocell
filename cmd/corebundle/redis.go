package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

const (
	envRedisAddr         = "GOCELL_REDIS_ADDR"
	envRedisClusterAddrs = "GOCELL_REDIS_CLUSTER_ADDRS"
	envRedisPassword     = "GOCELL_REDIS_PASSWORD"
	envRedisDB           = "GOCELL_REDIS_DB"
)

type (
	redisNonceStoreFactory      func(*adapterredis.Client, time.Duration) (auth.NonceStore, error)
	redisConsumerClaimerFactory func(*adapterredis.Client) (idempotency.Claimer, error)
	redisClientFactory          func(context.Context, adapterredis.Config) (*adapterredis.Client, error)
)

type redisClientResult struct {
	Client *adapterredis.Client
}

// nonceStoreNamespace and consumerClaimerNamespace are the KeyNamespace
// values composition root passes into the shared Redis primitives.
//
//   - servicetoken-nonce: the NonceStore is a single global store for the
//     internal listener's service-token replay protection — namespace
//     names the role directly so wire keys read as
//     "servicetoken-nonce:<nonce>".
//   - _runtime: the IdempotencyClaimer is shared across all consumers; the
//     "_runtime" sentinel mirrors the HTTP metrics convention for shared
//     framework infrastructure where no cell context applies.
const (
	nonceStoreNamespace      adapterredis.KeyNamespace = "servicetoken-nonce"
	consumerClaimerNamespace adapterredis.KeyNamespace = "_runtime"
)

var (
	newRedisClient     redisClientFactory     = adapterredis.NewClient
	newRedisNonceStore redisNonceStoreFactory = func(client *adapterredis.Client, ttl time.Duration) (auth.NonceStore, error) {
		return adapterredis.NewNonceStore(client, nonceStoreNamespace, ttl)
	}
	newRedisIdempotencyClaimer redisConsumerClaimerFactory = func(client *adapterredis.Client) (idempotency.Claimer, error) {
		return adapterredis.NewIdempotencyClaimer(client, consumerClaimerNamespace)
	}
)

type consumerClaimerKind string

const (
	consumerClaimerKindUnknown     consumerClaimerKind = ""
	consumerClaimerKindInMemory    consumerClaimerKind = "in_memory"
	consumerClaimerKindDistributed consumerClaimerKind = "distributed"
)

func requiresDistributedReplay(topo bootstrap.Topology) bool {
	return topo.RequireProductionControlPlane() && !topo.SinglePodReplayProtection
}

func loadRedisConfigFromEnv(topo bootstrap.Topology) (adapterredis.Config, bool, error) {
	addr := os.Getenv(envRedisAddr)
	clusterRaw := os.Getenv(envRedisClusterAddrs)

	if addr != "" && clusterRaw != "" {
		return adapterredis.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"GOCELL_REDIS_ADDR and GOCELL_REDIS_CLUSTER_ADDRS are mutually exclusive; set exactly one")
	}

	if clusterRaw != "" {
		clusterAddrs, err := parseClusterAddrs(clusterRaw)
		if err != nil {
			return adapterredis.Config{}, false, err
		}
		if raw := os.Getenv(envRedisDB); raw != "" && raw != "0" {
			return adapterredis.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"GOCELL_REDIS_DB must be 0 (or unset) in cluster mode; cluster has no SELECT command")
		}
		return adapterredis.Config{
			Mode:                  adapterredis.ModeCluster,
			ClusterAddrs:          clusterAddrs,
			Password:              os.Getenv(envRedisPassword),
			AllowUnsafeNoPassword: !requiresDistributedReplay(topo),
		}, true, nil
	}

	if addr == "" {
		if requiresDistributedReplay(topo) {
			return adapterredis.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"GOCELL_REDIS_ADDR or GOCELL_REDIS_CLUSTER_ADDRS must be set "+
					"in adapter mode \"real\" unless GOCELL_SINGLE_POD=1; "+
					"multi-pod deployments require Redis-backed nonce and "+
					"idempotency stores")
		}
		return adapterredis.Config{}, false, nil
	}

	db := 0
	if raw := os.Getenv(envRedisDB); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return adapterredis.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"GOCELL_REDIS_DB must be a non-negative integer",
				errcode.WithDetails(slog.String("got", raw)))
		}
		db = parsed
	}

	return adapterredis.Config{
		Addr:                  addr,
		Password:              os.Getenv(envRedisPassword),
		DB:                    db,
		AllowUnsafeNoPassword: !requiresDistributedReplay(topo),
	}, true, nil
}

// parseClusterAddrs splits a comma-separated GOCELL_REDIS_CLUSTER_ADDRS value
// into individual node addresses. Empty entries (caused by trailing commas or
// double-commas) are rejected explicitly — silently dropping them would mask
// configuration typos that change cluster topology.
func parseClusterAddrs(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	addrs := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"GOCELL_REDIS_CLUSTER_ADDRS must not contain empty entries (check for trailing or double commas)")
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		addrs = append(addrs, trimmed)
	}
	return addrs, nil
}

func buildRedisClient(ctx context.Context, topo bootstrap.Topology) (redisClientResult, error) {
	cfg, configured, err := loadRedisConfigFromEnv(topo)
	if err != nil {
		return redisClientResult{}, err
	}
	if !configured {
		return redisClientResult{}, nil
	}
	client, err := newRedisClient(ctx, cfg)
	if err != nil {
		return redisClientResult{}, fmt.Errorf("build Redis client: %w", err)
	}
	return redisClientResult{Client: client}, nil
}

func buildServiceNonceStore(topo bootstrap.Topology, client *adapterredis.Client, clk clock.Clock) (auth.NonceStore, error) {
	if requiresDistributedReplay(topo) {
		if client == nil {
			return nil, errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
				"GOCELL_REDIS_ADDR or GOCELL_REDIS_CLUSTER_ADDRS must be set for distributed service-token nonce protection")
		}
		store, err := newRedisNonceStore(client, auth.ServiceTokenNonceTTL)
		if err != nil {
			return nil, fmt.Errorf("build Redis nonce store: %w", err)
		}
		return store, nil
	}
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
	if err != nil {
		return nil, fmt.Errorf("build in-memory nonce store: %w", err)
	}
	return store, nil
}

func buildConsumerClaimer(
	topo bootstrap.Topology, client *adapterredis.Client, clk clock.Clock,
) (idempotency.Claimer, consumerClaimerKind, error) {
	if requiresDistributedReplay(topo) {
		if client == nil {
			return nil, consumerClaimerKindUnknown, errcode.New(errcode.KindInternal, errcode.ErrControlplaneClaimerNotDistributed,
				"GOCELL_REDIS_ADDR or GOCELL_REDIS_CLUSTER_ADDRS must be set for distributed outbox idempotency in real multi-pod deployments")
		}
		claimer, err := newRedisIdempotencyClaimer(client)
		if err != nil {
			return nil, consumerClaimerKindUnknown, fmt.Errorf("build Redis idempotency claimer: %w", err)
		}
		return claimer, consumerClaimerKindDistributed, nil
	}
	return idempotency.NewInMemClaimer(clk), consumerClaimerKindInMemory, nil
}
