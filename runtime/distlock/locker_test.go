package distlock_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// testTimeout is the default guard timeout used in select statements across
// locker tests. It applies only as a hard deadline to prevent test hangs —
// it does not assert anything about real execution time.
const testTimeout = testtime.D10s

// lockerSmallTTL is a TTL small enough to exercise renew-timeout with race-detector
// overhead (renew timeout = 495ms = TTL*0.99).
const lockerSmallTTL = testtime.D500ms

// lockerExpiredTTLFrac is the fractional nanosecond below 1ms used to verify
// boundary conditions in expiry tests.
const (
	lockerExpiredTTL500us = 500 * time.Microsecond
	lockerExpiredTTL999us = 999_999 * time.Nanosecond
)

// lockerTTLHalf is exactly half of the default 10s test TTL, used when
// computing the renewal trigger point (ttl * 0.5 = 5s) without introducing
// numeric literals in arithmetic expressions.
const lockerTTLHalf = testtime.D10s / 2

// lockerRenewTolerance is the tolerance window for Renew ctx deadline assertions.
const lockerRenewTolerance = testtime.D10ms

// lockerWaitRenewTimeout is the hard deadline used in waitForRenewOnMgr.
const lockerWaitRenewTimeout = testtime.D30s

func assertSameErrorIdentity(t *testing.T, got, want error, msg string) {
	t.Helper()
	if sameErrorIdentity(got, want) {
		return
	}
	t.Fatalf("want exact error identity: got %T %v, want %T %v: %s", got, got, want, want, msg)
}

func sameErrorIdentity(got, want error) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	if gv.Type() != wv.Type() || !gv.Comparable() {
		return false
	}
	return gv.Equal(wv)
}

// mgr returns the internal Manager via type assertion.
// lockerImpl exposes Manager() returning *Manager.
func mgr(l distlock.Locker) *distlock.Manager {
	type mgrGetter interface {
		Manager() *distlock.Manager
	}
	return l.(mgrGetter).Manager()
}

// newTestLocker constructs a Locker backed by FakeDriver + clockmock.FakeClock.
func newTestLocker(fc *clockmock.FakeClock, fd *locktest.FakeDriver) distlock.Locker {
	return distlock.MustNew(fd, fc)
}

type typedNilDriver struct{}

func (*typedNilDriver) SetNX(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}

func (*typedNilDriver) Renew(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}

func (*typedNilDriver) Release(context.Context, string, string) error {
	return nil
}

// waitForRenewL waits for Renew count using the locker's manager RenewNotify.
func waitForRenewL(t *testing.T, l distlock.Locker, fd *locktest.FakeDriver, want int) {
	t.Helper()
	waitForRenewOnMgr(t, mgr(l), fd, want)
}

// TC-1: Happy path — acquire, advance to trigger renew, release.
// Cause == ErrLockReleased.
func TestLocker_TC1_HappyPath(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := testtime.D10s
	lockCtx, release, err := l.Acquire(context.Background(), "key1", ttl)
	if err != nil {
		t.Fatalf("TC-1 Acquire: %v", err)
	}

	// Wait for manager to start and register the timer.
	<-mgr(l).Started()
	waitPendingTimers(t, fc)

	// Advance to trigger renew (ttl * 0.5 = 5s).
	fc.Advance(lockerTTLHalf)

	// Give renew goroutine a moment to process.
	waitForRenewL(t, l, fd, 1)

	if fd.Calls("Renew") < 1 {
		t.Errorf("TC-1: expected at least 1 Renew call, got %d", fd.Calls("Renew"))
	}

	if err := release(); err != nil {
		t.Errorf("TC-1: release() returned unexpected error: %v", err)
	}

	select {
	case <-lockCtx.Done():
	case <-time.After(testTimeout):
		t.Fatal("TC-1: lockCtx should be Done after release()")
	}

	cause := context.Cause(lockCtx)
	assertSameErrorIdentity(t, cause, distlock.ErrLockReleased, "TC-1 cause")
}

// TC-2: Advance ttl*0.5 - 1ns → no renew; Advance 1ns → exactly 1 renew.
func TestLocker_TC2_RenewIntervalPrecision(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := testtime.D10s
	renewAt := time.Duration(float64(ttl) * 0.5)

	_, release, err := l.Acquire(context.Background(), "key2", ttl)
	if err != nil {
		t.Fatalf("TC-2 Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()
	waitPendingTimers(t, fc)

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

// TC-3: RenewError exhausts retry budget → lockCtx canceled with ErrLockLost.
// Also verifies sibling-lock isolation: after key3a is lost, key3b continues
// to be renewed independently.
//
// maxRenewAttempts=1 is used so a single-shot error fully exhausts the budget.
// SetNextRenewError is single-shot: key3a's one attempt consumes the error;
// key3b's renew call has no injected error and succeeds.
func TestLocker_TC3_RenewError_LockLost(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	// Use maxRenewAttempts=1 so a single injected error exhausts the budget.
	l := distlock.MustNew(fd, fc,
		distlock.WithMaxRenewAttempts(1),
	)

	ttl := testtime.D10s

	lockCtx1, release1, err := l.Acquire(context.Background(), "key3a", ttl)
	if err != nil {
		t.Fatalf("TC-3 Acquire key3a: %v", err)
	}
	defer func() {
		if err := release1(); err != nil {
			t.Logf("release1: %v", err)
		}
	}()

	// Acquire key3b before advancing so both locks are in the manager heap.
	_, release2, err := l.Acquire(context.Background(), "key3b", ttl)
	if err != nil {
		t.Fatalf("TC-3 Acquire key3b: %v", err)
	}
	defer func() {
		if err := release2(); err != nil {
			t.Logf("release2: %v", err)
		}
	}()

	<-mgr(l).Started()

	// Wait until the manager has registered at least one timer (earliest heap entry).
	waitPendingTimers(t, fc)

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

	// Inject single-shot error. With maxRenewAttempts=1, key3a's single attempt
	// consumes this error (budget exhausted → ErrLockLost). key3b's renew call
	// has no injected error and succeeds normally (sibling isolation).
	fd.SetNextRenewError(locktest.ErrDriverIO)

	// Advance to trigger the first renew (key3a, earlier in heap).
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// lockCtx1 should be canceled with ErrLockLost.
	select {
	case <-lockCtx1.Done():
	case <-time.After(testTimeout):
		t.Fatal("TC-3: lockCtx1 should be Done after renew error budget exhausted")
	}

	cause := context.Cause(lockCtx1)
	assertSameErrorIdentity(t, cause, distlock.ErrLockLost, "TC-3 cause")

	// Sibling isolation: advance past key3b's next renewal window and verify
	// that the manager still renews key3b (no error was injected for it).
	waitPendingTimers(t, fc) // key3b's timer should be registered
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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := testtime.D10s

	lockCtx, release, err := l.Acquire(context.Background(), "key4", ttl)
	if err != nil {
		t.Fatalf("TC-4 Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()
	waitPendingTimers(t, fc)

	fd.SetNextRenewHeld(false)
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	select {
	case <-lockCtx.Done():
	case <-time.After(testTimeout):
		t.Fatal("TC-4: lockCtx should be Done when held=false")
	}

	assertSameErrorIdentity(t, context.Cause(lockCtx), distlock.ErrLockLost, "TC-4 cause")

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
		fc := clockmock.New(time.Time{})
		fd := locktest.NewFakeDriver()
		l := newTestLocker(fc, fd)

		ttl := testtime.D10s

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
		assertSameErrorIdentity(t, cause, context.Canceled, "TC-5a cause")

		// release() should not panic (error is intentionally discarded after context cancel).
		if err := release(); err != nil {
			t.Logf("release after cancel: %v", err)
		}
	})

	t.Run("TC5b_CustomCausePropagation", func(t *testing.T) {
		fc := clockmock.New(time.Time{})
		fd := locktest.NewFakeDriver()
		l := newTestLocker(fc, fd)

		ttl := testtime.D10s

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
		assertSameErrorIdentity(t, cause, parentCause, "TC-5b parent cause")
		assertSameErrorIdentity(t, cause, customErr, "TC-5b custom cause")

		// release() should not panic (error is intentionally discarded after context cancel).
		if err := release(); err != nil {
			t.Logf("release after cancel: %v", err)
		}
	})
}

// TC-6: Double release — idempotent, Driver.Release called exactly once.
func TestLocker_TC6_DoubleRelease(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := testtime.D10s

	_, release, err := l.Acquire(context.Background(), "key6", ttl)
	if err != nil {
		t.Fatalf("TC-6 Acquire: %v", err)
	}

	<-mgr(l).Started()

	if err := release(); err != nil {
		t.Errorf("TC-6: first release() returned unexpected error: %v", err)
	}
	if err := release(); err != nil { // second call — must not panic, must return nil (idempotent)
		t.Errorf("TC-6: second release() should return nil (idempotent), got: %v", err)
	}

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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	fd.SetNextSetNX(false)

	l := newTestLocker(fc, fd)

	ttl := testtime.D10s

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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := l.Acquire(canceledCtx, "key8", testtime.D10s)
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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	const n = 100
	ttl := testtime.D1min

	baseline := runtime.NumGoroutine()

	type result struct {
		lCtx    context.Context
		release func() error
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
		if err := r.release(); err != nil {
			t.Logf("release: %v", err)
		}
	}

	// Wait for drain.
	select {
	case <-mgr(l).Drained():
	case <-time.After(testtime.D30s):
		t.Fatal("TC-9: manager should drain after all releases")
	}

	// Bounded goroutine-count check after drain — must return to baseline.
	drainDeadline := time.Now().Add(testtime.EventuallyLong)
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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	ttl := testtime.D10s

	// First acquisition.
	_, release, err := l.Acquire(context.Background(), "key10", ttl)
	if err != nil {
		t.Fatalf("TC-10 first Acquire: %v", err)
	}
	<-mgr(l).Started()

	if err := release(); err != nil {
		t.Logf("release: %v", err)
	}
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
	defer func() {
		if err := release2(); err != nil {
			t.Logf("release2: %v", err)
		}
	}()

	select {
	case <-mgr(l).Started():
	case <-time.After(testTimeout):
		t.Fatal("TC-10: manager should restart on second Acquire")
	}
}

// TestLocker_TC11_SmallTTLNoSpinLoop verifies that a small (but realistic) TTL
// produces exactly one renew per FakeClock advance — no spin-loop behavior.
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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	l := newTestLocker(fc, fd)

	ttl := lockerSmallTTL // small TTL; large enough for race-detector scheduling overhead (renew timeout = 495ms)

	_, release, err := l.Acquire(context.Background(), "key11", ttl)
	if err != nil {
		t.Fatalf("TC-11 Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()

	renewAt := time.Duration(float64(ttl) * 0.5)

	// Each Advance(renewAt) should trigger exactly one renew, then the manager
	// re-queues. We verify incrementally over 3 cycles.
	for i := range 3 {
		// Wait for the manager to register the timer before advancing the clock.
		waitPendingTimers(t, fc)
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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)

	const driftFactor = 0.01
	const renewFraction = 0.5
	l := distlock.MustNew(fd, fc,
		distlock.WithDriftFactor(driftFactor),
		distlock.WithRenewFraction(renewFraction),
	)

	// Use a large TTL so the renew context timeout (ttl-drift = 9.9s) is safely
	// larger than race-detector goroutine scheduling overhead.
	ttl := testtime.D10s

	_, release, err := l.Acquire(context.Background(), "key12", ttl)
	if err != nil {
		t.Fatalf("TC-12 Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()
	waitPendingTimers(t, fc)

	// Verify the drift math: drift = ttl * driftFactor = 10s * 0.01 = 100ms.
	drift := time.Duration(float64(ttl) * driftFactor)
	if drift != testtime.D100ms {
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
	const tolerance = lockerRenewTolerance
	diff := recorded.Sub(expectedDeadline)
	if diff < -tolerance || diff > tolerance {
		t.Errorf("TC-12: Renew ctx deadline = %v, expected ≈ %v (diff %v, tolerance ±%v); "+
			"driftFactor=%v drift=%v renewTimeout=%v",
			recorded, expectedDeadline, diff, tolerance, driftFactor, drift, renewTimeout)
	}

	// Wait for manager to re-register the timer after the first renew.
	waitPendingTimers(t, fc)

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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	type ctxKey struct{ name string }
	key := ctxKey{"trace-id"}
	val := "test-trace-id-abc123"

	parentCtx := context.WithValue(context.Background(), key, val)

	lockCtx, release, err := l.Acquire(parentCtx, "key-val-prop", testtime.D10s)
	if err != nil {
		t.Fatalf("ValuePropagation Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	got := lockCtx.Value(key)
	if got != val {
		t.Errorf("ValuePropagation: lockCtx.Value(key) = %v, want %v", got, val)
	}
}

// TestLocker_LockCtxDeadlinePropagation verifies that the parent deadline is
// propagated into lockCtx (lockCtx derives from ctx).
func TestLocker_LockCtxDeadlinePropagation(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	deadline := time.Now().Add(testtime.D10min)
	parentCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	lockCtx, release, err := l.Acquire(parentCtx, "key-deadline-prop", testtime.D10s)
	if err != nil {
		t.Fatalf("DeadlinePropagation Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

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
	_ = distlock.MustNew(nil, clock.Real())
}

func TestLocker_New_ReturnsErrorOnTypedNilDriver(t *testing.T) {
	var driver *typedNilDriver
	locker, err := distlock.New(driver, clock.Real())
	if err == nil {
		t.Fatal("New(typed nil driver) should return error")
	}
	if locker != nil {
		t.Fatalf("New(typed nil driver) locker = %T, want nil", locker)
	}
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
			_ = distlock.MustNew(fd, clock.Real(), distlock.WithRenewFraction(tc.fraction))
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
			_ = distlock.MustNew(fd, clock.Real(), distlock.WithDriftFactor(tc.factor))
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
		{"negative", testtime.DNeg1s},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fd := locktest.NewFakeDriver()
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("New with releaseTimeout=%v should panic", tc.timeout)
				}
			}()
			_ = distlock.MustNew(fd, clock.Real(), distlock.WithReleaseTimeout(tc.timeout))
		})
	}
}

// TestLocker_Acquire_RejectsZeroTTL verifies that Acquire returns an error (not panic)
// when TTL is zero, negative, or sub-millisecond — runtime input validation.
//
// Sub-millisecond TTLs would truncate to 0 milliseconds when passed to Redis
// SetNX/PEXPIRE, which go-redis v9 documents as "no expiration" — creating a
// permanent lock that survives process death. Reject at the contract boundary.
func TestLocker_Acquire_RejectsZeroTTL(t *testing.T) {
	tests := []struct {
		name string
		ttl  time.Duration
	}{
		{"zero", 0},
		{"negative", testtime.DNeg1s},
		{"sub-millisecond/microsecond", lockerExpiredTTL500us},
		{"sub-millisecond/nanosecond", lockerExpiredTTL999us},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := clockmock.New(time.Time{})
			fd := locktest.NewFakeDriver()
			l := newTestLocker(fc, fd)

			lockCtx, release, err := l.Acquire(context.Background(), "key-zero-ttl", tc.ttl)
			if err == nil {
				t.Errorf("Acquire with TTL=%v should return error", tc.ttl)
				releaseIfNotNil(t, release)
			}
			if lockCtx != nil {
				t.Error("Acquire with invalid TTL should return nil lockCtx")
			}
		})
	}
}

// releaseIfNotNil invokes release if it is non-nil and logs any error from
// it. Used to keep the unexpected-success path of TTL-rejection tests flat,
// since test bodies shouldn't carry the cognitive overhead of nested
// release-and-handle chains.
func releaseIfNotNil(t *testing.T, release func() error) {
	t.Helper()
	if release == nil {
		return
	}
	if err := release(); err != nil {
		t.Logf("release: %v", err)
	}
}

// TestLocker_ConcurrentRelease acquires 100 locks then releases all 100 concurrently.
// Asserts no panic, no race, and all 100 Driver.Release calls are made.
func TestLocker_ConcurrentRelease(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	const n = 100
	ttl := testtime.D1min

	releases := make([]func() error, n)
	for i := range n {
		key := "concurrent-release-" + strconv.Itoa(i)
		_, rel, err := l.Acquire(context.Background(), key, ttl)
		if err != nil {
			t.Fatalf("ConcurrentRelease Acquire[%d]: %v", i, err)
		}
		releases[i] = rel
	}

	<-mgr(l).Started()

	// Release all 100 concurrently. All release() calls should return nil.
	var wg sync.WaitGroup
	for _, rel := range releases {
		wg.Add(1)
		rel := rel
		go func() {
			defer wg.Done()
			if err := rel(); err != nil {
				t.Errorf("ConcurrentRelease: release() returned unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	select {
	case <-mgr(l).Drained():
	case <-time.After(testtime.D30s):
		t.Fatal("ConcurrentRelease: manager should drain after all concurrent releases")
	}

	if fd.Calls("Release") != n {
		t.Errorf("ConcurrentRelease: expected %d Release calls, got %d", n, fd.Calls("Release"))
	}
}

// TestLocker_ExtremeTTL_LongDuration verifies renewal fires at ttl*renewFraction
// with a 1-hour TTL, using a fake clock (no real waits).
func TestLocker_ExtremeTTL_LongDuration(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	l := distlock.MustNew(fd, fc,
		distlock.WithRenewFraction(0.5),
	)

	ttl := time.Hour
	renewAt := time.Duration(float64(ttl) * 0.5) // 30 minutes

	_, release, err := l.Acquire(context.Background(), "extreme-long-ttl", ttl)
	if err != nil {
		t.Fatalf("ExtremeTTL_Long Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()
	waitPendingTimers(t, fc)

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
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	l := distlock.MustNew(fd, fc,
		distlock.WithRenewFraction(0.5),
	)

	ttl := time.Millisecond
	renewAt := time.Duration(float64(ttl) * 0.5)

	_, release, err := l.Acquire(context.Background(), "extreme-short-ttl", ttl)
	if err != nil {
		t.Fatalf("ExtremeTTL_Short Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()

	// Verify exactly one renew per Advance over 3 cycles.
	for i := range 3 {
		waitPendingTimers(t, fc)
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

// TC-13: Transient-then-success — FakeDriver returns one I/O error then succeeds.
// With the default retry budget (maxRenewAttempts=3), the manager retries and
// the lock is NOT lost. Calls("Renew") == 2 (1 fail + 1 success).
func TestLocker_TC13_TransientRenewError_ThenSuccess(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd) // default maxRenewAttempts=3

	ttl := testtime.D10s

	lockCtx, release, err := l.Acquire(context.Background(), "key13", ttl)
	if err != nil {
		t.Fatalf("TC-13 Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()
	waitPendingTimers(t, fc)

	// Inject single-shot error — first attempt fails, second succeeds.
	fd.SetNextRenewError(locktest.ErrDriverIO)

	// Advance to trigger renewal.
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// Wait for both attempts (1 fail + 1 success = 2 Renew calls).
	// waitForRenewL uses fd.Calls("Renew") >= want as the condition,
	// so waiting for 2 ensures both the failed attempt and the successful retry
	// have been made.
	waitForRenewL(t, l, fd, 2)

	// Lock must NOT be lost — lockCtx should still be live.
	select {
	case <-lockCtx.Done():
		t.Errorf("TC-13: lockCtx should NOT be canceled after transient error + successful retry; cause=%v",
			context.Cause(lockCtx))
	default:
		// Good — lock still live.
	}

	// Exactly 2 Renew calls: attempt 1 (error) + attempt 2 (success).
	if got := fd.Calls("Renew"); got != 2 {
		t.Errorf("TC-13: expected 2 Renew calls (1 fail + 1 success), got %d", got)
	}
}

// TC-14: Budget exhausted → lock lost. FakeDriver returns persistent I/O error.
// With maxRenewAttempts=3, all 3 attempts fail → ErrLockLost.
// Calls("Renew") == 3.
func TestLocker_TC14_BudgetExhausted_LockLost(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd) // default maxRenewAttempts=3

	ttl := testtime.D10s

	lockCtx, release, err := l.Acquire(context.Background(), "key14", ttl)
	if err != nil {
		t.Fatalf("TC-14 Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()
	waitPendingTimers(t, fc)

	// Inject persistent error — all attempts fail.
	fd.SetRenewErrorPersistent(locktest.ErrDriverIO)

	// Advance to trigger renewal.
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// Lock must be lost.
	select {
	case <-lockCtx.Done():
	case <-time.After(testTimeout):
		t.Fatal("TC-14: lockCtx should be Done after budget exhausted")
	}

	cause := context.Cause(lockCtx)
	assertSameErrorIdentity(t, cause, distlock.ErrLockLost, "TC-14 cause")

	// Exactly 3 Renew calls (default budget=3).
	if got := fd.Calls("Renew"); got != 3 {
		t.Errorf("TC-14: expected 3 Renew calls (budget=3, all failed), got %d", got)
	}
}

// TC-15: Permanent ownership-lost (held=false) skips retry → immediate ErrLockLost.
// Even with maxRenewAttempts=3, held=false is not an I/O error — no retry.
// Calls("Renew") == 1.
func TestLocker_TC15_PermanentOwnershipLost_NoRetry(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd) // default maxRenewAttempts=3

	ttl := testtime.D10s

	lockCtx, release, err := l.Acquire(context.Background(), "key15", ttl)
	if err != nil {
		t.Fatalf("TC-15 Acquire: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Logf("release: %v", err)
		}
	}()

	<-mgr(l).Started()
	waitPendingTimers(t, fc)

	// Simulate ownership lost (held=false, no I/O error) — permanent; no retry.
	fd.SetNextRenewHeld(false)

	// Advance to trigger renewal.
	fc.Advance(time.Duration(float64(ttl) * 0.5))

	// Lock must be lost immediately.
	select {
	case <-lockCtx.Done():
	case <-time.After(testTimeout):
		t.Fatal("TC-15: lockCtx should be Done immediately on ownership lost")
	}

	cause := context.Cause(lockCtx)
	assertSameErrorIdentity(t, cause, distlock.ErrLockLost, "TC-15 cause")

	// Exactly 1 Renew call — no retry on permanent ownership loss.
	if got := fd.Calls("Renew"); got != 1 {
		t.Errorf("TC-15: expected 1 Renew call (no retry on held=false), got %d", got)
	}
}

// TestLocker_WithMaxRenewAttempts_Validation verifies that New() panics on invalid values.
func TestLocker_WithMaxRenewAttempts_Validation(t *testing.T) {
	tests := []struct {
		name string
		n    int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fd := locktest.NewFakeDriver()
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("New with maxRenewAttempts=%d should panic", tc.n)
				}
			}()
			_ = distlock.MustNew(fd, clock.Real(), distlock.WithMaxRenewAttempts(tc.n))
		})
	}
}

// TestLocker_Release_ReturnsError verifies that release() propagates Driver.Release errors.
func TestLocker_Release_ReturnsError(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	_, release, err := l.Acquire(context.Background(), "key-release-err", testtime.D10s)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	<-mgr(l).Started()

	// Inject a release error.
	fd.SetNextReleaseError(locktest.ErrDriverIO)

	releaseErr := release()
	if releaseErr == nil {
		t.Error("release() should return an error when Driver.Release fails")
	}
	if !errors.Is(releaseErr, locktest.ErrDriverIO) {
		t.Errorf("release() error = %v, want wrapping ErrDriverIO", releaseErr)
	}
}

// TestLocker_Stats_Empty verifies that a fresh Locker reports 0 active locks.
func TestLocker_Stats_Empty(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	s := l.Stats()
	if s.ActiveLocks != 0 {
		t.Errorf("Stats_Empty: ActiveLocks = %d, want 0", s.ActiveLocks)
	}
}

// TestLocker_Stats_AfterAcquire verifies that Stats().ActiveLocks reflects active count.
func TestLocker_Stats_AfterAcquire(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	_, r1, _ := l.Acquire(context.Background(), "stats-key1", testtime.D1min)
	_, r2, _ := l.Acquire(context.Background(), "stats-key2", testtime.D1min)
	_, r3, _ := l.Acquire(context.Background(), "stats-key3", testtime.D1min)

	<-mgr(l).Started()

	// Wait for all 3 to appear in snapshot.
	deadline := time.Now().Add(testTimeout)
	for l.Stats().ActiveLocks < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("Stats_AfterAcquire: timed out; ActiveLocks = %d, want 3", l.Stats().ActiveLocks)
		}
		runtime.Gosched()
	}

	if got := l.Stats().ActiveLocks; got != 3 {
		t.Errorf("Stats_AfterAcquire: ActiveLocks = %d, want 3", got)
	}

	defer func() {
		if err := r1(); err != nil {
			t.Logf("r1: %v", err)
		}
	}()
	defer func() {
		if err := r2(); err != nil {
			t.Logf("r2: %v", err)
		}
	}()
	if err := r3(); err != nil {
		t.Logf("r3: %v", err)
	}
}

// TestLocker_Stats_AfterRelease verifies Stats().ActiveLocks decrements on release.
func TestLocker_Stats_AfterRelease(t *testing.T) {
	fc := clockmock.New(time.Time{})
	fd := locktest.NewFakeDriver()
	l := newTestLocker(fc, fd)

	_, r1, _ := l.Acquire(context.Background(), "stats-rel-key1", testtime.D1min)
	_, r2, _ := l.Acquire(context.Background(), "stats-rel-key2", testtime.D1min)
	_, r3, _ := l.Acquire(context.Background(), "stats-rel-key3", testtime.D1min)
	defer func() {
		if err := r2(); err != nil {
			t.Logf("r2: %v", err)
		}
	}()
	defer func() {
		if err := r3(); err != nil {
			t.Logf("r3: %v", err)
		}
	}()

	<-mgr(l).Started()

	// Wait for all 3.
	deadline := time.Now().Add(testTimeout)
	for l.Stats().ActiveLocks < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("Stats_AfterRelease: timed out waiting for 3 active locks; got %d", l.Stats().ActiveLocks)
		}
		runtime.Gosched()
	}

	// Release one lock.
	if err := r1(); err != nil {
		t.Logf("r1: %v", err)
	}

	// Wait for count to drop to 2.
	deadline = time.Now().Add(testTimeout)
	for l.Stats().ActiveLocks != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("Stats_AfterRelease: timed out waiting for ActiveLocks=2; got %d", l.Stats().ActiveLocks)
		}
		runtime.Gosched()
	}

	if got := l.Stats().ActiveLocks; got != 2 {
		t.Errorf("Stats_AfterRelease: ActiveLocks = %d, want 2", got)
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
	const totalTimeout = lockerWaitRenewTimeout
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
