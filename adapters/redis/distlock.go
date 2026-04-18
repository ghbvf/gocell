package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/distlock"
)

// Compile-time interface satisfaction checks.
var (
	_ distlock.Locker = (*DistLock)(nil)
	_ distlock.Lock   = (*Lock)(nil)
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
// critical section is complete. Use a fresh context for Release, not the
// Acquire context, to avoid early cancellation:
//
//	lock, err := dl.Acquire(requestCtx, key, ttl)
//	if err != nil { return err }
//	defer lock.Release(context.Background())
//
// Safety: DistLock provides distributed mutual exclusion on a best-effort
// basis (efficiency lock). It is suitable for avoiding duplicate work. For
// correctness-critical paths, use application-level conditional writes
// (e.g., Postgres optimistic locking with row versions).
type Lock struct {
	rdb      cmdable
	key      string
	value    string
	cancel   context.CancelFunc
	done     chan struct{} // closed when renewLoop exits
	lost     chan struct{} // closed when lock is lost (renewal failure or Release)
	lostOnce sync.Once     // guards against double-close of lost

	// expiresAt holds the Unix nanosecond timestamp of the lock's current
	// expected expiry. It is set after SetNX success in Acquire and updated on
	// each successful renewal in renewLoop. Release reads this to compute its
	// DEL deadline without any hardcoded constant.
	expiresAt atomic.Int64
}

// Key returns the key that was passed to Acquire.
func (l *Lock) Key() string { return l.key }

// Lost returns a channel that is closed when the lock is known to have been
// lost. This occurs on renewal I/O failure, ownership loss (renew returns 0),
// or after Release (defensive signal for callers selecting on Lost).
//
// ref: github.com/hashicorp/consul/api lock.go — lostCh pattern
// ref: github.com/temporalio/sdk-go AggregatedWorker.stopC — signal-by-close idiom
func (l *Lock) Lost() <-chan struct{} { return l.lost }

// closeLost closes the lost channel exactly once.
func (l *Lock) closeLost() {
	l.lostOnce.Do(func() { close(l.lost) })
}

// Release releases the distributed lock. It stops the background renewal
// goroutine, waits for it to exit (bounded by the supplied ctx), closes
// Lost(), then issues the DEL command.
//
// The DEL's deadline is the lock's own natural expiry (acquire time + ttl,
// updated on each successful renewal). This guarantees Release never blocks
// longer than the lock could have existed anyway — beyond expiresAt, Redis'
// own TTL handling has already removed the key, so a DEL is redundant.
// No artificial timeout constant is required.
//
// When the caller-supplied ctx is still alive, it is kept as the DEL's
// parent so cancellation still propagates. When the ctx has already been
// cancelled, DEL proceeds anyway on a fresh Background-derived deadline,
// because the caller ctx being dead does not mean we should leave a key
// lingering — cleanup is best effort.
//
// It is safe to call multiple times; subsequent calls find the key already
// gone and return ErrLockLost (see below) rather than succeeding silently.
//
// Returns:
//   - nil on success (DEL removed our key) or when the lock already expired
//     via Redis TTL before Release was issued
//   - ErrLockRelease wrapping any Redis I/O error
//   - ErrLockLost when the lock is no longer owned (another holder took it,
//     our TTL expired between the script's GET and our DEL, or Release was
//     called twice on the same Lock)
//
// Use a fresh context for Release, not the Acquire context:
//
//	lock, err := dl.Acquire(requestCtx, key, ttl)
//	if err != nil { return err }
//	defer lock.Release(context.Background())
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

	// Signal Lost so any goroutine selecting on it can unblock after Release.
	// This happens before the DEL so that selectors waiting on Lost() unblock
	// immediately even if the DEL takes time or fails.
	l.closeLost()

	// F5: compute the DEL deadline from the lock's natural expiry — no hardcoded
	// timeout constant. If expiresAt is already past, the key has self-cleaned via
	// Redis TTL and issuing a DEL would be redundant.
	expiresAt := time.Unix(0, l.expiresAt.Load())
	if time.Now().After(expiresAt) {
		// Lock already expired via Redis-side TTL. The key is self-cleaning
		// (Redis removes expired keys lazily + actively). Issuing DEL is
		// redundant and would only confirm the key is gone. Skip.
		slog.Debug("redis: release skipped, lock expired via TTL",
			"key", l.key,
			"expiredAgo", time.Since(expiresAt).String())
		return nil
	}

	// Build DEL context: inherit caller's parent cancellation when alive (so a
	// subsequent caller-cancel still aborts the Eval), otherwise start fresh.
	// In either case, deadline is the lock's own natural expiry — waiting past
	// that is pointless because the key would self-expire anyway.
	parent := ctx
	if ctx.Err() != nil {
		parent = context.Background()
	}
	delCtx, cancel := context.WithDeadline(parent, expiresAt)
	defer cancel()

	result, err := l.rdb.Eval(delCtx, releaseLockScript, []string{l.key}, l.value).Int64()
	if err != nil {
		return errcode.Wrap(distlock.ErrLockRelease,
			fmt.Sprintf("redis: failed to release lock (key=%s)", l.key), err)
	}
	if result == 0 {
		// The Lua script found a different (or missing) value at the key,
		// meaning we no longer own this lock. Causes: TTL expired before our
		// DEL, another holder took over, or a prior Release already removed it.
		slog.Warn("redis: lock already released or expired",
			"key", l.key)
		return errcode.New(distlock.ErrLockLost,
			fmt.Sprintf("redis: lock no longer owned (key=%s)", l.key))
	}
	slog.Debug("redis: lock released", "key", l.key)
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
// distlock.ErrLockTimeout is returned.
//
// The returned Lock starts a background renewal goroutine that extends the TTL
// at half the lock period until Release is called or the context is cancelled.
func (d *DistLock) Acquire(ctx context.Context, key string, ttl time.Duration) (distlock.Lock, error) {
	if ttl == 0 {
		ttl = d.ttl
	}

	value, err := randomToken()
	if err != nil {
		return nil, errcode.Wrap(distlock.ErrLockAcquire,
			"redis: failed to generate lock token", err)
	}

	ok, err := d.rdb.SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		return nil, errcode.Wrap(distlock.ErrLockAcquire,
			fmt.Sprintf("redis: failed to acquire lock (key=%s)", key), err)
	}
	if !ok {
		return nil, errcode.New(distlock.ErrLockTimeout,
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
		lost:   make(chan struct{}),
	}
	// Record the initial expected expiry time so Release can compute its
	// DEL deadline without any hardcoded constant.
	lock.expiresAt.Store(time.Now().Add(ttl).UnixNano())

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
			// O1: explicitly close Lost() here via sync.Once so that callers
			// selecting on Lost() are unblocked when the renewal context is
			// cancelled (e.g. by Release or external cancellation). Previously
			// relied on an unwritten invariant that Release always calls
			// closeLost — making it explicit removes the coupling.
			lock.closeLost()
			return
		case <-ticker.C:
			ttlMs := ttl.Milliseconds()
			result, err := d.rdb.Eval(ctx, renewLockScript,
				[]string{lock.key}, lock.value, ttlMs).Int64()
			if err != nil {
				slog.Error("redis: lock renewal failed",
					"key", lock.key,
					"error", err)
				lock.closeLost()
				return
			}
			if result == 0 {
				slog.Warn("redis: lock lost during renewal",
					"key", lock.key)
				lock.closeLost()
				return
			}
			// Update the expected expiry so Release can use it as a DEL deadline.
			lock.expiresAt.Store(time.Now().Add(ttl).UnixNano())
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
		return "", errcode.Wrap(distlock.ErrLockAcquire, "redis: random token generation failed", err)
	}
	return hex.EncodeToString(b), nil
}
