package redis

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time assertion: *RedisDriver implements distlock.Driver.
var _ distlock.Driver = (*RedisDriver)(nil)

// TestRedisDriver_CompileTimeInterfaceAssertion is a test-visible compile-time
// check that *RedisDriver satisfies distlock.Driver.
func TestRedisDriver_CompileTimeInterfaceAssertion(t *testing.T) {
	var _ distlock.Driver = (*RedisDriver)(nil)
}

// TestRedisDriver_LuaScriptContent guards against accidental edits to either
// Lua script. The test will fail at compile time (constant reference) if the
// script strings differ from what is expected.
func TestRedisDriver_LuaScriptContent(t *testing.T) {
	wantRelease := "\nif redis.call(\"GET\", KEYS[1]) == ARGV[1] then\n" +
		"    return redis.call(\"DEL\", KEYS[1])\nelse\n    return 0\nend\n"
	wantRenew := "\nif redis.call(\"GET\", KEYS[1]) == ARGV[1] then\n" +
		"    return redis.call(\"PEXPIRE\", KEYS[1], ARGV[2])\nelse\n    return 0\nend\n"

	assert.Equal(t, wantRelease, releaseLockScript, "releaseLockScript must not be modified")
	assert.Equal(t, wantRenew, renewLockScript, "renewLockScript must not be modified")
}

// TestRedisDriver_SetNX_Happy verifies SetNX returns (true, nil) on the first
// call when the key is not yet held.
func TestRedisDriver_SetNX_Happy(t *testing.T) {
	mock := newMockCmdable()
	drv := NewRedisDriver(mock)
	ctx := context.Background()

	ok, err := drv.SetNX(ctx, "lock:setnx:happy", "token-A", time.Second)
	require.NoError(t, err)
	assert.True(t, ok, "first SetNX should return true")
}

// TestRedisDriver_SetNX_Busy verifies SetNX returns (false, nil) when the key
// is already held by another token.
func TestRedisDriver_SetNX_Busy(t *testing.T) {
	mock := newMockCmdable()
	drv := NewRedisDriver(mock)
	ctx := context.Background()

	// First acquire.
	ok, err := drv.SetNX(ctx, "lock:setnx:busy", "token-A", time.Second)
	require.NoError(t, err)
	require.True(t, ok)

	// Second acquire — key is already held.
	ok2, err2 := drv.SetNX(ctx, "lock:setnx:busy", "token-B", time.Second)
	require.NoError(t, err2)
	assert.False(t, ok2, "second SetNX on held key should return false")
}

// TestRedisDriver_SetNX_IOError verifies SetNX returns (false, wrapped err)
// when Redis returns an I/O error.
func TestRedisDriver_SetNX_IOError(t *testing.T) {
	mock := newMockCmdable()
	mock.setNXErr = errMock
	drv := NewRedisDriver(mock)

	ok, err := drv.SetNX(context.Background(), "lock:setnx:ioerr", "token-A", time.Second)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "redis distlock: SetNX")
}

// TestRedisDriver_Renew_Happy verifies Renew returns (true, nil) when token
// matches and the lock is extended.
func TestRedisDriver_Renew_Happy(t *testing.T) {
	mock := newMockCmdable()
	drv := NewRedisDriver(mock)
	ctx := context.Background()

	// Acquire the key first.
	ok, err := drv.SetNX(ctx, "lock:renew:happy", "token-A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	held, err := drv.Renew(ctx, "lock:renew:happy", "token-A", time.Minute)
	require.NoError(t, err)
	assert.True(t, held, "Renew with correct token should return held=true")
}

// TestRedisDriver_Renew_TokenMismatch verifies Renew returns (false, nil) when
// the token does not match (Lua returns 0 — not an I/O error).
func TestRedisDriver_Renew_TokenMismatch(t *testing.T) {
	mock := newMockCmdable()
	drv := NewRedisDriver(mock)
	ctx := context.Background()

	// Acquire with token-A.
	ok, err := drv.SetNX(ctx, "lock:renew:mismatch", "token-A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Try to renew with wrong token.
	held, err := drv.Renew(ctx, "lock:renew:mismatch", "token-B", time.Minute)
	require.NoError(t, err, "token mismatch is not an I/O error")
	assert.False(t, held, "Renew with wrong token should return held=false")
}

// TestRedisDriver_Renew_IOError verifies Renew returns (false, wrapped err) on
// Redis I/O failure.
func TestRedisDriver_Renew_IOError(t *testing.T) {
	mock := newMockCmdable()
	mock.evalErr = errMock
	drv := NewRedisDriver(mock)

	held, err := drv.Renew(context.Background(), "lock:renew:ioerr", "token-A", time.Minute)
	require.Error(t, err)
	assert.False(t, held)
	assert.Contains(t, err.Error(), "redis distlock: Renew")
}

// TestRedisDriver_Release_Happy verifies Release returns nil and the key is
// removed when the token matches.
func TestRedisDriver_Release_Happy(t *testing.T) {
	mock := newMockCmdable()
	drv := NewRedisDriver(mock)
	ctx := context.Background()

	ok, err := drv.SetNX(ctx, "lock:release:happy", "token-A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	err = drv.Release(ctx, "lock:release:happy", "token-A")
	require.NoError(t, err)

	// Key should be gone.
	mock.mu.Lock()
	_, exists := mock.store["lock:release:happy"]
	mock.mu.Unlock()
	assert.False(t, exists, "key must be removed after Release")
}

// TestRedisDriver_Release_NoOp verifies Release returns nil (not an error) when
// the token does not match (Lua returns 0 — idempotent by contract).
func TestRedisDriver_Release_NoOp(t *testing.T) {
	mock := newMockCmdable()
	drv := NewRedisDriver(mock)
	ctx := context.Background()

	ok, err := drv.SetNX(ctx, "lock:release:noop", "token-A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Release with wrong token — should be a no-op, not an error.
	err = drv.Release(ctx, "lock:release:noop", "token-B")
	assert.NoError(t, err, "Release with wrong token must return nil (idempotent)")

	// Key must still exist (token-A still holds it).
	mock.mu.Lock()
	_, exists := mock.store["lock:release:noop"]
	mock.mu.Unlock()
	assert.True(t, exists, "key must still exist after wrong-token Release")
}

// TestRedisDriver_Release_IOError verifies Release returns a wrapped error on
// Redis I/O failure.
func TestRedisDriver_Release_IOError(t *testing.T) {
	mock := newMockCmdable()
	mock.evalErr = errMock
	drv := NewRedisDriver(mock)

	err := drv.Release(context.Background(), "lock:release:ioerr", "token-A")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis distlock: Release")
}
