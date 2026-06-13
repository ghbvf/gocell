package replaydeps

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/lifecycle"
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

// nonceStoreNamespace and consumerClaimerNamespace are the KeyNamespace values
// the resolver passes into the shared Redis primitives:
//
//   - servicetoken-nonce: the NonceStore is the single global store for the
//     internal listener's service-token replay protection — the namespace names
//     the role directly so wire keys read as "servicetoken-nonce:<nonce>".
//   - _runtime (consumer claimer): the IdempotencyClaimer is shared across all
//     consumers; the "_runtime" sentinel mirrors the HTTP metrics convention for
//     shared framework infrastructure where no cell context applies
//     (cf. .claude/rules/gocell/observability.md §Redis namespace). Its key
//     format ("_runtime:{eventID}:lease|done") is structurally distinct from the
//     HTTP idempotency store's, so the shared sentinel cannot collide.
const (
	nonceStoreNamespace      adapterredis.KeyNamespace = "servicetoken-nonce"
	consumerClaimerNamespace adapterredis.KeyNamespace = "_runtime"
)

type (
	redisNonceStoreFactory      func(*adapterredis.Client, time.Duration) (kauth.NonceStore, error)
	redisConsumerClaimerFactory func(*adapterredis.Client) (idempotency.Claimer, error)
	redisClientFactory          func(context.Context, adapterredis.Config) (*adapterredis.Client, error)
)

// Package-level factory seams so white-box tests can inject fakes without a live
// Redis (mirrors cmd/corebundle's original pattern, migrated here intact).
var (
	newRedisClient     redisClientFactory     = adapterredis.NewClient
	newRedisNonceStore redisNonceStoreFactory = func(client *adapterredis.Client, ttl time.Duration) (kauth.NonceStore, error) {
		return adapterredis.NewNonceStore(client, nonceStoreNamespace, ttl)
	}
	newRedisIdempotencyClaimer redisConsumerClaimerFactory = func(client *adapterredis.Client) (idempotency.Claimer, error) {
		return adapterredis.NewIdempotencyClaimer(client, consumerClaimerNamespace)
	}
)

// ReplayDeps bundles the resolved distributed-replay primitives and the Redis
// client (when configured) as a managed resource for LIFO shutdown.
//
// In demo / single-pod topology ConsumerClaimer and NonceStore are in-memory,
// RedisClient is nil, and Resources is empty. In real multi-pod topology they
// are Redis-backed, RedisClient is the live client, and Resources holds it so
// the composition root closes it during shutdown.
type ReplayDeps struct {
	RedisClient     *adapterredis.Client
	ConsumerClaimer idempotency.Claimer
	NonceStore      kauth.NonceStore
	Resources       []lifecycle.ManagedResource
}

// Resolve reads the GOCELL_REDIS_* environment, then derives the consumer
// idempotency claimer and the service-token nonce store for topo. clk is the
// mandatory positional clock (ADR clock-positional-injection-funnel), threaded
// into the in-memory primitives.
//
// Fail-closed invariant: in real multi-pod topology
// (topo.RequiresDistributedReplay()) a missing Redis configuration is a startup
// error, never a silent degrade to in-memory — an in-process claimer/nonce store
// cannot coordinate at-most-once / replay defense across replicas. This is the
// same gate cmd/corebundle's composition.SharedDeps.ConsumerClaimer.Kind() check
// enforces, but it lives here so composition roots that do NOT go through
// SharedDeps (examples/ssobff) are equally protected.
//
// On any error after the Redis client is built, the client is closed before
// returning so a failed startup never leaks an open connection.
func Resolve(ctx context.Context, clk clock.Clock, topo bootstrap.Topology) (ReplayDeps, error) {
	clock.MustHaveClock(clk, "replaydeps.Resolve")

	redisClient, err := buildRedisClient(ctx, topo)
	if err != nil {
		return ReplayDeps{}, err
	}
	resolved := false
	defer func() {
		if !resolved {
			closeClientAfterFailedResolve(ctx, redisClient)
		}
	}()

	nonceStore, err := buildServiceNonceStore(topo, redisClient, clk)
	if err != nil {
		return ReplayDeps{}, err
	}
	claimer, err := buildConsumerClaimer(topo, redisClient, clk)
	if err != nil {
		return ReplayDeps{}, err
	}

	resolved = true
	var resources []lifecycle.ManagedResource
	if redisClient != nil {
		resources = []lifecycle.ManagedResource{redisClient}
	}
	return ReplayDeps{
		RedisClient:     redisClient,
		ConsumerClaimer: claimer,
		NonceStore:      nonceStore,
		Resources:       resources,
	}, nil
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
			AllowUnsafeNoPassword: !topo.RequiresDistributedReplay(),
		}, true, nil
	}

	if addr == "" {
		if topo.RequiresDistributedReplay() {
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
				errcode.WithDetails(errcode.PublicString("got", raw)))
		}
		db = parsed
	}

	return adapterredis.Config{
		Addr:                  addr,
		Password:              os.Getenv(envRedisPassword),
		DB:                    db,
		AllowUnsafeNoPassword: !topo.RequiresDistributedReplay(),
	}, true, nil
}

// parseClusterAddrs splits a comma-separated GOCELL_REDIS_CLUSTER_ADDRS value
// into individual node addresses. Empty entries (caused by trailing commas or
// double-commas) are rejected explicitly — silently dropping them would mask
// configuration typos that change cluster topology. Whitespace is trimmed and
// exact duplicates folded.
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

// buildRedisClient reads the GOCELL_REDIS_* environment and creates a Redis
// client when configured. A nil client with a nil error means Redis is not
// configured (demo / single-pod) — a legal state, distinct from a build error.
func buildRedisClient(ctx context.Context, topo bootstrap.Topology) (*adapterredis.Client, error) {
	cfg, configured, err := loadRedisConfigFromEnv(topo)
	if err != nil {
		return nil, err
	}
	if !configured {
		// nil client + nil error is intentional: means "Redis not configured"
		// (demo / single-pod). Callers distinguish this from a build error by
		// checking whether the returned pointer is nil, not the error.
		return nil, nil //nolint:nilnil // nil client + nil error = "not configured"; see comment above
	}
	client, err := newRedisClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build Redis client: %w", err)
	}
	return client, nil
}

func buildServiceNonceStore(topo bootstrap.Topology, client *adapterredis.Client, clk clock.Clock) (kauth.NonceStore, error) {
	if topo.RequiresDistributedReplay() {
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
) (idempotency.Claimer, error) {
	if topo.RequiresDistributedReplay() {
		if client == nil {
			return nil, errcode.New(errcode.KindInternal, errcode.ErrControlplaneClaimerNotDistributed,
				"GOCELL_REDIS_ADDR or GOCELL_REDIS_CLUSTER_ADDRS must be set for distributed outbox idempotency in real multi-pod deployments")
		}
		claimer, err := newRedisIdempotencyClaimer(client)
		if err != nil {
			return nil, fmt.Errorf("build Redis idempotency claimer: %w", err)
		}
		return claimer, nil
	}
	return idempotency.NewInMemClaimer(clk), nil
}

// closeClientAfterFailedResolve best-effort closes a Redis client opened by
// buildRedisClient when a later Resolve step fails, so a failed startup never
// leaks an open connection. Nil-safe; errors are logged by the client's Close.
func closeClientAfterFailedResolve(ctx context.Context, client *adapterredis.Client) {
	if client == nil {
		return
	}
	// Close error is intentionally dropped: the resolve already failed and the
	// caller propagates that error; this is best-effort cleanup on the failure path.
	_ = client.Close(ctx)
}
