package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

const (
	envRedisAddr     = "GOCELL_REDIS_ADDR"
	envRedisPassword = "GOCELL_REDIS_PASSWORD"
	envRedisDB       = "GOCELL_REDIS_DB"
)

type (
	redisNonceStoreFactory      func(*adapterredis.Client, time.Duration) (auth.NonceStore, error)
	redisConsumerClaimerFactory func(*adapterredis.Client) idempotency.Claimer
	redisClientFactory          func(context.Context, adapterredis.Config) (*adapterredis.Client, error)
)

type redisClientResult struct {
	Client *adapterredis.Client
}

var (
	newRedisClient     redisClientFactory     = adapterredis.NewClient
	newRedisNonceStore redisNonceStoreFactory = func(client *adapterredis.Client, ttl time.Duration) (auth.NonceStore, error) {
		return adapterredis.NewNonceStore(client, ttl)
	}
	newRedisIdempotencyClaimer redisConsumerClaimerFactory = func(client *adapterredis.Client) idempotency.Claimer {
		return adapterredis.NewIdempotencyClaimer(client)
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
	if addr == "" {
		if requiresDistributedReplay(topo) {
			return adapterredis.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				envRedisAddr+" must be set in adapter mode \"real\" unless GOCELL_SINGLE_POD=1; "+
					"multi-pod deployments require Redis-backed nonce and idempotency stores")
		}
		return adapterredis.Config{}, false, nil
	}

	db := 0
	if raw := os.Getenv(envRedisDB); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return adapterredis.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				fmt.Sprintf("%s must be a non-negative integer, got %q", envRedisDB, raw))
		}
		db = parsed
	}

	return adapterredis.Config{
		Addr:     addr,
		Password: os.Getenv(envRedisPassword),
		DB:       db,
	}, true, nil
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
				envRedisAddr+" must be set for distributed service-token nonce protection")
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
				envRedisAddr+" must be set for distributed outbox idempotency in real multi-pod deployments")
		}
		return newRedisIdempotencyClaimer(client), consumerClaimerKindDistributed, nil
	}
	return idempotency.NewInMemClaimer(clk), consumerClaimerKindInMemory, nil
}
