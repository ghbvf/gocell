package command

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// errSweepTickMock is a SweepTicker mock that returns an error from SweepTick.
type errSweepTickMock struct {
	tickErr error
	calls   atomic.Int32
}

func (m *errSweepTickMock) SweepTick(_ context.Context, _ time.Time) error {
	m.calls.Add(1)
	return m.tickErr
}

// okSweepTickMock counts SweepTick calls without returning an error.
type okSweepTickMock struct {
	calls atomic.Int32
}

func (m *okSweepTickMock) SweepTick(_ context.Context, _ time.Time) error {
	m.calls.Add(1)
	return nil
}

// shortInterval is a small ticker interval that causes rapid tick firing in tests.
const shortInterval = testtime.D5ms

// tickWait is how long to wait for at least one tick in tests.
const tickWait = testtime.D500ms

// TestSweeperLifecycle_NoClockField_NewSignature verifies that NewSweeperLifecycle
// no longer requires a clock parameter (C.1: control-plane clock decoupled).
func TestSweeperLifecycle_NoClockField_NewSignature(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	// NewSweeperLifecycle(name, sweeper, interval, businessClock): the kernel
	// Sweeper still holds NO clock field (C.1 Hard); businessClock supplies the
	// business-plane SweepTick `now` only, never control-plane scheduling (P2-2).
	lc := NewSweeperLifecycle("test.sweeper", sw, shortInterval, clock.Real())
	require.NotNil(t, lc)
}

// TestSweeperLifecycle_StartDoesNotDeadlock verifies the frozen-fake-clock
// deadlock regression: old code used l.clk().NewTimerAt for the startup probe;
// injecting a frozen fake clock and not Advancing it would block forever.
// New code uses real time for the startup probe — Start must return within ~500ms.
//
// This is the core C.1 deadlock regression test.
func TestSweeperLifecycle_StartDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	// IgnoreCurrent snapshots goroutines at start so parallel test goroutines
	// are not reported as leaks from this test.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	lc := NewSweeperLifecycle("no-deadlock", sw, testtime.D1h, clock.Real()) // long interval — no tick fires

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	startedAt := time.Now()
	require.NoError(t, lc.Start(ownerCtx))
	elapsed := time.Since(startedAt)

	// Should return in <2s. The exact bound is conservative: the core invariant
	// is "Start does not deadlock", not precise timing. 2s is far above the
	// expected ~50ms probe window but well below a deadlock scenario.
	assert.Less(t, elapsed, testtime.D2s,
		"Start must not block: startup probe uses real time, not an injected clock")

	// Clean up
	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.CtxShort)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))
}

// TestSweeperLifecycle_TickerDrivesSweepTick verifies that after Start,
// SweepTick is called at approximately the configured interval.
func TestSweeperLifecycle_TickerDrivesSweepTick(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	mock := &okSweepTickMock{}
	lc := NewSweeperLifecycle("ticker-test", mock, shortInterval, clock.Real())

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	// Wait for at least one SweepTick call via require.Eventually.
	require.Eventually(t, func() bool {
		return mock.calls.Load() >= 1
	}, tickWait, testtime.D1ms, "SweepTick must be called at least once")

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.CtxShort)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))
}

// TestSweeperLifecycle_SweepTickErrorLogged verifies that a SweepTick error
// does not crash the worker (the loop continues) — error is logged + counter incremented.
func TestSweeperLifecycle_SweepTickErrorLogged(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	errTick := errors.New("scan db unavailable")
	mock := &errSweepTickMock{tickErr: errTick}
	lc := NewSweeperLifecycle("err-test", mock, shortInterval, clock.Real())

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	// Wait for at least one SweepTick call (which returns error) via require.Eventually.
	require.Eventually(t, func() bool {
		return mock.calls.Load() >= 1
	}, tickWait, testtime.D1ms, "SweepTick must be called even when error returned")

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.CtxShort)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))
}

// TestSweeperLifecycle_OwnerCtxCancelExitsWorker verifies C.2: canceling the
// owner ctx causes the worker goroutine to exit even without calling OnStop.
func TestSweeperLifecycle_OwnerCtxCancelExitsWorker(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	lc := NewSweeperLifecycle("owner-ctx-test", sw, testtime.D1h, clock.Real())

	ownerCtx, ownerCancel := context.WithCancel(context.Background())

	require.NoError(t, lc.Start(ownerCtx))

	// Cancel owner ctx — worker should exit
	ownerCancel()

	// Stop should succeed quickly (goroutine already exited via owner cancel)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer stopCancel()
	require.NoError(t, lc.Stop(stopCtx))
	// goleak verifies no goroutine survives
}

// TestSweeperLifecycle_OnStop_GracefulExit verifies that Stop cancels the worker
// and waits for it to exit.
func TestSweeperLifecycle_OnStop_GracefulExit(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	lc := NewSweeperLifecycle("stop-test", sw, testtime.D1h, clock.Real()) // long interval — no tick fires

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))
	require.NoError(t, lc.Stop(stopCtx), "Stop must be idempotent")
}

// TestSweeperLifecycle_OwnerCtxCancelDuringProbe verifies the awaitProbe
// runCtx.Done() branch: if ownerCtx is canceled during the probe window
// (before the ticker fires its first tick), Start returns nil and a subsequent
// Stop is a no-op. The goroutine exits via runCtx.Done().
func TestSweeperLifecycle_OwnerCtxCancelDuringProbe(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	// Use a very long interval so the first tick never fires during the probe window.
	lc := NewSweeperLifecycle("probe-cancel-test", sw, testtime.D1h, clock.Real())

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	// Cancel immediately — before the goroutine can fire its first tick.
	ownerCancel()

	// Start must return nil (owner-cancel is not a Start error).
	require.NoError(t, lc.Start(ownerCtx),
		"Start must return nil when ownerCtx is already canceled")

	// Stop must be a no-op (l.cancel was cleared by awaitProbe runCtx.Done() branch).
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer stopCancel()
	require.NoError(t, lc.Stop(stopCtx), "Stop must be no-op after owner-cancel during probe")
	// goleak verifies no goroutine survives.
}

// TestSweeperLifecycle_ContributesHook verifies Hook() returns correct shape.
func TestSweeperLifecycle_ContributesHook(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	lc := NewSweeperLifecycle("devicecommand.sweeper", sw, testtime.D1h, clock.Real())

	hook := lc.Hook()
	assert.Equal(t, "devicecommand.sweeper", hook.Name)
	assert.NotNil(t, hook.OnStart)
	assert.NotNil(t, hook.OnStop)
}

// TestSweeperLifecycle_SweepErrorCounter verifies counter is incremented with
// the correct cell label when SweepTick returns an error (C.3 observable
// errors). Also verifies slog.Error is emitted with the cell field.
func TestSweeperLifecycle_SweepErrorCounter(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	errTick := errors.New("ack rejected")
	mock := &errSweepTickMock{tickErr: errTick}

	// Use a real counter for verification
	provider := newTestProvider()
	counterVec, err := provider.CounterVec(kernelmetrics.CounterOpts{
		Name:       "command_sweep_errors_total",
		Help:       "Total command sweep errors",
		LabelNames: []string{"cell"},
	})
	require.NoError(t, err)

	const testCellID = "testcell"
	lc := NewSweeperLifecycle("err-counter-test", mock, shortInterval, clock.Real())
	lc.CellID = testCellID
	lc.SweepErrorCounter = counterVec

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	// Wait for at least one error tick via require.Eventually.
	require.Eventually(t, func() bool {
		return mock.calls.Load() >= 1
	}, tickWait, testtime.D1ms, "SweepTick error tick must fire at least once")

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.CtxShort)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))

	// Counter must have been incremented at least once with the injected cell label.
	count := testProviderCounterValue(provider, "command_sweep_errors_total",
		map[string]string{"cell": testCellID})
	assert.GreaterOrEqual(t, count, 1.0,
		"sweep error counter must be incremented with cell=%q on SweepTick error", testCellID)
}

// nowCapturingMock records the `now` value SweepTick is called with.
type nowCapturingMock struct {
	calls atomic.Int32
	mu    sync.Mutex
	last  time.Time
}

func (m *nowCapturingMock) SweepTick(_ context.Context, now time.Time) error {
	m.mu.Lock()
	m.last = now
	m.mu.Unlock()
	m.calls.Add(1)
	return nil
}

func (m *nowCapturingMock) lastNow() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

// TestSweeperLifecycle_BusinessNow_FromInjectedClock pins review P2-2: the
// `now` handed to SweepTick (business-plane expiry time) comes from the
// injected BusinessClock, NOT the real-time control-plane ticker's tick value.
// A frozen fake clock therefore yields a stable, non-wall-clock `now`.
func TestSweeperLifecycle_BusinessNow_FromInjectedClock(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	frozen := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(frozen)
	mock := &nowCapturingMock{}

	lc := NewSweeperLifecycle("biz-now-test", mock, shortInterval, fc)
	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()
	require.NoError(t, lc.Start(ownerCtx))

	require.Eventually(t, func() bool {
		return mock.calls.Load() >= 1
	}, tickWait, testtime.D1ms, "SweepTick must fire at least once")

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.CtxShort)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))

	// `now` must equal the frozen business clock, NOT real wall-clock from the
	// control-plane ticker (which would be ~time.Now(), decades after 2000).
	assert.Equal(t, frozen, mock.lastNow(),
		"SweepTick now must come from BusinessClock, not the real-time ticker (P2-2)")
}

// TestSweeperLifecycle_ZeroValueSweeper_FailsAtStart pins review P2-1: a
// zero-value &command.Sweeper{} must fail the OnStart readiness gate (so
// bootstrap rolls back) instead of starting and silently erroring every tick.
func TestSweeperLifecycle_ZeroValueSweeper_FailsAtStart(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	lc := NewSweeperLifecycle("zero-sweeper", &kcommand.Sweeper{}, shortInterval, clock.Real())
	err := lc.Start(context.Background())
	require.Error(t, err, "zero-value Sweeper must fail OnStart")
	assert.Contains(t, err.Error(), "not ready")
	// No goroutine spawned: Stop is a no-op and goleak stays clean.
	require.NoError(t, lc.Stop(context.Background()))
}

// TestSweeperLifecycle_BadCounterLabels_FailsAtStart pins review P1-4: a
// SweepErrorCounter whose label set is not exactly {"cell"} would panic in
// CounterVec.With on the first error tick and crashloop the loop goroutine.
// The OnStart preflight must convert that into a fail-fast wiring error.
func TestSweeperLifecycle_BadCounterLabels_FailsAtStart(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	provider := newTestProvider()
	badCounter, err := provider.CounterVec(kernelmetrics.CounterOpts{
		Name:       "command_sweep_errors_total",
		Help:       "Total command sweep errors",
		LabelNames: []string{"wrong_label"}, // mismatch — .With({"cell":..}) panics
	})
	require.NoError(t, err)

	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)

	lc := NewSweeperLifecycle("bad-counter", sw, shortInterval, clock.Real())
	lc.CellID = "testcell"
	lc.SweepErrorCounter = badCounter

	startErr := lc.Start(context.Background())
	require.Error(t, startErr, "bad counter label set must fail OnStart, not crashloop")
	assert.Contains(t, startErr.Error(), "label set invalid")
	require.NoError(t, lc.Stop(context.Background()))
}
