package executor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// fakeHeartbeater is a test double that records Heartbeat calls.
type fakeHeartbeater struct {
	mu        sync.Mutex
	calls     []heartbeatCall
	okReturn  bool
	errReturn error
}

type heartbeatCall struct {
	instanceID    idutil.SafeID
	leaseID       idutil.SafeID
	leaseDuration time.Duration
}

func (f *fakeHeartbeater) Heartbeat(_ context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, heartbeatCall{instanceID: instanceID, leaseID: leaseID, leaseDuration: leaseDuration})
	return f.okReturn, f.errReturn
}

func (f *fakeHeartbeater) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeHeartbeater) LastCall() heartbeatCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return heartbeatCall{}
	}
	return f.calls[len(f.calls)-1]
}

// waitForOneTicker waits up to 500ms until FakeClock has at least 1 pending ticker.
// This ensures the goroutine has registered its ticker before Advance is called.
func waitForOneTicker(t *testing.T, fc *clockmock.FakeClock) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fc.PendingTickers() >= 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for 1 pending ticker; got %d", fc.PendingTickers())
}

// TestHeartbeat_TickerFiresOnAdvance verifies one Advance(interval) triggers one Heartbeat call.
func TestHeartbeat_TickerFiresOnAdvance(t *testing.T) {
	fc := clockmock.New(time.Now())
	hb := &fakeHeartbeater{okReturn: true}
	instID := idutil.SafeID("inst-hb-1")
	leaseID := idutil.SafeID("lease-hb-1")
	interval := 10 * time.Second
	leaseDur := 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, instID, leaseID, interval, leaseDur, noopLogger())
	}()

	// Wait for the goroutine to register its ticker.
	waitForOneTicker(t, fc)

	// Advance one interval: exactly one heartbeat.
	fc.Advance(interval)
	waitForCalls(t, hb, 1, 500*time.Millisecond)

	if got := hb.CallCount(); got != 1 {
		t.Errorf("after one Advance: CallCount = %d, want 1", got)
	}

	// Advance two more intervals.
	fc.Advance(interval)
	waitForCalls(t, hb, 2, 500*time.Millisecond)
	fc.Advance(interval)
	waitForCalls(t, hb, 3, 500*time.Millisecond)

	if got := hb.CallCount(); got != 3 {
		t.Errorf("after three Advances: CallCount = %d, want 3", got)
	}

	cancel()
	wg.Wait()
	if fc.PendingTickers() != 0 {
		t.Error("goroutine leaked: PendingTickers != 0 after cancel+join")
	}
}

// TestHeartbeat_NoLeakOnCancel verifies cancel + wg.Wait leaves no pending tickers.
func TestHeartbeat_NoLeakOnCancel(t *testing.T) {
	fc := clockmock.New(time.Now())
	hb := &fakeHeartbeater{okReturn: true}
	instID := idutil.SafeID("inst-noleak")
	leaseID := idutil.SafeID("lease-noleak")

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, instID, leaseID, 5*time.Second, 30*time.Second, noopLogger())
	}()

	// Wait for ticker registration then cancel.
	waitForOneTicker(t, fc)
	cancel()
	wg.Wait()

	if fc.PendingTickers() != 0 {
		t.Errorf("PendingTickers = %d, want 0 after cancel+join", fc.PendingTickers())
	}
}

// TestHeartbeat_StaleLease verifies ok=false stops the goroutine.
func TestHeartbeat_StaleLease(t *testing.T) {
	fc := clockmock.New(time.Now())
	hb := &fakeHeartbeater{okReturn: false} // stale from the start
	instID := idutil.SafeID("inst-stale")
	leaseID := idutil.SafeID("lease-stale")
	interval := 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, instID, leaseID, interval, 30*time.Second, noopLogger())
	}()

	// Wait for ticker to be registered.
	waitForOneTicker(t, fc)

	// First tick → ok=false → goroutine returns.
	fc.Advance(interval)
	wg.Wait() // goroutine should have exited

	callsAfterStale := hb.CallCount()
	if callsAfterStale == 0 {
		t.Error("expected at least one Heartbeat call")
	}

	// Advance more; goroutine has exited so ticker is stopped.
	fc.Advance(interval)
	fc.Advance(interval)
	// Small sleep to let any inadvertent goroutine run
	time.Sleep(10 * time.Millisecond)

	if got := hb.CallCount(); got != callsAfterStale {
		t.Errorf("after stale lease, more calls: before=%d after=%d", callsAfterStale, got)
	}

	if fc.PendingTickers() != 0 {
		t.Errorf("PendingTickers = %d after stale lease exit, want 0", fc.PendingTickers())
	}
}

// TestHeartbeat_TransientError verifies err logged but goroutine continues.
func TestHeartbeat_TransientError(t *testing.T) {
	fc := clockmock.New(time.Now())
	var callCount int32
	hb := &countingHeartbeater{count: &callCount, okReturn: true, errOnFirst: true}
	instID := idutil.SafeID("inst-transient")
	leaseID := idutil.SafeID("lease-transient")
	interval := 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, instID, leaseID, interval, 30*time.Second, noopLogger())
	}()

	// Wait for ticker registration.
	waitForOneTicker(t, fc)

	// First tick → error (but goroutine keeps going).
	fc.Advance(interval)
	waitForAtomicCount(t, &callCount, 1, 500*time.Millisecond)

	// Second tick → success.
	fc.Advance(interval)
	waitForAtomicCount(t, &callCount, 2, 500*time.Millisecond)

	if got := atomic.LoadInt32(&callCount); got < 2 {
		t.Errorf("transient error: total calls = %d, want >= 2", got)
	}

	cancel()
	wg.Wait()
	if fc.PendingTickers() != 0 {
		t.Error("leaked tickers after transient error test")
	}
}

// countingHeartbeater returns an error on the first call only.
type countingHeartbeater struct {
	count      *int32
	okReturn   bool
	errOnFirst bool
}

func (c *countingHeartbeater) Heartbeat(_ context.Context, _, _ idutil.SafeID, _ time.Duration) (bool, error) {
	n := atomic.AddInt32(c.count, 1)
	if c.errOnFirst && n == 1 {
		return false, fmt.Errorf("transient heartbeat error")
	}
	return c.okReturn, nil
}

// waitForCalls spins until hb.CallCount() >= want or deadline exceeded.
func waitForCalls(t *testing.T, hb *fakeHeartbeater, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if hb.CallCount() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for %d calls; got %d", want, hb.CallCount())
}

func waitForAtomicCount(t *testing.T, count *int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(count) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout: count = %d, want >= %d", atomic.LoadInt32(count), want)
}
