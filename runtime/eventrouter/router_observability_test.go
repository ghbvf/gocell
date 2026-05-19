package eventrouter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// testReadyTimeout bounds Phase 3 ready-wait for the timeout test. Picked
// short enough to keep the test fast but long enough that scheduling jitter
// on a busy CI host does not race the deadline.
const testReadyTimeout = 10 * time.Millisecond

// ---------------------------------------------------------------------------
// Spy EventCollector
// ---------------------------------------------------------------------------

type spyEvent struct {
	cellID string
	topic  string
	reason string
}

type spyDuration struct {
	cellID   string
	duration time.Duration
}

type spyEventCollector struct {
	mu            sync.Mutex
	setupErrors   []spyEvent
	runtimeErrors []spyEvent
	inc           []string
	dec           []string
	readyWaits    []spyDuration
}

func (s *spyEventCollector) IncSubscriptionActive(cellID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inc = append(s.inc, cellID)
}

func (s *spyEventCollector) DecSubscriptionActive(cellID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dec = append(s.dec, cellID)
}

func (s *spyEventCollector) RecordSetupError(cellID, topic, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setupErrors = append(s.setupErrors, spyEvent{cellID: cellID, topic: topic, reason: reason})
}

func (s *spyEventCollector) RecordRuntimeError(cellID, topic string, reason RuntimeErrorReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeErrors = append(s.runtimeErrors, spyEvent{cellID: cellID, topic: topic, reason: string(reason)})
}

func (s *spyEventCollector) ObserveReadyWait(cellID string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyWaits = append(s.readyWaits, spyDuration{cellID: cellID, duration: d})
}

func (s *spyEventCollector) incCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.inc))
	copy(out, s.inc)
	return out
}

func (s *spyEventCollector) decCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.dec))
	copy(out, s.dec)
	return out
}

func (s *spyEventCollector) errorsCopy() []spyEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spyEvent, len(s.setupErrors))
	copy(out, s.setupErrors)
	return out
}

func (s *spyEventCollector) runtimeErrorsCopy() []spyEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spyEvent, len(s.runtimeErrors))
	copy(out, s.runtimeErrors)
	return out
}

func (s *spyEventCollector) waitsCopy() []spyDuration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spyDuration, len(s.readyWaits))
	copy(out, s.readyWaits)
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRouter_Run_IncSubscriptionActive_PerHandler verifies that when two handlers
// with different cellIDs both become ready, IncSubscriptionActive is called once
// per cellID.
func TestRouter_Run_IncSubscriptionActive_PerHandler(t *testing.T) {
	spy := &spyEventCollector{}
	sub := &recordingGroupSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "cell-alpha", "cell-alpha"))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.b"), noopHandler, "cell-beta", "cell-beta"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	incs := spy.incCopy()
	assert.Len(t, incs, 2, "IncSubscriptionActive must be called once per handler")
	assert.Contains(t, incs, "cell-alpha")
	assert.Contains(t, incs, "cell-beta")

	cancel()
	<-done
}

// TestRouter_Run_ObserveReadyWait_PerHandler verifies that ObserveReadyWait is
// called for each handler and records a non-negative duration.
func TestRouter_Run_ObserveReadyWait_PerHandler(t *testing.T) {
	spy := &spyEventCollector{}
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.rw"), noopHandler, "cell-rw", "cell-rw"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	waits := spy.waitsCopy()
	require.Len(t, waits, 1, "ObserveReadyWait must be called once per handler")
	assert.Equal(t, "cell-rw", waits[0].cellID)
	assert.GreaterOrEqual(t, waits[0].duration, time.Duration(0))

	cancel()
	<-done
}

// TestRouter_runSetup_ObserveSetupError_OnSetupFailure verifies that when
// Subscriber.Setup returns an error, RecordSetupError is called with
// reason="setup_error".
func TestRouter_runSetup_ObserveSetupError_OnSetupFailure(t *testing.T) {
	spy := &spyEventCollector{}
	setupErr := &setupFailSubscriber{err: assert.AnError}
	r := New(wrap(setupErr), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.fail"), noopHandler, "cell-fail", "cell-fail"))

	ctx := t.Context()
	err := r.Run(ctx)
	require.Error(t, err)

	errs := spy.errorsCopy()
	require.Len(t, errs, 1, "RecordSetupError must be called once on Setup failure")
	assert.Equal(t, "cell-fail", errs[0].cellID)
	assert.Equal(t, "topic.fail", errs[0].topic)
	assert.Equal(t, "setup_error", errs[0].reason)
}

// TestRouter_runAwaitReady_Timeout_ObserveSetupError_ReadyTimeout verifies that
// when the ready timeout fires and a handler has not readied, RecordSetupError
// is called with reason="ready_timeout".
func TestRouter_runAwaitReady_Timeout_ObserveSetupError_ReadyTimeout(t *testing.T) {
	spy := &spyEventCollector{}

	// neverReadySubscriber: Setup succeeds, Ready never closes, Subscribe blocks on ctx.
	neverReady := &neverReadySubscriber{}
	r := New(
		wrap(neverReady), clock.Real(),
		WithEventRouterCollector(spy),
		WithReadyTimeout(testReadyTimeout), // very short timeout
	)

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.stuck"), noopHandler, "cell-stuck", "cell-stuck"))

	ctx := t.Context()
	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")

	errs := spy.errorsCopy()
	require.Len(t, errs, 1, "RecordSetupError must be called once for ready_timeout")
	assert.Equal(t, "cell-stuck", errs[0].cellID)
	assert.Equal(t, "topic.stuck", errs[0].topic)
	assert.Equal(t, "ready_timeout", errs[0].reason)
}

// TestRouter_Close_DecSubscriptionActive_PerHandler verifies that after Run
// completes and Close is called, DecSubscriptionActive is called for each
// handler that was previously Inc'd (balancing the gauge).
func TestRouter_Close_DecSubscriptionActive_PerHandler(t *testing.T) {
	spy := &spyEventCollector{}
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.x"), noopHandler, "cell-x", "cell-x"))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.y"), noopHandler, "cell-y", "cell-y"))

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	// Inc should have been called for both cells.
	assert.Len(t, spy.incCopy(), 2)

	err := r.Close(context.Background())
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Run did not exit after Close")
	}

	// Dec should balance Inc exactly.
	decs := spy.decCopy()
	incs := spy.incCopy()
	assert.Len(t, decs, len(incs), "Dec count must match Inc count")
	assert.Contains(t, decs, "cell-x")
	assert.Contains(t, decs, "cell-y")
}

// TestRouter_NoCollector_DefaultsToNop verifies that constructing a Router
// without WithEventRouterCollector defaults to NopEventCollector and does not
// panic during normal Run/Close.
func TestRouter_NoCollector_DefaultsToNop(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real()) // no collector option

	assert.IsType(t, NopEventCollector{}, r.collector, "default collector must be NopEventCollector")

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.nop"), noopHandler, "cell-nop", "cell-nop"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Run did not exit after cancel")
	}
}

// TestRouter_WithEventRouterCollector_TypedNilIgnored verifies that passing a
// typed-nil EventCollector to WithEventRouterCollector is silently ignored and
// the Router still falls back to NopEventCollector.
func TestRouter_WithEventRouterCollector_TypedNilIgnored(t *testing.T) {
	var c *spyEventCollector // typed nil
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(c))
	assert.IsType(t, NopEventCollector{}, r.collector, "typed-nil collector must be ignored; fallback to NopEventCollector")
}

// ---------------------------------------------------------------------------
// Helper subscriber: never signals Ready
// ---------------------------------------------------------------------------

// neverReadySubscriber: Setup succeeds, Ready never closes, Subscribe blocks ctx.
type neverReadySubscriber struct{}

func (s *neverReadySubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *neverReadySubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	return make(chan struct{}) // never closes
}

func (s *neverReadySubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *neverReadySubscriber) Close(_ context.Context) error { return nil }

// ---------------------------------------------------------------------------
// Wave 1 RED tests — RecordRuntimeError (P2#5)
// ---------------------------------------------------------------------------
//
// These tests assert that router.go Phase 4 error paths call
// collector.RecordRuntimeError with the correct cellID, topic, and reason.
//
// All three tests FAIL against the current production code because
// router.go has no RecordRuntimeError call-sites yet (Wave 2 wires them).
//
// When Wave 2 adds `r.collector.RecordRuntimeError(...)` to Phase 4 paths,
// these tests turn GREEN.

// TestRouter_RecordRuntimeError_SubscribeFailure verifies that when
// SubscribeEntry returns an error (Phase 4 runtime fault), the collector
// receives RecordRuntimeError with reason=RuntimeErrorReasonSubscribeFailure.
//
// Wave 1 RED: RecordRuntimeError is never called in the current production
// code, so runtimeErrors will be empty and the assertion fails.
func TestRouter_RecordRuntimeError_SubscribeFailure(t *testing.T) {
	spy := &spyEventCollector{}
	// failingSubscriber: Setup+Ready succeed immediately, Subscribe returns error.
	failSub := &failingSubscriber{err: assert.AnError}
	r := New(wrap(failSub), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.rtfail"), noopHandler, "cell-rt", "cell-rt"))

	ctx := t.Context()
	err := r.Run(ctx)
	require.Error(t, err)

	rtErrs := spy.runtimeErrorsCopy()
	// Wave 1 RED: this assertion fails because RecordRuntimeError is not called.
	require.Len(t, rtErrs, 1,
		"RecordRuntimeError must be called once when SubscribeEntry returns an error "+
			"(Phase 4 subscribe_failure path not yet wired in Wave 2)")
	assert.Equal(t, "cell-rt", rtErrs[0].cellID)
	assert.Equal(t, "topic.rtfail", rtErrs[0].topic)
	assert.Equal(t, string(RuntimeErrorReasonSubscribeFailure), rtErrs[0].reason)
}

// TestRouter_RecordRuntimeError_ReadyWaitTimeout verifies that when the ready
// timeout fires (Phase 3 timeout surfaced as Phase 4 error), the collector
// receives RecordRuntimeError with reason=RuntimeErrorReasonReadyWaitTimeout.
//
// Wave 1 RED: RecordRuntimeError is never called.
func TestRouter_RecordRuntimeError_ReadyWaitTimeout(t *testing.T) {
	spy := &spyEventCollector{}
	neverReady := &neverReadySubscriber{}
	r := New(
		wrap(neverReady), clock.Real(),
		WithEventRouterCollector(spy),
		WithReadyTimeout(testReadyTimeout),
	)

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.rttimeout"), noopHandler, "cell-rtt", "cell-rtt"))

	ctx := t.Context()
	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")

	rtErrs := spy.runtimeErrorsCopy()
	// Wave 1 RED: this assertion fails because RecordRuntimeError is not called.
	require.Len(t, rtErrs, 1,
		"RecordRuntimeError must be called once on ready-wait timeout "+
			"(Phase 4 ready_wait_timeout path not yet wired in Wave 2)")
	assert.Equal(t, "cell-rtt", rtErrs[0].cellID)
	assert.Equal(t, "topic.rttimeout", rtErrs[0].topic)
	assert.Equal(t, string(RuntimeErrorReasonReadyWaitTimeout), rtErrs[0].reason)
}

// TestRouter_RecordRuntimeError_RuntimeFault verifies that when a subscription
// goroutine encounters a delayed failure (Phase 4 runtime fault), the collector
// receives RecordRuntimeError with reason=RuntimeErrorReasonRuntimeFault.
//
// Wave 1 RED: RecordRuntimeError is never called.
func TestRouter_RecordRuntimeError_RuntimeFault(t *testing.T) {
	spy := &spyEventCollector{}
	// delayedFailSubscriber: ready immediately, then returns error after delay.
	delayFail := &delayedFailSubscriber{
		delay: testtime.D10ms,
		err:   assert.AnError,
	}
	r := New(
		wrap(delayFail), clock.Real(),
		WithEventRouterCollector(spy),
		WithReadyTimeout(0), // disable ready timeout so only runtime fault fires
	)

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.rtfault"), noopHandler, "cell-rtf", "cell-rtf"))

	ctx := t.Context()
	err := r.Run(ctx)
	require.Error(t, err)

	rtErrs := spy.runtimeErrorsCopy()
	// Wave 1 RED: this assertion fails because RecordRuntimeError is not called.
	require.Len(t, rtErrs, 1,
		"RecordRuntimeError must be called once on delayed Subscribe error "+
			"(Phase 4 runtime_fault path not yet wired in Wave 2)")
	assert.Equal(t, "cell-rtf", rtErrs[0].cellID)
	assert.Equal(t, "topic.rtfault", rtErrs[0].topic)
	assert.Equal(t, string(RuntimeErrorReasonRuntimeFault), rtErrs[0].reason)
}

// ---------------------------------------------------------------------------
// Wave 1 RED tests — panic isolation (P2#6) for EventCollector
// ---------------------------------------------------------------------------
//
// These tests verify that a panicking EventCollector does not escape the router
// and crash the subscriber goroutine. Currently these panic isolation wraps do
// not exist in production code, so the tests FAIL (panic propagates, test panics).

// panicEventCollector panics on every method call.
type panicEventCollector struct{}

func (panicEventCollector) IncSubscriptionActive(_ string) {
	panic("panicEventCollector: IncSubscriptionActive panics")
}

func (panicEventCollector) DecSubscriptionActive(_ string) {
	panic("panicEventCollector: DecSubscriptionActive panics")
}

func (panicEventCollector) RecordSetupError(_, _, _ string) {
	panic("panicEventCollector: RecordSetupError panics")
}

func (panicEventCollector) ObserveReadyWait(_ string, _ time.Duration) {
	panic("panicEventCollector: ObserveReadyWait panics")
}

func (panicEventCollector) RecordRuntimeError(_, _ string, _ RuntimeErrorReason) {
	panic("panicEventCollector: RecordRuntimeError panics")
}

// mustNotPanic is a table-driven helper for running a function that should
// not panic; used across the panic-isolation sub-tests below.
func mustNotPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: collector panic escaped the router — must be isolated via SafeObserve: %v", name, r)
		}
	}()
	fn()
}

// TestRouter_CollectorPanic_IncSubscriptionActive_DoesNotEscape verifies that
// a panic from IncSubscriptionActive does not propagate.
//
// Wave 1 RED: no SafeObserve wrapping exists yet; the panic will propagate and
// the test will fail with a recovered panic.
func TestRouter_CollectorPanic_IncSubscriptionActive_DoesNotEscape(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(panicEventCollector{}))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.panic-inc"), noopHandler, "cell-p", "cell-p"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wave 1 RED: Run itself will panic due to unprotected IncSubscriptionActive.
	// If panic isolation is in place, Run succeeds and Running() closes.
	mustNotPanic(t, "IncSubscriptionActive", func() {
		select {
		case <-r.Running():
		case err := <-done:
			t.Errorf("Run exited early: %v", err)
		case <-time.After(testtime.D2s):
			t.Error("Router did not become ready within timeout")
		}
	})

	cancel()
	<-done
}

// TestRouter_CollectorPanic_RecordSetupError_DoesNotEscape verifies that a
// panic from RecordSetupError does not propagate.
//
// Wave 1 RED: no SafeObserve wrapping; panic propagates.
func TestRouter_CollectorPanic_RecordSetupError_DoesNotEscape(t *testing.T) {
	setupFail := &setupFailSubscriber{err: assert.AnError}
	r := New(wrap(setupFail), clock.Real(), WithEventRouterCollector(panicEventCollector{}))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.panic-setup"), noopHandler, "cell-ps", "cell-ps"))

	ctx := t.Context()
	mustNotPanic(t, "RecordSetupError", func() {
		err := r.Run(ctx)
		// Run should return an error (setup failed), but must NOT panic.
		require.Error(t, err, "Run must return setup error even when RecordSetupError panics")
	})
}

// TestRouter_CollectorPanic_ObserveReadyWait_DoesNotEscape verifies that a
// panic from ObserveReadyWait does not propagate.
//
// Wave 1 RED: no SafeObserve wrapping; panic propagates.
func TestRouter_CollectorPanic_ObserveReadyWait_DoesNotEscape(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(panicEventCollector{}))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.panic-rw"), noopHandler, "cell-prw", "cell-prw"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	mustNotPanic(t, "ObserveReadyWait", func() {
		select {
		case <-r.Running():
		case err := <-done:
			t.Errorf("Run exited early: %v", err)
		case <-time.After(testtime.D2s):
			t.Error("Router did not become ready")
		}
	})

	cancel()
	<-done
}

// TestRouter_CollectorPanic_RecordRuntimeError_DoesNotEscape verifies that a
// panic from RecordRuntimeError (new method, Phase 4) does not propagate.
//
// Wave 1 RED: no SafeObserve wrapping + RecordRuntimeError not called yet.
func TestRouter_CollectorPanic_RecordRuntimeError_DoesNotEscape(t *testing.T) {
	failSub := &failingSubscriber{err: assert.AnError}
	r := New(wrap(failSub), clock.Real(), WithEventRouterCollector(panicEventCollector{}))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.panic-rte"), noopHandler, "cell-prte", "cell-prte"))

	ctx := t.Context()
	mustNotPanic(t, "RecordRuntimeError", func() {
		err := r.Run(ctx)
		require.Error(t, err, "Run must return error even when RecordRuntimeError panics")
	})
}

// TestRouter_CollectorPanic_DecSubscriptionActive_DoesNotEscape verifies that
// a panic from DecSubscriptionActive does not propagate during router shutdown.
//
// Wave 2: IncSubscriptionActive is now protected by SafeObserve, so the router
// reaches Running() and we can independently test Dec panic isolation during teardown.
func TestRouter_CollectorPanic_DecSubscriptionActive_DoesNotEscape(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(panicEventCollector{}))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.panic-dec"), noopHandler, "cell-pd", "cell-pd"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for router to reach Running() — IncSubscriptionActive is already
	// protected by SafeObserve (Wave 2), so the panic does not block startup.
	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("router did not reach Running() within deadline; " +
			"IncSubscriptionActive panic may still be escaping SafeObserve")
	}

	// Cancel and verify DecSubscriptionActive panic does not escape.
	cancel()
	mustNotPanic(t, "DecSubscriptionActive", func() {
		select {
		case <-done:
		case <-time.After(testtime.D2s):
			t.Error("Run did not exit after cancel")
		}
	})
}

// ---------------------------------------------------------------------------
// W1 RED tests — PR #593 review fix-up (P1#1 reason flicker + P1#2 panic redaction)
// ---------------------------------------------------------------------------
//
// These tests assert the post-fix behavior expected after Fix 1/2/3 land in
// Wave 2 GREEN. They are guarded by t.Skip so the W1 RED commit keeps CI green
// while documenting the target invariants in test form.

// TestRouter_RecordRuntimeError_SubscribeFailure_NoFlicker drives the P1#1
// repro: a single subscription whose SubscribeEntry fails immediately must
// classify deterministically as a setup-phase subscribe_failure, never as a
// Phase 4 runtime_fault. The pre-fix producer-side `select <-r.running` races
// against markRunning/closeRunning, so on a busy host roughly 10–30% of
// iterations land in the runtime metric. The post-fix design moves
// classification to the router phase-consumer point, eliminating the race.
//
// Wave 2 GREEN removes t.Skip after the producer no longer reads r.running.
func TestRouter_RecordRuntimeError_SubscribeFailure_NoFlicker(t *testing.T) {
	t.Skip("RED — Wave 2 (W2 GREEN) removes the producer-side select <-r.running flicker; un-skip then")

	const iterations = 50
	for i := 0; i < iterations; i++ {
		spy := &spyEventCollector{}
		failSub := &failingSubscriber{err: assert.AnError}
		r := New(wrap(failSub), clock.Real(), WithEventRouterCollector(spy))

		require.NoError(t,
			r.AddContractHandler(testEventSpec("topic.noflicker"), noopHandler, "cell-nf", "cell-nf"))

		err := r.Run(t.Context())
		require.Error(t, err, "iter %d: Run must return Subscribe error", i)

		setupErrs := spy.errorsCopy()
		rtErrs := spy.runtimeErrorsCopy()

		require.Lenf(t, setupErrs, 1,
			"iter %d: immediate Subscribe failure must record exactly one setup error", i)
		assert.Equalf(t, string(SetupErrorReasonSubscribeFailure), setupErrs[0].reason,
			"iter %d: reason must be subscribe_failure (Phase 3 setup)", i)
		assert.Emptyf(t, rtErrs,
			"iter %d: immediate Subscribe failure must NOT record any runtime metric "+
				"(Phase 4 runtime_fault is reserved for delayed failures after Running())", i)
	}
}

// TestRouter_PanicRecover_ErrorMessageRedacted verifies that when a
// subscription goroutine panics, the recovered value never leaks into the
// error message returned by Run / surfaced by Health. The panic value is fed
// through pkg/redaction.RedactString and only the sentinel constant
// "subscription panicked (redacted)" appears on the error path; the original
// value lives only in server-side slog as a redacted typed field.
//
// Wave 2 GREEN removes t.Skip after the recover branch swaps `fmt.Errorf("%v", rv)`
// for the fixed sentinel.
func TestRouter_PanicRecover_ErrorMessageRedacted(t *testing.T) {
	t.Skip("RED — Wave 2 (W2 GREEN) wires pkg/redaction into the recover branch; un-skip then")

	spy := &spyEventCollector{}
	const secret = "secret123abc"
	panicSub := &panicWithSecretSubscriber{payload: "password=" + secret}
	r := New(wrap(panicSub), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t,
		r.AddContractHandler(testEventSpec("topic.panic-redact"), noopHandler, "cell-pr", "cell-pr"))

	err := r.Run(t.Context())
	require.Error(t, err)

	assert.NotContains(t, err.Error(), secret,
		"Run error must not leak the panic value; redaction is fail-closed")

	if hErr := r.Health(); hErr != nil {
		assert.NotContains(t, hErr.Error(), secret,
			"Health() error must not leak the panic value; redaction is fail-closed")
	}

	setupErrs := spy.errorsCopy()
	require.Len(t, setupErrs, 1, "panic must record exactly one setup error")
	assert.Equal(t, string(SetupErrorReasonPanic), setupErrs[0].reason,
		"reason must be panic (Phase 2 unrecoverable failure)")
}

// panicWithSecretSubscriber panics with a value that includes a key=value pair
// matching pkg/redaction's sensitive-key list. Used to verify that recovered
// panic values are redacted before entering error chains.
type panicWithSecretSubscriber struct {
	payload string
}

func (s *panicWithSecretSubscriber) Setup(_ context.Context, _ outbox.Subscription) error {
	return nil
}

func (s *panicWithSecretSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (s *panicWithSecretSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	panic(s.payload)
}
func (s *panicWithSecretSubscriber) Close(_ context.Context) error { return nil }
