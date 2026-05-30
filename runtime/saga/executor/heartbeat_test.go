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
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// noStale is a no-op onStale callback for heartbeat tests that don't exercise
// the lease-lost cancellation path.
func noStale() {}

// noHBFailure is a no-op onHBFailure callback for heartbeat tests that don't
// assert observer fan-out. Tests that DO want to assert reason classification
// pass a closure recording reasons; see observer_test.go.
func noHBFailure(HeartbeatFailureReason) {}

// fakeHeartbeater is a test double that records Heartbeat calls. Each call also
// pulses beat (buffered) so tests can synchronize via testwait.Deterministic
// instead of polling — the channel send is the happens-before sync point after
// fc.Advance fires the ticker on a separate goroutine.
type fakeHeartbeater struct {
	mu        sync.Mutex
	calls     []heartbeatCall
	okReturn  bool
	errReturn error
	beat      chan struct{}
}

func newFakeHeartbeater(ok bool) *fakeHeartbeater {
	return &fakeHeartbeater{okReturn: ok, beat: make(chan struct{}, 16)}
}

type heartbeatCall struct {
	instanceID    idutil.SafeID
	leaseID       idutil.SafeID
	leaseDuration time.Duration
}

func (f *fakeHeartbeater) Heartbeat(_ context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, heartbeatCall{instanceID: instanceID, leaseID: leaseID, leaseDuration: leaseDuration})
	f.mu.Unlock()
	f.beat <- struct{}{}
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

// waitForOneTicker waits until FakeClock has at least 1 pending ticker. There is
// no channel signal for "the goroutine registered its ticker", so polling is
// unavoidable — the legitimate testwait.External carve-out.
func waitForOneTicker(t *testing.T, fc *clockmock.FakeClock) {
	t.Helper()
	testwait.External(t, "fakeclock-ticker-registration",
		func() bool { return fc.PendingTickers() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll,
		"goroutine did not register its heartbeat ticker")
}

// TestHeartbeat_TickerFiresOnAdvance verifies one Advance(interval) triggers one Heartbeat call.
func TestHeartbeat_TickerFiresOnAdvance(t *testing.T) {
	fc := clockmock.New(time.Now())
	hb := newFakeHeartbeater(true)
	instID := idutil.SafeID("inst-hb-1")
	leaseID := idutil.SafeID("lease-hb-1")
	interval := testtime.D10s
	leaseDur := testtime.D30s

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, heartbeatConfig{
			InstanceID:    instID,
			LeaseID:       leaseID,
			DefinitionID:  "def-x",
			Interval:      interval,
			LeaseDuration: leaseDur,
		}, noopLogger(), noStale, noHBFailure)
	}()

	// Wait for the goroutine to register its ticker.
	waitForOneTicker(t, fc)

	// Advance one interval: exactly one heartbeat.
	fc.Advance(interval)
	testwait.Deterministic(t, hb.beat, testtime.EventuallyShort, "heartbeat-tick-1")

	if got := hb.CallCount(); got != 1 {
		t.Errorf("after one Advance: CallCount = %d, want 1", got)
	}

	// Advance two more intervals.
	fc.Advance(interval)
	testwait.Deterministic(t, hb.beat, testtime.EventuallyShort, "heartbeat-tick-2")
	fc.Advance(interval)
	testwait.Deterministic(t, hb.beat, testtime.EventuallyShort, "heartbeat-tick-3")

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
	hb := newFakeHeartbeater(true)
	instID := idutil.SafeID("inst-noleak")
	leaseID := idutil.SafeID("lease-noleak")

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, heartbeatConfig{
			InstanceID:    instID,
			LeaseID:       leaseID,
			DefinitionID:  "def-x",
			Interval:      testtime.D5s,
			LeaseDuration: testtime.D30s,
		}, noopLogger(), noStale, noHBFailure)
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
	hb := newFakeHeartbeater(false) // stale from the start
	instID := idutil.SafeID("inst-stale")
	leaseID := idutil.SafeID("lease-stale")
	interval := testtime.D5s

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, heartbeatConfig{
			InstanceID:    instID,
			LeaseID:       leaseID,
			DefinitionID:  "def-x",
			Interval:      interval,
			LeaseDuration: testtime.D30s,
		}, noopLogger(), noStale, noHBFailure)
	}()

	// Wait for ticker to be registered.
	waitForOneTicker(t, fc)

	// First tick → ok=false → goroutine returns.
	fc.Advance(interval)
	testwait.Deterministic(t, hb.beat, testtime.EventuallyShort, "stale-first-tick")
	wg.Wait() // goroutine should have exited

	callsAfterStale := hb.CallCount()
	if callsAfterStale == 0 {
		t.Error("expected at least one Heartbeat call")
	}

	// Advance more; goroutine has exited so ticker is stopped. Because wg.Wait()
	// already established the goroutine is gone, advancing the (now-stopped)
	// clock deterministically produces no further calls — no sleep needed.
	fc.Advance(interval)
	fc.Advance(interval)

	if got := hb.CallCount(); got != callsAfterStale {
		t.Errorf("after stale lease, more calls: before=%d after=%d", callsAfterStale, got)
	}

	if fc.PendingTickers() != 0 {
		t.Errorf("PendingTickers = %d after stale lease exit, want 0", fc.PendingTickers())
	}
}

// TestHeartbeat_StaleLease_InvokesOnStale verifies that ok=false invokes the
// onStale callback (which the executor wires to cancel the running step's
// context — the C1/F4 lease-loss propagation).
func TestHeartbeat_StaleLease_InvokesOnStale(t *testing.T) {
	fc := clockmock.New(time.Now())
	hb := newFakeHeartbeater(false) // stale from the start
	instID := idutil.SafeID("inst-stale-cb")
	leaseID := idutil.SafeID("lease-stale-cb")
	interval := testtime.D5s

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	staleCh := make(chan struct{}, 1)
	onStale := func() { staleCh <- struct{}{} }

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, heartbeatConfig{
			InstanceID:    instID,
			LeaseID:       leaseID,
			DefinitionID:  "def-x",
			Interval:      interval,
			LeaseDuration: testtime.D30s,
		}, noopLogger(), onStale, noHBFailure)
	}()

	waitForOneTicker(t, fc)
	fc.Advance(interval)
	// onStale must fire on the stale tick. If it never fires (e.g. the ok=false
	// branch only logs+returns) this Deterministic times out → test fails.
	testwait.Deterministic(t, staleCh, testtime.EventuallyShort, "onstale-invoked")
	wg.Wait()
}

// TestHeartbeat_TransientError verifies err logged but goroutine continues.
func TestHeartbeat_TransientError(t *testing.T) {
	fc := clockmock.New(time.Now())
	hb := newCountingHeartbeater(true, true)
	instID := idutil.SafeID("inst-transient")
	leaseID := idutil.SafeID("lease-transient")
	interval := testtime.D5s

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, heartbeatConfig{
			InstanceID:    instID,
			LeaseID:       leaseID,
			DefinitionID:  "def-x",
			Interval:      interval,
			LeaseDuration: testtime.D30s,
		}, noopLogger(), noStale, noHBFailure)
	}()

	// Wait for ticker registration.
	waitForOneTicker(t, fc)

	// First tick → error (but goroutine keeps going).
	fc.Advance(interval)
	testwait.Deterministic(t, hb.beat, testtime.EventuallyShort, "transient-tick-1")

	// Second tick → success.
	fc.Advance(interval)
	testwait.Deterministic(t, hb.beat, testtime.EventuallyShort, "transient-tick-2")

	if got := atomic.LoadInt32(hb.count); got < 2 {
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
	beat       chan struct{}
}

func newCountingHeartbeater(ok, errOnFirst bool) *countingHeartbeater {
	var n int32
	return &countingHeartbeater{count: &n, okReturn: ok, errOnFirst: errOnFirst, beat: make(chan struct{}, 16)}
}

func (c *countingHeartbeater) Heartbeat(_ context.Context, _, _ idutil.SafeID, _ time.Duration) (bool, error) {
	n := atomic.AddInt32(c.count, 1)
	defer func() { c.beat <- struct{}{} }()
	if c.errOnFirst && n == 1 {
		return false, fmt.Errorf("transient heartbeat error")
	}
	return c.okReturn, nil
}
