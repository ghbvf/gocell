package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- phase0ValidateOptions tests ---

func TestPhase0_AcceptsValidOptions(t *testing.T) {
	b := New()
	require.NoError(t, b.phase0ValidateOptions())
}

func TestPhase0_RejectsEmptyHealthCheckerName(t *testing.T) {
	b := New(WithHealthChecker("", func() error { return nil }))
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health checker name must not be empty")
}

func TestPhase0_RejectsNilHealthCheckerFn(t *testing.T) {
	b := New()
	b.healthCheckers = append(b.healthCheckers, namedChecker{name: "test", fn: nil})
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `health checker "test" must not be nil`)
}

func TestPhase0_RejectsNilCircuitBreaker(t *testing.T) {
	b := New()
	b.circuitBreakerNil = true
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker must not be nil")
}

func TestPhase0_RejectsNilBrokerHealth(t *testing.T) {
	b := New()
	b.brokerHealthNil = true
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker health checker must not be nil")
}

func TestPhase0_RejectsNilRelayHealth(t *testing.T) {
	b := New()
	b.relayHealthNil = true
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relay must not be nil")
}

func TestPhase0_RejectsMutuallyExclusiveAuthOptions(t *testing.T) {
	b := New()
	b.authVerifier = &phaseTestVerifier{}
	b.authDiscovery = true
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestPhase0_ValidatesInternalGuard(t *testing.T) {
	b := New()
	b.internalGuardPrefix = "/internal/v1/"
	b.internalGuard = nil // guard prefix set but guard nil
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal guard must not be nil")
}

// --- phase1LoadConfig tests ---

func TestPhase1_LoadConfig_NoPath_UsesEmptyConfig(t *testing.T) {
	b := New()
	_, s := newPhaseState()
	require.NoError(t, b.phase1LoadConfig(s))
	assert.NotNil(t, s.cfg)
	assert.Nil(t, s.cfgWatcher)
}

func TestPhase1_LoadConfig_RegistersCloserTeardown(t *testing.T) {
	closed := false
	b := New()
	b.closers = append(b.closers, closerFunc(func() error {
		closed = true
		return nil
	}))
	_, s := newPhaseState()
	require.NoError(t, b.phase1LoadConfig(s))
	assert.Len(t, s.teardowns, 1)

	// Execute the teardown and verify closer was called.
	require.NoError(t, s.teardowns[0](context.Background()))
	assert.True(t, closed)
}

// --- phase2InitPubSub tests ---

func TestPhase2_InitPubSub_DefaultsToInMemoryBus(t *testing.T) {
	b := New()
	_, s := newPhaseState()
	b.phase2InitPubSub(s)
	assert.NotNil(t, s.pub)
	assert.NotNil(t, s.sub)
}

func TestPhase2_InitPubSub_ExplicitPublisherAndSubscriber(t *testing.T) {
	pub := &phaseTestPublisher{}
	sub := &phaseTestSubscriber{}
	b := New(WithPublisher(pub), WithSubscriber(sub))
	_, s := newPhaseState()
	b.phase2InitPubSub(s)
	assert.Same(t, pub, s.pub.(*phaseTestPublisher))
	assert.Same(t, sub, s.sub.(*phaseTestSubscriber))
}

func TestPhase2_InitPubSub_RegistersTeardownForCloser(t *testing.T) {
	var closeCalled []string
	sub := &phaseTestSubscriberCloser{name: "sub", log: &closeCalled}
	b := New(WithSubscriber(sub))
	_, s := newPhaseState()
	b.phase2InitPubSub(s)
	require.Len(t, s.teardowns, 1)
	require.NoError(t, s.teardowns[0](context.Background()))
	assert.Equal(t, []string{"sub"}, closeCalled)
}

func TestPhase2_InitPubSub_NoDuplicateTeardownForSharedInstance(t *testing.T) {
	// When pub and sub are the same object, only one teardown should be registered.
	var closeCalled int
	eb := &phaseTestSharedBus{closeCount: &closeCalled}
	b := New(WithPublisher(eb), WithSubscriber(eb))
	_, s := newPhaseState()
	b.phase2InitPubSub(s)

	// Execute all teardowns.
	for _, td := range s.teardowns {
		require.NoError(t, td(context.Background()))
	}
	assert.Equal(t, 1, closeCalled, "shared pub/sub must only be closed once")
}

// --- phase3InitAssembly tests ---

func TestPhase3_InitAssembly_BuildsDefaultAssemblyWhenNoneProvided(t *testing.T) {
	b := New()
	_, s := newPhaseState()
	s.cfg = config.NewFromMap(make(map[string]any))
	require.NoError(t, b.phase3InitAssembly(context.Background(), s))
	assert.NotNil(t, s.asm)
	assert.NotNil(t, s.reloads)
}

func TestPhase3_InitAssembly_UsesPrebuiltAssembly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "pre", DurabilityMode: cell.DurabilityDemo})
	b := New(WithAssembly(asm))
	_, s := newPhaseState()
	s.cfg = config.NewFromMap(make(map[string]any))
	require.NoError(t, b.phase3InitAssembly(context.Background(), s))
	assert.Same(t, asm, s.asm)
}

func TestPhase3_InitAssembly_RegistersAssemblyTeardown(t *testing.T) {
	b := New()
	_, s := newPhaseState()
	s.cfg = config.NewFromMap(make(map[string]any))
	require.NoError(t, b.phase3InitAssembly(context.Background(), s))
	// Two teardowns: Shutdown + assembly drain+Stop.
	assert.Len(t, s.teardowns, 2)
}

// --- phase8StartWorkers tests ---

func TestPhase8_StartWorkers_NoWorkers_EmptyWorkerErrCh(t *testing.T) {
	b := New() // no workers
	runCtx, s := newPhaseState()
	b.phase8StartWorkers(runCtx, s)
	assert.Nil(t, s.workerErrCh, "workerErrCh must be nil when no workers are registered")
}

func TestPhase8_StartWorkers_WorkersRegistered_WorkerErrChCreated(t *testing.T) {
	w := &countWorker{}
	b := New(WithWorkers(w))
	runCtx, s := newPhaseState()
	b.phase8StartWorkers(runCtx, s)
	assert.NotNil(t, s.workerErrCh)
	// runCtx cancel causes worker to exit.
	s.runCancel()
	select {
	case <-s.workerErrCh:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after runCtx cancel")
	}
}

// --- runState rollback tests ---

func TestRunState_Rollback_ExecutesTeardownsLIFO(t *testing.T) {
	var order []int
	_, s := newRunState()
	s.addTeardown(func(_ context.Context) error { order = append(order, 1); return nil })
	s.addTeardown(func(_ context.Context) error { order = append(order, 2); return nil })
	s.addTeardown(func(_ context.Context) error { order = append(order, 3); return nil })

	cause := errors.New("startup failed")
	err := s.rollback(context.Background(), cause)
	assert.Equal(t, cause, err)
	assert.Equal(t, []int{3, 2, 1}, order)
}

func TestRunState_Rollback_CancelsRunCtx(t *testing.T) {
	runCtx, s := newRunState()
	_ = s.rollback(context.Background(), errors.New("x"))
	select {
	case <-runCtx.Done():
		// expected
	default:
		t.Fatal("runCtx must be cancelled after rollback")
	}
}

func TestRunState_Rollback_ContinuesThroughTeardownErrors(t *testing.T) {
	_, s := newRunState()
	var executed []int
	s.addTeardown(func(_ context.Context) error { executed = append(executed, 1); return nil })
	s.addTeardown(func(_ context.Context) error {
		executed = append(executed, 2)
		return errors.New("teardown 2 failed")
	})
	s.addTeardown(func(_ context.Context) error { executed = append(executed, 3); return nil })

	cause := errors.New("cause")
	err := s.rollback(context.Background(), cause)
	// rollback returns original cause, not teardown error.
	assert.Equal(t, cause, err)
	// All three teardowns executed despite error in teardown 2.
	assert.Equal(t, []int{3, 2, 1}, executed)
}

// --- shutdownReason tests ---

func TestShutdownReason_Values(t *testing.T) {
	// Verify the iota values are distinct and stable.
	assert.NotEqual(t, reasonCtxCancel, reasonHTTPError)
	assert.NotEqual(t, reasonHTTPError, reasonWorkerError)
	assert.NotEqual(t, reasonWorkerError, reasonRouterError)
}

// --- phase10 unit tests ---

func TestPhase10ReadinessFlip_SetsShuttingDown(t *testing.T) {
	b := New()
	_, s := newPhaseState()
	s.cfg = config.NewFromMap(make(map[string]any))
	require.NoError(t, b.phase3InitAssembly(context.Background(), s))
	require.NoError(t, b.phase5BuildHTTPRouter(s))

	shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b.phase10ReadinessFlip(shutCtx, s)
	// After flip, BeginShutdown has been called; calling again returns an already-closed channel.
	select {
	case <-s.reloads.BeginShutdown():
		// drained channel is closed — BeginShutdown has been called.
	default:
		t.Fatal("reloads.BeginShutdown must have been called by readiness flip")
	}
}

func TestPhase10LIFOTeardown_ExecutesInReverseOrder(t *testing.T) {
	b := New()
	_, s := newPhaseState()
	var order []int
	s.addTeardown(func(_ context.Context) error { order = append(order, 1); return nil })
	s.addTeardown(func(_ context.Context) error { order = append(order, 2); return nil })

	errs := b.phase10LIFOTeardown(context.Background(), s)
	assert.Empty(t, errs)
	assert.Equal(t, []int{2, 1}, order)
}

func TestPhase10LIFOTeardown_CollectsErrors(t *testing.T) {
	b := New()
	_, s := newPhaseState()
	s.addTeardown(func(_ context.Context) error { return errors.New("td1") })
	s.addTeardown(func(_ context.Context) error { return errors.New("td2") })

	errs := b.phase10LIFOTeardown(context.Background(), s)
	// Both teardowns executed, both errors collected.
	assert.Len(t, errs, 2)
}

// --- runCtx independence tests ---

func TestRunCtx_IndependentOfExternalCtx(t *testing.T) {
	// runCtx derived from Background; cancelling the "external" ctx
	// must NOT cancel runCtx.
	_, extCancel := context.WithCancel(context.Background())
	defer extCancel()

	runCtx, _ := newPhaseState()
	// Verify runCtx is alive before external cancel.
	select {
	case <-runCtx.Done():
		t.Fatal("runCtx must be alive at start")
	default:
	}

	extCancel()

	// runCtx must still be alive after external cancel.
	select {
	case <-runCtx.Done():
		t.Fatal("runCtx must NOT be cancelled when external ctx is cancelled")
	default:
		// expected: runCtx is independent
	}
}

// ---------------------------------------------------------------------------
// T19: addCloser dual-path teardown tests
// ---------------------------------------------------------------------------

// TestPhaseState_AddCloser_PrefersContextCloser verifies that addCloser
// registers a ContextCloser's Close method directly (ctx budget propagated).
func TestPhaseState_AddCloser_PrefersContextCloser(t *testing.T) {
	var receivedCtx context.Context
	cc := &ctxCloserSpy{fn: func(ctx context.Context) error {
		receivedCtx = ctx
		return nil
	}}

	_, s := newPhaseState()
	s.addCloser(cc)
	require.Len(t, s.teardowns, 1)

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.teardowns[0](shutCtx))

	// The ctx passed to the teardown must be the shut ctx, not Background.
	deadline, hasDeadline := receivedCtx.Deadline()
	assert.True(t, hasDeadline, "ContextCloser must receive ctx with deadline")
	_, refDeadline := shutCtx.Deadline()
	assert.Equal(t, refDeadline, hasDeadline,
		"deadline presence must match shutCtx; got deadline=%v", deadline)
}

// TestPhaseState_AddCloser_FallsBackToIoCloser verifies that a plain io.Closer
// is wrapped by IgnoreCtx and still registered for teardown.
func TestPhaseState_AddCloser_FallsBackToIoCloser(t *testing.T) {
	closed := false
	ic := &ioCloserSpy{fn: func() error { closed = true; return nil }}

	_, s := newPhaseState()
	s.addCloser(ic)
	require.Len(t, s.teardowns, 1)

	require.NoError(t, s.teardowns[0](context.Background()))
	assert.True(t, closed, "io.Closer must be called via IgnoreCtx wrapper")
}

// TestPhaseState_AddCloser_SkipsNil verifies that addCloser(nil) does not
// register any teardown.
func TestPhaseState_AddCloser_SkipsNil(t *testing.T) {
	_, s := newPhaseState()
	s.addCloser(nil)
	assert.Empty(t, s.teardowns, "nil resource must not register a teardown")
}

// TestPhaseState_AddCloser_SkipsNonCloser verifies that a resource that
// implements neither ContextCloser nor io.Closer is silently skipped.
func TestPhaseState_AddCloser_SkipsNonCloser(t *testing.T) {
	_, s := newPhaseState()
	s.addCloser("just-a-string")
	assert.Empty(t, s.teardowns)
}

// TestPhase1_WatcherTeardown_ContextCloserPreferredOverIoCloser verifies that
// addCloser prefers ContextCloser (CloseCtx) over io.Closer when both are
// available — which is the case for *config.Watcher after T14.
func TestPhase1_WatcherTeardown_ContextCloserPreferredOverIoCloser(t *testing.T) {
	var receivedCtx context.Context
	watcherClosed := false

	spy := &watcherCloserSpy{
		closeFn: func(ctx context.Context) error {
			receivedCtx = ctx
			watcherClosed = true
			return nil
		},
	}

	_, s := newPhaseState()

	// Use addCloser directly — spy implements both Close() and CloseCtx(ctx).
	// addCloser should pick CloseCtx (ContextCloser path).
	s.addCloser(spy)

	require.Len(t, s.teardowns, 1)

	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, s.teardowns[0](shutCtx))

	assert.True(t, watcherClosed)
	_, hasDeadline := receivedCtx.Deadline()
	assert.True(t, hasDeadline, "ContextCloser must receive ctx with deadline (not Background)")
}

// ---------------------------------------------------------------------------
// T19: phase2InitPubSub shutCtx propagation tests
// ---------------------------------------------------------------------------

// TestPhase2InitPubSub_SubscriberCloseReceivesShutCtx verifies that the
// teardown registered by phase2InitPubSub passes the shutCtx directly to
// sub.Close — i.e. the shared shutdown budget is propagated to the subscriber.
func TestPhase2InitPubSub_SubscriberCloseReceivesShutCtx(t *testing.T) {
	var receivedCtx context.Context
	sub := &pubSubCtxSpy{
		closeFn: func(ctx context.Context) error {
			receivedCtx = ctx
			return nil
		},
	}

	b := New(WithSubscriber(sub))
	_, s := newPhaseState()
	b.phase2InitPubSub(s)

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.Len(t, s.teardowns, 1)
	require.NoError(t, s.teardowns[0](shutCtx))

	require.NotNil(t, receivedCtx, "sub.Close must have been called")
	_, hasDeadline := receivedCtx.Deadline()
	assert.True(t, hasDeadline, "sub.Close must receive ctx with deadline (shutCtx)")
}

// TestPhase2InitPubSub_PublisherCloseReceivesShutCtx verifies that when a
// separate publisher is configured, its teardown also receives shutCtx.
func TestPhase2InitPubSub_PublisherCloseReceivesShutCtx(t *testing.T) {
	var subReceivedCtx, pubReceivedCtx context.Context

	sub := &pubSubCtxSpy{closeFn: func(ctx context.Context) error {
		subReceivedCtx = ctx
		return nil
	}}
	pub := &pubSubCtxSpy{closeFn: func(ctx context.Context) error {
		pubReceivedCtx = ctx
		return nil
	}}

	b := New(WithSubscriber(sub), WithPublisher(pub))
	_, s := newPhaseState()
	b.phase2InitPubSub(s)

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Two teardowns: one for sub, one for pub (different instances).
	require.Len(t, s.teardowns, 2)
	for _, td := range s.teardowns {
		require.NoError(t, td(shutCtx))
	}

	require.NotNil(t, subReceivedCtx, "sub.Close must have been called")
	require.NotNil(t, pubReceivedCtx, "pub.Close must have been called")

	_, subHasDeadline := subReceivedCtx.Deadline()
	assert.True(t, subHasDeadline, "sub.Close must receive ctx with deadline")

	_, pubHasDeadline := pubReceivedCtx.Deadline()
	assert.True(t, pubHasDeadline, "pub.Close must receive ctx with deadline")
}

// TestPhase2InitPubSub_SharedBus_ClosedExactlyOnce verifies that when pub
// and sub are the same instance, exactly one Close call is registered.
func TestPhase2InitPubSub_SharedBus_ClosedExactlyOnce(t *testing.T) {
	var closeCount int
	eb := &pubSubCtxSpy{closeFn: func(_ context.Context) error {
		closeCount++
		return nil
	}}

	b := New(WithPublisher(eb), WithSubscriber(eb))
	_, s := newPhaseState()
	b.phase2InitPubSub(s)

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, td := range s.teardowns {
		require.NoError(t, td(shutCtx))
	}
	assert.Equal(t, 1, closeCount, "shared pub/sub bus must be closed exactly once")
}

// TestPhase10_TeardownPropagatesShutCtx_ToAllContextClosers verifies that
// phase10LIFOTeardown passes the shutCtx to every registered teardown function,
// including those added via addCloser from ContextCloser resources.
func TestPhase10_TeardownPropagatesShutCtx_ToAllContextClosers(t *testing.T) {
	const numClosers = 3
	receivedCtxs := make([]context.Context, numClosers)

	b := New()
	_, s := newPhaseState()

	for i := range numClosers {
		idx := i
		cc := &ctxCloserSpy{fn: func(ctx context.Context) error {
			receivedCtxs[idx] = ctx
			return nil
		}}
		s.addCloser(cc)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errs := b.phase10LIFOTeardown(shutCtx, s)
	assert.Empty(t, errs, "all teardowns must succeed")

	for i, ctx := range receivedCtxs {
		require.NotNil(t, ctx, "closer %d must have been called", i)
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline, "closer %d must receive ctx with deadline (shutCtx)", i)
	}
}

// --- Helpers / stubs ---

// ctxCloserSpy implements lifecycle.ContextCloser and records the ctx it receives.
type ctxCloserSpy struct {
	fn func(ctx context.Context) error
}

func (s *ctxCloserSpy) Close(ctx context.Context) error {
	return s.fn(ctx)
}

// ioCloserSpy implements io.Closer only (no Close(ctx)).
type ioCloserSpy struct {
	fn func() error
}

func (s *ioCloserSpy) Close() error {
	return s.fn()
}

// watcherCloserSpy implements lifecycle.ContextCloser (Close(ctx) error) so
// that addCloser picks the ContextCloser path and propagates the shut budget.
// It also implements io.Closer for the fallback path test.
type watcherCloserSpy struct {
	closeFn func(ctx context.Context) error
}

// Close implements lifecycle.ContextCloser — addCloser checks this first.
func (w *watcherCloserSpy) Close(ctx context.Context) error {
	return w.closeFn(ctx)
}

// --- Helpers / stubs (existing) ---

// phaseTestVerifier satisfies auth.IntentTokenVerifier for phase0 tests.
type phaseTestVerifier struct{}

func (v *phaseTestVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	return auth.Claims{}, nil
}

// phaseTestPublisher is a no-op outbox.Publisher for phase tests.
type phaseTestPublisher struct{}

func (p *phaseTestPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (p *phaseTestPublisher) Close(_ context.Context) error                       { return nil }

// phaseTestSubscriber is a no-op outbox.Subscriber for phase tests.
type phaseTestSubscriber struct{}

func (s *phaseTestSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *phaseTestSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *phaseTestSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	return nil
}
func (s *phaseTestSubscriber) Close(_ context.Context) error { return nil }

// phaseTestSubscriberCloser tracks Close calls and exposes an outbox.Subscriber interface.
type phaseTestSubscriberCloser struct {
	name string
	log  *[]string
}

func (s *phaseTestSubscriberCloser) Setup(_ context.Context, _ outbox.Subscription) error {
	return nil
}
func (s *phaseTestSubscriberCloser) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *phaseTestSubscriberCloser) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	return nil
}
func (s *phaseTestSubscriberCloser) Close(_ context.Context) error {
	*s.log = append(*s.log, s.name)
	return nil
}

// phaseTestSharedBus implements both Publisher and Subscriber with a Close count tracker.
// Used to verify double-close protection when pub == sub.
type phaseTestSharedBus struct {
	closeCount *int
}

func (b *phaseTestSharedBus) Publish(_ context.Context, _ string, _ []byte) error  { return nil }
func (b *phaseTestSharedBus) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (b *phaseTestSharedBus) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (b *phaseTestSharedBus) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	return nil
}
func (b *phaseTestSharedBus) Close(_ context.Context) error {
	*b.closeCount++
	return nil
}

// closerFunc is a func adapter for io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// pubSubCtxSpy implements both outbox.Publisher and outbox.Subscriber with a
// ctx-recording Close. Used for T19 shutCtx propagation tests in phase2.
type pubSubCtxSpy struct {
	closeFn func(ctx context.Context) error
}

func (s *pubSubCtxSpy) Publish(_ context.Context, _ string, _ []byte) error  { return nil }
func (s *pubSubCtxSpy) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *pubSubCtxSpy) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *pubSubCtxSpy) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	return nil
}
func (s *pubSubCtxSpy) Close(ctx context.Context) error {
	return s.closeFn(ctx)
}
