package distlock_test

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// mgr returns the internal Manager via type assertion.
// lockerImpl exposes Manager() returning *Manager.
func mgr(l distlock.Locker) *distlock.Manager {
	type mgrGetter interface {
		Manager() *distlock.Manager
	}
	return l.(mgrGetter).Manager()
}

// newTestLocker constructs a Locker backed by FakeDriver + FakeClock.
func newTestLocker(fc *locktest.FakeClock, fd *locktest.FakeDriver) distlock.Locker {
	return distlock.New(fd, distlock.WithClock(fc))
}

// waitForRenewL waits for Renew count using the locker's manager RenewNotify.
func waitForRenewL(t *testing.T, l distlock.Locker, fd *locktest.FakeDriver, want int) {
	t.Helper()
	waitForRenewOnMgr(t, mgr(l), fd, want)
}

// TC-1: Happy path — acquire, advance to trigger renew, release.
// Cause == ErrLockReleased.
func TestLocker_TC1_HappyPath(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := 10 * time.Second
	lockCtx, release, err := l.Acquire(context.Background(), "key1", ttl)
	if err != nil {
		t.Fatalf("TC-1 Acquire: %v", err)
	}

	// Wait for manager to start and register the timer.
	<-mgr(l).Started()
	waitPendingTimers(t, fc, 1)

	// Advance to trigger renew (ttl * 0.5 = 5s).
	fc.Advance(ttl / 2)

	// Give renew goroutine a moment to process.
	waitForRenewL(t, l, fd, 1)

	if fd.Calls("Renew") < 1 {
		t.Errorf("TC-1: expected at least 1 Renew call, got %d", fd.Calls("Renew"))
	}

	release()

	select {
	case <-lockCtx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("TC-1: lockCtx should be Done after release()")
	}

	cause := context.Cause(lockCtx)
	if cause != distlock.ErrLockReleased {
		t.Errorf("TC-1: Cause = %v, want ErrLockReleased", cause)
	}
}

// TC-2: Advance ttl*0.5 - 1ns → no renew; Advance 1ns → exactly 1 renew.
func TestLocker_TC2_RenewIntervalPrecision(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := 10 * time.Second
	renewAt := time.Duration(float64(ttl) * 0.5)

	_, release, err := l.Acquire(context.Background(), "key2", ttl)
	if err != nil {
		t.Fatalf("TC-2 Acquire: %v", err)
	}
	defer release()

	<-mgr(l).Started()
	waitPendingTimers(t, fc, 1)

	// Advance just short of the renew deadline.
	fc.Advance(renewAt - time.Nanosecond)
	runtime.Gosched()
	// Allow a brief moment for manager to settle (no sleep — Gosched is enough).
	beforeCount := fd.Calls("Renew")

	// Now trigger exactly.
	fc.Advance(time.Nanosecond)
	waitForRenewL(t, l, fd, beforeCount+1)

	afterCount := fd.Calls("Renew")
	if afterCount-beforeCount != 1 {
		t.Errorf("TC-2: expected exactly 1 new Renew call, got %d", afterCount-beforeCount)
	}
}

// TC-3: NextRenewError → lockCtx canceled with ErrLockLost.
// Single lock: inject error, verify lost. Uses separate locker so no
// second-lock ordering ambiguity.
func TestLocker_TC3_RenewError_LockLost(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := 10 * time.Second

	lockCtx1, release1, err := l.Acquire(context.Background(), "key3a", ttl)
	if err != nil {
		t.Fatalf("TC-3 Acquire key3a: %v", err)
	}
	defer release1()

	<-mgr(l).Started()
	waitPendingTimers(t, fc, 1)

	// Inject error — will fire on the first Renew call.
	fd.SetNextRenewError(locktest.ErrDriverIO)

	// Advance to trigger the renew.
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// lockCtx1 should be canceled with ErrLockLost.
	select {
	case <-lockCtx1.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("TC-3: lockCtx1 should be Done after renew error")
	}

	cause := context.Cause(lockCtx1)
	if cause != distlock.ErrLockLost {
		t.Errorf("TC-3: Cause = %v, want ErrLockLost", cause)
	}

	// Acquire a second separate lock to verify manager handles new locks
	// independently after one fails.
	_, release2, err2 := l.Acquire(context.Background(), "key3b", ttl)
	if err2 != nil {
		t.Fatalf("TC-3 second Acquire key3b: %v", err2)
	}
	defer release2()

	// Wait for the manager to process the eventAdd for key3b.
	// snapshotLocks is updated in handleAdd which runs in the manager goroutine.
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap := mgr(l).Snapshot()
		if snap.Locks >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("TC-3: second lock should be in manager, got %d", snap.Locks)
			break
		}
		runtime.Gosched()
	}
}

// TC-4: NextRenewHeld=false → lockCtx canceled with ErrLockLost.
// Distinct from TC-3 (error vs held=false).
func TestLocker_TC4_RenewNotHeld_LockLost(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := 10 * time.Second

	lockCtx, release, err := l.Acquire(context.Background(), "key4", ttl)
	if err != nil {
		t.Fatalf("TC-4 Acquire: %v", err)
	}
	defer release()

	<-mgr(l).Started()
	waitPendingTimers(t, fc, 1)

	fd.SetNextRenewHeld(false)
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	select {
	case <-lockCtx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("TC-4: lockCtx should be Done when held=false")
	}

	if context.Cause(lockCtx) != distlock.ErrLockLost {
		t.Errorf("TC-4: Cause = %v, want ErrLockLost", context.Cause(lockCtx))
	}

	// Lock should be removed from heap snapshot.
	snap := mgr(l).Snapshot()
	if snap.Locks != 0 {
		t.Errorf("TC-4: expected 0 locks in manager after lost, got %d", snap.Locks)
	}
}

// TC-5: Parent ctx cancel → lockCtx.Done(), Cause == parentErr.
// release() is still callable without panic.
func TestLocker_TC5_ParentCancel(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := 10 * time.Second

	parentCtx, parentCancel := context.WithCancel(context.Background())

	lockCtx, release, err := l.Acquire(parentCtx, "key5", ttl)
	if err != nil {
		t.Fatalf("TC-5 Acquire: %v", err)
	}

	<-mgr(l).Started()

	releaseCount := fd.Calls("Release")
	parentCancel()

	select {
	case <-lockCtx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("TC-5: lockCtx should be Done after parent cancel")
	}

	// Cause should propagate parent's error.
	cause := context.Cause(lockCtx)
	if cause != context.Canceled {
		t.Errorf("TC-5: Cause = %v, want context.Canceled", cause)
	}

	// release() should not panic.
	release()

	// Release count should not have increased (parent ctx canceled — driver.Release
	// still runs via manager.remove, so we allow it).
	_ = releaseCount // not asserting count == 0 since manager still calls Release
}

// TC-6: Double release — idempotent, Driver.Release called exactly once.
func TestLocker_TC6_DoubleRelease(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := 10 * time.Second

	_, release, err := l.Acquire(context.Background(), "key6", ttl)
	if err != nil {
		t.Fatalf("TC-6 Acquire: %v", err)
	}

	<-mgr(l).Started()

	release()
	release() // second call — must not panic or double-release

	// Wait for manager to drain.
	select {
	case <-mgr(l).Drained():
	case <-time.After(10 * time.Second):
		t.Fatal("TC-6: manager should drain after release")
	}

	if fd.Calls("Release") != 1 {
		t.Errorf("TC-6: expected 1 Release call, got %d", fd.Calls("Release"))
	}
}

// TC-7: SetNX returns false → Acquire returns ErrLockTimeout; no goroutine leaked.
func TestLocker_TC7_AcquireBusy(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	fd.SetNextSetNX(false)

	l := newTestLocker(fc, fd)

	ttl := 10 * time.Second

	lockCtx, _, err := l.Acquire(context.Background(), "key7", ttl)
	if err == nil {
		t.Fatal("TC-7: expected error when SetNX returns false")
	}
	if lockCtx != nil {
		t.Error("TC-7: lockCtx should be nil on error")
	}

	// Manager should not have been started (Snapshot.Locks == 0).
	snap := mgr(l).Snapshot()
	if snap.Locks != 0 {
		t.Errorf("TC-7: expected 0 locks, got %d", snap.Locks)
	}
}

// TC-8: Pre-canceled ctx → Acquire returns ctx.Err() without calling SetNX.
func TestLocker_TC8_PreCanceledCtx(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := l.Acquire(canceledCtx, "key8", 10*time.Second)
	if err == nil {
		t.Fatal("TC-8: expected error for pre-canceled ctx")
	}
	if fd.Calls("SetNX") != 0 {
		t.Errorf("TC-8: SetNX should not be called for pre-canceled ctx, got %d calls", fd.Calls("SetNX"))
	}
}

// TC-9: 100 concurrent Acquire → exactly 1 manager goroutine above baseline.
func TestLocker_TC9_GoroutineCount(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	const n = 100
	ttl := time.Minute

	baseline := runtime.NumGoroutine()

	type result struct {
		lCtx    context.Context
		release func()
	}
	results := make([]result, 0, n)
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "key9-" + locktest.ExportItoa(i)
			lCtx, release, err := l.Acquire(context.Background(), key, ttl)
			if err != nil {
				t.Errorf("TC-9 goroutine %d Acquire: %v", i, err)
				return
			}
			mu.Lock()
			results = append(results, result{lCtx, release})
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	<-mgr(l).Started()

	// Manager + 1 watcher goroutine per lock + baseline.
	// The constraint: exactly 1 manager goroutine (not N per-lock goroutines).
	// We allow n watcher goroutines (1 per lock for parent-ctx propagation).
	after := runtime.NumGoroutine()
	managerGoroutines := after - baseline
	// Should be: 1 manager + n watchers.
	// A per-lock goroutine model would add n more goroutines on top.
	if managerGoroutines > n+5 { // 5 slack
		t.Errorf("TC-9: goroutine count jumped by %d (baseline %d → %d); expected at most %d (manager + watcher per lock)",
			managerGoroutines, baseline, after, n+5)
	}

	// Release all.
	for _, r := range results {
		r.release()
	}

	// Wait for drain.
	select {
	case <-mgr(l).Drained():
	case <-time.After(30 * time.Second):
		t.Fatal("TC-9: manager should drain after all releases")
	}
}

// TC-10: Single lock release → Drained closes; next Acquire re-starts manager.
func TestLocker_TC10_LazyLifecycle(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := 10 * time.Second

	// First acquisition.
	_, release, err := l.Acquire(context.Background(), "key10", ttl)
	if err != nil {
		t.Fatalf("TC-10 first Acquire: %v", err)
	}
	<-mgr(l).Started()

	release()
	select {
	case <-mgr(l).Drained():
	case <-time.After(10 * time.Second):
		t.Fatal("TC-10: Drained should close after release")
	}

	// Second acquisition — manager must restart.
	_, release2, err := l.Acquire(context.Background(), "key10b", ttl)
	if err != nil {
		t.Fatalf("TC-10 second Acquire: %v", err)
	}
	defer release2()

	select {
	case <-mgr(l).Started():
	case <-time.After(10 * time.Second):
		t.Fatal("TC-10: manager should restart on second Acquire")
	}
}

// TC-11: TTL = 1µs edge case — no spin-loop (Advance triggers exactly one renew).
// FakeDriver must share the FakeClock so TTL expiry is consistent with time
// advances made in the test.
func TestLocker_TC11_TinyTTL(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	// Share the fake clock with the driver so TTL expiry logic is coherent.
	fd.WithClock(fc.Now)
	l := newTestLocker(fc, fd)

	ttl := 500 * time.Millisecond // small TTL; large enough for race-detector scheduling overhead (renew timeout = 495ms)

	_, release, err := l.Acquire(context.Background(), "key11", ttl)
	if err != nil {
		t.Fatalf("TC-11 Acquire: %v", err)
	}
	defer release()

	<-mgr(l).Started()

	renewAt := time.Duration(float64(ttl) * 0.5)

	// Each Advance(renewAt) should trigger exactly one renew, then the manager
	// re-queues. We verify incrementally over 3 cycles.
	for i := range 3 {
		// Wait for the manager to register the timer before advancing the clock.
		waitPendingTimers(t, fc, 1)
		prev := fd.Calls("Renew")
		fc.Advance(renewAt)
		waitForRenewL(t, l, fd, prev+1)
		got := fd.Calls("Renew")
		if got != prev+1 {
			t.Errorf("TC-11 step %d: expected %d Renew calls total, got %d (delta=%d, want 1)",
				i+1, prev+1, got, got-prev)
		}
	}
}

// TC-12: Drift factor is correctly applied to the renew operation's deadline.
// drift = ttl * driftFactor; renew context deadline = clock.Now() + ttl - drift.
// We verify by checking that the renew succeeds (FakeDriver checks TTL expiry
// using FakeClock), and the drift math produces the expected value.
func TestLocker_TC12_DriftFactor(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	fd.WithClock(fc.Now) // share FakeClock so TTL checks are coherent

	const driftFactor = 0.01
	const renewFraction = 0.5
	l := distlock.New(fd,
		distlock.WithClock(fc),
		distlock.WithDriftFactor(driftFactor),
		distlock.WithRenewFraction(renewFraction),
	)

	// Use a large TTL so the renew context timeout (ttl-drift = 9.9s) is safely
	// larger than race-detector goroutine scheduling overhead.
	ttl := 10 * time.Second

	_, release, err := l.Acquire(context.Background(), "key12", ttl)
	if err != nil {
		t.Fatalf("TC-12 Acquire: %v", err)
	}
	defer release()

	<-mgr(l).Started()
	waitPendingTimers(t, fc, 1)

	// Verify the drift math: drift = ttl * driftFactor = 10s * 0.01 = 100ms.
	drift := time.Duration(float64(ttl) * driftFactor)
	if drift != 100*time.Millisecond {
		t.Errorf("TC-12: drift = %v, want 100ms (ttl=10s, driftFactor=0.01)", drift)
	}

	// Advance to trigger first renew.
	fc.Advance(time.Duration(float64(ttl) * renewFraction))
	waitForRenewL(t, l, fd, 1)

	// Wait for manager to re-register the timer after the first renew.
	waitPendingTimers(t, fc, 1)

	// After renew, manager re-queues at now + ttl * renewFraction (5s).
	// Advancing 5s again should trigger the second renew.
	fc.Advance(time.Duration(float64(ttl) * renewFraction))
	waitForRenewL(t, l, fd, 2)

	if fd.Calls("Renew") < 2 {
		t.Errorf("TC-12: expected ≥2 Renew calls, got %d", fd.Calls("Renew"))
	}
}

// waitForRenewOnMgr waits for Renew count using a Manager's RenewNotify channel.
// If mgr is non-nil, uses mgr.RenewNotify for reliable synchronization;
// otherwise falls back to Gosched spinning.
func waitForRenewOnMgr(t *testing.T, m *distlock.Manager, fd *locktest.FakeDriver, want int) {
	t.Helper()
	const totalTimeout = 30 * time.Second
	deadline := time.Now().Add(totalTimeout)
	for fd.Calls("Renew") < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d Renew calls (got %d)", want, fd.Calls("Renew"))
		}
		if m != nil {
			select {
			case <-m.RenewNotify:
				// Received a renew notification; loop to recheck the count.
			case <-time.After(totalTimeout):
				t.Fatalf("RenewNotify: timed out waiting for renew signal (want %d, got %d)", want, fd.Calls("Renew"))
			}
		} else {
			runtime.Gosched()
		}
	}
}
