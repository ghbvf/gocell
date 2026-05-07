package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

var errRedisTestFactory = errors.New("redis factory failed")

func TestLoadRedisConfigFromEnv_RealMultiPodMissingAddrFailFast(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}

	_, configured, err := loadRedisConfigFromEnv(topo)

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), envRedisAddr)
	assert.Contains(t, err.Error(), "multi-pod")
}

func TestLoadRedisConfigFromEnv_MissingAddrWhenDistributedReplayNotRequired(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	topo := bootstrap.Topology{
		AdapterMode:               "real",
		StorageBackend:            "postgres",
		SinglePodReplayProtection: true,
	}

	cfg, configured, err := loadRedisConfigFromEnv(topo)

	require.NoError(t, err)
	assert.False(t, configured)
	assert.Empty(t, cfg.Addr)
}

func TestLoadRedisConfigFromEnv_ConfiguredParsesPasswordAndDB(t *testing.T) {
	t.Setenv(envRedisAddr, "redis:6379")
	t.Setenv(envRedisPassword, "secret")
	t.Setenv(envRedisDB, "3")

	cfg, configured, err := loadRedisConfigFromEnv(bootstrap.Topology{AdapterMode: "dev"})

	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, "redis:6379", cfg.Addr)
	assert.Equal(t, "secret", cfg.Password)
	assert.Equal(t, 3, cfg.DB)
}

func TestLoadRedisConfigFromEnv_InvalidDBFailFast(t *testing.T) {
	tests := []struct {
		name string
		db   string
	}{
		{name: "not integer", db: "abc"},
		{name: "negative", db: "-1"},
	}

	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envRedisAddr, "redis:6379")
			t.Setenv(envRedisDB, tc.db)

			_, configured, err := loadRedisConfigFromEnv(topo)

			require.Error(t, err)
			assert.False(t, configured)
			assertErrCode(t, err, errcode.ErrValidationFailed)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Contains(t, ecErr.Message, envRedisDB)
			attr, ok := ecErr.FindAttr("got")
			assert.True(t, ok, "expected 'got' detail attr")
			assert.Equal(t, tc.db, attr.Value.String())
		})
	}
}

func TestLoadRedisConfigFromEnv_ClusterAddrsBuildClusterMode(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "node-a:7000,node-b:7000,node-c:7000")
	t.Setenv(envRedisPassword, "secret")

	cfg, configured, err := loadRedisConfigFromEnv(bootstrap.Topology{AdapterMode: "dev"})

	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, adapterredis.ModeCluster, cfg.Mode)
	assert.Equal(t, []string{"node-a:7000", "node-b:7000", "node-c:7000"}, cfg.ClusterAddrs)
	assert.Equal(t, "secret", cfg.Password)
	assert.Empty(t, cfg.Addr, "Addr must remain empty in cluster mode")
}

func TestLoadRedisConfigFromEnv_ClusterAndAddrMutuallyExclusive(t *testing.T) {
	t.Setenv(envRedisAddr, "redis:6379")
	t.Setenv(envRedisClusterAddrs, "node-a:7000")

	_, configured, err := loadRedisConfigFromEnv(bootstrap.Topology{AdapterMode: "dev"})

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestLoadRedisConfigFromEnv_ClusterRejectsNonZeroDB(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "node-a:7000")
	t.Setenv(envRedisDB, "2")

	_, configured, err := loadRedisConfigFromEnv(bootstrap.Topology{AdapterMode: "dev"})

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), "must be 0")
}

// Empty entries (trailing or double commas) are typos that change cluster
// topology silently if dropped — we reject them explicitly.
func TestLoadRedisConfigFromEnv_ClusterRejectsEmptyEntries(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "node-a:7000,,node-c:7000")

	_, configured, err := loadRedisConfigFromEnv(bootstrap.Topology{AdapterMode: "dev"})

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), "empty entries")
}

// Whitespace gets trimmed and exact duplicates de-duplicated; semantically
// equivalent (host:port is a Redis identity), redundant entries are silently
// folded so config stays declarative even when sourced from line-broken env.
func TestLoadRedisConfigFromEnv_ClusterTrimAndDedupe(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, " node-a:7000 , node-b:7000 ,node-a:7000")

	cfg, configured, err := loadRedisConfigFromEnv(bootstrap.Topology{AdapterMode: "dev"})

	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, []string{"node-a:7000", "node-b:7000"}, cfg.ClusterAddrs)
}

// In real multi-pod mode either addr OR cluster_addrs satisfies the
// distributed-replay requirement.
func TestLoadRedisConfigFromEnv_ClusterAddrsSatisfyMultiPodRequirement(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "node-a:7000,node-b:7000")
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}

	cfg, configured, err := loadRedisConfigFromEnv(topo)

	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, adapterredis.ModeCluster, cfg.Mode)
}

func TestBuildRedisClient_NotConfiguredReturnsNil(t *testing.T) {
	t.Setenv(envRedisAddr, "")

	result, err := buildRedisClient(context.Background(), bootstrap.Topology{AdapterMode: "dev"})

	require.NoError(t, err)
	assert.Nil(t, result.Client)
}

func TestBuildRedisClient_UsesConfiguredFactory(t *testing.T) {
	t.Setenv(envRedisAddr, "redis:6379")
	t.Setenv(envRedisPassword, "secret")
	t.Setenv(envRedisDB, "2")
	var gotCfg adapterredis.Config
	restoreRedisClientFactory(t, func(_ context.Context, cfg adapterredis.Config) (*adapterredis.Client, error) {
		gotCfg = cfg
		return new(adapterredis.Client), nil
	})

	result, err := buildRedisClient(context.Background(), bootstrap.Topology{AdapterMode: "dev"})

	require.NoError(t, err)
	client := result.Client
	require.NotNil(t, client)
	assert.Equal(t, "redis:6379", gotCfg.Addr)
	assert.Equal(t, "secret", gotCfg.Password)
	assert.Equal(t, 2, gotCfg.DB)
}

func TestBuildRedisClient_FactoryErrorWrapped(t *testing.T) {
	t.Setenv(envRedisAddr, "redis:6379")
	restoreRedisClientFactory(t, func(context.Context, adapterredis.Config) (*adapterredis.Client, error) {
		return nil, errRedisTestFactory
	})

	result, err := buildRedisClient(context.Background(), bootstrap.Topology{AdapterMode: "dev"})

	require.Error(t, err)
	assert.Nil(t, result.Client)
	assert.ErrorIs(t, err, errRedisTestFactory)
	assert.Contains(t, err.Error(), "build Redis client")
}

func TestBuildReplayDependencies_RealSinglePodUsesInMemory(t *testing.T) {
	topo := bootstrap.Topology{
		AdapterMode:               "real",
		StorageBackend:            "postgres",
		SinglePodReplayProtection: true,
	}

	nonceStore, err := buildServiceNonceStore(topo, nil, clock.Real())
	require.NoError(t, err)
	assert.Equal(t, auth.NonceStoreKindInMemory, nonceStore.Kind())
	inMemoryNonceStore, ok := nonceStore.(*auth.InMemoryNonceStore)
	require.True(t, ok)
	assert.Equal(t, auth.ServiceTokenNonceTTL, inMemoryNonceStore.MaxAge())

	claimer, kind, err := buildConsumerClaimer(topo, nil, clock.Real())
	require.NoError(t, err)
	assert.Equal(t, consumerClaimerKindInMemory, kind)
	assert.IsType(t, &idempotency.InMemClaimer{}, claimer)
}

func TestBuildServiceNonceStore_RealMultiPodRequiresRedisClient(t *testing.T) {
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}

	store, err := buildServiceNonceStore(topo, nil, clock.Real())

	require.Error(t, err)
	assert.Nil(t, store)
	assertErrCode(t, err, errcode.ErrControlplaneNonceStoreMissing)
}

func TestBuildServiceNonceStore_DistributedFactoryErrorWrapped(t *testing.T) {
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}
	restoreRedisNonceStoreFactory(t, func(*adapterredis.Client, time.Duration) (auth.NonceStore, error) {
		return nil, errRedisTestFactory
	})

	store, err := buildServiceNonceStore(topo, new(adapterredis.Client), clock.Real())

	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, errRedisTestFactory)
	assert.Contains(t, err.Error(), "build Redis nonce store")
}

func TestBuildConsumerClaimer_RealMultiPodRequiresRedisClient(t *testing.T) {
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}

	claimer, kind, err := buildConsumerClaimer(topo, nil, clock.Real())

	require.Error(t, err)
	assert.Nil(t, claimer)
	assert.Equal(t, consumerClaimerKindUnknown, kind)
	assertErrCode(t, err, errcode.ErrControlplaneClaimerNotDistributed)
}

// TestBuildConsumerClaimer_DistributedFactoryErrorWrapped pins the
// error-first path through buildConsumerClaimer: when the Redis claimer
// factory returns an error (e.g., NewIdempotencyClaimer rejecting an
// invalid namespace or nil client), the wrapper produces nil claimer +
// consumerClaimerKindUnknown + a wrap message containing the original
// error. Mirrors TestBuildServiceNonceStore_DistributedFactoryErrorWrapped.
func TestBuildConsumerClaimer_DistributedFactoryErrorWrapped(t *testing.T) {
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}
	restoreRedisClaimerFactory(t, func(*adapterredis.Client) (idempotency.Claimer, error) {
		return nil, errRedisTestFactory
	})

	claimer, kind, err := buildConsumerClaimer(topo, new(adapterredis.Client), clock.Real())

	require.Error(t, err)
	assert.Nil(t, claimer)
	assert.Equal(t, consumerClaimerKindUnknown, kind)
	assert.ErrorIs(t, err, errRedisTestFactory)
	assert.Contains(t, err.Error(), "build Redis idempotency claimer")
}

func TestBuildReplayDependencies_RealMultiPodConfiguredRedisUsesDistributedStores(t *testing.T) {
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}
	client := new(adapterredis.Client)
	var gotNonceClient *adapterredis.Client
	var gotNonceTTL time.Duration
	var gotClaimerClient *adapterredis.Client

	restoreRedisNonceStoreFactory(t, func(c *adapterredis.Client, ttl time.Duration) (auth.NonceStore, error) {
		gotNonceClient = c
		gotNonceTTL = ttl
		return fakeDistributedNonceStore{}, nil
	})
	restoreRedisClaimerFactory(t, func(c *adapterredis.Client) (idempotency.Claimer, error) {
		gotClaimerClient = c
		return fakeDistributedClaimer{}, nil
	})

	nonceStore, err := buildServiceNonceStore(topo, client, clock.Real())
	require.NoError(t, err)
	assert.Same(t, client, gotNonceClient)
	assert.Equal(t, auth.ServiceTokenNonceTTL, gotNonceTTL)
	assert.Equal(t, auth.NonceStoreKindDistributed, nonceStore.Kind())

	claimer, kind, err := buildConsumerClaimer(topo, client, clock.Real())
	require.NoError(t, err)
	assert.Same(t, client, gotClaimerClient)
	assert.Equal(t, consumerClaimerKindDistributed, kind)
	assert.IsType(t, fakeDistributedClaimer{}, claimer)
}

func restoreRedisClientFactory(t *testing.T, fn redisClientFactory) {
	t.Helper()
	original := newRedisClient
	newRedisClient = fn
	t.Cleanup(func() { newRedisClient = original })
}

func restoreRedisNonceStoreFactory(t *testing.T, fn redisNonceStoreFactory) {
	t.Helper()
	original := newRedisNonceStore
	newRedisNonceStore = fn
	t.Cleanup(func() { newRedisNonceStore = original })
}

func restoreRedisClaimerFactory(t *testing.T, fn redisConsumerClaimerFactory) {
	t.Helper()
	original := newRedisIdempotencyClaimer
	newRedisIdempotencyClaimer = fn
	t.Cleanup(func() { newRedisIdempotencyClaimer = original })
}

func assertErrCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, want, ec.Code)
}

type fakeDistributedNonceStore struct{}

func (fakeDistributedNonceStore) CheckAndMark(context.Context, string) error {
	return nil
}

func (fakeDistributedNonceStore) Kind() auth.NonceStoreKind {
	return auth.NonceStoreKindDistributed
}

type fakeDistributedClaimer struct{}

func (fakeDistributedClaimer) Claim(
	context.Context, string, time.Duration, time.Duration,
) (idempotency.ClaimState, idempotency.Receipt, error) {
	return idempotency.ClaimDone, idempotency.NonAcquiredReceipt(), nil
}
