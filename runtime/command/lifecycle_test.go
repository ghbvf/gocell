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
const shortInterval = 5 * time.Millisecond

// tickWait is how long to wait for at least one tick in tests.
const tickWait = 500 * time.Millisecond

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
// New code uses real time for the startup probe — Start must return within ~100ms.
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
	lc := NewSweeperLifecycle("no-deadlock", sw, time.Hour) // long interval — no tick fires

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	startedAt := time.Now()
	require.NoError(t, lc.Start(ownerCtx))
	elapsed := time.Since(startedAt)

	// Should return in <500ms (startup probe is ~50ms real time; generous headroom for CI).
	// The point of this test is that Start doesn't block indefinitely (old behavior
	// with a frozen clock would block forever). 500ms is well below any reasonable
	// human-observable delay.
	assert.Less(t, elapsed, 500*time.Millisecond,
		"Start must not block: startup probe uses real time, not an injected clock")

	// Clean up
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
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

	// Wait for at least one SweepTick call
	deadline := time.Now().Add(tickWait)
	for time.Now().Before(deadline) {
		if mock.calls.Load() >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	assert.GreaterOrEqual(t, mock.calls.Load(), int32(1), "SweepTick must be called at least once")

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
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

	// Wait for at least one SweepTick call (which returns error)
	deadline := time.Now().Add(tickWait)
	for time.Now().Before(deadline) {
		if mock.calls.Load() >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	assert.GreaterOrEqual(t, mock.calls.Load(), int32(1), "SweepTick must be called even when error returned")

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
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
	lc := NewSweeperLifecycle("owner-ctx-test", sw, time.Hour)

	ownerCtx, ownerCancel := context.WithCancel(context.Background())

	require.NoError(t, lc.Start(ownerCtx))

	// Cancel owner ctx — worker should exit
	ownerCancel()

	// Stop should succeed quickly (goroutine already exited via owner cancel)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
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
	lc := NewSweeperLifecycle("stop-test", sw, time.Hour) // long interval — no tick fires

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
	lc := NewSweeperLifecycle("devicecommand.sweeper", sw, time.Hour)

	hook := lc.Hook()
	assert.Equal(t, "devicecommand.sweeper", hook.Name)
	assert.NotNil(t, hook.OnStart)
	assert.NotNil(t, hook.OnStop)
}

// TestSweeperLifecycle_SweepErrorCounter verifies counter is incremented
// when SweepTick returns an error (C.3 observable errors).
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

	lc := NewSweeperLifecycle("err-counter-test", mock, shortInterval)
	lc.SweepErrorCounter = counterVec

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	// Wait for at least one error tick
	deadline := time.Now().Add(tickWait)
	for time.Now().Before(deadline) {
		if mock.calls.Load() >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))

	// Counter must have been incremented at least once
	count := kernelmetrics.TestProviderCounterValue(provider, "command_sweep_errors_total", map[string]string{"cell": ""})
	assert.GreaterOrEqual(t, count, 1.0, "sweep error counter must be incremented on SweepTick error")
}
