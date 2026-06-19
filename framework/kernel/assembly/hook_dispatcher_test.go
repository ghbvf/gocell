package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
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
	goleak.VerifyTestMain(
		m,
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

func (c *spyCounter) Inc(ctx context.Context) { c.Add(ctx, 1) }
func (c *spyCounter) Add(_ context.Context, d float64) {
	c.parent.mu.Lock()
	c.parent.v[c.reason] += int(d)
	c.parent.mu.Unlock()
}

func (s *spyCounterVec) count(reason string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v[reason]
}

// Registered satisfies metrics.Collector for the shared vec interface.
func (s *spyCounterVec) Registered() bool { return true }

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

func (p *spyProvider) GaugeVec(_ metrics.GaugeOpts) (metrics.GaugeVec, error) {
	// Unused by the dispatcher but required for the Provider contract.
	return metrics.NopProvider{}.GaugeVec(metrics.GaugeOpts{})
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

func captureDefaultSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(&buf, nil)))
	return &buf
}

func requireLogRecord(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec), "bad log line %q", line)
		if rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("log record %q not found in logs:\n%s", msg, buf.String())
	return nil
}

func TestHookDispatcher_SlowSinkDoesNotBlockEmit(t *testing.T) {
	// A sink that hangs for 10s must not delay emit() more than a few ms.
	// The dispatcher must return immediately; only the observer goroutine
	// is blocked.
	bo := newBlockingObserver()
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: bo, QueueSize: 8, SinkTimeout: testtime.D10ms,
	})
	t.Cleanup(func() {
		bo.release()
		d.stop(context.Background(), testtime.D500ms)
	})

	start := time.Now()
	for range 5 {
		d.emit(cell.HookEvent{CellID: "slow", Hook: cell.HookBeforeStart})
	}
	assert.Less(t, time.Since(start), testtime.D100ms,
		"emit must be non-blocking even when the sink is hung")
}

func TestHookDispatcher_OverflowDropsAndCounts(t *testing.T) {
	// Use a long sink timeout (1s) so the blocked observer doesn't time
	// out during the test; we want to witness queue overflow, not sink
	// timeouts. emit spikes of many events into a tiny queue so at least
	// one DropReasonQueueFull is certain regardless of scheduler jitter.
	bo := newBlockingObserver()
	cv := newSpyCounterVec()
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: bo, QueueSize: 2, SinkTimeout: testtime.D1s, Provider: &spyProvider{cv: cv},
	})
	t.Cleanup(func() {
		bo.release()
		d.stop(context.Background(), testtime.D2s)
	})

	// Prime the pipeline: emit one event and wait until the worker has
	// started dispatching it, so the buffer is at steady state before we
	// drive overflow.
	d.emit(cell.HookEvent{CellID: "prime", Hook: cell.HookBeforeStart})
	testwait.External(t, "hook-observer-received", func() bool { return bo.received.Load() >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "primer event should reach observer")

	for range 100 {
		d.emit(cell.HookEvent{CellID: "overflow", Hook: cell.HookBeforeStart})
	}

	// With queue=2 + 1 blocked in flight, the remaining 97+ emit calls
	// cannot enqueue and must be counted as queue_full drops.
	assert.GreaterOrEqual(t, cv.count(DropReasonQueueFull), 1,
		"overflow must surface as queue_full drops")
}

func TestHookDispatcher_QueueFullDropLogsWarnFallback(t *testing.T) {
	buf := captureDefaultSlog(t)
	bo := newBlockingObserver()
	cv := newSpyCounterVec()
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: bo, QueueSize: 2, SinkTimeout: testtime.D1s, Provider: &spyProvider{cv: cv},
	})
	t.Cleanup(func() {
		bo.release()
		d.stop(context.Background(), testtime.D2s)
	})

	d.emit(cell.HookEvent{CellID: "prime", Hook: cell.HookBeforeStart})
	testwait.External(t, "hook-observer-received", func() bool { return bo.received.Load() >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "primer event should reach observer")

	for range 100 {
		d.emit(cell.HookEvent{CellID: "overflow", Hook: cell.HookBeforeStart})
	}
	require.GreaterOrEqual(t, cv.count(DropReasonQueueFull), 1,
		"overflow must surface as queue_full drops")

	rec := requireLogRecord(t, buf, "assembly: hook dispatcher queue full; dropping hook event")
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, DropReasonQueueFull, rec["reason"])
	assert.Equal(t, "overflow", rec["cell"])
	assert.Equal(t, string(cell.HookBeforeStart), rec["hook"])
}

func TestHookDispatcher_PerSinkTimeoutCountsAndContinues(t *testing.T) {
	bo := newBlockingObserver()
	cv := newSpyCounterVec()
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: bo, QueueSize: 8, SinkTimeout: testtime.D20ms, Provider: &spyProvider{cv: cv},
	})
	t.Cleanup(func() {
		bo.release()
		d.stop(context.Background(), testtime.D500ms)
	})

	d.emit(cell.HookEvent{CellID: "slow-sink", Hook: cell.HookBeforeStart})

	testwait.External(t, "hook-dispatcher-drop-counted", func() bool { return cv.count(DropReasonSinkTimeout) >= 1 },
		testtime.D200ms, testtime.FastPoll, "sink timeout must be counted")
}

// panicObserver panics on every OnHookEvent — simulates a buggy observer.
type panicObserver struct{}

func (panicObserver) OnHookEvent(cell.HookEvent) { panic("sink crashed") }

type panicValueObserver struct {
	value any
}

func (p panicValueObserver) OnHookEvent(cell.HookEvent) { panic(p.value) }

func TestHookDispatcher_PanicIsCountedAndIsolated(t *testing.T) {
	cv := newSpyCounterVec()
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer:    panicObserver{},
		QueueSize:   8,
		SinkTimeout: testtime.D1s,
		Provider:    &spyProvider{cv: cv},
	})
	t.Cleanup(func() { d.stop(context.Background(), testtime.D500ms) })

	d.emit(cell.HookEvent{CellID: "crash", Hook: cell.HookBeforeStart})
	require.True(t, d.flush(testtime.D500ms), "flush should succeed even after sink panic")

	assert.Equal(t, 1, cv.count(DropReasonObserverPanic),
		"observer panic should be counted")

	// Subsequent events must still be delivered via dispatchOne (the panic
	// is contained in the per-event goroutine).
	d.emit(cell.HookEvent{CellID: "after-crash", Hook: cell.HookBeforeStart})
	require.True(t, d.flush(testtime.D500ms))
	assert.Equal(t, 2, cv.count(DropReasonObserverPanic),
		"subsequent events continue to be dispatched (and continue to panic)")
}

func TestHookDispatcher_ObserverPanicLogValueIsRedactedAndTruncated(t *testing.T) {
	buf := captureDefaultSlog(t)
	cv := newSpyCounterVec()
	secret := "super-secret-token-value"
	tail := "panic-tail-must-not-appear"
	panicValue := "observer panic token=" + secret + " " + strings.Repeat("x", 300) + tail
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer:    panicValueObserver{value: panicValue},
		QueueSize:   8,
		SinkTimeout: testtime.D1s,
		Provider:    &spyProvider{cv: cv},
	})
	t.Cleanup(func() { d.stop(context.Background(), testtime.D500ms) })

	d.emit(cell.HookEvent{CellID: "panic-redaction", Hook: cell.HookAfterStop})
	require.True(t, d.flush(testtime.D500ms), "flush should succeed after sink panic")
	require.Equal(t, 1, cv.count(DropReasonObserverPanic),
		"observer panic should still be counted")

	rec := requireLogRecord(t, buf, "lifecycle: hook observer panicked")
	got, ok := rec["panic"].(string)
	require.Truef(t, ok, "panic log field must be a string, got %T", rec["panic"])
	panicType, ok := rec["panic_type"].(string)
	require.Truef(t, ok, "panic_type log field must be a string, got %T", rec["panic_type"])
	assert.Equal(t, "string", panicType)
	assert.LessOrEqual(t, len(got), maxHookObserverPanicLogBytes)
	assert.Contains(t, got, redaction.Mask)
	assert.NotContains(t, got, secret)
	assert.NotContains(t, got, tail)
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
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: obs, QueueSize: 32, SinkTimeout: testtime.D1s,
	})

	for i := range 10 {
		d.emit(cell.HookEvent{CellID: "drain", Hook: cell.HookBeforeStart, Duration: time.Duration(i)})
	}
	d.stop(context.Background(), testtime.D2s)

	assert.Equal(t, 10, obs.len(), "stop(drainTimeout) must drain all in-flight events")
}

func TestHookDispatcher_StopWaitsForTimedOutSinkBeforeReturning(t *testing.T) {
	clk := clockmock.New(time.Time{})
	bo := newBlockingObserver()
	cv := newSpyCounterVec()
	d := newHookDispatcher(clk, dispatcherConfig{
		Observer: bo, QueueSize: 8, SinkTimeout: testtime.D1s, Provider: &spyProvider{cv: cv},
	})
	t.Cleanup(func() {
		bo.release()
		d.stop(context.Background(), testtime.D10s)
	})

	d.emit(cell.HookEvent{CellID: "slow-drain", Hook: cell.HookBeforeStart})
	testwait.External(t, "hook-observer-received", func() bool { return bo.received.Load() >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "event should reach observer")
	clk.Advance(testtime.D1s)
	testwait.External(t, "hook-dispatcher-drop-counted", func() bool { return cv.count(DropReasonSinkTimeout) >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "sink timeout must be counted")

	stopDone := make(chan struct{})
	go func() {
		d.stop(context.Background(), testtime.D10s)
		close(stopDone)
	}()
	testwait.External(t, "hook-dispatcher-done-drained", func() bool {
		select {
		case <-d.done:
			return true
		default:
			return false
		}
	}, testtime.EventuallyDefault, testtime.FastPoll, "worker should drain before sink wait assertion")

	select {
	case <-stopDone:
		t.Fatal("stop returned before the timed-out observer sink exited")
	case <-time.After(testtime.ShortSleep):
	}

	bo.release()
	select {
	case <-stopDone:
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("stop did not return after observer sink exited")
	}
}

func TestHookDispatcher_StopDoesNotHangForeverOnStuckSink(t *testing.T) {
	clk := clockmock.New(time.Time{})
	bo := newBlockingObserver()
	cv := newSpyCounterVec()
	d := newHookDispatcher(clk, dispatcherConfig{
		Observer: bo, QueueSize: 8, SinkTimeout: testtime.D1s, Provider: &spyProvider{cv: cv},
	})
	t.Cleanup(bo.release)

	d.emit(cell.HookEvent{CellID: "stuck-drain", Hook: cell.HookBeforeStart})
	testwait.External(t, "hook-observer-received", func() bool { return bo.received.Load() >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "event should reach observer")
	clk.Advance(testtime.D1s)
	testwait.External(t, "hook-dispatcher-drop-counted", func() bool { return cv.count(DropReasonSinkTimeout) >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "sink timeout must be counted")

	stopDone := make(chan struct{})
	go func() {
		d.stop(context.Background(), testtime.D2s)
		close(stopDone)
	}()
	testwait.External(t, "hook-handler-finalized", func() bool { return clk.PendingTimers() >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "stop should wait on the remaining drain budget")

	select {
	case <-stopDone:
		t.Fatal("stop returned before the drain budget elapsed")
	default:
	}
	clk.Advance(testtime.D2s)
	select {
	case <-stopDone:
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("stop did not return after drain budget elapsed")
	}
	bo.release()
}

func TestHookDispatcher_StopReturnsWhenContextCanceledDuringSinkDrain(t *testing.T) {
	clk := clockmock.New(time.Time{})
	bo := newBlockingObserver()
	cv := newSpyCounterVec()
	d := newHookDispatcher(clk, dispatcherConfig{
		Observer: bo, QueueSize: 8, SinkTimeout: testtime.D1s, Provider: &spyProvider{cv: cv},
	})
	t.Cleanup(bo.release)

	d.emit(cell.HookEvent{CellID: "ctx-drain", Hook: cell.HookBeforeStart})
	testwait.External(t, "hook-observer-received", func() bool { return bo.received.Load() >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "event should reach observer")
	clk.Advance(testtime.D1s)
	testwait.External(t, "hook-dispatcher-drop-counted", func() bool { return cv.count(DropReasonSinkTimeout) >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "sink timeout must be counted")

	ctx, cancel := context.WithCancel(context.Background())
	stopDone := make(chan struct{})
	go func() {
		d.stop(ctx, testtime.D10s)
		close(stopDone)
	}()
	testwait.External(t, "hook-dispatcher-done-drained", func() bool {
		select {
		case <-d.done:
			return true
		default:
			return false
		}
	}, testtime.EventuallyDefault, testtime.FastPoll, "worker should drain before sink wait assertion")

	select {
	case <-stopDone:
		t.Fatal("stop returned before sink completion or context cancellation")
	default:
	}
	cancel()
	select {
	case <-stopDone:
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("stop did not return after context cancellation")
	}

	bo.release()
	testwait.External(t, "hook-dispatcher-sink-idle", func() bool {
		select {
		case <-d.currentSinkIdle():
			return true
		default:
			return false
		}
	}, testtime.EventuallyDefault, testtime.FastPoll, "observer sink should exit after release")
}

func TestHookDispatcher_StopIsIdempotent(t *testing.T) {
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: cell.NopHookObserver{}, QueueSize: 4, SinkTimeout: testtime.D1s,
	})
	d.stop(context.Background(), testtime.D200ms)
	d.stop(context.Background(), testtime.D200ms) // second call must be a no-op, no panic
}

func TestHookDispatcher_FlushOnIdleReturnsTrue(t *testing.T) {
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: cell.NopHookObserver{}, QueueSize: 8, SinkTimeout: testtime.D1s,
	})
	t.Cleanup(func() { d.stop(context.Background(), testtime.D200ms) })

	require.True(t, d.flush(testtime.D500ms), "flush on idle dispatcher must succeed")
}

// TestHookDispatcher_EmitAfterStopCountsQueueFull pins the recovery
// branch in emit(): sending on a closed channel panics at runtime (not
// "selected with default" — the send-case fires before default when
// selected), and that panic must be converted into a queue_full drop so
// a post-Stop caller never crashes the assembly.
func TestHookDispatcher_EmitAfterStopCountsQueueFull(t *testing.T) {
	buf := captureDefaultSlog(t)
	cv := newSpyCounterVec()
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: cell.NopHookObserver{}, QueueSize: 4,
		SinkTimeout: testtime.D1s, Provider: &spyProvider{cv: cv},
	})

	d.stop(context.Background(), testtime.D200ms)
	// At least one emit after stop must still not panic; drop is counted.
	d.emit(cell.HookEvent{CellID: "after-stop", Hook: cell.HookBeforeStart})

	assert.GreaterOrEqual(t, cv.count(DropReasonQueueFull), 1,
		"emit after stop must land in queue_full drop counter")
	rec := requireLogRecord(t, buf, "assembly: hook dispatcher queue full; dropping hook event")
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, DropReasonQueueFull, rec["reason"])
	assert.Equal(t, "after-stop", rec["cell"])
	assert.Equal(t, string(cell.HookBeforeStart), rec["hook"])
}

// TestHookDispatcher_FlushAfterStopIsNoOpSuccess locks in the
// "send-on-closed treated as flush success" branch of flush(). Intent:
// once the dispatcher has stopped accepting events, any further fence is
// a no-op for callers that only need a stable stopped state. This does
// not assert that a timed-out stop drained observer sinks; sink drain is
// covered by the dedicated stop tests above.
func TestHookDispatcher_FlushAfterStopIsNoOpSuccess(t *testing.T) {
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: cell.NopHookObserver{}, QueueSize: 4, SinkTimeout: testtime.D1s,
	})
	d.stop(context.Background(), testtime.D200ms)

	require.True(t, d.flush(testtime.D200ms),
		"flush after stop must return true (dispatcher stopped accepting fences)")
}

// TestHookDispatcher_FlushTimeoutThenSuccess exercises the subtle case
// where a flush call times out while the fence sits in the queue, then a
// later flush sees the dispatcher catch up and returns true. Pins the
// shared-timer behavior documented in flush().
func TestHookDispatcher_FlushTimeoutThenSuccess(t *testing.T) {
	bo := newBlockingObserver()
	d := newHookDispatcher(clock.Real(), dispatcherConfig{
		Observer: bo, QueueSize: 2, SinkTimeout: testtime.D1s,
	})
	t.Cleanup(func() {
		bo.release()
		d.stop(context.Background(), testtime.D500ms)
	})

	d.emit(cell.HookEvent{CellID: "slow", Hook: cell.HookBeforeStart})
	testwait.External(t, "hook-observer-received", func() bool { return bo.received.Load() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll, "worker should pick up the primed event")

	// Worker is blocked on the sink; flush with a 10ms budget cannot
	// reach the fence in time.
	if d.flush(testtime.D10ms) {
		t.Fatal("flush with insufficient budget should return false")
	}

	bo.release()
	// Now the worker unblocks; a generous flush must succeed.
	require.True(t, d.flush(testtime.D2s),
		"flush after sink release should succeed")
}

// assemblyHookFlush_IntegrationTest exercises the assembly-level contract:
// after a.Stop(), the HookObserver must have received every event (no race
// on process exit).
func TestCoreAssembly_StopDrainsDispatcher(t *testing.T) {
	obs := &collectObserver{}
	a := newTestAssembly(t, clock.Real(), Config{
		ID:             "drain-test",
		DurabilityMode: outbox.DurabilityDemo,
		HookObserver:   obs,
	})
	require.NoError(t, a.Register(newHookOrderCell("A", new([]string), "")))
	require.NoError(t, a.Start(context.Background()))
	require.NoError(t, a.Stop(context.Background()))

	// No extra flush needed: Stop() must drain internally.
	assert.GreaterOrEqual(t, obs.len(), 4,
		"Stop must drain before returning (4 hook events minimum: BeforeStart, AfterStart, BeforeStop, AfterStop)")
}

func TestCoreAssembly_RestartRebuildsHookDispatcher(t *testing.T) {
	obs := &collectObserver{}
	a := newTestAssembly(t, clock.Real(), Config{
		ID:             "restart-dispatcher-test",
		DurabilityMode: outbox.DurabilityDemo,
		HookObserver:   obs,
	})
	require.NoError(t, a.Register(newHookOrderCell("A", new([]string), "")))

	require.NoError(t, a.Start(context.Background()))
	firstDispatcher := a.currentDispatcher()
	require.NotNil(t, firstDispatcher)
	require.NoError(t, a.Stop(context.Background()))
	require.Equal(t, 4, obs.len(), "first lifecycle must deliver all hook events before Stop returns")
	require.Nil(t, a.currentDispatcher(), "Stop must retire the one-shot dispatcher")

	require.NoError(t, a.Start(context.Background()))
	secondDispatcher := a.currentDispatcher()
	require.NotNil(t, secondDispatcher)
	if firstDispatcher == secondDispatcher {
		t.Fatal("restart must create a fresh dispatcher")
	}
	require.True(t, a.FlushHookEvents(testtime.D500ms), "restarted dispatcher must accept flush fences")
	require.Equal(t, 6, obs.len(), "second Start must deliver hook events through the new dispatcher")

	require.NoError(t, a.Stop(context.Background()))
	require.Equal(t, 8, obs.len(), "second Stop must drain hook events before returning")
}

func TestCoreAssembly_StopContextCancelsDispatcherDrain(t *testing.T) {
	clk := clockmock.New(time.Time{})
	obs := newBlockingObserver()
	a := newTestAssembly(t, clk, Config{
		ID:                       "ctx-drain-test",
		DurabilityMode:           outbox.DurabilityDemo,
		HookObserver:             obs,
		HookObserverSinkTimeout:  testtime.D10s,
		HookObserverDrainTimeout: testtime.D10s,
	})
	var calls []string
	require.NoError(t, a.Register(newHookOrderCell("A", &calls, "")))
	require.NoError(t, a.Start(context.Background()))
	testwait.External(t, "hook-observer-received", func() bool { return obs.received.Load() >= 1 },
		testtime.EventuallyDefault, testtime.FastPoll, "start hook event should reach observer")
	dispatcher := a.currentDispatcher()
	require.NotNil(t, dispatcher)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- a.Stop(ctx)
	}()
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("Stop(ctx) waited for HookObserverDrainTimeout despite canceled context")
	}

	obs.release()
	testwait.External(t, "hook-dispatcher-done-drained", func() bool {
		select {
		case <-dispatcher.done:
			return true
		default:
			return false
		}
	}, testtime.EventuallyDefault, testtime.FastPoll, "dispatcher worker should exit after observer release")
}
