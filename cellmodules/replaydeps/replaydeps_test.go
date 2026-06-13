package replaydeps

import (
	"context"
	"errors"
	"testing"
	"time"

	kauth "github.com/ghbvf/gocell/kernel/auth"

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

// mkTopo builds a validated bootstrap.Topology for tests, panicking on an
// invalid combination (mirrors cmd/corebundle/topology_testhelper_test.go).
func mkTopo(adapterMode, storageBackend string, singlePod bool) bootstrap.Topology {
	topo, err := bootstrap.NewTopology(adapterMode, storageBackend, singlePod)
	if err != nil {
		panic(err)
	}
	return topo
}

// --- loadRedisConfigFromEnv (migrated verbatim from cmd/corebundle/redis_test.go) ---

func TestLoadRedisConfigFromEnv_RealMultiPodMissingAddrFailFast(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	topo := mkTopo("real", "postgres", false)

	_, configured, err := loadRedisConfigFromEnv(topo)

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), envRedisAddr)
	assert.Contains(t, err.Error(), "multi-pod")
}

func TestLoadRedisConfigFromEnv_MissingAddrWhenDistributedReplayNotRequired(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	topo := mkTopo("real", "postgres", true)

	cfg, configured, err := loadRedisConfigFromEnv(topo)

	require.NoError(t, err)
	assert.False(t, configured)
	assert.Empty(t, cfg.Addr)
}

func TestLoadRedisConfigFromEnv_ConfiguredParsesPasswordAndDB(t *testing.T) {
	t.Setenv(envRedisAddr, "redis:6379")
	t.Setenv(envRedisPassword, "secret")
	t.Setenv(envRedisDB, "3")

	cfg, configured, err := loadRedisConfigFromEnv(mkTopo("", "memory", false))

	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, "redis:6379", cfg.Addr)
	assert.Equal(t, "secret", cfg.Password)
	assert.Equal(t, 3, cfg.DB)
}

func TestLoadRedisConfigFromEnv_AllowUnsafeNoPasswordMatrix(t *testing.T) {
	tests := []struct {
		name            string
		topo            bootstrap.Topology
		wantAllowUnsafe bool
		wantConfigDescr string
	}{
		{
			name:            "dev mode allows missing password",
			topo:            mkTopo("", "memory", false),
			wantAllowUnsafe: true,
			wantConfigDescr: "dev never requires production credentials",
		},
		{
			name:            "real + single-pod allows missing password (e2e/single-pod prod)",
			topo:            mkTopo("real", "postgres", true),
			wantAllowUnsafe: true,
			wantConfigDescr: "single-pod real with localhost Redis is a recognized e2e shape",
		},
		{
			name:            "real multi-pod fails closed without password",
			topo:            mkTopo("real", "postgres", false),
			wantAllowUnsafe: false,
			wantConfigDescr: "production multi-pod must set GOCELL_REDIS_PASSWORD",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envRedisAddr, "127.0.0.1:6379")
			t.Setenv(envRedisPassword, "")
			t.Setenv(envRedisDB, "")

			cfg, configured, err := loadRedisConfigFromEnv(tt.topo)
			require.NoError(t, err)
			assert.True(t, configured)
			assert.Equal(t, tt.wantAllowUnsafe, cfg.AllowUnsafeNoPassword,
				"AllowUnsafeNoPassword should be %v: %s", tt.wantAllowUnsafe, tt.wantConfigDescr)
		})
	}
}

func TestLoadRedisConfigFromEnv_InvalidDBFailFast(t *testing.T) {
	tests := []struct {
		name string
		db   string
	}{
		{name: "not integer", db: "abc"},
		{name: "negative", db: "-1"},
	}

	topo := mkTopo("real", "postgres", false)

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
			got, ok := attr.Value().(string)
			require.True(t, ok, "expected string detail value, got %T", attr.Value())
			assert.Equal(t, tc.db, got)
		})
	}
}

func TestLoadRedisConfigFromEnv_ClusterAddrsBuildClusterMode(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "node-a:7000,node-b:7000,node-c:7000")
	t.Setenv(envRedisPassword, "secret")

	cfg, configured, err := loadRedisConfigFromEnv(mkTopo("", "memory", false))

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

	_, configured, err := loadRedisConfigFromEnv(mkTopo("", "memory", false))

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestLoadRedisConfigFromEnv_ClusterRejectsNonZeroDB(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "node-a:7000")
	t.Setenv(envRedisDB, "2")

	_, configured, err := loadRedisConfigFromEnv(mkTopo("", "memory", false))

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), "must be 0")
}

func TestLoadRedisConfigFromEnv_ClusterRejectsEmptyEntries(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "node-a:7000,,node-c:7000")

	_, configured, err := loadRedisConfigFromEnv(mkTopo("", "memory", false))

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), "empty entries")
}

func TestLoadRedisConfigFromEnv_ClusterTrimAndDedupe(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, " node-a:7000 , node-b:7000 ,node-a:7000")

	cfg, configured, err := loadRedisConfigFromEnv(mkTopo("", "memory", false))

	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, []string{"node-a:7000", "node-b:7000"}, cfg.ClusterAddrs)
}

func TestLoadRedisConfigFromEnv_ClusterAddrsSatisfyMultiPodRequirement(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "node-a:7000,node-b:7000")
	topo := mkTopo("real", "postgres", false)

	cfg, configured, err := loadRedisConfigFromEnv(topo)

	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, adapterredis.ModeCluster, cfg.Mode)
}

// --- buildRedisClient (migrated) ---

func TestBuildRedisClient_NotConfiguredReturnsNil(t *testing.T) {
	t.Setenv(envRedisAddr, "")

	result, err := buildRedisClient(context.Background(), mkTopo("", "memory", false))

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

	result, err := buildRedisClient(context.Background(), mkTopo("", "memory", false))

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

	result, err := buildRedisClient(context.Background(), mkTopo("", "memory", false))

	require.Error(t, err)
	assert.Nil(t, result.Client)
	assert.ErrorIs(t, err, errRedisTestFactory)
	assert.Contains(t, err.Error(), "build Redis client")
}

// --- buildServiceNonceStore / buildConsumerClaimer (migrated) ---

func TestBuildReplayDependencies_RealSinglePodUsesInMemory(t *testing.T) {
	topo := mkTopo("real", "postgres", true)

	nonceStore, err := buildServiceNonceStore(topo, nil, clock.Real())
	require.NoError(t, err)
	assert.Equal(t, kauth.NonceStoreKindInMemory, nonceStore.Kind())
	inMemoryNonceStore, ok := nonceStore.(*auth.InMemoryNonceStore)
	require.True(t, ok)
	assert.Equal(t, auth.ServiceTokenNonceTTL, inMemoryNonceStore.MaxAge())

	claimer, err := buildConsumerClaimer(topo, nil, clock.Real())
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimerKindInMemory, claimer.Kind())
	assert.IsType(t, &idempotency.InMemClaimer{}, claimer)
}

func TestBuildServiceNonceStore_RealMultiPodRequiresRedisClient(t *testing.T) {
	topo := mkTopo("real", "postgres", false)

	store, err := buildServiceNonceStore(topo, nil, clock.Real())

	require.Error(t, err)
	assert.Nil(t, store)
	assertErrCode(t, err, errcode.ErrControlplaneNonceStoreMissing)
}

func TestBuildServiceNonceStore_DistributedFactoryErrorWrapped(t *testing.T) {
	topo := mkTopo("real", "postgres", false)
	restoreRedisNonceStoreFactory(t, func(*adapterredis.Client, time.Duration) (kauth.NonceStore, error) {
		return nil, errRedisTestFactory
	})

	store, err := buildServiceNonceStore(topo, new(adapterredis.Client), clock.Real())

	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, errRedisTestFactory)
	assert.Contains(t, err.Error(), "build Redis nonce store")
}

func TestBuildConsumerClaimer_RealMultiPodRequiresRedisClient(t *testing.T) {
	topo := mkTopo("real", "postgres", false)

	claimer, err := buildConsumerClaimer(topo, nil, clock.Real())

	require.Error(t, err)
	assert.Nil(t, claimer)
	assertErrCode(t, err, errcode.ErrControlplaneClaimerNotDistributed)
}

func TestBuildConsumerClaimer_DistributedFactoryErrorWrapped(t *testing.T) {
	topo := mkTopo("real", "postgres", false)
	restoreRedisClaimerFactory(t, func(*adapterredis.Client) (idempotency.Claimer, error) {
		return nil, errRedisTestFactory
	})

	claimer, err := buildConsumerClaimer(topo, new(adapterredis.Client), clock.Real())

	require.Error(t, err)
	assert.Nil(t, claimer)
	assert.ErrorIs(t, err, errRedisTestFactory)
	assert.Contains(t, err.Error(), "build Redis idempotency claimer")
}

func TestBuildReplayDependencies_RealMultiPodConfiguredRedisUsesDistributedStores(t *testing.T) {
	topo := mkTopo("real", "postgres", false)
	client := new(adapterredis.Client)
	var gotNonceClient *adapterredis.Client
	var gotNonceTTL time.Duration
	var gotClaimerClient *adapterredis.Client

	restoreRedisNonceStoreFactory(t, func(c *adapterredis.Client, ttl time.Duration) (kauth.NonceStore, error) {
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
	assert.Equal(t, kauth.NonceStoreKindDistributed, nonceStore.Kind())

	claimer, err := buildConsumerClaimer(topo, client, clock.Real())
	require.NoError(t, err)
	assert.Same(t, client, gotClaimerClient)
	assert.Equal(t, idempotency.ClaimerKindDistributed, claimer.Kind())
	assert.IsType(t, fakeDistributedClaimer{}, claimer)
}

// --- Resolve façade (NEW: the public entry both composition roots call) ---

// TestResolve_DemoUsesInMemoryNoResources pins the demo/memory topology:
// in-memory claimer + in-memory nonce store, no Redis client, empty Resources.
func TestResolve_DemoUsesInMemoryNoResources(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	topo := mkTopo("", "memory", false)

	rd, err := Resolve(context.Background(), clock.Real(), topo)

	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimerKindInMemory, rd.ConsumerClaimer.Kind())
	assert.Equal(t, kauth.NonceStoreKindInMemory, rd.NonceStore.Kind())
	assert.Nil(t, rd.RedisClient)
	assert.Empty(t, rd.Resources, "demo topology owns no managed resources")
}

// TestResolve_RealMultiPodMissingRedisFailsClosed pins the fail-closed gate:
// real multi-pod topology with no Redis env must error at startup, never
// silently degrade to in-memory. This is the funnel that protects ssobff (it
// does not go through composition.SharedDeps' ConsumerClaimer.Kind() check).
func TestResolve_RealMultiPodMissingRedisFailsClosed(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	t.Setenv(envRedisClusterAddrs, "")
	topo := mkTopo("real", "postgres", false)

	rd, err := Resolve(context.Background(), clock.Real(), topo)

	require.Error(t, err)
	assert.Nil(t, rd.ConsumerClaimer)
	assert.Nil(t, rd.NonceStore)
	assert.Empty(t, rd.Resources)
}

// TestResolve_RealMultiPodConfiguredReturnsClientResource pins that a configured
// Redis client is returned as a managed resource (so the composition root closes
// it LIFO) and both primitives report distributed.
func TestResolve_RealMultiPodConfiguredReturnsClientResource(t *testing.T) {
	t.Setenv(envRedisAddr, "127.0.0.1:6379")
	t.Setenv(envRedisPassword, "secret")
	client := new(adapterredis.Client)
	restoreRedisClientFactory(t, func(context.Context, adapterredis.Config) (*adapterredis.Client, error) {
		return client, nil
	})
	restoreRedisNonceStoreFactory(t, func(*adapterredis.Client, time.Duration) (kauth.NonceStore, error) {
		return fakeDistributedNonceStore{}, nil
	})
	restoreRedisClaimerFactory(t, func(*adapterredis.Client) (idempotency.Claimer, error) {
		return fakeDistributedClaimer{}, nil
	})
	topo := mkTopo("real", "postgres", false)

	rd, err := Resolve(context.Background(), clock.Real(), topo)

	require.NoError(t, err)
	assert.Same(t, client, rd.RedisClient)
	assert.Equal(t, idempotency.ClaimerKindDistributed, rd.ConsumerClaimer.Kind())
	assert.Equal(t, kauth.NonceStoreKindDistributed, rd.NonceStore.Kind())
	require.Len(t, rd.Resources, 1, "the Redis client must be returned as a managed resource")
	assert.Same(t, client, rd.Resources[0])
}

// --- shared test helpers (migrated from cmd/corebundle test files) ---

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

func (fakeDistributedNonceStore) Kind() kauth.NonceStoreKind {
	return kauth.NonceStoreKindDistributed
}

type fakeDistributedClaimer struct{}

func (fakeDistributedClaimer) Claim(
	context.Context, string, time.Duration, time.Duration,
) (idempotency.ClaimState, idempotency.Receipt, error) {
	return idempotency.ClaimDone, idempotency.NonAcquiredReceipt(), nil
}

func (fakeDistributedClaimer) Kind() idempotency.ClaimerKind {
	return idempotency.ClaimerKindDistributed
}
