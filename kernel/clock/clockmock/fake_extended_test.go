package clockmock_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

const (
	threeIntervals = 3 * time.Second // multi-tick coalescing test span
	twoIntervals   = 2 * time.Second // 2x interval span used to verify post-Stop quiescence
)

// TestFakeClock_NewTicker_FiresOnAdvance verifies that NewTicker fires
// every interval when Advance is called.
func TestFakeClock_NewTicker_FiresOnAdvance(t *testing.T) {
	fc := clockmock.New(epoch)

	tk := fc.NewTicker(testtime.D1s)
	defer tk.Stop()

	// Before any Advance, no tick available.
	select {
	case <-tk.C():
		t.Fatal("ticker fired before any Advance")
	default:
	}

	// Advance one full interval — one tick.
	fc.Advance(testtime.D1s)
	select {
	case <-tk.C():
	default:
		t.Fatal("ticker did not fire after one interval Advance")
	}

	// Advance another full interval — another tick.
	fc.Advance(testtime.D1s)
	select {
	case <-tk.C():
	default:
		t.Fatal("ticker did not fire after second interval Advance")
	}
}

// TestFakeClock_NewTicker_CoalescesOnLargeJump verifies that a single
// Advance jumping past multiple intervals delivers at most one tick on the
// channel (matches stdlib time.Ticker semantics).
func TestFakeClock_NewTicker_CoalescesOnLargeJump(t *testing.T) {
	fc := clockmock.New(epoch)
	tk := fc.NewTicker(testtime.D1s)
	defer tk.Stop()

	// Jump past 3 intervals in one Advance.
	fc.Advance(threeIntervals)

	// Drain ch — must produce exactly 1 value, not 3.
	count := 0
	for {
		select {
		case <-tk.C():
			count++
		default:
			if count != 1 {
				t.Fatalf("expected exactly 1 coalesced tick, got %d", count)
			}
			return
		}
	}
}

// TestFakeClock_NewTicker_StopReleases verifies Stop is idempotent and
// removes the ticker from the registry.
func TestFakeClock_NewTicker_StopReleases(t *testing.T) {
	fc := clockmock.New(epoch)
	tk := fc.NewTicker(testtime.D1s)

	if got := fc.PendingTickers(); got != 1 {
		t.Fatalf("PendingTickers before Stop = %d, want 1", got)
	}
	tk.Stop()
	if got := fc.PendingTickers(); got != 0 {
		t.Fatalf("PendingTickers after Stop = %d, want 0", got)
	}
	tk.Stop() // idempotent
	if got := fc.PendingTickers(); got != 0 {
		t.Fatalf("PendingTickers after second Stop = %d, want 0", got)
	}

	// Stopped ticker must not fire on Advance.
	fc.Advance(twoIntervals)
	select {
	case <-tk.C():
		t.Fatal("stopped ticker fired on Advance")
	default:
	}
}

// TestFakeClock_AfterFunc_RunsCallbackOnAdvance verifies that AfterFunc
// schedules the callback on a goroutine and that Advance triggers it.
func TestFakeClock_AfterFunc_RunsCallbackOnAdvance(t *testing.T) {
	fc := clockmock.New(epoch)

	done := make(chan struct{})
	var ran atomic.Int32
	tm := fc.AfterFunc(epoch.Add(testtime.D1s), func() {
		ran.Add(1)
		close(done)
	})
	defer tm.Stop()

	// Before deadline, callback must not run.
	fc.Advance(testtime.D500ms)
	select {
	case <-done:
		t.Fatal("AfterFunc callback ran before deadline")
	default:
	}

	// Crossing deadline triggers exactly one callback.
	fc.Advance(testtime.D1s)
	<-done
	if got := ran.Load(); got != 1 {
		t.Fatalf("AfterFunc callback ran %d times, want 1", got)
	}
}

// TestFakeClock_AfterFunc_StopPreventsCallback verifies Stop returns true
// for an active AfterFunc timer and prevents the callback.
func TestFakeClock_AfterFunc_StopPreventsCallback(t *testing.T) {
	fc := clockmock.New(epoch)

	var ran atomic.Int32
	tm := fc.AfterFunc(epoch.Add(testtime.D1s), func() { ran.Add(1) })
	if !tm.Stop() {
		t.Fatal("Stop() returned false for active AfterFunc timer")
	}

	fc.Advance(twoIntervals)
	if got := ran.Load(); got != 0 {
		t.Fatalf("stopped AfterFunc callback ran %d times, want 0", got)
	}
}

// TestFakeClock_Sleep_ReturnsOnAdvance verifies Sleep blocks until Advance
// reaches the deadline, then returns nil.
func TestFakeClock_Sleep_ReturnsOnAdvance(t *testing.T) {
	fc := clockmock.New(epoch)

	done := make(chan error, 1)
	go func() {
		done <- fc.Sleep(context.Background(), epoch.Add(testtime.D1s))
	}()

	// Wait for Sleep to register its timer.
	for fc.PendingTimers() == 0 {
	}

	fc.Advance(testtime.D1s)
	if err := <-done; err != nil {
		t.Fatalf("Sleep returned %v, want nil", err)
	}
}

// TestFakeClock_Sleep_CtxCancel verifies Sleep returns ctx.Err() when ctx
// is canceled before the deadline.
func TestFakeClock_Sleep_CtxCancel(t *testing.T) {
	fc := clockmock.New(epoch)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- fc.Sleep(ctx, epoch.Add(testtime.D1s))
	}()

	for fc.PendingTimers() == 0 {
	}

	cancel()
	if err := <-done; err == nil {
		t.Fatal("Sleep returned nil after ctx cancel; want non-nil error")
	}
}

// TestFakeClock_Sleep_PastDeadlineReturnsImmediately verifies Sleep with an
// already-passed deadline returns immediately.
func TestFakeClock_Sleep_PastDeadlineReturnsImmediately(t *testing.T) {
	fc := clockmock.New(epoch)
	if err := fc.Sleep(context.Background(), epoch.Add(-testtime.D1s)); err != nil {
		t.Fatalf("Sleep on past deadline returned %v, want nil", err)
	}
}
