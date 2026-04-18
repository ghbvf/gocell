//go:build integration

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/tests/testutil"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// startRedis launches a testcontainers Redis instance and returns a
// connected Client plus a cleanup function.
func startRedis(t *testing.T) (*Client, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcredis.Run(ctx, testutil.RedisImage)
	require.NoError(t, err, "start redis container")

	connStr, err := container.ConnectionString(ctx)
	require.NoError(t, err, "get redis connection string")

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
		Addr:        addr,
		Mode:        ModeStandalone,
		DialTimeout: 10 * time.Second,
		DistLockTTL: 5 * time.Second,
	})
	require.NoError(t, err, "create redis client")

	cleanup := func() {
		_ = client.Close()
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
	err := rdb.Set(ctx, "test:key", "hello", 30*time.Second).Err()
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

// TestIntegration_DistLockContention acquires a distributed lock,
// verifies a second acquire fails, then releases and re-acquires.
func TestIntegration_DistLockContention(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	dl := NewDistLock(client, 5*time.Second)

	// Acquire first lock.
	lock1, err := dl.Acquire(ctx, "lock:integration:test", 5*time.Second)
	require.NoError(t, err, "first Acquire should succeed")

	// Second acquire on the same key should fail (lock held).
	_, err = dl.Acquire(ctx, "lock:integration:test", 5*time.Second)
	assert.Error(t, err, "second Acquire should fail while lock is held")

	// Release the first lock.
	err = lock1.Release(ctx)
	require.NoError(t, err, "Release should succeed")

	// Now acquire should succeed again.
	lock2, err := dl.Acquire(ctx, "lock:integration:test", 5*time.Second)
	require.NoError(t, err, "Acquire after Release should succeed")
	defer func() { _ = lock2.Release(ctx) }()
}

// TestIntegration_IdempotencyClaimer verifies the Claimer two-phase model:
// Claim → ClaimAcquired, Commit, Claim again → ClaimDone.
func TestIntegration_IdempotencyClaimer(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	claimer := NewIdempotencyClaimer(client)
	key := "idem:integration:test:001"

	// First claim should acquire.
	state, receipt, err := claimer.Claim(ctx, key, 5*time.Minute, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Commit the receipt.
	err = receipt.Commit(ctx)
	require.NoError(t, err, "Commit should succeed")

	// Second claim should return ClaimDone.
	state, receipt2, err := claimer.Claim(ctx, key, 5*time.Minute, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimDone, state)
	assert.Nil(t, receipt2)
}

// TestIntegration_DistLock_Release_WithCancelledCtx_StillDeletesKey verifies
// the F5 contract: DEL executes and the key is removed from Redis even when
// the caller ctx passed to Release is already cancelled. Release must use a
// fresh Background-derived deadline (bounded by expiresAt) when the caller ctx
// is dead, so the Eval still runs.
func TestIntegration_DistLock_Release_WithCancelledCtx_StillDeletesKey(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	dl := NewDistLock(client, 0) // use default TTL from client config
	key := "test:f5:cancelled-ctx:" + t.Name()

	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer acquireCancel()
	lock, err := dl.Acquire(acquireCtx, key, 30*time.Second)
	require.NoError(t, err)

	// Verify the key exists pre-Release (Get returns nil error when key is present).
	_, err = client.cmdable().Get(context.Background(), key).Result()
	require.NoError(t, err, "key must exist before Release")

	// Cancel the ctx, then call Release with it — F5 contract: DEL still runs.
	releaseCtx, releaseCancel := context.WithCancel(context.Background())
	releaseCancel() // pre-cancel
	require.Error(t, releaseCtx.Err(), "sanity: ctx must be cancelled")

	require.NoError(t, lock.Release(releaseCtx), "Release with cancelled ctx must still succeed")

	// Verify key is actually gone from Redis (Get returns goredis.Nil when absent).
	_, err = client.cmdable().Get(context.Background(), key).Result()
	require.ErrorIs(t, err, goredis.Nil, "F5: key must be DELd from Redis even with cancelled caller ctx")
}

// TestIntegration_DistLock_Release_AfterNaturalExpiry_SkipsDEL verifies that
// when the lock's expiresAt is already in the past (simulating Redis TTL
// self-cleanup), Release returns nil without issuing a DEL to Redis.
//
// Implementation note: this test manipulates *Lock internals directly
// (same-package white-box) because stopping the renewal goroutine and
// backdating expiresAt is the only reliable way to drive this branch without
// waiting for real Redis TTL (which would require a multi-second sleep and
// a non-renewable lock).
func TestIntegration_DistLock_Release_AfterNaturalExpiry_SkipsDEL(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	dl := NewDistLock(client, 0)
	key := "test:f5:natural-expiry:" + t.Name()

	ctx := context.Background()
	lock, err := dl.Acquire(ctx, key, 200*time.Millisecond)
	require.NoError(t, err)

	// Cast to concrete type (same-package so internals are accessible).
	concrete, ok := lock.(*Lock)
	require.True(t, ok, "expected *Lock concrete type")

	// Stop the renewal goroutine so our manipulated expiresAt sticks.
	concrete.cancel()
	<-concrete.done

	// Simulate "already expired": backdate expiresAt, and DEL the Redis
	// key out-of-band to match what Redis TTL would have done.
	concrete.expiresAt.Store(time.Now().Add(-time.Second).UnixNano())
	_, err = client.cmdable().Del(context.Background(), key).Result()
	require.NoError(t, err)

	// Release should detect expired and skip DEL entirely.
	require.NoError(t, lock.Release(ctx), "Release must be nil when lock already expired")
}
