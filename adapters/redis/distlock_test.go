package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
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

	// Second release returns ErrLockLost — Lua returns 0, key is already gone.
	err2 := lock.Release(ctx)
	require.Error(t, err2, "second release should report lock no longer owned")
	var ec *errcode.Error
	require.True(t, errors.As(err2, &ec))
	assert.Equal(t, distlock.ErrLockLost, ec.Code)

	// Guard: sync.Once prevents double-close; Lost() channel stays closed.
	select {
	case <-lock.Lost():
		// expected — lost channel closed exactly once on first Release
	default:
		t.Fatal("Lost() channel must remain closed after double-Release")
	}
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
	// White-box test: accesses *Lock internals (done channel) to verify the
	// renewal goroutine has drained before Release returns. Same-package test
	// (package redis) so this is legitimate; callers outside the adapter see
	// only the distlock.Lock interface surface.
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

// TestDistLock_Release_WithExpiredCtx_StillAttemptsDEL verifies that Release
// issues the Redis DEL command even when the supplied ctx is already cancelled.
// Without a fresh bounded context, an expired caller ctx would cause Eval to
// return immediately with a ctx error, leaving the lock key in Redis until TTL.
func TestDistLock_Release_WithExpiredCtx_StillAttemptsDEL(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)

	// Acquire with a valid context.
	lockIface, err := dl.Acquire(context.Background(), "test:lock:expired-ctx", 10*time.Second)
	require.NoError(t, err)

	// Snapshot Eval calls so far (renewal may have fired none yet for a fresh lock).
	mock.mu.Lock()
	evalsBefore := mock.evalCallCount
	mock.mu.Unlock()

	// Cancel the context before calling Release.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	// Release with the already-cancelled ctx — DEL must still be issued.
	err = lockIface.Release(cancelledCtx)
	assert.NoError(t, err)

	mock.mu.Lock()
	evalsAfter := mock.evalCallCount
	mock.mu.Unlock()

	if evalsAfter <= evalsBefore {
		t.Fatalf("expected at least one Eval (DEL) after Release with expired ctx; got evalsBefore=%d evalsAfter=%d", evalsBefore, evalsAfter)
	}

	// Key must have been deleted from the mock store.
	mock.mu.Lock()
	_, exists := mock.store["test:lock:expired-ctx"]
	mock.mu.Unlock()
	assert.False(t, exists, "lock key must be removed from store after Release, even with expired ctx")
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

// TestLock_Release_IsIdempotent confirms the second Release call returns ErrLockLost.
// Idempotent in the sense of: safe to call twice, no panic, but explicitly surfaces
// that the lock is no longer owned on the second call.
func TestLock_Release_IsIdempotent(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)
	ctx := context.Background()

	lock, err := dl.Acquire(ctx, "test:lock:idem2", 10*time.Second)
	require.NoError(t, err)

	err = lock.Release(ctx)
	assert.NoError(t, err)

	// Second release returns ErrLockLost — Lua returns 0, key is already gone.
	err2 := lock.Release(ctx)
	require.Error(t, err2, "second release should report lock no longer owned")
	var ec *errcode.Error
	require.True(t, errors.As(err2, &ec))
	assert.Equal(t, distlock.ErrLockLost, ec.Code)

	// Guard: sync.Once prevents double-close; Lost() channel stays closed.
	select {
	case <-lock.Lost():
		// expected — lost channel closed exactly once on first Release
	default:
		t.Fatal("Lost() channel must remain closed after double-Release")
	}
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

// TestDistLock_Release_WhenLockTTLExpired_SkipsDEL verifies that Release skips
// the DEL command when the lock's expiresAt is already in the past.  When the
// TTL has elapsed Redis will have self-cleaned the key, so the DEL would be a
// no-op anyway. The function must return nil and close Lost().
func TestDistLock_Release_WhenLockTTLExpired_SkipsDEL(t *testing.T) {
	rec := newRecordingCmdable()
	dl := newDistLockFromCmdable(rec, 30*time.Second)

	lockIface, err := dl.Acquire(context.Background(), "test:lock:ttl-expired", 10*time.Second)
	require.NoError(t, err)
	lock := lockIface.(*Lock)

	// Snapshot Eval calls made so far (SetNX, possible early renewals).
	rec.mu.Lock()
	evalsBefore := rec.evalCallCount
	rec.mu.Unlock()

	// Simulate the lock already being past its natural expiry.
	lock.expiresAt.Store(time.Now().Add(-time.Second).UnixNano())

	err = lock.Release(context.Background())
	assert.NoError(t, err, "Release must return nil when lock already expired via TTL")

	// No Eval (DEL) should have been issued after the snapshot.
	rec.mu.Lock()
	evalsAfter := rec.evalCallCount
	rec.mu.Unlock()
	assert.Equal(t, evalsBefore, evalsAfter, "DEL must not be issued when lock already expired via TTL")

	// Lost() must still be closed.
	select {
	case <-lock.Lost():
		// Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Lost() channel must be closed even when DEL is skipped")
	}
}

// TestDistLock_Release_DeadlineIsLockExpiry verifies that the DEL context
// deadline is bounded by the lock's natural expiry (no artificial constant).
func TestDistLock_Release_DeadlineIsLockExpiry(t *testing.T) {
	rec := newRecordingCmdable()
	ttl := 5 * time.Second
	dl := newDistLockFromCmdable(rec, ttl)

	before := time.Now()
	lockIface, err := dl.Acquire(context.Background(), "test:lock:deadline-expiry", ttl)
	require.NoError(t, err)
	after := time.Now()

	// Drain any renewal Eval calls from before Release.
	rec.mu.Lock()
	rec.evalContexts = nil
	rec.mu.Unlock()

	err = lockIface.Release(context.Background())
	require.NoError(t, err)

	ctx := rec.lastEvalCtx()
	require.NotNil(t, ctx, "expected Eval to be called during Release")
	dl2, hasDeadline := ctx.Deadline()
	require.True(t, hasDeadline, "DEL context must have a deadline")

	// The deadline must be between before+ttl and after+ttl+100ms slack.
	assert.True(t, !dl2.Before(before.Add(ttl)),
		"DEL deadline %v must be >= acquire-time + ttl (%v)", dl2, before.Add(ttl))
	assert.True(t, !dl2.After(after.Add(ttl+100*time.Millisecond)),
		"DEL deadline %v must be <= acquire-time + ttl + 100ms slack (%v)", dl2, after.Add(ttl+100*time.Millisecond))
}

// TestDistLock_Release_RespectsCallerDeadlineWhenEarlier verifies that when
// the caller's ctx deadline is earlier than the lock's natural expiry, the
// tighter (caller) deadline wins.
func TestDistLock_Release_RespectsCallerDeadlineWhenEarlier(t *testing.T) {
	rec := newRecordingCmdable()
	ttl := 10 * time.Second
	dl := newDistLockFromCmdable(rec, ttl)

	lockIface, err := dl.Acquire(context.Background(), "test:lock:caller-deadline", ttl)
	require.NoError(t, err)

	// Drain renewal Eval contexts from Acquire path.
	rec.mu.Lock()
	rec.evalContexts = nil
	rec.mu.Unlock()

	// Caller context with a 1s deadline — much shorter than lock's 10s expiry.
	callerCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err = lockIface.Release(callerCtx)
	// May succeed (DEL completed within 1s) or fail — either way check deadline.
	_ = err

	ctx := rec.lastEvalCtx()
	require.NotNil(t, ctx, "expected Eval to be called during Release")
	dl2, hasDeadline := ctx.Deadline()
	require.True(t, hasDeadline, "DEL context must have a deadline")

	// The deadline must be at most ~1s from now (caller's deadline), definitely
	// not 10s from now (lock's natural expiry).
	assert.True(t, time.Until(dl2) <= 2*time.Second,
		"DEL deadline %v should honour the caller's 1s ctx, not the lock's 10s expiry; time until deadline: %v",
		dl2, time.Until(dl2))
}

// TestDistLock_Release_OwnershipLost_ReturnsErrLockLost verifies that when
// the Lua release script returns 0 (another holder took the key), Release
// returns an errcode.Error with Code == ErrLockLost.
func TestDistLock_Release_OwnershipLost_ReturnsErrLockLost(t *testing.T) {
	mock := newMockCmdable()
	dl := newDistLockFromCmdable(mock, 30*time.Second)

	lockIface, err := dl.Acquire(context.Background(), "test:lock:ownership-lost-release", 10*time.Second)
	require.NoError(t, err)
	lock := lockIface.(*Lock)

	// Remove the key from the mock store so the Lua script returns 0
	// (simulating another holder taking over or TTL expiry between check and DEL).
	mock.mu.Lock()
	delete(mock.store, "test:lock:ownership-lost-release")
	mock.mu.Unlock()

	err = lockIface.Release(context.Background())
	require.Error(t, err, "Release must return error when lock is no longer owned")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error, got: %T %v", err, err)
	assert.Equal(t, distlock.ErrLockLost, ec.Code)

	// Lost() must also be closed.
	select {
	case <-lock.Lost():
		// Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Lost() channel must be closed when ownership is lost on Release")
	}
}

// TestDistLock_Renewal_UpdatesExpiresAt verifies that the background renewal
// loop updates lock.expiresAt after each successful renewal Eval.
func TestDistLock_Renewal_UpdatesExpiresAt(t *testing.T) {
	mock := newMockCmdable()
	ttl := 200 * time.Millisecond // short so renewal fires quickly
	dl := newDistLockFromCmdable(mock, ttl)

	lockIface, err := dl.Acquire(context.Background(), "test:lock:renewal-expiresAt", ttl)
	require.NoError(t, err)
	lock := lockIface.(*Lock)
	defer func() { _ = lockIface.Release(context.Background()) }()

	// Snapshot the expiresAt right after Acquire.
	initialExpiry := lock.expiresAt.Load()
	require.NotZero(t, initialExpiry, "expiresAt must be set after Acquire")

	// Wait long enough for the renewal ticker (ttl/2 = 100ms) to fire at least once.
	time.Sleep(250 * time.Millisecond)

	updatedExpiry := lock.expiresAt.Load()
	assert.Greater(t, updatedExpiry, initialExpiry,
		"expiresAt must be updated after a successful renewal (initial=%d, updated=%d)",
		initialExpiry, updatedExpiry)
}

// TestRenewLoop_CtxDone_ClosesLost verifies the O1 fix: when the renewCtx is
// cancelled (ctx.Done() fires in renewLoop), Lost() is also closed via the
// defensive closeLost() call in that branch.
func TestRenewLoop_CtxDone_ClosesLost(t *testing.T) {
	mock := newMockCmdable()
	ttl := 10 * time.Second // long TTL so the ticker doesn't fire
	dl := newDistLockFromCmdable(mock, ttl)

	lockIface, err := dl.Acquire(context.Background(), "test:lock:ctx-done-lost", ttl)
	require.NoError(t, err)
	lock := lockIface.(*Lock)

	// Lost() must not be closed yet.
	select {
	case <-lock.Lost():
		t.Fatal("Lost() must not be closed immediately after Acquire")
	default:
	}

	// Cancel the renewal context directly by calling lock.cancel (white-box).
	// This simulates the "ctx.Done()" path in renewLoop without going through Release.
	lock.cancel()

	// The renewLoop's ctx.Done branch should now call closeLost() and exit.
	select {
	case <-lock.Lost():
		// Good — O1 fix confirmed: Lost() closed via ctx.Done path.
	case <-time.After(2 * time.Second):
		t.Fatal("Lost() channel was not closed when renewCtx was cancelled (O1 fix missing)")
	}
}
