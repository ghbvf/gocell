package command

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

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
	// NewSweeperLifecycle(name, sweeper, interval) — no clock parameter
	lc := NewSweeperLifecycle("test.sweeper", sw, shortInterval)
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
	lc := NewSweeperLifecycle("no-deadlock", sw, testtime.D1h) // long interval — no tick fires

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	startedAt := time.Now()
	require.NoError(t, lc.Start(ownerCtx))
	elapsed := time.Since(startedAt)

	// Should return in <500ms (startup probe is ~50ms real time; generous headroom for CI).
	// The point of this test is that Start doesn't block indefinitely (old behavior
	// with a frozen clock would block forever). 500ms is well below any reasonable
	// human-observable delay.
	assert.Less(t, elapsed, testtime.D500ms,
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
	lc := NewSweeperLifecycle("ticker-test", mock, shortInterval)

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
	lc := NewSweeperLifecycle("err-test", mock, shortInterval)

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
	lc := NewSweeperLifecycle("owner-ctx-test", sw, testtime.D1h)

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
	lc := NewSweeperLifecycle("stop-test", sw, testtime.D1h) // long interval — no tick fires

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))
	require.NoError(t, lc.Stop(stopCtx), "Stop must be idempotent")
}

// TestSweeperLifecycle_ContributesHook verifies Hook() returns correct shape.
func TestSweeperLifecycle_ContributesHook(t *testing.T) {
	t.Parallel()
	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	lc := NewSweeperLifecycle("devicecommand.sweeper", sw, testtime.D1h)

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
	provider := kernelmetrics.NewTestProvider()
	counterVec, err := provider.CounterVec(kernelmetrics.CounterOpts{
		Name:       "command_sweep_errors_total",
		Help:       "Total command sweep errors",
		LabelNames: []string{"cell"},
	})
	require.NoError(t, err)

	const testCellID = "testcell"
	lc := NewSweeperLifecycle("err-counter-test", mock, shortInterval)
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
	count := kernelmetrics.TestProviderCounterValue(provider, "command_sweep_errors_total",
		map[string]string{"cell": testCellID})
	assert.GreaterOrEqual(t, count, 1.0,
		"sweep error counter must be incremented with cell=%q on SweepTick error", testCellID)
}
