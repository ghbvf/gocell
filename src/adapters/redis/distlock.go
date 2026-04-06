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

// fenceTokenScript atomically checks lock ownership (KEYS[1] == ARGV[1])
// before incrementing the per-key fencing counter (KEYS[2]). Returns the
// new counter value if owned, or 0 if the caller no longer holds the lock.
// ref: Kleppmann "How to do distributed locking" — fencing tokens
const fenceTokenScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("INCR", KEYS[2])
else
    return 0
end
`

// Lock represents an acquired distributed lock. It must be released when the
// critical section is complete. Use a fresh context for Release, not the
// Acquire context, to avoid early cancellation:
//
//	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	defer lock.Release(cleanupCtx)
//
// Safety: DistLock provides distributed mutual exclusion on a best-effort
// basis. It is suitable for efficiency (avoiding duplicate work). For
// correctness-critical paths that must prevent stale writes after lock
// expiry, use FenceToken() and enforce monotonicity at the downstream store
// (e.g., UPDATE ... WHERE fence_token < $1).
type Lock struct {
	rdb    cmdable
	key    string
	value  string
	cancel context.CancelFunc
	done   chan struct{} // closed when renewLoop exits
}

// Release releases the distributed lock. It stops the background renewal
// goroutine and waits for it to exit before issuing the release command.
// It is safe to call multiple times; subsequent calls are no-ops.
//
// Use a fresh context for Release, not the Acquire context:
//
//	lock, err := dl.Acquire(requestCtx, key, ttl)
//	if err != nil { return err }
//	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	defer lock.Release(cleanupCtx)
func (l *Lock) Release(ctx context.Context) error {
	// Stop renewal goroutine and wait for it to exit.
	if l.cancel != nil {
		l.cancel()
	}
	if l.done != nil {
		select {
		case <-l.done:
		case <-ctx.Done():
			// Goroutine will exit eventually via renewCtx cancellation above.
		}
	}

	result, err := l.rdb.Eval(ctx, releaseLockScript, []string{l.key}, l.value).Int64()
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisLockRelease,
			fmt.Sprintf("redis: failed to release lock (key=%s)", l.key), err)
	}
	if result == 0 {
		slog.Warn("redis: lock already released or expired",
			"key", l.key)
	}
	return nil
}

// FenceToken generates a monotonically increasing fencing token for this lock.
// The token is only issued if the caller still owns the lock (atomic GET+INCR
// via Lua script). A stale holder whose lease has expired will receive an error.
//
// Callers pass this token to downstream stores which reject writes bearing a
// token older than the highest seen. Enforcement is the store's responsibility.
//
// ref: Kleppmann "How to do distributed locking" — fencing tokens
func (l *Lock) FenceToken(ctx context.Context) (int64, error) {
	fenceKey := "fence:" + l.key
	token, err := l.rdb.Eval(ctx, fenceTokenScript,
		[]string{l.key, fenceKey}, l.value).Int64()
	if err != nil {
		return 0, errcode.Wrap(ErrAdapterRedisLockAcquire,
			fmt.Sprintf("redis: fence token generation failed (key=%s)", l.key), err)
	}
	if token == 0 {
		return 0, errcode.New(ErrAdapterRedisLockAcquire,
			fmt.Sprintf("redis: lock not owned, cannot generate fence token (key=%s)", l.key))
	}
	return token, nil
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

	// Renewal runs independently of the acquire context: caller ctx may
	// carry a deadline that only limits the SetNX call, not the lock
	// lifetime. Release() cancels this context and waits for the
	// goroutine to exit via the done channel.
	renewCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lock := &Lock{
		rdb:    d.rdb,
		key:    key,
		value:  value,
		cancel: cancel,
		done:   done,
	}

	// Start background renewal at half the TTL interval.
	go func() {
		defer close(done)
		d.renewLoop(renewCtx, lock, ttl)
	}()

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
		return "", errcode.Wrap(ErrAdapterRedisLockAcquire, "redis: random token generation failed", err)
	}
	return hex.EncodeToString(b), nil
}
