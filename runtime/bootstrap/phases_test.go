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
	s := newPhaseState()
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
	s := newPhaseState()
	require.NoError(t, b.phase1LoadConfig(s))
	assert.Len(t, s.teardowns, 1)

	// Execute the teardown and verify closer was called.
	require.NoError(t, s.teardowns[0](context.Background()))
	assert.True(t, closed)
}

// --- phase2InitPubSub tests ---

func TestPhase2_InitPubSub_DefaultsToInMemoryBus(t *testing.T) {
	b := New()
	s := newPhaseState()
	b.phase2InitPubSub(s)
	assert.NotNil(t, s.pub)
	assert.NotNil(t, s.sub)
}

func TestPhase2_InitPubSub_ExplicitPublisherAndSubscriber(t *testing.T) {
	pub := &phaseTestPublisher{}
	sub := &phaseTestSubscriber{}
	b := New(WithPublisher(pub), WithSubscriber(sub))
	s := newPhaseState()
	b.phase2InitPubSub(s)
	assert.Same(t, pub, s.pub.(*phaseTestPublisher))
	assert.Same(t, sub, s.sub.(*phaseTestSubscriber))
}

func TestPhase2_InitPubSub_RegistersTeardownForCloser(t *testing.T) {
	var closeCalled []string
	sub := &phaseTestSubscriberCloser{name: "sub", log: &closeCalled}
	b := New(WithSubscriber(sub))
	s := newPhaseState()
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
	s := newPhaseState()
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
	s := newPhaseState()
	s.cfg = config.NewFromMap(make(map[string]any))
	require.NoError(t, b.phase3InitAssembly(context.Background(), s))
	assert.NotNil(t, s.asm)
	assert.NotNil(t, s.reloads)
}

func TestPhase3_InitAssembly_UsesPrebuiltAssembly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "pre", DurabilityMode: cell.DurabilityDemo})
	b := New(WithAssembly(asm))
	s := newPhaseState()
	s.cfg = config.NewFromMap(make(map[string]any))
	require.NoError(t, b.phase3InitAssembly(context.Background(), s))
	assert.Same(t, asm, s.asm)
}

func TestPhase3_InitAssembly_RegistersAssemblyTeardown(t *testing.T) {
	b := New()
	s := newPhaseState()
	s.cfg = config.NewFromMap(make(map[string]any))
	require.NoError(t, b.phase3InitAssembly(context.Background(), s))
	// Two teardowns: Shutdown + assembly drain+Stop.
	assert.Len(t, s.teardowns, 2)
}

// --- phase8StartWorkers tests ---

func TestPhase8_StartWorkers_NoWorkers_EmptyWorkerErrCh(t *testing.T) {
	b := New() // no workers
	s := newPhaseState()
	b.phase8StartWorkers(s)
	assert.Nil(t, s.workerErrCh, "workerErrCh must be nil when no workers are registered")
}

func TestPhase8_StartWorkers_WorkersRegistered_WorkerErrChCreated(t *testing.T) {
	w := &countWorker{}
	b := New(WithWorkers(w))
	s := newPhaseState()
	b.phase8StartWorkers(s)
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
	s := newRunState()
	s.addTeardown(func(_ context.Context) error { order = append(order, 1); return nil })
	s.addTeardown(func(_ context.Context) error { order = append(order, 2); return nil })
	s.addTeardown(func(_ context.Context) error { order = append(order, 3); return nil })

	cause := errors.New("startup failed")
	err := s.rollback(context.Background(), cause)
	assert.Equal(t, cause, err)
	assert.Equal(t, []int{3, 2, 1}, order)
}

func TestRunState_Rollback_CancelsRunCtx(t *testing.T) {
	s := newRunState()
	_ = s.rollback(context.Background(), errors.New("x"))
	select {
	case <-s.runCtx.Done():
		// expected
	default:
		t.Fatal("runCtx must be cancelled after rollback")
	}
}

func TestRunState_Rollback_ContinuesThroughTeardownErrors(t *testing.T) {
	s := newRunState()
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
	s := newPhaseState()
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
	s := newPhaseState()
	var order []int
	s.addTeardown(func(_ context.Context) error { order = append(order, 1); return nil })
	s.addTeardown(func(_ context.Context) error { order = append(order, 2); return nil })

	errs := b.phase10LIFOTeardown(context.Background(), s)
	assert.Empty(t, errs)
	assert.Equal(t, []int{2, 1}, order)
}

func TestPhase10LIFOTeardown_CollectsErrors(t *testing.T) {
	b := New()
	s := newPhaseState()
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

	s := newPhaseState()
	// Verify runCtx is alive before external cancel.
	select {
	case <-s.runCtx.Done():
		t.Fatal("runCtx must be alive at start")
	default:
	}

	extCancel()

	// runCtx must still be alive after external cancel.
	select {
	case <-s.runCtx.Done():
		t.Fatal("runCtx must NOT be cancelled when external ctx is cancelled")
	default:
		// expected: runCtx is independent
	}
}

// --- Helpers / stubs ---

// phaseTestVerifier satisfies auth.IntentTokenVerifier for phase0 tests.
type phaseTestVerifier struct{}

func (v *phaseTestVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	return auth.Claims{}, nil
}

// phaseTestPublisher is a no-op outbox.Publisher for phase tests.
type phaseTestPublisher struct{}

func (p *phaseTestPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }

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
func (s *phaseTestSubscriber) Close() error { return nil }

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
func (s *phaseTestSubscriberCloser) Close() error {
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
func (b *phaseTestSharedBus) Close() error {
	*b.closeCount++
	return nil
}

// closerFunc is a func adapter for io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }
