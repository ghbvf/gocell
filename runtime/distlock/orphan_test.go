package distlock_test

import (
	"context"
	"runtime"
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
