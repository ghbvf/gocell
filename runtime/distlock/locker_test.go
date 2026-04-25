package distlock_test

import (
	"context"
	"errors"
	"math"
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
//
// Sub-cases:
//   - TC-5a: plain context.WithCancel → Cause == context.Canceled
//   - TC-5b: context.WithCancelCause with custom cause → Cause propagated exactly
func TestLocker_TC5_ParentCancel(t *testing.T) {
	t.Run("TC5a_PlainCancel", func(t *testing.T) {
		fc := locktest.NewFakeClock(time.Time{})
		fd := locktest.NewFakeDriver()
		l := newTestLocker(fc, fd)

		ttl := 10 * time.Second

		parentCtx, parentCancel := context.WithCancel(context.Background())

		lockCtx, release, err := l.Acquire(parentCtx, "key5a", ttl)
		if err != nil {
			t.Fatalf("TC-5a Acquire: %v", err)
		}

		<-mgr(l).Started()
		parentCancel()

		select {
		case <-lockCtx.Done():
		case <-time.After(testTimeout):
			t.Fatal("TC-5a: lockCtx should be Done after parent cancel")
		}

		// Cause should propagate parent's cause (context.Canceled for plain cancel).
		cause := context.Cause(lockCtx)
		if cause != context.Canceled {
			t.Errorf("TC-5a: Cause = %v, want context.Canceled", cause)
		}

		// release() should not panic.
		release()
	})

	t.Run("TC5b_CustomCausePropagation", func(t *testing.T) {
		fc := locktest.NewFakeClock(time.Time{})
		fd := locktest.NewFakeDriver()
		l := newTestLocker(fc, fd)

		ttl := 10 * time.Second

		customErr := errors.New("custom-parent-cause")
		parentCtx, parentCancelCause := context.WithCancelCause(context.Background())

		lockCtx, release, err := l.Acquire(parentCtx, "key5b", ttl)
		if err != nil {
			t.Fatalf("TC-5b Acquire: %v", err)
		}

		<-mgr(l).Started()
		parentCancelCause(customErr)

		select {
		case <-lockCtx.Done():
		case <-time.After(testTimeout):
			t.Fatal("TC-5b: lockCtx should be Done after parent cancel with custom cause")
		}

		// context.Cause(lockCtx) must equal context.Cause(parentCtx) == customErr.
		cause := context.Cause(lockCtx)
		parentCause := context.Cause(parentCtx)
		if cause != parentCause {
			t.Errorf("TC-5b: Cause = %v, want parentCause = %v", cause, parentCause)
		}
		if cause != customErr {
			t.Errorf("TC-5b: Cause = %v, want customErr = %v", cause, customErr)
		}

		// release() should not panic.
		release()
	})
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

// TC-9: 100 concurrent Acquire → goroutine count == 1 (manager only) above baseline.
//
// Resource model: 1 manager goroutine for all N held locks (0 per-lock goroutines).
// lockCtx is derived from ctx, so parent cancellation propagates via stdlib
// context machinery — no watcher goroutines are needed.
// After all releases and drain, goroutine count returns to baseline.
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

	// Expected goroutine count above baseline: exactly 1 (the manager goroutine).
	// No per-lock watcher goroutines — lockCtx derives from ctx directly.
	// Allow +2 slack for test-framework goroutines.
	after := runtime.NumGoroutine()
	managerGoroutines := after - baseline
	const maxExpected = 1 + 2 // 1 manager + 2 slack
	if managerGoroutines > maxExpected {
		t.Errorf("TC-9: goroutine count jumped by %d (baseline %d → %d); expected at most %d "+
			"(1 manager + 2 slack — 0 per-lock goroutines)",
			managerGoroutines, baseline, after, maxExpected)
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

	// Bounded goroutine-count check after drain — must return to baseline.
	drainDeadline := time.Now().Add(5 * time.Second)
	for {
		current := runtime.NumGoroutine()
		if current <= baseline+2 { // 2 slack for any test-framework goroutines
			break
		}
		if time.Now().After(drainDeadline) {
			t.Errorf("TC-9: goroutines after drain: %d (baseline %d); expected ≤ %d",
				current, baseline, baseline+2)
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

// TestLocker_LockCtxValuePropagation verifies that context values stored in the
// parent ctx are accessible via lockCtx (lockCtx derives from ctx).
func TestLocker_LockCtxValuePropagation(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	type ctxKey struct{ name string }
	key := ctxKey{"trace-id"}
	val := "test-trace-id-abc123"

	parentCtx := context.WithValue(context.Background(), key, val)

	lockCtx, release, err := l.Acquire(parentCtx, "key-val-prop", 10*time.Second)
	if err != nil {
		t.Fatalf("ValuePropagation Acquire: %v", err)
	}
	defer release()

	got := lockCtx.Value(key)
	if got != val {
		t.Errorf("ValuePropagation: lockCtx.Value(key) = %v, want %v", got, val)
	}
}

// TestLocker_LockCtxDeadlinePropagation verifies that the parent deadline is
// propagated into lockCtx (lockCtx derives from ctx).
func TestLocker_LockCtxDeadlinePropagation(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	deadline := time.Now().Add(10 * time.Minute)
	parentCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	lockCtx, release, err := l.Acquire(parentCtx, "key-deadline-prop", 10*time.Second)
	if err != nil {
		t.Fatalf("DeadlinePropagation Acquire: %v", err)
	}
	defer release()

	gotDeadline, ok := lockCtx.Deadline()
	if !ok {
		t.Fatal("DeadlinePropagation: lockCtx has no deadline, want parent deadline propagated")
	}
	if !gotDeadline.Equal(deadline) {
		t.Errorf("DeadlinePropagation: lockCtx.Deadline() = %v, want %v", gotDeadline, deadline)
	}
}

// TestLocker_New_PanicsOnNilDriver verifies that New panics immediately on nil driver.
func TestLocker_New_PanicsOnNilDriver(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("New(nil) should panic")
		}
	}()
	_ = distlock.New(nil)
}

// TestLocker_New_PanicsOnInvalidRenewFraction verifies fail-fast validation in New().
func TestLocker_New_PanicsOnInvalidRenewFraction(t *testing.T) {
	tests := []struct {
		name     string
		fraction float64
	}{
		{"negative", -0.1},
		{"zero", 0},
		{"one", 1},
		{"above_one", 1.5},
		{"NaN", math.NaN()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fd := locktest.NewFakeDriver()
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("New with renewFraction=%v should panic", tc.fraction)
				}
			}()
			_ = distlock.New(fd, distlock.WithRenewFraction(tc.fraction))
		})
	}
}

// TestLocker_New_PanicsOnInvalidDriftFactor verifies fail-fast validation in New().
func TestLocker_New_PanicsOnInvalidDriftFactor(t *testing.T) {
	tests := []struct {
		name   string
		factor float64
	}{
		{"negative", -0.1},
		{"one", 1},
		{"NaN", math.NaN()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fd := locktest.NewFakeDriver()
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("New with driftFactor=%v should panic", tc.factor)
				}
			}()
			_ = distlock.New(fd, distlock.WithDriftFactor(tc.factor))
		})
	}
}

// TestLocker_New_PanicsOnNonPositiveReleaseTimeout verifies fail-fast validation in New().
func TestLocker_New_PanicsOnNonPositiveReleaseTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{"zero", 0},
		{"negative", -1 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fd := locktest.NewFakeDriver()
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("New with releaseTimeout=%v should panic", tc.timeout)
				}
			}()
			_ = distlock.New(fd, distlock.WithReleaseTimeout(tc.timeout))
		})
	}
}

// TestLocker_Acquire_RejectsZeroTTL verifies that Acquire returns an error (not panic)
// when TTL is zero or negative — runtime input validation.
func TestLocker_Acquire_RejectsZeroTTL(t *testing.T) {
	tests := []struct {
		name string
		ttl  time.Duration
	}{
		{"zero", 0},
		{"negative", -1 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := locktest.NewFakeClock(time.Time{})
			fd := locktest.NewFakeDriver()
			l := newTestLocker(fc, fd)

			lockCtx, release, err := l.Acquire(context.Background(), "key-zero-ttl", tc.ttl)
			if err == nil {
				t.Errorf("Acquire with TTL=%v should return error", tc.ttl)
				if release != nil {
					release()
				}
			}
			if lockCtx != nil {
				t.Error("Acquire with invalid TTL should return nil lockCtx")
			}
		})
	}
}

// TestLocker_ConcurrentRelease acquires 100 locks then releases all 100 concurrently.
// Asserts no panic, no race, and all 100 Driver.Release calls are made.
func TestLocker_ConcurrentRelease(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	const n = 100
	ttl := time.Minute

	releases := make([]func(), n)
	for i := range n {
		key := "concurrent-release-" + strconv.Itoa(i)
		_, rel, err := l.Acquire(context.Background(), key, ttl)
		if err != nil {
			t.Fatalf("ConcurrentRelease Acquire[%d]: %v", i, err)
		}
		releases[i] = rel
	}

	<-mgr(l).Started()

	// Release all 100 concurrently.
	var wg sync.WaitGroup
	for _, rel := range releases {
		wg.Add(1)
		rel := rel
		go func() {
			defer wg.Done()
			rel()
		}()
	}
	wg.Wait()

	select {
	case <-mgr(l).Drained():
	case <-time.After(30 * time.Second):
		t.Fatal("ConcurrentRelease: manager should drain after all concurrent releases")
	}

	if fd.Calls("Release") != n {
		t.Errorf("ConcurrentRelease: expected %d Release calls, got %d", n, fd.Calls("Release"))
	}
}

// TestLocker_ExtremeTTL_LongDuration verifies renewal fires at ttl*renewFraction
// with a 1-hour TTL, using a fake clock (no real waits).
func TestLocker_ExtremeTTL_LongDuration(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	l := distlock.New(fd,
		distlock.WithClock(fc),
		distlock.WithRenewFraction(0.5),
	)

	ttl := time.Hour
	renewAt := time.Duration(float64(ttl) * 0.5) // 30 minutes

	_, release, err := l.Acquire(context.Background(), "extreme-long-ttl", ttl)
	if err != nil {
		t.Fatalf("ExtremeTTL_Long Acquire: %v", err)
	}
	defer release()

	<-mgr(l).Started()
	waitPendingTimers(t, fc, 1)

	// Advance just past the renew point.
	fc.Advance(renewAt)
	waitForRenewL(t, l, fd, 1)

	if fd.Calls("Renew") < 1 {
		t.Errorf("ExtremeTTL_Long: expected ≥1 Renew call after advancing %v, got %d", renewAt, fd.Calls("Renew"))
	}
}

// TestLocker_ExtremeTTL_ShortDuration verifies that a 1ms TTL does not spin-loop.
// Each Advance(ttl/2) should trigger exactly one renew.
func TestLocker_ExtremeTTL_ShortDuration(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	l := distlock.New(fd,
		distlock.WithClock(fc),
		distlock.WithRenewFraction(0.5),
	)

	ttl := time.Millisecond
	renewAt := time.Duration(float64(ttl) * 0.5)

	_, release, err := l.Acquire(context.Background(), "extreme-short-ttl", ttl)
	if err != nil {
		t.Fatalf("ExtremeTTL_Short Acquire: %v", err)
	}
	defer release()

	<-mgr(l).Started()

	// Verify exactly one renew per Advance over 3 cycles.
	for i := range 3 {
		waitPendingTimers(t, fc, 1)
		prev := fd.Calls("Renew")
		fc.Advance(renewAt)
		waitForRenewL(t, l, fd, prev+1)
		got := fd.Calls("Renew")
		if got != prev+1 {
			t.Errorf("ExtremeTTL_Short step %d: expected delta=1, got delta=%d (total=%d)",
				i+1, got-prev, got)
		}
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
