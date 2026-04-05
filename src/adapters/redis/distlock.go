package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// releaseLockScript is a Lua script that atomically releases a lock only if
// the caller still owns it (value matches). This prevents releasing a lock
// that has been acquired by another holder after expiry.
const releaseLockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
`

// renewLockScript is a Lua script that atomically renews a lock TTL only if
// the caller still owns it.
const renewLockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
    return 0
end
`

// Lock represents an acquired distributed lock. It must be released when the
// critical section is complete.
type Lock struct {
	rdb    cmdable
	key    string
	value  string
	cancel context.CancelFunc
}

// Release releases the distributed lock. It is safe to call multiple times;
// subsequent calls are no-ops (the Lua script checks ownership).
func (l *Lock) Release(ctx context.Context) error {
	// Stop renewal goroutine if running.
	if l.cancel != nil {
		l.cancel()
	}

	result, err := l.rdb.Eval(ctx, releaseLockScript, []string{l.key}, l.value).Int64()
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisLockAcquire,
			fmt.Sprintf("redis: failed to release lock (key=%s)", l.key), err)
	}
	if result == 0 {
		slog.Warn("redis: lock already released or expired",
			"key", l.key)
	}
	return nil
}

// DistLock provides distributed locking backed by Redis.
type DistLock struct {
	rdb cmdable
	ttl time.Duration
}

// NewDistLock creates a new DistLock using the given Client.
// If ttl is zero, the client's configured DistLockTTL is used (default 30s).
func NewDistLock(client *Client, ttl time.Duration) *DistLock {
	if ttl == 0 {
		ttl = client.config.DistLockTTL
	}
	return &DistLock{
		rdb: client.cmdable(),
		ttl: ttl,
	}
}

// newDistLockFromCmdable creates a DistLock with a pre-built cmdable for testing.
func newDistLockFromCmdable(rdb cmdable, ttl time.Duration) *DistLock {
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	return &DistLock{rdb: rdb, ttl: ttl}
}

// Acquire attempts to acquire a distributed lock for the given key.
// It uses SET NX with the configured TTL. If the lock is already held,
// ErrAdapterRedisLockTimeout is returned.
//
// The returned Lock starts a background renewal goroutine that extends the TTL
// at half the lock period until Release is called or the context is cancelled.
func (d *DistLock) Acquire(ctx context.Context, key string, ttl time.Duration) (*Lock, error) {
	if ttl == 0 {
		ttl = d.ttl
	}

	value, err := randomToken()
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterRedisLockAcquire,
			"redis: failed to generate lock token", err)
	}

	ok, err := d.rdb.SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterRedisLockAcquire,
			fmt.Sprintf("redis: failed to acquire lock (key=%s)", key), err)
	}
	if !ok {
		return nil, errcode.New(ErrAdapterRedisLockTimeout,
			fmt.Sprintf("redis: lock already held (key=%s)", key))
	}

	renewCtx, cancel := context.WithCancel(context.Background())
	lock := &Lock{
		rdb:    d.rdb,
		key:    key,
		value:  value,
		cancel: cancel,
	}

	// Start background renewal at half the TTL interval.
	go d.renewLoop(renewCtx, lock, ttl)

	slog.Debug("redis: lock acquired",
		"key", key,
		"ttl", ttl.String())

	return lock, nil
}

// renewLoop periodically extends the lock TTL until cancelled.
func (d *DistLock) renewLoop(ctx context.Context, lock *Lock, ttl time.Duration) {
	interval := ttl / 2
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ttlMs := ttl.Milliseconds()
			result, err := d.rdb.Eval(ctx, renewLockScript,
				[]string{lock.key}, lock.value, ttlMs).Int64()
			if err != nil {
				slog.Error("redis: lock renewal failed",
					"key", lock.key,
					"error", err)
				return
			}
			if result == 0 {
				slog.Warn("redis: lock lost during renewal",
					"key", lock.key)
				return
			}
			slog.Debug("redis: lock renewed",
				"key", lock.key,
				"ttl", ttl.String())
		}
	}
}

// randomToken generates a cryptographically random hex token for lock ownership.
func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
