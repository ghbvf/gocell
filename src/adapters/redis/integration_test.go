//go:build integration

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// startRedis launches a testcontainers Redis instance and returns a
// connected Client plus a cleanup function.
func startRedis(t *testing.T) (*Client, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
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

// TestIntegration_IdempotencyKeyExpiry verifies the IdempotencyChecker:
// IsProcessed(new key) = false -> MarkProcessed -> IsProcessed = true.
func TestIntegration_IdempotencyKeyExpiry(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	ctx := context.Background()
	checker := NewIdempotencyChecker(client)
	key := "idem:integration:test:001"

	// New key should not be processed.
	processed, err := checker.IsProcessed(ctx, key)
	require.NoError(t, err)
	assert.False(t, processed, "new key should not be processed")

	// Mark as processed.
	err = checker.MarkProcessed(ctx, key, 10*time.Second)
	require.NoError(t, err, "MarkProcessed should succeed")

	// Now it should be processed.
	processed, err = checker.IsProcessed(ctx, key)
	require.NoError(t, err)
	assert.True(t, processed, "key should be processed after MarkProcessed")
}
