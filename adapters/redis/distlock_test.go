package redis

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDistLock_AcquireAndRelease(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lockIface, err := dl.Acquire(ctx, "test:lock:1", 10*time.Second)
	require.NoError(t, err)
	require.NotNil(t, lockIface)
	lock := lockIface.(*Lock)
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
	assert.Contains(t, err.Error(), string(distlock.ErrLockTimeout))
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
	assert.Contains(t, err.Error(), string(distlock.ErrLockAcquire))
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
	assert.Contains(t, err.Error(), string(distlock.ErrLockRelease))
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

	lockIface, err := dl.Acquire(ctx, "test:lock:done", 10*time.Second)
	require.NoError(t, err)
	lock := lockIface.(*Lock)
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

	lockIface, err := dl.Acquire(acquireCtx, "test:lock:timeout-ctx", 10*time.Second)
	require.NoError(t, err)
	lock := lockIface.(*Lock)

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

// --- New tests for runtime/distlock interface compliance ---

// TestDistLock_ImplementsDistlockLocker is a compile-time assertion that
// *DistLock satisfies distlock.Locker and *Lock satisfies distlock.Lock.
var (
	_ distlock.Locker = (*DistLock)(nil)
	_ distlock.Lock   = (*Lock)(nil)
)

func TestDistLock_ImplementsDistlockLocker(t *testing.T) {
	// The compile-time var block above is the real assertion.
	// This test body ensures the assertion appears in test output.
	var _ distlock.Locker = (*DistLock)(nil)
	var _ distlock.Lock = (*Lock)(nil)
}

// TestLock_Lost_ChannelReturnedNonNil asserts that after Acquire, Lost()
// returns a non-nil channel that is not yet closed.
func TestLock_Lost_ChannelReturnedNonNil(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:lost-nonnill", 10*time.Second)
	require.NoError(t, err)
	defer func() { _ = lock.Release(context.Background()) }()

	ch := lock.Lost()
	require.NotNil(t, ch, "Lost() must return a non-nil channel")

	// Channel must not be closed yet.
	select {
	case <-ch:
		t.Fatal("Lost() channel should not be closed immediately after Acquire")
	default:
		// OK
	}
}

// TestLock_Lost_ClosedOnRenewalFailure asserts that Lost() is closed when
// the background renewal Eval returns an I/O error.
func TestLock_Lost_ClosedOnRenewalFailure(t *testing.T) {
	mock := newMockCmdable()
	// Short TTL so the renewal ticker fires quickly.
	ttl := 200 * time.Millisecond
	dl := newDistLockFromCmdable(mock, ttl)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:renewal-fail", ttl)
	require.NoError(t, err)
	defer func() { _ = lock.Release(context.Background()) }()

	// Inject error so the next Eval (renewal) fails.
	mock.mu.Lock()
	mock.evalErr = errMock
	mock.mu.Unlock()

	// Wait for Lost() to be closed (renewal fires at ttl/2 = 100ms).
	select {
	case <-lock.Lost():
		// Good — renewal failure signalled via Lost().
	case <-time.After(2 * time.Second):
		t.Fatal("Lost() channel was not closed after renewal I/O failure")
	}
}

// TestLock_Lost_ClosedOnOwnershipLost asserts that Lost() is closed when the
// renew Lua script returns 0 (another holder took ownership).
func TestLock_Lost_ClosedOnOwnershipLost(t *testing.T) {
	mock := newMockCmdable()
	ttl := 200 * time.Millisecond
	dl := newDistLockFromCmdable(mock, ttl)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:ownership-lost", ttl)
	require.NoError(t, err)
	defer func() { _ = lock.Release(context.Background()) }()

	// Make renewals return 0 — ownership taken by another holder.
	zero := int64(0)
	mock.mu.Lock()
	mock.evalRenewResult = &zero
	mock.mu.Unlock()

	select {
	case <-lock.Lost():
		// Good — ownership loss signalled.
	case <-time.After(2 * time.Second):
		t.Fatal("Lost() channel was not closed after ownership loss (renew returned 0)")
	}
}

// TestLock_Key_ReturnsAcquiredKey asserts Key() equals the key passed to Acquire.
func TestLock_Key_ReturnsAcquiredKey(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	const wantKey = "test:lock:key-check"
	lock, err := dl.Acquire(ctx, wantKey, 10*time.Second)
	require.NoError(t, err)
	defer func() { _ = lock.Release(context.Background()) }()

	assert.Equal(t, wantKey, lock.Key())
}

// TestLock_Release_IsIdempotent confirms the second Release call is a no-op.
// (Preserves existing TestDistLock_ReleaseIdempotent behaviour via the new interface.)
func TestLock_Release_IsIdempotent(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:idem2", 10*time.Second)
	require.NoError(t, err)

	err = lock.Release(ctx)
	assert.NoError(t, err)

	// Second release must be a no-op (Lua returns 0, no error).
	err = lock.Release(ctx)
	assert.NoError(t, err)
}

// TestLock_Lost_ClosedAfterRelease asserts that Release also closes Lost()
// so goroutines selecting on Lost() exit after an explicit Release.
func TestLock_Lost_ClosedAfterRelease(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:lost-after-release", 10*time.Second)
	require.NoError(t, err)

	err = lock.Release(context.Background())
	require.NoError(t, err)

	// After Release, Lost() must be closed.
	select {
	case <-lock.Lost():
		// Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Lost() channel was not closed after Release")
	}
}
