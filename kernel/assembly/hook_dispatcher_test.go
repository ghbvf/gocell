package assembly

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestMain installs a goleak guard so any hook-dispatcher goroutine that
// fails to exit at test teardown surfaces as a red test, not as silent
// background rot that accumulates across the suite. Slow-sink scenarios
// legitimately abandon per-event goroutines; those are captured inside
// each test's scope via t.Cleanup and must finish before the test returns.
//
// ref: go.uber.org/goleak README@main — VerifyTestMain is the canonical
// last-resort guard against goroutine leaks in Go.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// stdlib http keep-alive loops that adapter tests spin up can linger
		// briefly on fast shutdown; explicit allowlist keeps the main assembly
		// coverage strict.
		goleak.IgnoreTopFunction("net/http.(*Transport).dialConnFor"),
		// OBS-LEAK-02 closed: every `New(Config{…})` call site in this
		// package now goes through newTestAssembly(t, …) which registers
		// `t.Cleanup(a.Shutdown)` — the dispatcher worker goroutine is
		// drained on every test teardown, so no blanket ignore is needed.
	)
}

// spyCounterVec records per-reason increments for drop-counter assertions.
// Defined here (same package) so tests can inspect internal state without
// widening the export surface.
type spyCounterVec struct {
	mu sync.Mutex
	v  map[string]int
}

func newSpyCounterVec() *spyCounterVec { return &spyCounterVec{v: map[string]int{}} }

func (s *spyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels([]string{"reason"}, l)
	return &spyCounter{parent: s, reason: l["reason"]}
}

type spyCounter struct {
	parent *spyCounterVec
	reason string
}

func (c *spyCounter) Inc() { c.Add(1) }
func (c *spyCounter) Add(d float64) {
	c.parent.mu.Lock()
	c.parent.v[c.reason] += int(d)
	c.parent.mu.Unlock()
}

func (s *spyCounterVec) count(reason string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v[reason]
}

// spyProvider satisfies metrics.Provider and returns a captured CounterVec
// so tests can assert on drop counts.
type spyProvider struct {
	cv *spyCounterVec
}

func (p *spyProvider) CounterVec(_ metrics.CounterOpts) (metrics.CounterVec, error) {
	return p.cv, nil
}

func (p *spyProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	// Unused by the dispatcher but required for the Provider contract.
	return metrics.NopProvider{}.HistogramVec(metrics.HistogramOpts{})
}

// blockingObserver blocks on OnHookEvent until the test calls release().
// Used to exercise slow-sink scenarios deterministically. release() is
// idempotent so tests can both explicitly release AND register release
// as a cleanup without a double-close panic.
type blockingObserver struct {
	received    atomic.Int32
	gate        chan struct{}
	releaseOnce sync.Once
}

func newBlockingObserver() *blockingObserver {
	return &blockingObserver{gate: make(chan struct{})}
}

func (b *blockingObserver) OnHookEvent(cell.HookEvent) {
	b.received.Add(1)
	<-b.gate
}

func (b *blockingObserver) release() {
	b.releaseOnce.Do(func() { close(b.gate) })
}

func TestHookDispatcher_SlowSinkDoesNotBlockEmit(t *testing.T) {
	// A sink that hangs for 10s must not delay emit() more than a few ms.
	// The dispatcher must return immediately; only the observer goroutine
	// is blocked.
	bo := newBlockingObserver()
	d, err := newHookDispatcher(dispatcherConfig{Observer: bo, QueueSize: 8, SinkTimeout: 10 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() {
		bo.release()
		d.stop(500 * time.Millisecond)
	})

	start := time.Now()
	for range 5 {
		d.emit(cell.HookEvent{CellID: "slow", Hook: cell.HookBeforeStart})
	}
	assert.Less(t, time.Since(start), 100*time.Millisecond,
		"emit must be non-blocking even when the sink is hung")
}

func TestHookDispatcher_OverflowDropsAndCounts(t *testing.T) {
	// Use a long sink timeout (1s) so the blocked observer doesn't time
	// out during the test; we want to witness queue overflow, not sink
	// timeouts. emit spikes of many events into a tiny queue so at least
	// one DropReasonQueueFull is certain regardless of scheduler jitter.
	bo := newBlockingObserver()
	cv := newSpyCounterVec()
	d, err := newHookDispatcher(dispatcherConfig{Observer: bo, QueueSize: 2, SinkTimeout: time.Second, Provider: &spyProvider{cv: cv}})
	require.NoError(t, err)
	t.Cleanup(func() {
		bo.release()
		d.stop(2 * time.Second)
	})

	// Prime the pipeline: emit one event and wait until the worker has
	// started dispatching it, so the buffer is at steady state before we
	// drive overflow.
	d.emit(cell.HookEvent{CellID: "prime", Hook: cell.HookBeforeStart})
	require.Eventually(t, func() bool { return bo.received.Load() >= 1 },
		2*time.Second, 5*time.Millisecond, "primer event should reach observer")

	for range 100 {
		d.emit(cell.HookEvent{CellID: "overflow", Hook: cell.HookBeforeStart})
	}

	// With queue=2 + 1 blocked in flight, the remaining 97+ emit calls
	// cannot enqueue and must be counted as queue_full drops.
	assert.GreaterOrEqual(t, cv.count(DropReasonQueueFull), 1,
		"overflow must surface as queue_full drops")
}

func TestHookDispatcher_PerSinkTimeoutCountsAndContinues(t *testing.T) {
	bo := newBlockingObserver()
	cv := newSpyCounterVec()
	d, err := newHookDispatcher(dispatcherConfig{Observer: bo, QueueSize: 8, SinkTimeout: 20 * time.Millisecond, Provider: &spyProvider{cv: cv}})
	require.NoError(t, err)
	t.Cleanup(func() {
		bo.release()
		d.stop(500 * time.Millisecond)
	})

	d.emit(cell.HookEvent{CellID: "slow-sink", Hook: cell.HookBeforeStart})

	require.Eventually(t, func() bool { return cv.count(DropReasonSinkTimeout) >= 1 },
		200*time.Millisecond, 5*time.Millisecond, "sink timeout must be counted")
}

// panicObserver panics on every OnHookEvent — simulates a buggy observer.
type panicObserver struct{}

func (panicObserver) OnHookEvent(cell.HookEvent) { panic("sink crashed") }

func TestHookDispatcher_PanicIsCountedAndIsolated(t *testing.T) {
	cv := newSpyCounterVec()
	d, err := newHookDispatcher(dispatcherConfig{Observer: panicObserver{}, QueueSize: 8, SinkTimeout: time.Second, Provider: &spyProvider{cv: cv}})
	require.NoError(t, err)
	t.Cleanup(func() { d.stop(500 * time.Millisecond) })

	d.emit(cell.HookEvent{CellID: "crash", Hook: cell.HookBeforeStart})
	require.True(t, d.flush(500*time.Millisecond), "flush should succeed even after sink panic")

	assert.Equal(t, 1, cv.count(DropReasonObserverPanic),
		"observer panic should be counted")

	// Subsequent events must still be delivered via dispatchOne (the panic
	// is contained in the per-event goroutine).
	d.emit(cell.HookEvent{CellID: "after-crash", Hook: cell.HookBeforeStart})
	require.True(t, d.flush(500*time.Millisecond))
	assert.Equal(t, 2, cv.count(DropReasonObserverPanic),
		"subsequent events continue to be dispatched (and continue to panic)")
}

// collectObserver records events for drain-on-stop verification.
type collectObserver struct {
	mu  sync.Mutex
	got []cell.HookEvent
}

func (c *collectObserver) OnHookEvent(e cell.HookEvent) {
	c.mu.Lock()
	c.got = append(c.got, e)
	c.mu.Unlock()
}

func (c *collectObserver) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

func TestHookDispatcher_StopDrainsPending(t *testing.T) {
	obs := &collectObserver{}
	d, err := newHookDispatcher(dispatcherConfig{Observer: obs, QueueSize: 32, SinkTimeout: time.Second})
	require.NoError(t, err)

	for i := range 10 {
		d.emit(cell.HookEvent{CellID: "drain", Hook: cell.HookBeforeStart, Duration: time.Duration(i)})
	}
	d.stop(2 * time.Second)

	assert.Equal(t, 10, obs.len(), "stop(drainTimeout) must drain all in-flight events")
}

func TestHookDispatcher_StopIsIdempotent(t *testing.T) {
	d, err := newHookDispatcher(dispatcherConfig{Observer: cell.NopHookObserver{}, QueueSize: 4, SinkTimeout: time.Second})
	require.NoError(t, err)
	d.stop(200 * time.Millisecond)
	d.stop(200 * time.Millisecond) // second call must be a no-op, no panic
}

func TestHookDispatcher_FlushOnIdleReturnsTrue(t *testing.T) {
	d, err := newHookDispatcher(dispatcherConfig{Observer: cell.NopHookObserver{}, QueueSize: 8, SinkTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { d.stop(200 * time.Millisecond) })

	require.True(t, d.flush(500*time.Millisecond), "flush on idle dispatcher must succeed")
}

// TestHookDispatcher_EmitAfterStopCountsQueueFull pins the recovery
// branch in emit(): sending on a closed channel panics at runtime (not
// "selected with default" — the send-case fires before default when
// selected), and that panic must be converted into a queue_full drop so
// a post-Stop caller never crashes the assembly.
func TestHookDispatcher_EmitAfterStopCountsQueueFull(t *testing.T) {
	cv := newSpyCounterVec()
	d, err := newHookDispatcher(dispatcherConfig{Observer: cell.NopHookObserver{}, QueueSize: 4, SinkTimeout: time.Second, Provider: &spyProvider{cv: cv}})
	require.NoError(t, err)

	d.stop(200 * time.Millisecond)
	// At least one emit after stop must still not panic; drop is counted.
	d.emit(cell.HookEvent{CellID: "after-stop", Hook: cell.HookBeforeStart})

	assert.GreaterOrEqual(t, cv.count(DropReasonQueueFull), 1,
		"emit after stop must land in queue_full drop counter")
}

// TestHookDispatcher_FlushAfterStopReturnsTrue locks in the
// "send-on-closed treated as flush success" branch of flush(). Intent:
// once the dispatcher has been stopped, the channel is fully drained, so
// any further fence is semantically satisfied (there is nothing to wait
// for). Returning false here would make clean-shutdown callers block or
// retry for no gain.
func TestHookDispatcher_FlushAfterStopReturnsTrue(t *testing.T) {
	d, err := newHookDispatcher(dispatcherConfig{Observer: cell.NopHookObserver{}, QueueSize: 4, SinkTimeout: time.Second})
	require.NoError(t, err)
	d.stop(200 * time.Millisecond)

	require.True(t, d.flush(200*time.Millisecond),
		"flush after stop must return true (channel drained, fence is trivially satisfied)")
}

// TestHookDispatcher_FlushTimeoutThenSuccess exercises the subtle case
// where a flush call times out while the fence sits in the queue, then a
// later flush sees the dispatcher catch up and returns true. Pins the
// shared-timer behaviour documented in flush().
func TestHookDispatcher_FlushTimeoutThenSuccess(t *testing.T) {
	bo := newBlockingObserver()
	d, err := newHookDispatcher(dispatcherConfig{Observer: bo, QueueSize: 2, SinkTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() {
		bo.release()
		d.stop(500 * time.Millisecond)
	})

	d.emit(cell.HookEvent{CellID: "slow", Hook: cell.HookBeforeStart})
	require.Eventually(t, func() bool { return bo.received.Load() >= 1 },
		time.Second, 5*time.Millisecond, "worker should pick up the primed event")

	// Worker is blocked on the sink; flush with a 10ms budget cannot
	// reach the fence in time.
	if d.flush(10 * time.Millisecond) {
		t.Fatal("flush with insufficient budget should return false")
	}

	bo.release()
	// Now the worker unblocks; a generous flush must succeed.
	require.True(t, d.flush(2*time.Second),
		"flush after sink release should succeed")
}

// assemblyHookFlush_IntegrationTest exercises the assembly-level contract:
// after a.Stop(), the HookObserver must have received every event (no race
// on process exit).
func TestCoreAssembly_StopDrainsDispatcher(t *testing.T) {
	obs := &collectObserver{}
	a := newTestAssembly(t, Config{
		ID:             "drain-test",
		DurabilityMode: cell.DurabilityDemo,
		HookObserver:   obs,
	})
	require.NoError(t, a.Register(newHookOrderCell("A", new([]string), "")))
	require.NoError(t, a.Start(context.Background()))
	require.NoError(t, a.Stop(context.Background()))

	// No extra flush needed: Stop() must drain internally.
	assert.GreaterOrEqual(t, obs.len(), 4,
		"Stop must drain before returning (4 hook events minimum: BeforeStart, AfterStart, BeforeStop, AfterStop)")
}
