package distlock_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// waitPendingTimers spins until fc.PendingTimers() >= want or the deadline
// passes. Called after a renew to ensure the manager has re-registered its
// next timer before the test advances the clock again.
func waitPendingTimers(t *testing.T, fc *locktest.FakeClock, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for fc.PendingTimers() < want {
		if time.Now().After(deadline) {
			t.Fatalf("waitPendingTimers: timed out waiting for %d pending timers (got %d)", want, fc.PendingTimers())
		}
		runtime.Gosched()
	}
}

// waitForRenewM waits until fd.Calls("Renew") >= want using RenewNotify.
func waitForRenewM(t *testing.T, m *distlock.Manager, fd *locktest.FakeDriver, want int) {
	t.Helper()
	const totalTimeout = 60 * time.Second
	deadline := time.Now().Add(totalTimeout)
	for fd.Calls("Renew") < want {
		if time.Now().After(deadline) {
			t.Fatalf("waitForRenewM: timed out waiting for %d Renew calls (got %d)", want, fd.Calls("Renew"))
		}
		select {
		case <-m.RenewNotify():
		case <-time.After(totalTimeout):
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
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	fd.WithClock(fc.Now)

	l := distlock.New(fd,
		distlock.WithClock(fc),
		distlock.WithRenewFraction(0.5),
	)

	// ttl1=4s → renewAt=2s. ttl2=10s → renewAt=5s.
	ttl1 := 4 * time.Second
	ttl2 := 10 * time.Second

	_, release1, err := l.Acquire(context.Background(), "heap-key1", ttl1)
	if err != nil {
		t.Fatalf("Acquire key1: %v", err)
	}
	defer release1()

	_, release2, err := l.Acquire(context.Background(), "heap-key2", ttl2)
	if err != nil {
		t.Fatalf("Acquire key2: %v", err)
	}
	defer release2()

	m := mgr(l)
	<-m.Started()

	// Wait for the manager to register the first timer (h[0]=key1, deadline 2s).
	waitPendingTimers(t, fc, 1)

	// --- Phase 1: advance to 2s+1ms. Only key1 fires (heap order verified). ---
	fc.Advance(2*time.Second + time.Millisecond) // fake time: 2s+1ms
	waitForRenewM(t, m, fd, 1)

	if fd.Calls("Renew") != 1 {
		t.Errorf("HeapOrder: expected 1 Renew after advancing to 2s+1ms, got %d", fd.Calls("Renew"))
	}

	// --- Phase 2: step past key1 re-queue, stop before key2. ---
	// key1 re-queued @4s+1ms, key2 @5s. Advance to 4s+2ms so d(key2)=998ms>0.
	waitPendingTimers(t, fc, 1)                  // key1 re-queued timer registered
	fc.Advance(2*time.Second + time.Millisecond) // fake time: 4s+2ms
	waitForRenewM(t, m, fd, 2)

	// --- Phase 3: now key2's timer has positive d; advance past 5s. ---
	waitPendingTimers(t, fc, 1) // key2's timer registered (d=998ms>0)
	fc.Advance(time.Second)     // fake time: 5s+2ms; key2 fires
	waitForRenewM(t, m, fd, 3)

	if fd.Calls("Renew") < 3 {
		t.Errorf("HeapOrder: expected ≥3 Renew calls total, got %d", fd.Calls("Renew"))
	}
}

// TestManager_Lifecycle_LazyStart verifies that the manager goroutine is not
// started until the first Acquire and drains after the last release.
func TestManager_Lifecycle_LazyStart(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := distlock.New(fd, distlock.WithClock(fc))

	_, release, err := l.Acquire(context.Background(), "lifecycle-key", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Wait for the manager to signal it has started.
	select {
	case <-mgr(l).Started():
	case <-time.After(10 * time.Second):
		t.Fatal("Lifecycle: manager Started channel should close after Acquire")
	}

	release()

	select {
	case <-mgr(l).Drained():
	case <-time.After(10 * time.Second):
		t.Fatal("Lifecycle: manager should drain after last release")
	}
}

// TestManager_SnapshotLocks verifies that Snapshot().Locks tracks adds and removes.
func TestManager_SnapshotLocks(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := distlock.New(fd, distlock.WithClock(fc))

	if mgr(l).Snapshot().Locks != 0 {
		t.Errorf("SnapshotLocks: initial count should be 0")
	}

	_, r1, _ := l.Acquire(context.Background(), "snap-key1", time.Minute)
	_, r2, _ := l.Acquire(context.Background(), "snap-key2", time.Minute)

	<-mgr(l).Started()

	// Wait for both adds to be processed.
	deadline := time.Now().Add(10 * time.Second)
	for mgr(l).Snapshot().Locks != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("SnapshotLocks: expected 2 locks, got %d", mgr(l).Snapshot().Locks)
		}
		runtime.Gosched()
	}

	r1()

	deadline = time.Now().Add(10 * time.Second)
	for mgr(l).Snapshot().Locks != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("SnapshotLocks: expected 1 lock after r1 release, got %d", mgr(l).Snapshot().Locks)
		}
		runtime.Gosched()
	}

	r2()

	select {
	case <-mgr(l).Drained():
	case <-time.After(10 * time.Second):
		t.Fatal("SnapshotLocks: manager should drain after both releases")
	}

	if mgr(l).Snapshot().Locks != 0 {
		t.Errorf("SnapshotLocks: expected 0 after drain, got %d", mgr(l).Snapshot().Locks)
	}
}
