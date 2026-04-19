package eventrouter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Three-phase Close tests (Phase 2: drain barrier)
// ---------------------------------------------------------------------------

// stopIntakeRecorder is a mock Subscriber that also implements
// SubscriberIntakeStopper. It records the wall-clock times at which
// StopIntake is called so tests can verify ordering relative to runCtx cancel.
type stopIntakeRecorder struct {
	blockingSubscriber // embed ready/blocking behaviour

	mu              sync.Mutex
	stopIntakeCalls int
	stopIntakeTime  time.Time
	stopIntakeErr   error
}

func (s *stopIntakeRecorder) StopIntake(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopIntakeCalls++
	s.stopIntakeTime = time.Now()
	return s.stopIntakeErr
}

func (s *stopIntakeRecorder) StopIntakeAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopIntakeTime
}

func (s *stopIntakeRecorder) StopIntakeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopIntakeCalls
}

// cancelTimeRecorder wraps a subscriber and records when Subscribe's ctx is
// cancelled (i.e. when runCtx cancel is called by Close Phase 2).
type cancelTimeRecorder struct {
	inner        outbox.Subscriber
	cancelledAt  atomic.Int64  // UnixNano
	subscribedCh chan struct{} // closed once Subscribe goroutine is live
}

func newCancelTimeRecorder(inner outbox.Subscriber) *cancelTimeRecorder {
	return &cancelTimeRecorder{
		inner:        inner,
		subscribedCh: make(chan struct{}),
	}
}

func (r *cancelTimeRecorder) Setup(ctx context.Context, sub outbox.Subscription) error {
	return r.inner.Setup(ctx, sub)
}
func (r *cancelTimeRecorder) Ready(sub outbox.Subscription) <-chan struct{} {
	return r.inner.Ready(sub)
}
func (r *cancelTimeRecorder) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.EntryHandler) error {
	// Signal that we are inside Subscribe.
	select {
	case <-r.subscribedCh:
	default:
		close(r.subscribedCh)
	}
	// Block until ctx cancelled, recording the time.
	<-ctx.Done()
	r.cancelledAt.Store(time.Now().UnixNano())
	return ctx.Err()
}
func (r *cancelTimeRecorder) Close(ctx context.Context) error { return r.inner.Close(ctx) }

func (r *cancelTimeRecorder) WaitSubscribed(timeout time.Duration) bool {
	select {
	case <-r.subscribedCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (r *cancelTimeRecorder) CancelledAt() time.Time {
	ns := r.cancelledAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// compositeStopIntakeSubscriber combines stopIntakeRecorder (StopIntake) with
// cancelTimeRecorder (cancel-time tracking) into a single subscriber.
// We need this because Router.subscriber is a single field.
type compositeStopIntakeSubscriber struct {
	stopRecorder *stopIntakeRecorder
	cancelRec    *cancelTimeRecorder
}

func newCompositeStopIntakeSubscriber() *compositeStopIntakeSubscriber {
	sr := &stopIntakeRecorder{}
	cr := newCancelTimeRecorder(sr)
	return &compositeStopIntakeSubscriber{
		stopRecorder: sr,
		cancelRec:    cr,
	}
}

func (c *compositeStopIntakeSubscriber) Setup(ctx context.Context, sub outbox.Subscription) error {
	return c.cancelRec.Setup(ctx, sub)
}
func (c *compositeStopIntakeSubscriber) Ready(sub outbox.Subscription) <-chan struct{} {
	return c.cancelRec.Ready(sub)
}
func (c *compositeStopIntakeSubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.EntryHandler) error {
	return c.cancelRec.Subscribe(ctx, sub, handler)
}
func (c *compositeStopIntakeSubscriber) Close(ctx context.Context) error {
	return c.cancelRec.Close(ctx)
}
func (c *compositeStopIntakeSubscriber) StopIntake(ctx context.Context) error {
	return c.stopRecorder.StopIntake(ctx)
}

// TestRouterClose_CallsStopIntakeBeforeCancel verifies that when the subscriber
// implements SubscriberIntakeStopper, Close calls StopIntake before cancelling
// the runCtx (Phase 1 before Phase 2 in the three-phase drain).
func TestRouterClose_CallsStopIntakeBeforeCancel(t *testing.T) {
	composite := newCompositeStopIntakeSubscriber()

	r := New(composite)
	r.AddHandler("topic.drain", noopHandler, "test")

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for Running signal — all three phases complete and Phase 4 blocks.
	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	// Ensure Subscribe goroutine is live inside the blocking call.
	require.True(t, composite.cancelRec.WaitSubscribed(2*time.Second),
		"subscribe goroutine did not start")

	// Invoke Close and wait for it.
	closeErr := r.Close(context.Background())
	require.NoError(t, closeErr)
	<-done

	stopAt := composite.stopRecorder.StopIntakeAt()
	cancelAt := composite.cancelRec.CancelledAt()

	require.False(t, stopAt.IsZero(), "StopIntake must have been called")
	require.False(t, cancelAt.IsZero(), "runCtx must have been cancelled")

	assert.True(t, stopAt.Before(cancelAt) || stopAt.Equal(cancelAt),
		"StopIntake (%v) must happen before or at the same time as runCtx cancel (%v)",
		stopAt, cancelAt)
}

// TestRouterClose_NoStopIntakeFallback verifies that when the subscriber does
// NOT implement SubscriberIntakeStopper, Close works correctly without panic
// and returns nil.
func TestRouterClose_NoStopIntakeFallback(t *testing.T) {
	// blockingSubscriber does NOT implement SubscriberIntakeStopper.
	sub := &blockingSubscriber{}
	r := New(sub)

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	err := r.Close(context.Background())
	assert.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after Close (no-handler path)")
	}
}

// TestRouterClose_NoStopIntakeFallback_WithHandlers is the same as above but
// with a registered handler, confirming no panic on the fast-path (no handlers
// case is already covered by TestRouter_Close_ZeroHandlers).
func TestRouterClose_NoStopIntakeFallback_WithHandlers(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)
	r.AddHandler("topic.a", noopHandler, "test")

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	err := r.Close(context.Background())
	assert.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after Close")
	}
}

// inflightSubscriber simulates a subscriber whose handler sleeps 200ms to
// model in-flight processing during drain. Subscribe blocks until the handler
// goroutine completes OR ctx is cancelled — whichever comes first — and
// additionally signals via handlerDone when the simulated work finishes.
type inflightSubscriber struct {
	handlerDuration time.Duration
	handlerDone     chan struct{} // closed when simulated handler finishes
	subscribedCh    chan struct{} // closed once Subscribe is live
}

func newInflightSubscriber(d time.Duration) *inflightSubscriber {
	return &inflightSubscriber{
		handlerDuration: d,
		handlerDone:     make(chan struct{}),
		subscribedCh:    make(chan struct{}),
	}
}

func (s *inflightSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *inflightSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// Subscribe launches one simulated in-flight handler that takes handlerDuration
// to complete. It blocks until the handler finishes (mirroring real subscribers
// that drain in-flight before returning from Subscribe).
func (s *inflightSubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	select {
	case <-s.subscribedCh:
	default:
		close(s.subscribedCh)
	}

	// Simulate one in-flight message being processed.
	handlerFinished := make(chan struct{})
	go func() {
		defer close(handlerFinished)
		// The in-flight handler ignores ctx — it runs to completion even after
		// intake is stopped, which is the whole point of the drain barrier.
		time.Sleep(s.handlerDuration)
		close(s.handlerDone)
	}()

	// Subscribe blocks until handler finishes (drain completed) or ctx cancelled.
	select {
	case <-handlerFinished:
		return nil
	case <-ctx.Done():
		// Even on cancel, wait for the inflight handler to finish.
		<-handlerFinished
		return ctx.Err()
	}
}

func (s *inflightSubscriber) Close(_ context.Context) error { return nil }
func (s *inflightSubscriber) StopIntake(_ context.Context) error {
	// StopIntake tells the broker to stop delivering new messages.
	// In this mock we do nothing (simulates the broker cancellation being instant).
	return nil
}

// TestRouterClose_WaitsForInflightAfterStopIntake verifies that Close blocks
// until the in-flight handler finishes processing, proving the drain window
// is preserved between StopIntake (Phase 1) and wg.Wait (Phase 3).
func TestRouterClose_WaitsForInflightAfterStopIntake(t *testing.T) {
	const handlerDuration = 200 * time.Millisecond

	sub := newInflightSubscriber(handlerDuration)

	r := New(sub)
	r.AddHandler("topic.inflight", noopHandler, "test")

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	// Ensure the Subscribe goroutine is inside its blocking call.
	select {
	case <-sub.subscribedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe goroutine did not start")
	}

	start := time.Now()

	// Close with a generous timeout — should block until handler finishes.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()

	closeErr := r.Close(closeCtx)
	require.NoError(t, closeErr)

	elapsed := time.Since(start)

	// The in-flight handler took ~200ms; Close must have waited for it.
	assert.GreaterOrEqual(t, elapsed, handlerDuration-20*time.Millisecond,
		"Close returned too early; in-flight handler was still running")

	// Verify handler actually finished (not just ctx expiry).
	select {
	case <-sub.handlerDone:
	default:
		t.Fatal("in-flight handler did not finish before Close returned")
	}

	<-done
}

// TestRouterClose_StopIntakeError_ContinuesShutdown verifies that a non-nil
// error from StopIntake does not abort the Close sequence: the router must
// still cancel runCtx and wait for goroutines.
func TestRouterClose_StopIntakeError_ContinuesShutdown(t *testing.T) {
	sr := &stopIntakeRecorder{
		stopIntakeErr: context.DeadlineExceeded, // simulate StopIntake timeout
	}
	r := New(sr)
	r.AddHandler("topic.a", noopHandler, "test")

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	// Close should NOT propagate the StopIntake error; it should still succeed.
	err := r.Close(context.Background())
	assert.NoError(t, err, "Close must not return StopIntake error")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after Close")
	}

	// StopIntake was still called once despite the error.
	assert.Equal(t, 1, sr.StopIntakeCalls())
}

// hangingStopIntakeSubscriber embeds blockingSubscriber and implements
// SubscriberIntakeStopper such that StopIntake respects (or ignores) the
// passed ctx depending on the ignoreCtx flag.
type hangingStopIntakeSubscriber struct {
	blockingSubscriber
	release   chan struct{} // test controls when StopIntake may return
	ignoreCtx bool          // if true, ignore ctx — simulates buggy adapter
}

func (h *hangingStopIntakeSubscriber) StopIntake(ctx context.Context) error {
	if h.ignoreCtx {
		// Simulates a buggy adapter that ignores ctx (the pre-F1 rabbitmq bug).
		<-h.release
		return nil
	}
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestRouterClose_StopIntakeBlocksNeverCalled_CtxTimeoutContinues verifies
// that when StopIntake ignores its ctx (e.g. a misbehaving adapter), Router
// still advances to Phase 2 (cancel runCtx) once the outer Close ctx expires.
// This is the F1b protection: StopIntake must not stall the whole Close chain.
func TestRouterClose_StopIntakeBlocksNeverCalled_CtxTimeoutContinues(t *testing.T) {
	h := &hangingStopIntakeSubscriber{
		release:   make(chan struct{}),
		ignoreCtx: true, // buggy adapter: ignores ctx
	}

	r := New(h)
	r.AddHandler("t", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test-cg")

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(runCtx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	// Close with a short ctx; the buggy StopIntake ignores it, but Router.Close
	// must still return within the ctx budget (+ small margin) by advancing
	// to Phase 2 autonomously.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer closeCancel()

	start := time.Now()
	closeErr := r.Close(closeCtx)
	elapsed := time.Since(start)

	// Close should return ctx.DeadlineExceeded (could not wait for StopIntake).
	require.Error(t, closeErr, "Close must return ctx error when StopIntake ignores ctx")
	assert.ErrorIs(t, closeErr, context.DeadlineExceeded)
	assert.Less(t, elapsed, 600*time.Millisecond,
		"Close must not stall significantly past ctx deadline; got %s", elapsed)

	// Release the hanging StopIntake so it exits (avoid goroutine leak).
	close(h.release)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after StopIntake released")
	}
}

// ---------------------------------------------------------------------------
// F21: phaseError label tests
// ---------------------------------------------------------------------------

// TestRouterClose_WrapsErrorsByPhase verifies that Router.Close wraps timeout
// errors in phaseError so that the shutdown phase is preserved for post-mortem
// diagnosis via errors.As.
//
// Close always returns a "wg_wait" phaseError on ctx expiry regardless of
// whether the budget was consumed by StopIntake (Phase 1) or the goroutine
// drain (Phase 3) — the returned error reflects the final phase reached.
// The "stop_intake" phase is logged but not separately surfaced in the return
// value because Close must always complete Phases 2+3 to avoid goroutine leaks.
//
// ref: uber-go/fx app.go per-hook error surfacing.
func TestRouterClose_WrapsErrorsByPhase(t *testing.T) {
	t.Run("phaseError returned when ctx expires during StopIntake", func(t *testing.T) {
		// hangingStopIntakeSubscriber with ignoreCtx=true consumes the entire close
		// budget. Router still completes Phase 2 (cancel) + Phase 3 (wg.Wait), and
		// returns a "wg_wait" phaseError because that is the last phase observed.
		h := &hangingStopIntakeSubscriber{
			release:   make(chan struct{}),
			ignoreCtx: true,
		}

		r := New(h)
		r.AddHandler("t", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}, "test-cg")

		runCtx, runCancel := context.WithCancel(context.Background())
		defer runCancel()
		done := make(chan error, 1)
		go func() { done <- r.Run(runCtx) }()

		select {
		case <-r.Running():
		case <-time.After(2 * time.Second):
			t.Fatal("Router did not become ready")
		}

		closeCtx, closeCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer closeCancel()

		err := r.Close(closeCtx)

		// Must propagate a context error wrapped in phaseError.
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)

		var pe *phaseError
		require.True(t, errors.As(err, &pe),
			"Close error must be *phaseError; got %T: %v", err, err)
		// "wg_wait" is the phase label returned — StopIntake consumed the budget,
		// so Phase 3 also sees an expired ctx and labels accordingly.
		assert.Equal(t, "wg_wait", pe.Phase)

		// Unblock the hanging goroutine so test cleanup is clean.
		close(h.release)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not exit after releasing StopIntake")
		}
	})

	t.Run("wg_wait phase label when goroutine drain times out", func(t *testing.T) {
		// inflightSubscriber simulates a long in-flight message that outlasts the
		// close ctx. StopIntake is a no-op, so the budget is consumed by wg.Wait.
		const veryLongDrain = 5 * time.Second
		sub := newInflightSubscriber(veryLongDrain)

		r := New(sub)
		r.AddHandler("t", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}, "test-cg")

		runCtx, runCancel := context.WithCancel(context.Background())
		defer runCancel()
		done := make(chan error, 1)
		go func() { done <- r.Run(runCtx) }()

		select {
		case <-r.Running():
		case <-time.After(2 * time.Second):
			t.Fatal("Router did not become ready")
		}

		// Wait for Subscribe to be live before starting Close.
		select {
		case <-sub.subscribedCh:
		case <-time.After(2 * time.Second):
			t.Fatal("Subscribe goroutine did not start")
		}

		// Short ctx — StopIntake returns immediately (no-op), but the 5s inflight
		// message outlasts the 150ms close budget.
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer closeCancel()

		err := r.Close(closeCtx)

		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)

		var pe *phaseError
		require.True(t, errors.As(err, &pe),
			"Close error must be *phaseError; got %T: %v", err, err)
		assert.Equal(t, "wg_wait", pe.Phase)

		// Run will exit once the long inflight sleep finishes — cancel runCtx to speed up.
		runCancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Fatal("Run did not exit")
		}
	})
}

// TestRouterClose_StopIntakeErr_ProceedsToCancel verifies that when
// StopIntake returns before ctx expires (whether with nil or an error),
// Router advances to Phase 2 cancel + wait normally.
func TestRouterClose_StopIntakeErr_ProceedsToCancel(t *testing.T) {
	h := &hangingStopIntakeSubscriber{
		release: make(chan struct{}),
	}
	close(h.release) // allow StopIntake to return immediately

	r := New(h)
	r.AddHandler("t", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test-cg")

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(runCtx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	err := r.Close(context.Background())
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after Close")
	}
}
