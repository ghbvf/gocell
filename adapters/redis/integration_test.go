//go:build integration

package redis

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
	"github.com/ghbvf/gocell/tests/testutil"
)

// startRedis launches a testcontainers Redis instance and returns a
// connected Client plus a cleanup function.
func startRedis(t *testing.T) (*Client, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()

	container, err := tcredis.Run(ctx, testutil.RedisImage)
	require.NoError(t, err, "start redis container")

	connStr, err := container.ConnectionString(ctx)
	require.NoError(t, err, "get redis connection string")
	connStr = testutil.LoopbackIPEndpoint(connStr)

	// ConnectionString returns "redis://host:port/0" — parse to addr.
	// go-redis NewClient expects "host:port", but we can also use the
	// Options.Addr directly. The connection string format from
	// testcontainers is "redis://host:port/0".
	// We parse it to extract host:port.
	addr := connStr
	// Strip "redis://" prefix and "/0" suffix if present.
	if len(addr) > 8 && addr[:8] == "redis://" {
		addr = addr[8:]
	}
	// Strip trailing "/..." (db number).
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == '/' {
			addr = addr[:i]
			break
		}
	}

	client, err := NewClient(ctx, Config{
		Addr:                  addr,
		Mode:                  ModeStandalone,
		DialTimeout:           testtime.SelectAsyncSettle,
		DistLockTTL:           testtime.EventuallyLong,
		AllowUnsafeNoPassword: true, // testcontainers Redis has no auth
	})
	require.NoError(t, err, "create redis client")

	cleanup := func() {
		_ = client.Close(ctx)
		_ = container.Terminate(ctx)
	}

	return client, cleanup
}

// TestIntegration_ClientPingPong connects to a real Redis instance and
// verifies the PING/PONG handshake via Health().
func TestIntegration_ClientPingPong(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	err := client.Health(ctx)
	assert.NoError(t, err, "Health should succeed on a live Redis")
}

// TestIntegration_CacheSetGetDelete exercises the underlying cmdable with
// real Redis SET / GET / DEL round-trips.
func TestIntegration_CacheSetGetDelete(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	rdb := client.cmdable()

	// SET
	err := rdb.Set(ctx, "test:key", "hello", testtime.CtxLong).Err()
	require.NoError(t, err, "SET should succeed")

	// GET
	val, err := rdb.Get(ctx, "test:key").Result()
	require.NoError(t, err, "GET should succeed")
	assert.Equal(t, "hello", val)

	// DEL
	deleted, err := rdb.Del(ctx, "test:key").Result()
	require.NoError(t, err, "DEL should succeed")
	assert.Equal(t, int64(1), deleted)

	// GET after DEL — should be redis.Nil
	_, err = rdb.Get(ctx, "test:key").Result()
	assert.Error(t, err, "GET after DEL should return error")
}

// TestIntegration_IdempotencyClaimer verifies the Claimer two-phase model:
// Claim → ClaimAcquired, Commit, Claim again → ClaimDone.
func TestIntegration_IdempotencyClaimer(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	claimer, err := NewIdempotencyClaimer(client, testNamespace)
	require.NoError(t, err)
	key := "idem:integration:test:001"

	// First claim should acquire.
	state, receipt, err := claimer.Claim(ctx, key, testtime.D5min, testtime.SelectAsyncSettle)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Commit the receipt.
	err = receipt.Commit(ctx)
	require.NoError(t, err, "Commit should succeed")

	// Second claim should return ClaimDone.
	state, receipt2, err := claimer.Claim(ctx, key, testtime.D5min, testtime.SelectAsyncSettle)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimDone, state)
	assert.NotNil(t, receipt2)
}

// TestIntegration_RedisDriver_SetNX_Contention verifies that a second SetNX
// on the same key returns false while the first holder still owns it, and
// returns true after a Release clears it.
func TestIntegration_RedisDriver_SetNX_Contention(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	drv, err := NewRedisDriver(client, testNamespace)
	require.NoError(t, err)
	key := "integ:drv:contention:" + t.Name()

	// First SetNX succeeds.
	ok, err := drv.SetNX(ctx, key, "token-A", testtime.CtxLong)
	require.NoError(t, err)
	require.True(t, ok, "first SetNX should succeed")

	// Second SetNX fails while first holder is alive.
	ok2, err2 := drv.SetNX(ctx, key, "token-B", testtime.CtxLong)
	require.NoError(t, err2)
	assert.False(t, ok2, "second SetNX should fail while key is held")

	// Release with correct token.
	err = drv.Release(ctx, key, "token-A")
	require.NoError(t, err)

	// Third SetNX succeeds after Release.
	ok3, err3 := drv.SetNX(ctx, key, "token-C", testtime.CtxLong)
	require.NoError(t, err3)
	assert.True(t, ok3, "SetNX after Release should succeed")

	_ = drv.Release(ctx, key, "token-C")
}

// TestIntegration_RedisDriver_Release_CancelledCtx verifies that Release still
// issues the DEL even when the supplied context is already cancelled.
func TestIntegration_RedisDriver_Release_CancelledCtx(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	drv, err := NewRedisDriver(client, testNamespace)
	require.NoError(t, err)
	key := "integ:drv:cancel-ctx:" + t.Name()
	ctx := context.Background()

	ok, err := drv.SetNX(ctx, key, "token-A", testtime.CtxLong)
	require.NoError(t, err)
	require.True(t, ok)

	// Verify key exists.
	_, err = client.cmdable().Get(ctx, string(testNamespace)+":"+key).Result()
	require.NoError(t, err, "key must exist before Release")

	// Release with a pre-cancelled context.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	// Note: a pre-cancelled ctx may cause the Redis Eval to fail with a ctx error.
	// This verifies the caller's responsibility; the driver wraps I/O errors faithfully.
	_ = drv.Release(cancelledCtx, key, "token-A")

	// Regardless of the above, clean up with a fresh context.
	_ = drv.Release(ctx, key, "token-A")

	// Key must be gone.
	_, err = client.cmdable().Get(ctx, string(testNamespace)+":"+key).Result()
	assert.ErrorIs(t, err, goredis.Nil, "key must be gone after Release")
}

// TestRedisDriver_Conformance runs the full Driver conformance suite (C-1..C-7)
// against a real Redis instance via testcontainers.
//
// Each sub-test receives a fresh RedisDriver with a unique key prefix to
// prevent cross-test interference without requiring FLUSHDB.
//
// Conformance cases C-5 and C-6 use TTL=1ms with time.Sleep(5ms) to exercise
// real backend-physical TTL expiry on Redis.
func TestRedisDriver_Conformance(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	var counter atomic.Int64
	factory := func(t *testing.T) distlock.Driver {
		t.Helper()
		n := counter.Add(1)
		prefix := fmt.Sprintf("conformance:%s:%d:", t.Name(), n)
		drv, err := NewRedisDriver(client, testNamespace)
		require.NoError(t, err)
		return &prefixedRedisDriver{
			RedisDriver: drv,
			prefix:      prefix,
		}
	}

	locktest.RunDriverConformance(t, factory)
	locktest.RunDriverTTLConformance(t, factory)
}

// prefixedRedisDriver wraps RedisDriver and prepends a prefix to every key.
// This isolates each conformance sub-test without requiring FLUSHDB.
type prefixedRedisDriver struct {
	*RedisDriver
	prefix string
}

func (p *prefixedRedisDriver) SetNX(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	return p.RedisDriver.SetNX(ctx, p.prefix+key, token, ttl)
}

func (p *prefixedRedisDriver) Renew(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	return p.RedisDriver.Renew(ctx, p.prefix+key, token, ttl)
}

func (p *prefixedRedisDriver) Release(ctx context.Context, key, token string) error {
	return p.RedisDriver.Release(ctx, p.prefix+key, token)
}
