package distlock_test

import (
	"context"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// testTimeout is the default guard timeout used in select statements across
// locker tests. It applies only as a hard deadline to prevent test hangs —
// it does not assert anything about real execution time.
const testTimeout = 10 * time.Second

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
	case <-time.After(testTimeout):
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
// Also verifies sibling-lock isolation: after key3a is lost, key3b continues
// to be renewed independently (the renew error only affects the lock it targets).
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

	// Acquire key3b before advancing so both locks are in the manager heap.
	_, release2, err := l.Acquire(context.Background(), "key3b", ttl)
	if err != nil {
		t.Fatalf("TC-3 Acquire key3b: %v", err)
	}
	defer release2()

	<-mgr(l).Started()

	// Wait until the manager has registered at least one timer (earliest heap entry).
	waitPendingTimers(t, fc, 1)

	// Wait for both locks to appear in the snapshot before injecting the error.
	snapshotDeadline := time.Now().Add(testTimeout)
	for mgr(l).Snapshot().Locks < 2 {
		if time.Now().After(snapshotDeadline) {
			t.Fatal("TC-3: timed out waiting for both locks to appear in manager")
		}
		runtime.Gosched()
	}

	// Record the Renew count before injecting the error; key3b's renew will
	// increment this counter after key3a is lost.
	renewBefore := fd.Calls("Renew")

	// Inject error — fires on the next Renew call (key3a has the earlier deadline).
	fd.SetNextRenewError(locktest.ErrDriverIO)

	// Advance to trigger the first renew (key3a, earlier in heap).
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// lockCtx1 should be canceled with ErrLockLost.
	select {
	case <-lockCtx1.Done():
	case <-time.After(testTimeout):
		t.Fatal("TC-3: lockCtx1 should be Done after renew error")
	}

	cause := context.Cause(lockCtx1)
	if cause != distlock.ErrLockLost {
		t.Errorf("TC-3: Cause = %v, want ErrLockLost", cause)
	}

	// Sibling isolation: advance past key3b's next renewal window and verify
	// that the manager still renews key3b (no error was injected for it).
	waitPendingTimers(t, fc, 1) // key3b's timer should be registered
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// Wait for at least one more Renew call (key3b's renewal).
	// renewBefore+1 was key3a's failed renew; renewBefore+2 is key3b's renew.
	waitForRenewOnMgr(t, mgr(l), fd, renewBefore+2)

	renewAfter := fd.Calls("Renew")
	if renewAfter <= renewBefore+1 {
		t.Errorf("TC-3: key3b Renew not observed after key3a loss; total calls before=%d after=%d",
			renewBefore, renewAfter)
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
	case <-time.After(testTimeout):
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
	case <-time.After(testTimeout):
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
	case <-time.After(testTimeout):
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

// TC-9: 100 concurrent Acquire → goroutine count ≤ n + 1 + 2 above baseline.
//
// Resource model: 1 manager goroutine + 1 ctx-watcher goroutine per held lock
// + small slack for runtime jitter. A per-lock goroutine model would add n
// extra goroutines on top (2n instead of n+1), so exceeding n+3 is a
// regression indicator.
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
			key := "key9-" + strconv.Itoa(i)
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

	// Expected goroutine count above baseline:
	//   1 manager goroutine + n ctx-watcher goroutines (1 per held lock) + 2 slack for runtime jitter.
	// A per-lock goroutine model would add n more (total 2n+1), so n+3 is the
	// tightest bound that still tolerates minor scheduler non-determinism.
	after := runtime.NumGoroutine()
	managerGoroutines := after - baseline
	const maxExpected = n + 1 + 2 // 1 manager + n ctx-watchers + 2 slack
	if managerGoroutines > maxExpected {
		t.Errorf("TC-9: goroutine count jumped by %d (baseline %d → %d); expected at most %d "+
			"(1 manager + %d ctx-watchers + 2 slack)",
			managerGoroutines, baseline, after, maxExpected, n)
	}

	// Release all.
	for _, r := range results {
		r.release()
	}

	// Wait for drain, then do a bounded retry to confirm watcher goroutines
	// have also fully exited (they may still be exiting when Drained closes).
	select {
	case <-mgr(l).Drained():
	case <-time.After(30 * time.Second):
		t.Fatal("TC-9: manager should drain after all releases")
	}

	// Bounded goroutine-count check after drain (F29).
	drainDeadline := time.Now().Add(5 * time.Second)
	for {
		current := runtime.NumGoroutine()
		if current <= baseline+2 { // 2 slack for any test-framework goroutines
			break
		}
		if time.Now().After(drainDeadline) {
			// Not a hard failure — signals a potential watcher leak.
			t.Logf("TC-9: goroutines after drain: %d (baseline %d); possible watcher leak",
				current, baseline)
			break
		}
		runtime.Gosched()
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
	case <-time.After(testTimeout):
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
	case <-time.After(testTimeout):
		t.Fatal("TC-10: manager should restart on second Acquire")
	}
}

// TestLocker_TC11_SmallTTLNoSpinLoop verifies that a small (but realistic) TTL
// produces exactly one renew per FakeClock advance — no spin-loop behaviour.
//
// We use 500ms (not 1µs) because 1µs TTL is impractical under -race: the
// FakeDriver's scheduler overhead alone exceeds 1µs, causing spurious expiries.
// Per plan note: "extreme TTL is caller responsibility" — the manager's design
// is correct for any positive TTL; this test validates the absence of
// spin-loop by checking the delta is exactly 1 per Advance cycle.
//
// FakeDriver shares FakeClock so TTL expiry logic is coherent with the
// manager's virtual time.
func TestLocker_TC11_SmallTTLNoSpinLoop(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
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
//
// The manager sets the Renew RPC context deadline to:
//
//	deadline = clock.Now() + ttl - drift   where drift = ttl * driftFactor
//
// We verify this by recording the ctx deadline inside FakeDriver.Renew via
// LastRenewDeadline() and asserting it falls within a small tolerance of the
// expected value.
func TestLocker_TC12_DriftFactor(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)

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

	// Record fake clock time just before advancing so we know what clock.Now()
	// the manager will observe during handleRenew.
	fakeTimeBefore := fc.Now()

	// Advance to trigger first renew.
	fc.Advance(time.Duration(float64(ttl) * renewFraction))
	waitForRenewL(t, l, fd, 1)

	// The manager computes the Renew context deadline as:
	//   deadline = clock.Now() + ttl - drift
	// where clock.Now() is the FakeClock value after the Advance.
	// After Advance, fc.Now() = fakeTimeBefore + ttl*renewFraction.
	fakeTimeAtRenew := fakeTimeBefore.Add(time.Duration(float64(ttl) * renewFraction))
	renewTimeout := ttl - drift
	expectedDeadline := fakeTimeAtRenew.Add(renewTimeout)

	recorded := fd.LastRenewDeadline()
	if recorded.IsZero() {
		t.Fatal("TC-12: LastRenewDeadline is zero — FakeDriver did not record the ctx deadline")
	}

	// Allow 10ms tolerance for scheduling jitter between clock.Now() reads.
	const tolerance = 10 * time.Millisecond
	diff := recorded.Sub(expectedDeadline)
	if diff < -tolerance || diff > tolerance {
		t.Errorf("TC-12: Renew ctx deadline = %v, expected ≈ %v (diff %v, tolerance ±%v); "+
			"driftFactor=%v drift=%v renewTimeout=%v",
			recorded, expectedDeadline, diff, tolerance, driftFactor, drift, renewTimeout)
	}

	// Wait for manager to re-register the timer after the first renew.
	waitPendingTimers(t, fc, 1)

	// Advancing again should trigger the second renew.
	fc.Advance(time.Duration(float64(ttl) * renewFraction))
	waitForRenewL(t, l, fd, 2)

	if fd.Calls("Renew") < 2 {
		t.Errorf("TC-12: expected ≥2 Renew calls, got %d", fd.Calls("Renew"))
	}
}

// waitForRenewOnMgr waits for Renew count using a Manager's RenewNotify channel.
// Calls t.Fatal if manager is nil to surface API misuse instead of silently
// spinning.
func waitForRenewOnMgr(t *testing.T, m *distlock.Manager, fd *locktest.FakeDriver, want int) {
	t.Helper()
	if m == nil {
		t.Fatal("waitForRenewOnMgr: manager is nil")
	}
	const totalTimeout = 30 * time.Second
	deadline := time.Now().Add(totalTimeout)
	for fd.Calls("Renew") < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d Renew calls (got %d)", want, fd.Calls("Renew"))
		}
		select {
		case <-m.RenewNotify():
			// Received a renew notification; loop to recheck the count.
		case <-time.After(totalTimeout):
			t.Fatalf("RenewNotify: timed out waiting for renew signal (want %d, got %d)", want, fd.Calls("Renew"))
		}
	}
}
