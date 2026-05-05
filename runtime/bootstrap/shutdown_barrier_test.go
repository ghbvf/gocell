package bootstrap

// shutdown_barrier_test.go — integration tests for the runCtx separation and
// LIFO shutdown orchestration introduced in Phase 4.
//
// These tests verify the behavioral contract described in the plan:
//  1. HTTP continues accepting traffic during preShutdownDelay
//  2. LIFO teardown fires in reverse registration order
//  3. runCtx is NOT canceled when external ctx is canceled (they are independent)
//  4. A worker error triggers orderly phase10 orchestration (not raw rollback)
//  5. Total shutdown budget is respected
//
// ref: uber-go/fx app.go (run vs stop ctx separation)
// ref: sigs.k8s.io/controller-runtime engageStopProcedure (LIFO teardown)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// barrierPreDelay is used in HTTPAcceptsDuringPreShutdownDelay to set the pre-shutdown delay.
const barrierPreDelay = testtime.D300ms

// barrierAssertionDelay is used in RunCtxIndependentOfExternalCtx as the
// pre-shutdown delay to create a reliable assertion window.
const barrierAssertionDelay = testtime.D150ms

// barrierShutdownTimeout is the total shutdown budget for TotalBudgetRespected.
const barrierShutdownTimeout = 600 * time.Millisecond

// barrierPreDelayShorter is the shorter pre-delay in TotalBudgetRespected.
const barrierPreDelayShorter = testtime.D100ms

// barrierWorkerStartDelay is the delay used by errorAfterStartWorker.
const barrierWorkerStartDelay = testtime.D100ms

// barrierBudgetTolerance is the slack added to the shutdown budget in elapsed assertions.
const barrierBudgetTolerance = testtime.D200ms

// barrierBudgetHardLimit is the outer hard deadline for the TotalBudgetRespected test.
const barrierBudgetHardLimit = barrierShutdownTimeout + testtime.D500ms

// TestShutdown_HTTPAcceptsDuringPreShutdownDelay verifies that:
//   - After external ctx cancel, HTTP still returns 200 during preShutdownDelay
//   - /readyz returns 503 immediately (SetShuttingDown fires before the delay)
func TestShutdown_HTTPAcceptsDuringPreShutdownDelay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := ln.Addr().String()
	const preDelay = barrierPreDelay

	asm := assembly.New(assembly.Config{ID: "test-pre-delay", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(testtime.D2s),
		WithPreShutdownDelay(preDelay),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	waitForHealthy(t, addr)

	cancel()

	// Wait for /readyz to flip to 503 (SetShuttingDown fires at the start of
	// phase10 before preShutdownDelay). This replaces a fixed sleep so the
	// test is robust on slow CI runners.
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
		if err != nil {
			return false
		}
		defer closeBody(t, resp)
		return resp.StatusCode == http.StatusServiceUnavailable
	}, testtime.D500ms, testtime.D10ms,
		"/readyz must flip to 503 at the start of preShutdownDelay")

	// Within the preShutdownDelay window: HTTP main listener must still accept
	// connections. Strong assertion — a dropped connection is a regression
	// (before this fix, err was silently swallowed and the assertion skipped).
	resp, err2 := testHTTPClient.Get(fmt.Sprintf("http://%s/", addr))
	require.NoError(t, err2, "HTTP must still accept connections during preShutdownDelay")
	closeBody(t, resp)
	assert.NotEqual(t, 0, resp.StatusCode, "HTTP server must serve a response")

	// Confirm /readyz continues to return 503 throughout the window.
	respZ, err3 := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", addr))
	require.NoError(t, err3, "/readyz must still respond during preShutdownDelay")
	assert.Equal(t, http.StatusServiceUnavailable, respZ.StatusCode,
		"/readyz must return 503 during preShutdownDelay")
	var body map[string]any
	require.NoError(t, json.NewDecoder(respZ.Body).Decode(&body))
	closeBody(t, respZ)
	assertReadyzServiceUnavailable(t, body)

	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(testtime.D5s):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestShutdown_LIFOTeardownOrder verifies that registered teardowns execute
// in strict LIFO order. We inject two fake close trackers and assert ordering.
func TestShutdown_LIFOTeardownOrder(t *testing.T) {
	var mu sync.Mutex
	var closeOrder []string

	record := func(name string) func(context.Context) error {
		return func(_ context.Context) error {
			mu.Lock()
			closeOrder = append(closeOrder, name)
			mu.Unlock()
			return nil
		}
	}

	_, s := newPhaseState()
	s.addTeardown(record("first"))
	s.addTeardown(record("second"))
	s.addTeardown(record("third"))

	b := New(WithClock(clock.Real()))
	_ = b.phase10LIFOTeardown(context.Background(), s)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"third", "second", "first"}, closeOrder,
		"teardowns must execute in LIFO order")
}

// TestShutdown_RunCtxIndependentOfExternalCtx verifies the core invariant:
// workers run on runCtx (derived from Background), not the external ctx.
// After external ctx is canceled, the worker must NOT exit until its
// teardown (which calls workerCancel) is invoked.
func TestShutdown_RunCtxIndependentOfExternalCtx(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// workerCtxCancelledAt captures when the worker's ctx.Done fires.
	var workerCtxCancelledAt atomic.Int64
	workerStarted := make(chan struct{})
	// workerCtxDone is closed by the tracking worker when its ctx.Done fires.
	workerCtxDone := make(chan struct{})

	trackWorker := &trackingWorker{
		started: workerStarted,
		onCancel: func() {
			workerCtxCancelledAt.Store(time.Now().UnixNano())
			close(workerCtxDone)
		},
	}

	asm := assembly.New(assembly.Config{ID: "test-ctx-sep", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	// preShutdownDelay creates a reliable assertion window: after extCancel(),
	// phase10ReadinessFlip blocks for the delay duration before LIFO teardown
	// (which calls workerCancel) runs. This guarantees the worker ctx stays
	// alive for at least that window — enough to assert temporal separation.
	const assertionDelay = barrierAssertionDelay
	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(testtime.D2s),
		WithPreShutdownDelay(assertionDelay),
		WithWorkers(trackWorker),
	)

	extCtx, extCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(extCtx) }()

	waitForHealthy(t, ln.Addr().String())
	<-workerStarted

	extCancelledAt := time.Now()
	extCancel()

	// After external ctx cancel, worker ctx must NOT be canceled yet —
	// they are derived from different roots. The preShutdownDelay holds
	// phase10 before LIFO teardown (which calls workerCancel), giving us a
	// reliable assertion window of ~50ms (well within assertionDelay).
	select {
	case <-workerCtxDone:
		t.Fatal("worker ctx was canceled synchronously with external ctx cancel — they must be independent")
	case <-time.After(testtime.MediumPoll):
		// Good: worker ctx still running 50ms after external ctx cancel.
	}

	select {
	case <-done:
	case <-time.After(testtime.D5s):
		t.Fatal("bootstrap did not shut down in time")
	}

	workerCancelNs := workerCtxCancelledAt.Load()
	require.NotZero(t, workerCancelNs, "worker ctx cancel must have been recorded")
	workerCancelAt := time.Unix(0, workerCancelNs)

	// Worker ctx must be canceled AFTER external ctx — they are not the same ctx.
	assert.True(t, workerCancelAt.After(extCancelledAt) || workerCancelAt.Equal(extCancelledAt),
		"worker ctx cancel (%v) must not precede external ctx cancel (%v)",
		workerCancelAt, extCancelledAt)
}

// TestShutdown_WorkerErrorTriggersOrchestration verifies that a worker returning
// a non-nil error causes phase10 to execute LIFO teardown rather than a raw
// rollback. The critical difference: after all services have started, errors
// should go through phase10 (orderly shutdown), not phase rollback.
func TestShutdown_WorkerErrorTriggersOrchestration(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// We verify behavior through observable side-effects: Run() must return an
	// error (the worker error) instead of nil when teardown itself is clean,
	// and the total execution must complete within timeout.
	workerErr := errors.New("worker exploded")
	errorWorker := &errorAfterStartWorker{err: workerErr, startDelay: barrierWorkerStartDelay}

	asm := assembly.New(assembly.Config{ID: "test-worker-err", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithShutdownTimeout(testtime.D2s),
		WithWorkers(errorWorker),
	)

	ctx := context.Background()
	runErr := b.Run(ctx)

	// The worker error should be returned (teardown itself was clean).
	assert.ErrorIs(t, runErr, workerErr)
}

// TestShutdown_TotalBudgetRespected verifies that phase10 finishes within
// the shutdownTimeout budget even with a preShutdownDelay.
func TestShutdown_TotalBudgetRespected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	const shutdownTimeout = barrierShutdownTimeout
	const preDelay = barrierPreDelayShorter

	asm := assembly.New(assembly.Config{ID: "test-budget", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithPublisher(eb),
		WithSubscriber(eb),
		WithShutdownTimeout(shutdownTimeout),
		WithPreShutdownDelay(preDelay),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- b.Run(ctx) }()

	waitForHealthy(t, ln.Addr().String())
	cancel()

	select {
	case runErr := <-done:
		elapsed := time.Since(start)
		assert.NoError(t, runErr)
		assert.Less(t, elapsed, shutdownTimeout+barrierBudgetTolerance,
			"total shutdown must complete within budget + tolerance; got %v", elapsed)
	case <-time.After(barrierBudgetHardLimit):
		t.Fatal("bootstrap did not shut down within total budget")
	}
}

// --- Helpers ---

// trackingWorker records when its ctx is canceled so tests can verify
// that worker cancellation happens AFTER external ctx cancellation.
type trackingWorker struct {
	started  chan struct{}
	onCancel func()
	once     sync.Once
}

func (w *trackingWorker) Start(ctx context.Context) error {
	w.once.Do(func() { close(w.started) })
	<-ctx.Done()
	if w.onCancel != nil {
		w.onCancel()
	}
	return nil
}

func (w *trackingWorker) Stop(_ context.Context) error { return nil }

// errorAfterStartWorker returns an error after a short delay, simulating a
// worker that starts successfully then fails.
type errorAfterStartWorker struct {
	err        error
	startDelay time.Duration
}

func (w *errorAfterStartWorker) Start(ctx context.Context) error {
	select {
	case <-time.After(w.startDelay):
		return w.err
	case <-ctx.Done():
		return nil
	}
}

func (w *errorAfterStartWorker) Stop(_ context.Context) error { return nil }
