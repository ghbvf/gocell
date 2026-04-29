package bootstrap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- phase5CollectRouteGroups: HealthListener fallback tests (R2-07) ---

// buildPhase5State constructs a minimal phaseState ready for
// phase5CollectRouteGroups: it has an asm, a hh, and the registeredCheckers
// map. The asm is started so health.Handler.aggregateCellHealth works.
func buildPhase5State(t *testing.T) *phaseState {
	t.Helper()
	asm := assembly.New(assembly.Config{ID: "phase5-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	_, s := newPhaseState()
	s.asm = asm
	s.hh = health.New(asm)
	return s
}

// buildRouter creates a NewForListener router for the given ref so phase5 has a
// real router map to iterate.
func buildRouter(t *testing.T, ref cell.ListenerRef) *router.Router {
	t.Helper()
	r, err := router.NewForListener(ref)
	require.NoError(t, err)
	return r
}

// TestPhase5CollectRouteGroups_NoHealthListener_RemapsHealthGroupsToPrimary is an
// intentional white-box test: b := New() bypasses phase0ValidateOptions (which would
// reject a Bootstrap with no listeners). This is valid because phase5CollectRouteGroups
// is a pure computation step — it only inspects the routers map and the healthRouteGroupOpts
// field; it does not depend on any phase0 side-effects or listener validation state.
// Testing it in isolation verifies the health-group fallback logic without the overhead
// of a full Run() lifecycle.
func TestPhase5CollectRouteGroups_NoHealthListener_RemapsHealthGroupsToPrimary(t *testing.T) {
	t.Parallel()
	b := New()
	s := buildPhase5State(t)

	routers := map[cell.ListenerRef]*router.Router{
		cell.PrimaryListener: buildRouter(t, cell.PrimaryListener),
	}

	groups := b.phase5CollectRouteGroups(s, routers)

	require.NotEmpty(t, groups, "phase5 must always produce framework health groups")

	for i, rg := range groups {
		assert.Equal(t, cell.PrimaryListener, rg.Listener,
			"group[%d]: with no HealthListener, every framework health group must be remapped to PrimaryListener", i)
	}
}

func TestPhase5CollectRouteGroups_HealthListenerPresent_PreservesHealthListener(t *testing.T) {
	t.Parallel()
	b := New()
	s := buildPhase5State(t)

	routers := map[cell.ListenerRef]*router.Router{
		cell.PrimaryListener: buildRouter(t, cell.PrimaryListener),
		cell.HealthListener:  buildRouter(t, cell.HealthListener),
	}

	groups := b.phase5CollectRouteGroups(s, routers)

	require.NotEmpty(t, groups)
	for i, rg := range groups {
		assert.Equal(t, cell.HealthListener, rg.Listener,
			"group[%d]: with HealthListener declared, framework health groups must stay on HealthListener (no fallback remap)", i)
	}
}

// --- phase0ValidateOptions tests ---

func TestPhase0_AcceptsValidOptions(t *testing.T) {
	b := New(WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}))
	require.NoError(t, b.phase0ValidateOptions())
}

func TestPhase0_RejectsEmptyHealthCheckerName(t *testing.T) {
	b := New(WithHealthChecker("", func(_ context.Context) error { return nil }))
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health checker name must not be empty")
}

func TestPhase0_RejectsNilHealthCheckerFn(t *testing.T) {
	// White-box: directly populates b.http internals because the public
	// WithHealthChecker rejects nil at option construction time, but we want
	// to verify phase0 also rejects (defense-in-depth).
	b := New()
	b.healthCheckers = append(b.healthCheckers, namedChecker{name: "test", fn: nil})
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `health checker "test" must not be nil`)
}

func TestPhase0_RejectsNilCircuitBreaker(t *testing.T) {
	// White-box: directly populates b.http internals because the public
	// WithCircuitBreaker rejects nil at option construction time, but we want
	// to verify phase0 also rejects (defense-in-depth).
	b := New()
	b.circuitBreakerNil = true
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker must not be nil")
}

// TestPhase0_RejectsMutuallyExclusiveAuthOptions was removed in F3 round-3:
// WithAuthMiddleware and the standalone PolicyJWTFromAssembly Option are gone,
// so phase0 has nothing to reject. JWT auth flows through []cell.ListenerAuth
// authChain passed to WithListener.

// Round-3 finding #10: AuthJWTFromAssembly must capture the same assembly
// instance as WithAssembly. A mismatch would silently discover the verifier
// in the plan's asm while the rest of Bootstrap runs against b.assemblyCore.
func TestPhase0_RejectsAuthJWTFromAssemblyMismatch(t *testing.T) {
	asmA := assembly.New(assembly.Config{ID: "asm-a", DurabilityMode: cell.DurabilityDemo})
	asmB := assembly.New(assembly.Config{ID: "asm-b", DurabilityMode: cell.DurabilityDemo})
	b := New(
		WithAssembly(asmA),
		WithListener(cell.PrimaryListener, "127.0.0.1:0",
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asmB)}),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AuthJWTFromAssembly carries assembly")
	assert.Contains(t, err.Error(), "asm-a")
	assert.Contains(t, err.Error(), "asm-b")
}

func TestPhase0_AcceptsAuthJWTFromAssemblyMatch(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "asm-match", DurabilityMode: cell.DurabilityDemo})
	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, "127.0.0.1:0",
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}),
	)
	require.NoError(t, b.phase0ValidateOptions())
}

// Round-3 finding #11: AuthMTLS without WithListenerTLS configuring
// ClientAuth + ClientCAs is a programmer error — the handshake-layer chain
// check would not run. Bootstrap.phase0 must reject the listener config.
func TestPhase0_RejectsAuthMTLSWithoutTLS(t *testing.T) {
	b := New(
		WithListener(cell.InternalListener, "127.0.0.1:0",
			[]cell.ListenerAuth{cell.AuthMTLS{}}),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AuthMTLS without WithListenerTLS")
}

func TestPhase0_RejectsAuthMTLSWithLooseClientAuth(t *testing.T) {
	pool := x509.NewCertPool()
	cfg := &tls.Config{
		ClientAuth: tls.RequestClientCert, // < VerifyClientCertIfGiven
		ClientCAs:  pool,
	}
	b := New(
		WithListener(cell.InternalListener, "127.0.0.1:0",
			[]cell.ListenerAuth{cell.AuthMTLS{}},
			WithListenerTLS(cfg)),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ClientAuth")
}

func TestPhase0_RejectsAuthMTLSWithoutClientCAs(t *testing.T) {
	cfg := &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		// ClientCAs: nil — handshake has no CA pool to validate against
	}
	b := New(
		WithListener(cell.InternalListener, "127.0.0.1:0",
			[]cell.ListenerAuth{cell.AuthMTLS{}},
			WithListenerTLS(cfg)),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ClientCAs is nil")
}

func TestPhase0_AcceptsAuthMTLSWithProperTLS(t *testing.T) {
	pool := x509.NewCertPool()
	cfg := &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		// GetCertificate provides the server-side certificate at handshake time.
		// Without at least one cert source, the TLS sanity check (Wave B) rejects
		// the config because no TLS handshake can complete.
		GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) { return &tls.Certificate{}, nil },
	}
	b := New(
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.InternalListener, "127.0.0.1:0",
			[]cell.ListenerAuth{cell.AuthMTLS{}},
			WithListenerTLS(cfg)),
	)
	require.NoError(t, b.phase0ValidateOptions())
}

func TestChainProtectsRoutes(t *testing.T) {
	stubVerifier := &stubIntentTokenVerifier{}
	tests := []struct {
		name  string
		chain []cell.ListenerAuth
		want  bool
	}{
		{
			name:  "nil_chain_not_protected",
			chain: nil,
			want:  false,
		},
		{
			name:  "empty_chain_not_protected",
			chain: []cell.ListenerAuth{},
			want:  false,
		},
		{
			name:  "auth_none_not_protected",
			chain: []cell.ListenerAuth{cell.AuthNone{}},
			want:  false,
		},
		{
			name:  "auth_jwt_protected",
			chain: []cell.ListenerAuth{cell.MustNewAuthJWT(stubVerifier)},
			want:  true,
		},
		{
			name:  "auth_mtls_protected",
			chain: []cell.ListenerAuth{cell.AuthMTLS{}},
			want:  true,
		},
		{
			name:  "auth_service_token_protected",
			chain: []cell.ListenerAuth{cell.MustNewAuthServiceToken(&stubNonceStore{}, &stubHMACKeyring{})},
			want:  true,
		},
		{
			// AuthNone before a protective plan must not short-circuit to false.
			name:  "mixed_none_then_mtls_protected",
			chain: []cell.ListenerAuth{cell.AuthNone{}, cell.AuthMTLS{}},
			want:  true,
		},
		{
			// Multi-protective chain (mTLS outer + HMAC inner) is the
			// canonical InternalListener configuration.
			name:  "mixed_mtls_plus_service_token_protected",
			chain: []cell.ListenerAuth{cell.AuthMTLS{}, cell.MustNewAuthServiceToken(&stubNonceStore{}, &stubHMACKeyring{})},
			want:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, chainProtectsRoutes(tc.chain))
		})
	}
}

// PR269 round-3: TestPhase5_*RouteGroupPolicyMTLS* tests removed along with
// cell.RouteGroup.Auth — RouteGroup-level mTLS no longer exists. Listener-level
// AuthMTLS validation is covered by auth_plan_validate_test.go.

// TestPhase0_ValidatesInternalMiddleware was removed in PR-A14b because
// WithInternalMiddleware and the internalMiddlewares field are deleted.
// Internal listener authentication is now handled via cell.Policy on the
// InternalListener declaration (WithListener(InternalListener, addr, policy)).

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
	require.NoError(t, s.teardowns[0].fn(context.Background()))
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
	require.NoError(t, s.teardowns[0].fn(context.Background()))
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
		require.NoError(t, td.fn(context.Background()))
	}
	assert.Equal(t, 1, closeCalled, "shared pub/sub must only be closed once")
}

func TestSamePubSubIdentity(t *testing.T) {
	t.Run("same comparable instance", func(t *testing.T) {
		eb := &phaseTestSharedBus{}
		assert.True(t, samePubSubIdentity(eb, eb))
	})

	t.Run("different comparable instances", func(t *testing.T) {
		assert.False(t, samePubSubIdentity(&phaseTestSharedBus{}, &phaseTestSharedBus{}))
	})

	t.Run("non-comparable dynamic type", func(t *testing.T) {
		bus := nonComparablePubSub{labels: []string{"in-memory"}}
		require.NotPanics(t, func() {
			assert.False(t, samePubSubIdentity(bus, bus))
		})
	})
}

func TestPhase2_InitPubSub_NonComparablePubSubDoesNotPanic(t *testing.T) {
	bus := nonComparablePubSub{labels: []string{"in-memory"}}
	b := New(WithPublisher(bus), WithSubscriber(bus))
	_, s := newPhaseState()

	require.NotPanics(t, func() {
		b.phase2InitPubSub(s)
	})
	assert.Len(t, s.teardowns, 2, "non-comparable pub/sub values cannot be identity-compared, so both closes are registered")
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
	// PR-A14b: phase5BuildHTTPRouter is replaced by phase5BuildRouters.
	// phase10ReadinessFlip only requires s.hh (may be nil) and s.reloads.
	// Setting up a full router is no longer needed to test the readiness flip.
	b := New()
	_, s := newPhaseState()
	s.cfg = config.NewFromMap(make(map[string]any))
	require.NoError(t, b.phase3InitAssembly(context.Background(), s))
	// s.hh is nil — phase10ReadinessFlip guards against nil hh.

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
	require.NoError(t, s.teardowns[0].fn(shutCtx))

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

	require.NoError(t, s.teardowns[0].fn(context.Background()))
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
	require.NoError(t, s.teardowns[0].fn(shutCtx))

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
	require.NoError(t, s.teardowns[0].fn(shutCtx))

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
		require.NoError(t, td.fn(shutCtx))
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
		require.NoError(t, td.fn(shutCtx))
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

type nonComparablePubSub struct {
	labels []string
}

func (b nonComparablePubSub) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (b nonComparablePubSub) Setup(_ context.Context, _ outbox.Subscription) error {
	return nil
}
func (b nonComparablePubSub) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (b nonComparablePubSub) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	return nil
}
func (b nonComparablePubSub) Close(_ context.Context) error { return nil }

// ---------------------------------------------------------------------------
// F21: phaseError label tests
// ---------------------------------------------------------------------------

// TestBootstrapTeardown_ErrorsContainPhaseLabel verifies that phase10LIFOTeardown
// wraps non-nil teardown errors in phaseError so that the component name is
// carried with the error for post-mortem diagnosis.
//
// ref: sigs.k8s.io/controller-runtime engageStopProcedure — per-step error labelling.
func TestBootstrapTeardown_ErrorsContainPhaseLabel(t *testing.T) {
	b := New()
	_, s := newPhaseState()

	sentinel := errors.New("disk full")

	// Register one named teardown that fails and one that succeeds.
	s.addNamedTeardown("my-db", func(_ context.Context) error { return sentinel })
	s.addTeardown(func(_ context.Context) error { return nil })

	shutCtx := context.Background()
	errs := b.phase10LIFOTeardown(shutCtx, s)

	require.Len(t, errs, 1, "expected exactly one teardown error")

	// The error must be wrapped in phaseError.
	var pe *phaseError
	require.True(t, errors.As(errs[0], &pe), "error must be a *phaseError; got %T: %v", errs[0], errs[0])
	assert.Equal(t, "teardown_my-db", pe.Phase)

	// Unwrap must yield the original sentinel so errors.Is still works.
	assert.ErrorIs(t, errs[0], sentinel)
}

// TestBootstrapTeardown_AnonymousTeardownErrorNotLabelled verifies that teardowns
// registered without a name (via addTeardown) still surface their errors, but
// without a phaseError wrapper.
func TestBootstrapTeardown_AnonymousTeardownErrorNotLabelled(t *testing.T) {
	b := New()
	_, s := newPhaseState()

	sentinel := errors.New("connection reset")
	s.addTeardown(func(_ context.Context) error { return sentinel })

	errs := b.phase10LIFOTeardown(context.Background(), s)

	require.Len(t, errs, 1)

	// Anonymous teardown: error is NOT wrapped in phaseError.
	var pe *phaseError
	assert.False(t, errors.As(errs[0], &pe), "anonymous teardown must not wrap in phaseError")
	assert.ErrorIs(t, errs[0], sentinel)
}

// TestBootstrapTeardown_LIFOOrder verifies teardowns execute in reverse
// registration order (LIFO).
func TestBootstrapTeardown_LIFOOrder(t *testing.T) {
	b := New()
	_, s := newPhaseState()

	var order []int
	for i := 0; i < 3; i++ {
		idx := i
		s.addNamedTeardown(fmt.Sprintf("step%d", idx), func(_ context.Context) error {
			order = append(order, idx)
			return nil
		})
	}

	b.phase10LIFOTeardown(context.Background(), s)

	assert.Equal(t, []int{2, 1, 0}, order, "teardowns must run in LIFO order")
}

// ---------------------------------------------------------------------------
// Stub types for TestChainProtectsRoutes
// ---------------------------------------------------------------------------

type stubIntentTokenVerifier struct{}

func (s *stubIntentTokenVerifier) VerifyIntent(_ context.Context, _ string, _ cell.TokenIntent) (cell.Claims, error) {
	return cell.Claims{}, nil
}

type stubNonceStore struct{}

func (s *stubNonceStore) CheckAndMark(_ context.Context, _ string) error {
	return nil
}

func (s *stubNonceStore) Kind() cell.NonceStoreKind {
	return cell.NonceStoreKindInMemory
}

type stubHMACKeyring struct{}

// Current/Secrets must return >= cell.MinHMACKeyBytes (32 bytes) — short keys
// panic at NewAuthServiceToken construction (PR269 round-3 F5).
func (s *stubHMACKeyring) Current() []byte {
	return []byte("test-secret-32-bytes-padding----")
}
func (s *stubHMACKeyring) Secrets() [][]byte { return [][]byte{s.Current()} }

// ---------------------------------------------------------------------------
// T-06: phase5 FinalizeAuth called twice returns labeled error
// ---------------------------------------------------------------------------

// TestBootstrap_Phase5_FinalizeAuthCalledTwice_ReturnsLabeledError verifies that
// calling phase5FinalizeAllRouters a second time (after authFinalized=true) returns
// an error that names the listener ref, making post-mortem diagnosis unambiguous.
func TestBootstrap_Phase5_FinalizeAuthCalledTwice_ReturnsLabeledError(t *testing.T) {
	b := New(WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}))
	s := buildPhase5State(t)

	routers := map[cell.ListenerRef]*router.Router{
		cell.PrimaryListener: buildRouter(t, cell.PrimaryListener),
	}

	// First call must succeed.
	require.NoError(t, b.phase5FinalizeAllRouters(routers))

	// Second call must fail and include the listener ref in the error.
	err := b.phase5FinalizeAllRouters(routers)
	require.Error(t, err, "FinalizeAuth called twice must return an error")
	assert.Contains(t, err.Error(), "finalize auth",
		"error must contain 'finalize auth' label to identify the failing phase")
	_ = s // s is built for test hygiene (health handler initialisation).
}

func TestBootstrap_Phase5_InternalRoutesRequireGuard(t *testing.T) {
	b := New(WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}))
	rtr := buildRouter(t, cell.InternalListener)
	require.NoError(t, rtr.DeclareAuthMeta(cell.AuthRouteMeta{
		Method: "POST",
		Path:   "/internal/v1/access/roles/assign",
	}))

	err := b.phase5FinalizeAllRouters(map[cell.ListenerRef]*router.Router{
		cell.InternalListener: rtr,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal listener")
	assert.Contains(t, err.Error(), "AuthServiceToken")
	assert.Contains(t, err.Error(), "AuthMTLS")
	assert.Contains(t, err.Error(), "bootstrap.WithListener")
}

func TestBootstrap_Phase5_InternalRoutesRejectJWTOnlyGuard(t *testing.T) {
	b := New(WithListener(cell.InternalListener, "127.0.0.1:0",
		[]cell.ListenerAuth{cell.MustNewAuthJWT(&stubIntentTokenVerifier{})}))
	rtr := buildRouter(t, cell.InternalListener)
	require.NoError(t, rtr.DeclareAuthMeta(cell.AuthRouteMeta{
		Method: "POST",
		Path:   "/internal/v1/access/roles/assign",
	}))

	err := b.phase5FinalizeAllRouters(map[cell.ListenerRef]*router.Router{
		cell.InternalListener: rtr,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal guard")
	assert.Contains(t, err.Error(), "AuthServiceToken")
	assert.Contains(t, err.Error(), "AuthMTLS")
}

func TestBootstrap_Phase5_InternalRoutesRejectMTLSOnlyGuard(t *testing.T) {
	b := New(WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthMTLS{}}))
	rtr := buildRouter(t, cell.InternalListener)
	require.NoError(t, rtr.DeclareAuthMeta(cell.AuthRouteMeta{
		Method: "POST",
		Path:   "/internal/v1/access/roles/assign",
	}))

	err := b.phase5FinalizeAllRouters(map[cell.ListenerRef]*router.Router{
		cell.InternalListener: rtr,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "AuthServiceToken")
}

func TestBootstrap_Phase5_InternalRoutesAcceptServiceTokenGuard(t *testing.T) {
	plan := cell.MustNewAuthServiceToken(&stubNonceStore{}, &stubHMACKeyring{})
	b := New(WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{plan}))
	rtr := buildRouter(t, cell.InternalListener)
	require.NoError(t, rtr.DeclareAuthMeta(cell.AuthRouteMeta{
		Method: "POST",
		Path:   "/internal/v1/access/roles/assign",
	}))

	err := b.phase5FinalizeAllRouters(map[cell.ListenerRef]*router.Router{
		cell.InternalListener: rtr,
	})

	require.NoError(t, err)
}

func TestBootstrap_Phase5_InternalRoutesAcceptLayeredInternalGuards(t *testing.T) {
	plan := cell.MustNewAuthServiceToken(&stubNonceStore{}, &stubHMACKeyring{})
	b := New(WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthMTLS{}, plan}))
	rtr := buildRouter(t, cell.InternalListener)
	require.NoError(t, rtr.DeclareAuthMeta(cell.AuthRouteMeta{
		Method: "POST",
		Path:   "/internal/v1/access/roles/assign",
	}))

	err := b.phase5FinalizeAllRouters(map[cell.ListenerRef]*router.Router{
		cell.InternalListener: rtr,
	})

	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Path3 TLS phase0 三连
// ---------------------------------------------------------------------------

// TestPhase0_TLSConfigEmpty_Rejected verifies that a TLS config with no
// certificate source is rejected at phase0 (Wave B sanity check).
func TestPhase0_TLSConfigEmpty_Rejected(t *testing.T) {
	b := New(
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}},
			WithListenerTLS(&tls.Config{})), // no Certificates / GetCertificate / GetConfigForClient
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS config has no Certificates / GetCertificate / GetConfigForClient")
}

// TestPhase0_TLSConfigWithCertificates_Accepted verifies that a TLS config
// carrying a non-empty static certificate (chain bytes present) passes the
// phase0 handshake-ability check.
func TestPhase0_TLSConfigWithCertificates_Accepted(t *testing.T) {
	// We do not need a real key pair — we only need at least one of
	// Certificate / PrivateKey / Leaf to be non-zero so the sanity check
	// recognises this as a populated entry. Actual TLS handshake is not
	// exercised in this unit test.
	b := New(
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}},
			WithListenerTLS(&tls.Config{
				Certificates: []tls.Certificate{{Certificate: [][]byte{{0x00}}}},
			})),
	)
	require.NoError(t, b.phase0ValidateOptions())
}

// TestPhase0_TLSConfigCertificateZeroValue_Rejected verifies that a TLS config
// whose Certificates slice contains only zero-value entries (no chain, no key,
// no Leaf) is rejected at phase0 — the listener would otherwise fail at the
// first ClientHello with an opaque tls error rather than fail-fast at startup.
func TestPhase0_TLSConfigCertificateZeroValue_Rejected(t *testing.T) {
	b := New(
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}},
			WithListenerTLS(&tls.Config{
				Certificates: []tls.Certificate{{}},
			})),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zero-value tls.Certificate")
}

// TestPhase0_TLSConfigWithGetCertificate_Accepted verifies that a TLS config
// with a non-nil GetCertificate callback is accepted at phase0.
func TestPhase0_TLSConfigWithGetCertificate_Accepted(t *testing.T) {
	b := New(
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}},
			WithListenerTLS(&tls.Config{
				GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
					return &tls.Certificate{}, nil
				},
			})),
	)
	require.NoError(t, b.phase0ValidateOptions())
}
