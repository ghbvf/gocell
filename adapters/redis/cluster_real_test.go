//go:build integration_cluster

// Package redis cluster integration tests.
//
// Why a separate build tag (and env-driven discovery) instead of testcontainers
// boot inside the test process: testcontainers-go has no Redis Cluster module,
// and the grokzen/redis-cluster image announces gossip addresses as 127.0.0.1,
// which makes external clients reach nodes only when the container runs in
// host-network mode (not portable to macOS/Windows Docker Desktop). Rather
// than fight that topology in CI, operators launch a cluster out-of-band and
// export GOCELL_TEST_REDIS_CLUSTER_ADDRS to point this test at it (see
// docs/ops/redis-cluster-deployment.md). When the env is unset the test skips.
//
// Filename ends in _real_test.go (not _integration_test.go) on purpose: the
// archtest BUILD-CONSTRAINT gate enforces `//go:build integration` exactly
// for every *_integration_test.go file. Cluster tests need a stricter
// `integration_cluster` tag so default `-tags=integration` runs do not pull
// them in and demand a cluster the developer hasn't booted; the rename keeps
// the cluster tag isolated.
//
// Run locally:
//
//	docker run --rm -d --network host --name gocell-test-redis-cluster \
//	    grokzen/redis-cluster:7.0.10
//	GOCELL_TEST_REDIS_CLUSTER_ADDRS=127.0.0.1:7000,127.0.0.1:7001,127.0.0.1:7002 \
//	    go test -tags=integration_cluster ./adapters/redis/...

package redis

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/testutil"
)

func startCluster(t *testing.T) (*Client, func()) {
	t.Helper()
	raw := os.Getenv(testutil.RedisClusterTestAddrsEnv)
	if raw == "" {
		t.Skipf("%s not set; skipping cluster integration test (see docs/ops/redis-cluster-deployment.md)",
			testutil.RedisClusterTestAddrsEnv)
	}

	addrs := strings.Split(raw, ",")
	for i, a := range addrs {
		addrs[i] = strings.TrimSpace(a)
	}

	ctx := context.Background()
	client, err := NewClient(ctx, Config{
		Mode:         ModeCluster,
		ClusterAddrs: addrs,
		DialTimeout:  testtime.SelectAsyncSettle,
		DistLockTTL:  testtime.EventuallyLong,
	})
	require.NoError(t, err, "create redis cluster client")

	cleanup := func() {
		_ = client.Close(ctx)
	}
	return client, cleanup
}

// TestClusterIntegration_ClientHealth confirms NewClient + Health() succeed
// against a live cluster. This is the smoke test that proves all Wave 2
// fail-fast guards stay out of the happy path.
func TestClusterIntegration_ClientHealth(t *testing.T) {
	client, cleanup := startCluster(t)
	defer cleanup()
	require.NoError(t, client.Health(context.Background()))
}

// TestClusterIntegration_IdempotencyClaimer_NoCrossSlot is the load-bearing
// test for B10: claim/commit Lua scripts read 2 KEYS per EVAL, and the
// hashtag wrapping must put both keys on the same slot. Without it Redis
// returns CROSSSLOT errors. The test runs against the real cluster across
// keys whose business portions land on different slots; if any key produces
// a CROSSSLOT error the hashtag fix has regressed.
func TestClusterIntegration_IdempotencyClaimer_NoCrossSlot(t *testing.T) {
	client, cleanup := startCluster(t)
	defer cleanup()

	ctx := context.Background()
	claimer := NewIdempotencyClaimer(client)
	uniq := time.Now().UnixNano()

	// Distinct business keys that, without hashtags, would scatter across
	// many slots. The fix forces them into the slot determined by the
	// business key's CRC16 — see idempotency_cluster_slot_test.go.
	keys := []string{
		fmt.Sprintf("idem:cluster:user:%d:1", uniq),
		fmt.Sprintf("idem:cluster:order:%d:2", uniq),
		fmt.Sprintf("idem:cluster:event:%d:3", uniq),
	}
	for _, k := range keys {
		t.Run(k, func(t *testing.T) {
			state, receipt, err := claimer.Claim(ctx, k, testtime.D5min, testtime.SelectAsyncSettle)
			require.NoError(t, err, "Claim must not return CROSSSLOT")
			require.Equal(t, idempotency.ClaimAcquired, state)
			require.NoError(t, receipt.Commit(ctx), "Commit must not return CROSSSLOT")

			// Re-Claim returns ClaimDone now that done key is set.
			state2, _, err := claimer.Claim(ctx, k, testtime.D5min, testtime.SelectAsyncSettle)
			require.NoError(t, err)
			assert.Equal(t, idempotency.ClaimDone, state2)
		})
	}
}

// TestClusterIntegration_DistLock confirms the single-key SET NX EX +
// token-guarded Lua release primitives still work under cluster (they were
// already cluster-safe pre-B10, but we verify because routing changes between
// standalone and cluster clients).
func TestClusterIntegration_DistLock(t *testing.T) {
	client, cleanup := startCluster(t)
	defer cleanup()

	ctx := context.Background()
	drv := NewRedisDriver(client.cmdable())
	key := fmt.Sprintf("integ:cluster:lock:%d", time.Now().UnixNano())

	ok, err := drv.SetNX(ctx, key, "token-A", testtime.CtxLong)
	require.NoError(t, err)
	require.True(t, ok)

	ok2, err := drv.SetNX(ctx, key, "token-B", testtime.CtxLong)
	require.NoError(t, err)
	assert.False(t, ok2, "second SetNX should fail while key is held")

	require.NoError(t, drv.Release(ctx, key, "token-A"))

	ok3, err := drv.SetNX(ctx, key, "token-C", testtime.CtxLong)
	require.NoError(t, err)
	require.True(t, ok3, "SetNX after Release should succeed")
	_ = drv.Release(ctx, key, "token-C")
}

// TestClusterIntegration_Cache exercises Cache GET/SET/DELETE round-trips on
// the cluster. These are single-key operations and should be cluster-safe.
func TestClusterIntegration_Cache(t *testing.T) {
	client, cleanup := startCluster(t)
	defer cleanup()

	ctx := context.Background()
	cache := NewCache(client)
	key := fmt.Sprintf("integ:cluster:cache:%d", time.Now().UnixNano())

	require.NoError(t, cache.Set(ctx, key, "hello", testtime.CtxLong))
	val, err := cache.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
	require.NoError(t, cache.Delete(ctx, key))

	val2, err := cache.Get(ctx, key)
	require.NoError(t, err)
	assert.Empty(t, val2, "Get after Delete returns empty string + nil")
}

// TestClusterIntegration_NonceStore confirms SET NX EX-backed replay
// protection still works on cluster. Single-key op; sanity check.
func TestClusterIntegration_NonceStore(t *testing.T) {
	client, cleanup := startCluster(t)
	defer cleanup()

	ctx := context.Background()
	store, err := NewNonceStore(client, auth.ServiceTokenNonceTTL)
	require.NoError(t, err)
	nonce := fmt.Sprintf("nonce-%d", time.Now().UnixNano())

	require.NoError(t, store.CheckAndMark(ctx, nonce), "first use should succeed")
	err = store.CheckAndMark(ctx, nonce)
	assert.ErrorIs(t, err, auth.ErrNonceReused, "replay must be rejected")
}
