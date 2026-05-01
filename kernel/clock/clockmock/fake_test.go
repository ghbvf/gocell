package clockmock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

const (
	dStep        = 100 * time.Millisecond
	dShort       = 50 * time.Millisecond
	dLong        = 200 * time.Millisecond
	dWaitTimer   = 50 * time.Millisecond
	dGoroutineN  = 50
	dRaceIters   = 100
	dRaceTimerD  = 10 * time.Millisecond
	dResetWindow = 30 * time.Millisecond
)

var fixedEpoch = time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

func TestFakeNewClockUsesEpochWhenZero(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(time.Time{})
	got := fc.Now()
	want := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Now()=%v, want %v", got, want)
	}
}

func TestFakeNewClockUsesGivenInitial(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	if !fc.Now().Equal(fixedEpoch) {
		t.Fatalf("Now()=%v, want %v", fc.Now(), fixedEpoch)
	}
}

func TestFakeAdvanceMovesNow(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	fc.Advance(dStep)

	want := fixedEpoch.Add(dStep)
	if !fc.Now().Equal(want) {
		t.Fatalf("Now()=%v, want %v", fc.Now(), want)
	}
}

func TestFakeSinceUntil(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	past := fixedEpoch.Add(-dStep)
	future := fixedEpoch.Add(dStep)

	if got := fc.Since(past); got != dStep {
		t.Errorf("Since(past)=%v, want %v", got, dStep)
	}
	if got := fc.Until(future); got != dStep {
		t.Errorf("Until(future)=%v, want %v", got, dStep)
	}
}

func TestFakeAdvanceFiresPendingTimers(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	a := fc.NewTimerAt(fixedEpoch.Add(dShort))
	b := fc.NewTimerAt(fixedEpoch.Add(dLong))

	if got := fc.PendingTimers(); got != 2 {
		t.Fatalf("PendingTimers()=%d, want 2", got)
	}

	fc.Advance(dShort)

	select {
	case <-a.C():
	default:
		t.Fatal("timer a should have fired after Advance(dShort)")
	}
	select {
	case <-b.C():
		t.Fatal("timer b should not have fired yet")
	default:
	}
	if got := fc.PendingTimers(); got != 1 {
		t.Fatalf("PendingTimers()=%d after first Advance, want 1", got)
	}

	fc.Advance(dLong)
	select {
	case <-b.C():
	default:
		t.Fatal("timer b should have fired after second Advance")
	}
	if got := fc.PendingTimers(); got != 0 {
		t.Fatalf("PendingTimers()=%d after both fired, want 0", got)
	}
}

func TestFakeNewTimerAtPastDeadlineFiresImmediately(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	timer := fc.NewTimerAt(fixedEpoch.Add(-dShort))

	select {
	case <-timer.C():
		// expected
	case <-time.After(dWaitTimer):
		t.Fatal("past-deadline timer did not fire immediately")
	}
}

func TestFakeSetMovesAbsoluteAndFires(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	timer := fc.NewTimerAt(fixedEpoch.Add(dStep))

	fc.Set(fixedEpoch.Add(dLong))

	if got := fc.Now(); !got.Equal(fixedEpoch.Add(dLong)) {
		t.Errorf("Now()=%v after Set, want %v", got, fixedEpoch.Add(dLong))
	}
	select {
	case <-timer.C():
		// expected
	case <-time.After(dWaitTimer):
		t.Fatal("timer did not fire after Set past its deadline")
	}
}

func TestFakeStopPreventsTimer(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	timer := fc.NewTimerAt(fixedEpoch.Add(dStep))

	if !timer.Stop() {
		t.Fatal("Stop() returned false for active timer")
	}
	if got := fc.PendingTimers(); got != 0 {
		t.Fatalf("PendingTimers()=%d after Stop, want 0", got)
	}
	if timer.Stop() {
		t.Error("second Stop() returned true, want false")
	}

	fc.Advance(dStep + dStep)
	select {
	case <-timer.C():
		t.Fatal("stopped timer fired after Advance")
	case <-time.After(dWaitTimer):
	}
}

func TestFakeResetReschedules(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	timer := fc.NewTimerAt(fixedEpoch.Add(dStep))

	if !timer.Reset(dLong) {
		t.Error("Reset on active timer returned false, want true")
	}

	fc.Advance(dStep)
	select {
	case <-timer.C():
		t.Fatal("timer fired before Reset deadline")
	default:
	}

	fc.Advance(dStep + dStep)
	select {
	case <-timer.C():
		// expected
	case <-time.After(dWaitTimer):
		t.Fatal("timer did not fire after Advance past Reset deadline")
	}
}

func TestFakeResetZeroDurationFiresImmediately(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	timer := fc.NewTimerAt(fixedEpoch.Add(dStep))

	timer.Reset(0)

	select {
	case <-timer.C():
		// expected
	case <-time.After(dWaitTimer):
		t.Fatal("timer with Reset(0) did not fire immediately")
	}
}

func TestFakeAdvanceConcurrentSafe(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(fixedEpoch)
	var wg sync.WaitGroup

	for i := 0; i < dGoroutineN; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < dRaceIters; j++ {
				timer := fc.NewTimerAt(fc.Now().Add(dRaceTimerD))
				_ = timer.Stop()
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < dRaceIters; j++ {
			fc.Advance(dResetWindow)
		}
	}()

	wg.Wait()
}
