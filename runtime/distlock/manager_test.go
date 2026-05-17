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

// mgrWaitTimeout is the real-clock deadline for waitPendingTimers/waitTrackedLocks spin loops.
const mgrWaitTimeout = testtime.D10s

// mgrTotalTimeout is the maximum wait time for RenewNotify in waitForRenewM.
const mgrTotalTimeout = testtime.D60s

// mgrTTL1 is the first lock TTL in HeapOrder (4s → renewAt 2s).
const mgrTTL1 = testtime.D5s - testtime.D1s // 4s

// mgrTTL2 is the second lock TTL in HeapOrder.
const mgrTTL2 = testtime.D10s

// mgrAdvance2s1ms is the first phase advance in HeapOrder (2s+1ms).
const mgrAdvance2s1ms = testtime.D2s + testtime.D1ms

// waitPendingTimers spins until fc.PendingTimers() >= 1 or the deadline
// passes. Called after a renew to ensure the manager has re-registered its
// next timer before the test advances the clock again.
func waitPendingTimers(t *testing.T, fc *clockmock.FakeClock) {
	t.Helper()
	deadline := time.Now().Add(mgrWaitTimeout)
	for fc.PendingTimers() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("waitPendingTimers: timed out waiting for 1 pending timer (got %d)", fc.PendingTimers())
		}
		runtime.Gosched()
	}
}

// waitTrackedLocks spins until m.Snapshot().Locks >= want. Used after Acquire
// to force a synchronization point: Acquire merely enqueues an eventAdd to
// the manager goroutine, so without this barrier subsequent fc.Advance calls
// can race ahead of handleAdd and capture a later nextRenew baseline.
func waitTrackedLocks(t *testing.T, m *distlock.Manager, want int) {
	t.Helper()
	deadline := time.Now().Add(mgrWaitTimeout)
	for m.Snapshot().Locks < want {
		if time.Now().After(deadline) {
			t.Fatalf("waitTrackedLocks: timed out waiting for %d tracked locks (got %d)", want, m.Snapshot().Locks)
		}
		runtime.Gosched()
	}
}

// waitForRenewM waits until fd.Calls("Renew") >= want using RenewNotify.
func waitForRenewM(t *testing.T, m *distlock.Manager, fd *locktest.FakeDriver, want int) {
	t.Helper()
	deadline := time.Now().Add(mgrTotalTimeout)
	for fd.Calls("Renew") < want {
		if time.Now().After(deadline) {
			t.Fatalf("waitForRenewM: timed out waiting for %d Renew calls (got %d)", want, fd.Calls("Renew"))
		}
		select {
		case <-m.RenewNotify():
		case <-time.After(mgrTotalTimeout):
			t.Fatalf("waitForRenewM: RenewNotify timed out (want %d, got %d)", want, fd.Calls("Renew"))
		}
	}
}

// TestManager_HeapOrder verifies that the manager renews locks in deadline order
// (earliest first). We acquire two locks with different TTLs, advance to between
// their renew deadlines, and assert only the earlier one was renewed.
// Then we step-advance with waitPendingTimers barriers to avoid d<=0 immediate
// timers, ensuring key2's timer is properly registered before each advance.
func TestManager_HeapOrder(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	fd.WithClock(fc.Now)

	l := mustNewLocker(fd, fc,
		distlock.WithRenewFraction(0.5),
	)

	// ttl1=4s → renewAt=2s. ttl2=10s → renewAt=5s.
	ttl1 := mgrTTL1
	ttl2 := mgrTTL2

	_, release1, err := l.Acquire(context.Background(), "heap-key1", ttl1)
	if err != nil {
		t.Fatalf("Acquire key1: %v", err)
	}
	defer func() {
		if err := release1(); err != nil {
			t.Logf("release1: %v", err)
		}
	}()

	_, release2, err := l.Acquire(context.Background(), "heap-key2", ttl2)
	if err != nil {
		t.Fatalf("Acquire key2: %v", err)
	}
	defer func() {
		if err := release2(); err != nil {
			t.Logf("release2: %v", err)
		}
	}()

	m := mgr(l)
	<-m.Started()

	// Synchronization barrier: both Acquires only enqueue eventAdd events. We
	// need both handleAdd calls to land at fake-clock time 0 so key2.nextRenew
	// is fixed at 5s (not at "time of first Advance" + 5s). Without this, the
	// later phases assume a heap layout that may not hold.
	waitTrackedLocks(t, m, 2)
	// Wait for the manager to register the first timer (h[0]=key1, deadline 2s).
	waitPendingTimers(t, fc)

	// --- Phase 1: advance to 2s+1ms. Only key1 fires (heap order verified). ---
	fc.Advance(mgrAdvance2s1ms) // fake time: 2s+1ms
	waitForRenewM(t, m, fd, 1)

	if fd.Calls("Renew") != 1 {
		t.Errorf("HeapOrder: expected 1 Renew after advancing to 2s+1ms, got %d", fd.Calls("Renew"))
	}

	// --- Phase 2: step past key1 re-queue, stop before key2. ---
	// key1 re-queued @4s+1ms, key2 @5s. Advance to 4s+2ms so d(key2)=998ms>0.
	waitPendingTimers(t, fc)    // key1 re-queued timer registered
	fc.Advance(mgrAdvance2s1ms) // fake time: 4s+2ms
	waitForRenewM(t, m, fd, 2)

	// --- Phase 3: now key2's timer has positive d; advance past 5s. ---
	waitPendingTimers(t, fc) // key2's timer registered (d=998ms>0)
	fc.Advance(time.Second)  // fake time: 5s+2ms; key2 fires
	waitForRenewM(t, m, fd, 3)

	if fd.Calls("Renew") < 3 {
		t.Errorf("HeapOrder: expected ≥3 Renew calls total, got %d", fd.Calls("Renew"))
	}
}

// TestManager_Lifecycle_LazyStart verifies that the manager goroutine is not
// started until the first Acquire and drains after the last release.
func TestManager_Lifecycle_LazyStart(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := mustNewLocker(fd, fc)

	_, release, err := l.Acquire(context.Background(), "lifecycle-key", mgrWaitTimeout)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Wait for the manager to signal it has started.
	select {
	case <-mgr(l).Started():
	case <-time.After(mgrWaitTimeout):
		t.Fatal("Lifecycle: manager Started channel should close after Acquire")
	}

	if err := release(); err != nil {
		t.Logf("release: %v", err)
	}

	select {
	case <-mgr(l).Drained():
	case <-time.After(mgrWaitTimeout):
		t.Fatal("Lifecycle: manager should drain after last release")
	}
}

// TestManager_SnapshotLocks verifies that Snapshot().Locks tracks adds and removes.
func TestManager_SnapshotLocks(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := mustNewLocker(fd, fc)

	if mgr(l).Snapshot().Locks != 0 {
		t.Errorf("SnapshotLocks: initial count should be 0")
	}

	_, r1, _ := l.Acquire(context.Background(), "snap-key1", testtime.D1min)
	_, r2, _ := l.Acquire(context.Background(), "snap-key2", testtime.D1min)

	<-mgr(l).Started()

	// Wait for both adds to be processed.
	deadline := time.Now().Add(mgrWaitTimeout)
	for mgr(l).Snapshot().Locks != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("SnapshotLocks: expected 2 locks, got %d", mgr(l).Snapshot().Locks)
		}
		runtime.Gosched()
	}

	if err := r1(); err != nil {
		t.Logf("r1: %v", err)
	}

	deadline = time.Now().Add(mgrWaitTimeout)
	for mgr(l).Snapshot().Locks != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("SnapshotLocks: expected 1 lock after r1 release, got %d", mgr(l).Snapshot().Locks)
		}
		runtime.Gosched()
	}

	if err := r2(); err != nil {
		t.Logf("r2: %v", err)
	}

	select {
	case <-mgr(l).Drained():
	case <-time.After(mgrWaitTimeout):
		t.Fatal("SnapshotLocks: manager should drain after both releases")
	}

	if mgr(l).Snapshot().Locks != 0 {
		t.Errorf("SnapshotLocks: expected 0 after drain, got %d", mgr(l).Snapshot().Locks)
	}
}
