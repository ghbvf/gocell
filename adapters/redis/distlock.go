package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
)

// Compile-time assertion: *RedisDriver satisfies distlock.Driver.
var _ distlock.Driver = (*RedisDriver)(nil)

// releaseLockScript atomically releases a lock only if the caller still owns
// it (token matches). Prevents releasing a lock acquired by another holder
// after expiry.
const releaseLockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
`

// renewLockScript atomically renews a lock TTL only if the caller still owns it.
const renewLockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
    return 0
end
`

// RedisDriver implements runtime/distlock.Driver using Redis SET NX EX and
// two Lua scripts for atomic renew and release operations. All lock keys
// are prefixed with the constructor-injected KeyNamespace so per-cell or
// per-role driver instances cannot collide.
type RedisDriver struct {
	rdb cmdable
	ns  KeyNamespace
}

// NewRedisDriver creates a RedisDriver backed by the given cmdable and
// KeyNamespace. ns is validated up front; an invalid namespace produces
// an error.
func NewRedisDriver(rdb cmdable, ns KeyNamespace) (*RedisDriver, error) {
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	return &RedisDriver{rdb: rdb, ns: ns}, nil
}

// SetNX attempts to set key=token with the given TTL using Redis SET NX EX.
// Returns (true, nil) on success, (false, nil) when the key is already held,
// and (false, err) on I/O failure.
func (d *RedisDriver) SetNX(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	prefixed := d.ns.apply(key)
	ok, err := d.rdb.SetNX(ctx, prefixed, token, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redis distlock: SetNX: %w", err)
	}
	return ok, nil
}

// Renew extends the TTL of an existing lock only if token still matches.
// Returns (true, nil) on success, (false, nil) when the token no longer
// matches (ownership lost — not an I/O error), and (false, err) on I/O failure.
func (d *RedisDriver) Renew(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	prefixed := d.ns.apply(key)
	result, err := d.rdb.Eval(ctx, renewLockScript, []string{prefixed}, token, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("redis distlock: Renew: %w", err)
	}
	return result == 1, nil
}

// Release deletes the lock key only if token still matches.
// Returns nil on success or when the key is already gone (idempotent).
// Returns a non-nil error only on I/O failure.
func (d *RedisDriver) Release(ctx context.Context, key, token string) error {
	prefixed := d.ns.apply(key)
	_, err := d.rdb.Eval(ctx, releaseLockScript, []string{prefixed}, token).Int64()
	if err != nil {
		return fmt.Errorf("redis distlock: Release: %w", err)
	}
	return nil
}
