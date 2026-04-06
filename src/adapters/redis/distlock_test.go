package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDistLock_AcquireAndRelease(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:1", 10*time.Second)
	require.NoError(t, err)
	require.NotNil(t, lock)
	assert.Equal(t, "test:lock:1", lock.key)
	assert.NotEmpty(t, lock.value)

	// Key should exist in the mock store.
	mock.mu.Lock()
	_, exists := mock.store["test:lock:1"]
	mock.mu.Unlock()
	assert.True(t, exists)

	// Release.
	err = lock.Release(ctx)
	assert.NoError(t, err)

	// Key should be removed.
	mock.mu.Lock()
	_, exists = mock.store["test:lock:1"]
	mock.mu.Unlock()
	assert.False(t, exists)
}

func TestDistLock_AcquireAlreadyHeld(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	// First acquire succeeds.
	lock1, err := dl.Acquire(ctx, "test:lock:conflict", 10*time.Second)
	require.NoError(t, err)
	defer func() {
		_ = lock1.Release(ctx)
	}()

	// Second acquire should fail.
	lock2, err := dl.Acquire(ctx, "test:lock:conflict", 10*time.Second)
	require.Error(t, err)
	assert.Nil(t, lock2)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_LOCK_TIMEOUT")
	assert.Contains(t, err.Error(), "lock already held")
}

func TestDistLock_AcquireSetNXError(t *testing.T) {
	mock := newMockCmdable()
	mock.setNXErr = errMock
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:err", 10*time.Second)
	require.Error(t, err)
	assert.Nil(t, lock)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_LOCK_ACQUIRED")
}

func TestDistLock_ReleaseIdempotent(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:idem", 10*time.Second)
	require.NoError(t, err)

	// First release succeeds.
	err = lock.Release(ctx)
	assert.NoError(t, err)

	// Second release is a no-op (Lua returns 0, no error).
	err = lock.Release(ctx)
	assert.NoError(t, err)
}

func TestDistLock_ReleaseEvalError(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:evalerr", 10*time.Second)
	require.NoError(t, err)

	// Inject eval error for release.
	mock.evalErr = errMock

	err = lock.Release(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_LOCK_RELEASE")
}

func TestDistLock_DefaultTTL(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 0)

	// Should default to 30s.
	assert.Equal(t, 30*time.Second, dl.ttl)
}

func TestDistLock_UsesClientConfigTTL(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{DistLockTTL: 45 * time.Second})
	dl := NewDistLock(client, 0)

	assert.Equal(t, 45*time.Second, dl.ttl)
}

func TestRandomToken(t *testing.T) {
	token1, err := randomToken()
	require.NoError(t, err)
	assert.Len(t, token1, 32) // 16 bytes = 32 hex chars.

	token2, err := randomToken()
	require.NoError(t, err)
	assert.NotEqual(t, token1, token2)
}

func TestDistLock_ReleaseWaitsForRenewalGoroutine(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:done", 10*time.Second)
	require.NoError(t, err)
	require.NotNil(t, lock.done)

	// Release should not hang — it cancels renewal and waits for done.
	err = lock.Release(ctx)
	assert.NoError(t, err)

	// done channel should be closed after Release.
	select {
	case <-lock.done:
		// OK — goroutine exited.
	default:
		t.Fatal("done channel should be closed after Release")
	}
}

func TestDistLock_AcquireTimeoutCtxDoesNotKillRenewal(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)

	// Use a very short timeout ctx — only limits the SetNX call.
	acquireCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	lock, err := dl.Acquire(acquireCtx, "test:lock:timeout-ctx", 10*time.Second)
	require.NoError(t, err)

	// Wait for the acquire ctx to expire.
	<-acquireCtx.Done()

	// Renewal goroutine must still be running (done channel open).
	select {
	case <-lock.done:
		t.Fatal("renewal goroutine stopped after acquire ctx expired — should be independent")
	default:
		// OK — goroutine still alive.
	}

	// Release with a fresh context.
	err = lock.Release(context.Background())
	assert.NoError(t, err)
}
