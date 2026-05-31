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
