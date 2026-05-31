package distlock_test

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// TestLock_Orphan_StopsRenewalNoRelease verifies that Orphan() stops lease
// renewal, sets Cause() == ErrLockOrphaned, closes Done(), and never calls
// Driver.Release. The manager must drain after Orphan (same as Release).
func TestLock_Orphan_StopsRenewalNoRelease(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	const ttl = testtime.D10s
	const renewFrac = 0.5

	lock, err := l.Acquire(context.Background(), "orphan-key1", ttl)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	m := mgr(l)
	<-m.Started()
	waitPendingTimers(t, fc)

	// Advance just before renew — should NOT have renewed yet.
	fc.Advance(time.Duration(float64(ttl)*renewFrac) - time.Nanosecond)
	runtime.Gosched()
	renewBefore := fd.Calls("Renew")

	// Orphan the lock.
	lock.Orphan()

	// Done() must close.
	select {
	case <-lock.Done():
	case <-time.After(testTimeout):
		t.Fatal("Orphan: lock.Done() should be closed after Orphan()")
	}

	// Cause must be ErrLockOrphaned (exact pointer identity).
	assertSameErrorIdentity(t, lock.Cause(), distlock.ErrLockOrphaned, "Orphan cause")

	// Advance well past several renew intervals — renewal must NOT increase.
	// We do NOT call waitPendingTimers here because the lock was orphaned:
	// the heap is empty (no timers registered). Just advance and verify.
	for range 3 {
		fc.Advance(time.Duration(float64(ttl) * renewFrac))
		runtime.Gosched()
	}
	renewAfter := fd.Calls("Renew")
	if renewAfter > renewBefore {
		t.Errorf("Orphan: Renew count increased after Orphan: before=%d after=%d", renewBefore, renewAfter)
	}

	// Driver.Release must never be called.
	if got := fd.Calls("Release"); got != 0 {
		t.Errorf("Orphan: Driver.Release should NOT be called after Orphan; got %d", got)
	}

	// Manager must drain (orphan hands off the pendingReleases slot).
	select {
	case <-m.Drained():
	case <-time.After(testTimeout):
		t.Fatal("Orphan: manager should drain after Orphan()")
	}
}

// TestLock_Orphan_ThenReleaseNoop verifies that Release() after Orphan() is a
// no-op: returns nil, calls no Driver.Release, and Cause() stays ErrLockOrphaned.
func TestLock_Orphan_ThenReleaseNoop(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	lock, err := l.Acquire(context.Background(), "orphan-then-release", testtime.D10s)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	<-mgr(l).Started()

	lock.Orphan()
	select {
	case <-lock.Done():
	case <-time.After(testTimeout):
		t.Fatal("Orphan: Done() not closed")
	}

	// Release() after Orphan — must return nil, no I/O.
	if err := lock.Release(); err != nil {
		t.Errorf("Release() after Orphan should return nil, got %v", err)
	}
	if got := fd.Calls("Release"); got != 0 {
		t.Errorf("Driver.Release must not be called after Orphan; got %d", got)
	}
	// Cause stays ErrLockOrphaned (the first terminal cause wins).
	assertSameErrorIdentity(t, lock.Cause(), distlock.ErrLockOrphaned, "cause after Release()-after-Orphan")

	select {
	case <-mgr(l).Drained():
	case <-time.After(testTimeout):
		t.Fatal("manager should drain")
	}
}

// TestLock_Release_ThenOrphanNoop verifies that Orphan() after Release() is a
// no-op: Cause() stays ErrLockReleased and Driver.Release count stays 1.
func TestLock_Release_ThenOrphanNoop(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	lock, err := l.Acquire(context.Background(), "release-then-orphan", testtime.D10s)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	<-mgr(l).Started()

	if err := lock.Release(); err != nil {
		t.Logf("Release: %v", err)
	}
	select {
	case <-lock.Done():
	case <-time.After(testTimeout):
		t.Fatal("Release: Done() not closed")
	}

	// Orphan() after Release — must be a no-op.
	lock.Orphan()

	// Cause stays ErrLockReleased (Release won the race).
	assertSameErrorIdentity(t, lock.Cause(), distlock.ErrLockReleased, "cause after Orphan()-after-Release")

	// Exactly one Driver.Release call from the explicit Release().
	if got := fd.Calls("Release"); got != 1 {
		t.Errorf("Driver.Release count should be 1 after Release; got %d", got)
	}

	select {
	case <-mgr(l).Drained():
	case <-time.After(testTimeout):
		t.Fatal("manager should drain")
	}
}

// TestLock_Orphan_Idempotent verifies that calling Orphan() twice doesn't panic,
// sets cause exactly once, and the manager still drains.
func TestLock_Orphan_Idempotent(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	lock, err := l.Acquire(context.Background(), "orphan-idempotent", testtime.D10s)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	<-mgr(l).Started()

	lock.Orphan()
	lock.Orphan() // must not panic

	select {
	case <-lock.Done():
	case <-time.After(testTimeout):
		t.Fatal("Orphan idempotent: Done() not closed")
	}
	assertSameErrorIdentity(t, lock.Cause(), distlock.ErrLockOrphaned, "idempotent cause")

	select {
	case <-mgr(l).Drained():
	case <-time.After(testTimeout):
		t.Fatal("Orphan idempotent: manager should drain")
	}
}

// TestLock_RenewalLost_ThenOrphanNoop verifies that calling Orphan() after the
// lock was already lost via renewal failure (ErrLockLost) is a safe no-op.
//
// Mechanics: the renewal-failure path calls markCause(ErrLockLost) via
// causeOnce and removes the lock from the manager before the caller reaches
// Orphan(). The Acquire-level sync.Once for orphan was NOT consumed by the
// lost path, so Orphan() fires mgr.orphan(id) — but detachLock finds the lock
// already absent (ok=false) and is a safe no-op. pendingReleases still
// reaches 0 because the renewal-lost path calls decPendingAndMaybeDrain
// before the Orphan() event arrives.
//
// Verify:
//   - no panic
//   - lock.Cause() stays ErrLockLost (the renewal-failure markCause already fired)
//   - Driver.Release is never called
//   - manager Drained() closes (drain accounting stays correct)
func TestLock_RenewalLost_ThenOrphanNoop(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	// maxRenewAttempts=1: a single injected error fully exhausts the retry budget.
	l := mustNewLocker(fd, fc, distlock.WithMaxRenewAttempts(1))

	const ttl = testtime.D10s

	lock, err := l.Acquire(context.Background(), "renewal-lost-then-orphan", ttl)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	m := mgr(l)
	<-m.Started()
	waitPendingTimers(t, fc)

	// Inject persistent error so every Renew attempt returns an I/O error,
	// exhausting the budget (maxRenewAttempts=1) on the first attempt.
	fd.SetRenewErrorPersistent(locktest.ErrDriverIO)

	// Advance to trigger the renew timer.
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// Wait for the lock to be declared lost.
	select {
	case <-lock.Done():
	case <-time.After(testTimeout):
		t.Fatal("RenewalLost_ThenOrphanNoop: lock.Done() should close after renewal budget exhausted")
	}
	assertSameErrorIdentity(t, lock.Cause(), distlock.ErrLockLost, "cause after renewal loss")

	// Now call Orphan() — must not panic and must be a safe no-op.
	lock.Orphan()

	// Cause must still be ErrLockLost (renewal-failure markCause won the race
	// via causeOnce; orphan's sync.Once sends eventOrphan but detachLock finds
	// the lock absent).
	assertSameErrorIdentity(t, lock.Cause(), distlock.ErrLockLost, "cause after Orphan-after-lost")

	// Driver.Release must never be called.
	if got := fd.Calls("Release"); got != 0 {
		t.Errorf("RenewalLost_ThenOrphanNoop: Driver.Release must not be called; got %d", got)
	}

	// Manager must drain — pendingReleases accounting must stay correct.
	select {
	case <-m.Drained():
	case <-time.After(testTimeout):
		t.Fatal("RenewalLost_ThenOrphanNoop: manager must drain after renewal-lost + Orphan()")
	}
}

// TestLock_Orphan_KeyExpiresAfterTTL verifies the end-to-end key-handoff
// property of Orphan(): after the lock is orphaned and the clock advances past
// the TTL, a fresh Acquire on the same key succeeds because FakeDriver's
// time-based expiry model (expiresAt = clock.Now() + ttl, checked against the
// injected FakeClock) expires the orphaned key and allows a new holder.
//
// FakeDriver models time-based expiry using the injected clock (WithClock /
// NewFakeDriverWithClock). When the FakeClock advances past entry.expiresAt,
// FakeDriver.SetNX treats the key as expired and allows overwrite. This is the
// correct level at which to test the physical-expiry property; the redis
// integration RunDriverTTLConformance (C-5/C-6) covers the real backend.
func TestLock_Orphan_KeyExpiresAfterTTL(t *testing.T) {
	fc := clockmock.New(time.Time{})
	// Wire FakeDriver's TTL clock to the same FakeClock so time-based expiry
	// is consistent with the manager's logical time.
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	l := newTestLocker(fc, fd)

	const ttl = testtime.D10s
	const key = "orphan-ttl-expiry"

	// Acquire lock A on key K.
	lockA, err := l.Acquire(context.Background(), key, ttl)
	if err != nil {
		t.Fatalf("Acquire lockA: %v", err)
	}
	<-mgr(l).Started()

	// Orphan lock A — stops renewal, does NOT call Driver.Release.
	lockA.Orphan()
	select {
	case <-lockA.Done():
	case <-time.After(testTimeout):
		t.Fatal("Orphan_KeyExpiresAfterTTL: lockA.Done() not closed after Orphan")
	}
	assertSameErrorIdentity(t, lockA.Cause(), distlock.ErrLockOrphaned, "lockA cause")
	if got := fd.Calls("Release"); got != 0 {
		t.Errorf("Orphan_KeyExpiresAfterTTL: Driver.Release must not be called after Orphan; got %d", got)
	}

	// Wait for the manager to drain before advancing the clock past the TTL.
	select {
	case <-mgr(l).Drained():
	case <-time.After(testTimeout):
		t.Fatal("Orphan_KeyExpiresAfterTTL: manager must drain after Orphan")
	}

	// Advance the FakeClock past the TTL — FakeDriver expires the key.
	fc.Advance(ttl + time.Millisecond)

	// A fresh Acquire on the same key must succeed: FakeDriver.SetNX sees the
	// key as expired (clock.Now() is past entry.expiresAt) and allows a new holder.
	lockB, err := l.Acquire(context.Background(), key, ttl)
	if err != nil {
		t.Fatalf("Acquire lockB after TTL expiry: %v", err)
	}

	// lockB is live; clean up.
	if err := lockB.Release(); err != nil {
		t.Logf("lockB Release: %v", err)
	}
	select {
	case <-lockB.Done():
	case <-time.After(testTimeout):
		t.Fatal("Orphan_KeyExpiresAfterTTL: lockB.Done() not closed after Release")
	}
}

// TestLock_Orphan_SiblingStillRenewed verifies that orphaning one lock does not
// affect a sibling lock: the sibling keeps getting renewed, its Renew count grows,
// and it remains live.
func TestLock_Orphan_SiblingStillRenewed(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	const ttl = testtime.D10s

	lockA, err := l.Acquire(context.Background(), "sibling-a", ttl)
	if err != nil {
		t.Fatalf("Acquire sibling-a: %v", err)
	}
	lockB, err := l.Acquire(context.Background(), "sibling-b", ttl)
	if err != nil {
		t.Fatalf("Acquire sibling-b: %v", err)
	}
	defer func() {
		if err := lockB.Release(); err != nil {
			t.Logf("release B: %v", err)
		}
	}()

	m := mgr(l)
	<-m.Started()

	// Wait for both locks to appear in snapshot.
	deadline := time.Now().Add(testTimeout)
	for m.Snapshot().Locks < 2 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for 2 locks in manager")
		}
		runtime.Gosched()
	}

	waitPendingTimers(t, fc)

	// Orphan lock A.
	lockA.Orphan()
	select {
	case <-lockA.Done():
	case <-time.After(testTimeout):
		t.Fatal("lockA.Done() not closed after Orphan")
	}

	// Wait for manager to re-register its timer for lock B.
	waitPendingTimers(t, fc)

	// Record Renew baseline after orphan.
	renewBefore := fd.Calls("Renew")

	// Advance to trigger lockB's renew.
	fc.Advance(time.Duration(float64(ttl) * 0.5))
	waitForRenewL(t, l, fd, renewBefore+1)

	if fd.Calls("Renew") <= renewBefore {
		t.Errorf("sibling lockB should still be renewed; renewBefore=%d after=%d", renewBefore, fd.Calls("Renew"))
	}

	// lockA is orphaned, lockB should still be held.
	assertSameErrorIdentity(t, lockA.Cause(), distlock.ErrLockOrphaned, "lockA cause after orphan")

	select {
	case <-lockB.Done():
		t.Errorf("lockB should NOT be done after lockA orphan; cause=%v", lockB.Cause())
	default:
		// Good — lockB still live.
	}
}

// TestLock_Orphan_ManagerSelfDrainsNoGoroutineLeak proves the load-bearing
// claim behind deferring locker-level Close() (ADR §"Out of scope"): the
// Manager goroutine self-drains after every lock reaches a terminal disposition
// via Orphan. Once pendingReleases hits zero, Drained() closes and the manager
// goroutine exits, returning NumGoroutine to baseline — so there is no leaked
// goroutine for a process-wide Close()/Shutdown() to reclaim, which is why
// shipping Close() today would be unreachable dead code.
//
// This is the Orphan-path analog of TC-9 (which covers the Release path).
// NOT parallel: NumGoroutine() is process-global.
func TestLock_Orphan_ManagerSelfDrainsNoGoroutineLeak(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	baseline := runtime.NumGoroutine()

	const n = 50
	locks := make([]*distlock.Lock, 0, n)
	for i := range n {
		acquired, err := l.Acquire(context.Background(), "selfdrain-"+strconv.Itoa(i), testtime.D1min)
		if err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		locks = append(locks, acquired)
	}

	<-mgr(l).Started()

	// All N held by ONE manager goroutine (0 per-lock goroutines).
	after := runtime.NumGoroutine()
	if after-baseline > 3 { // 1 manager + 2 slack
		t.Errorf("goroutine count jumped by %d (baseline %d → %d); expected ≤ 3 "+
			"(1 manager + 2 slack — 0 per-lock goroutines)", after-baseline, baseline, after)
	}

	// Orphan every lock (no Driver.Release I/O on any of them).
	for _, lk := range locks {
		lk.Orphan()
	}
	if got := fd.Calls("Release"); got != 0 {
		t.Fatalf("Release called %d times across orphan-only disposition; want 0", got)
	}

	// Manager self-drains once the last lock is orphaned.
	select {
	case <-mgr(l).Drained():
	case <-time.After(testtime.D30s):
		t.Fatal("manager did not drain after orphaning all locks")
	}

	// The manager goroutine must have exited — NumGoroutine returns to baseline.
	// Poll: goroutine teardown is asynchronous after Drained() closes.
	deadline := time.Now().Add(testtime.EventuallyLong)
	for {
		current := runtime.NumGoroutine()
		if current <= baseline+2 { // 2 slack for test-framework goroutines
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutines after orphan-drain: %d (baseline %d); expected ≤ %d — "+
				"manager goroutine leaked (would invalidate the 'Close() has nothing to reclaim' ADR claim)",
				current, baseline, baseline+2)
			break
		}
		runtime.Gosched()
	}
}

// TestLock_OrphanRelease_ConcurrentRace verifies that concurrent Orphan() and
// Release() calls under -race produce no panics, set Cause() to exactly one of
// the expected sentinels, keep Driver.Release count in {0,1}, and allow the
// manager to drain.
//
// F2 safety net: 50 goroutines race on a single lock simultaneously calling
// Orphan or Release. The shared sync.Once ensures exactly one wins; all later
// calls are no-ops.
func TestLock_OrphanRelease_ConcurrentRace(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	lock, err := l.Acquire(context.Background(), "concurrent-orphan-release", testtime.D10s)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	m := mgr(l)
	<-m.Started()

	const n = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				lock.Orphan()
			} else {
				_ = lock.Release()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	// Exactly one terminal cause.
	cause := lock.Cause()
	if !errors.Is(cause, distlock.ErrLockReleased) && !errors.Is(cause, distlock.ErrLockOrphaned) {
		t.Errorf("ConcurrentRace: unexpected Cause=%v; want ErrLockReleased or ErrLockOrphaned", cause)
	}

	// At most one Driver.Release call.
	if got := fd.Calls("Release"); got > 1 {
		t.Errorf("ConcurrentRace: Driver.Release count=%d; want 0 or 1", got)
	}

	// Done must be closed.
	select {
	case <-lock.Done():
	case <-time.After(testTimeout):
		t.Fatal("ConcurrentRace: lock.Done() not closed")
	}

	// Manager drains.
	select {
	case <-m.Drained():
	case <-time.After(testTimeout):
		t.Fatal("ConcurrentRace: manager did not drain")
	}
}

// TestLock_Orphan_NotBlockedByInFlightRenew is the direct F1 regression test.
// It proves that Lock.Orphan() returns promptly even when a Driver.Renew call
// is concurrently blocked inside the manager's in-flight Renew goroutine.
//
// Before F1, handleRenew called Driver.Renew synchronously on the manager
// goroutine. An Orphan() event would therefore block until the Renew returned,
// which for a slow/unreachable backend could take up to TTL*(1-driftFactor).
// After F1, the Renew runs in a background goroutine; the manager loop remains
// live and dispatches the Orphan event immediately.
//
// Sequence:
//  1. Acquire a lock (TTL=10s, renewFraction=0.5).
//  2. Arm BlockNextRenew on FakeDriver — the next Renew call will block.
//  3. Advance FakeClock past the renew point → manager fires Renew goroutine.
//  4. Wait for the "renew entered" signal (goroutine is now blocked in Renew).
//  5. Call lock.Orphan() — must return within 2s (real wall clock).
//  6. lock.Done() must close with Cause==ErrLockOrphaned.
//  7. Unblock the Renew; verify no goroutine leak + manager drains.
func TestLock_Orphan_NotBlockedByInFlightRenew(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	l := newTestLocker(fc, fd)

	const ttl = testtime.D10s

	lock, err := l.Acquire(context.Background(), "inflight-renew-orphan", ttl)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	m := mgr(l)
	<-m.Started()
	waitPendingTimers(t, fc)

	// Arm the block: the next Renew call will block until UnblockRenew().
	renewEntered := fd.BlockNextRenew()

	// Advance to trigger the renew timer.
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// Wait until the Renew goroutine has entered FakeDriver.Renew.
	select {
	case <-renewEntered:
		// Renew is now blocked inside FakeDriver.
	case <-time.After(testTimeout):
		fd.UnblockRenew()
		t.Fatal("NotBlockedByInFlightRenew: timed out waiting for Renew to enter FakeDriver")
	}

	// NOW: Orphan must return promptly — the manager loop is live (Renew is
	// in a background goroutine). Use a done-channel + real 2s timeout.
	orphanDone := make(chan struct{})
	go func() {
		lock.Orphan()
		close(orphanDone)
	}()

	select {
	case <-orphanDone:
		// Good — Orphan returned before the blocked Renew completed.
	case <-time.After(2 * time.Second):
		fd.UnblockRenew()
		t.Fatal("NotBlockedByInFlightRenew: lock.Orphan() blocked for >2s while Renew was in-flight — F1 regression")
	}

	// Done must close with ErrLockOrphaned.
	select {
	case <-lock.Done():
	case <-time.After(testTimeout):
		t.Fatal("NotBlockedByInFlightRenew: lock.Done() not closed after Orphan")
	}
	assertSameErrorIdentity(t, lock.Cause(), distlock.ErrLockOrphaned, "cause")

	// Unblock the stalled Renew goroutine so it can post its result and exit.
	fd.UnblockRenew()

	// Manager must drain cleanly (no double-decrement, no goroutine leak).
	select {
	case <-m.Drained():
	case <-time.After(testTimeout):
		t.Fatal("NotBlockedByInFlightRenew: manager did not drain after Orphan + unblock")
	}

	// Allow goroutines to settle before the test exits.
	deadline := time.Now().Add(testtime.EventuallyLong)
	for time.Now().Before(deadline) {
		runtime.Gosched()
	}
}
