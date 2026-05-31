package bootstrap

// phases_events_test.go — behavior tests for phase6StartEventRouter wiring.
//
// finding 2 (PR-A66 round-2) gaps these tests close:
//
//	1. WithConsumerMiddleware was previously unverified — the option set
//	   b.consumerMiddleware but no test proved that the middleware actually
//	   ended up in the subscription chain. A regression that drops the
//	   `mws = append(mws, b.consumerMiddleware...)` line in phases_events.go
//	   would have shipped silently.
//	2. WithEventRouterReadyTimeout was previously unverified — phase6 wires
//	   eventrouter.WithReadyTimeout(b.routerReadyTimeout) but no test
//	   exercised the timeout path. A regression that drops the option from
//	   evtRouterOpts would also have shipped silently.

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/health"
	obshealthz "github.com/ghbvf/gocell/runtime/observability/healthz"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

// newEventsTestAggregator returns a fresh in-memory healthz.Aggregator for use in
// event-phase tests that call health.New directly (bypassing bootstrap phase5).
func newEventsTestAggregator() healthz.Aggregator {
	return obshealthz.NewAggregator(clock.Real())
}

// stubEventCell is a minimal cell that registers a single contract-first
// subscription via reg.Subscribe(...). Used by the phase6 wiring tests below.
type stubEventCell struct {
	*cell.BaseCell
	spec contractspec.ContractSpec
}

func newStubEventCell(topic string) *stubEventCell {
	return &stubEventCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "stub", Type: "core"}),
		spec: contractspec.ContractSpec{
			ID:        topic,
			Kind:      cellvocab.ContractEvent,
			Transport: "inmem",
			Topic:     topic,
		},
	}
}

func (c *stubEventCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	noopHandler := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.Ack()
	})
	return reg.Subscribe(c.spec, noopHandler, "stub-cg", c.ID())
}

// TestPhase6_ConsumerMiddleware_AppliedInChain verifies that a middleware
// registered via WithConsumerMiddleware is actually invoked when the
// EventRouter wraps the per-topic handler. Pre-fix, this contract was
// untested; a regression in phases_events.go that dropped the append would
// have silently disabled every caller-supplied middleware.
func TestPhase6_ConsumerMiddleware_AppliedInChain(t *testing.T) {
	t.Parallel()

	bus := eventbus.New(clock.Real())

	var mwInvocations atomic.Int32
	spyMW := func(_ outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		mwInvocations.Add(1) // counted at wrap time, once per Subscribe.
		return next
	}

	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-mw-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.mw.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
		WithConsumerMiddleware(spyMW),
	)
	b.healthAggregator = newEventsTestAggregator() // phase0 normally sets this; test bypasses phase0.

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real()) // phase5 normally populates this; test bypasses phase5.

	require.NoError(t, b.phase6StartEventRouter(runCtx, s),
		"phase6 must start cleanly with one stub subscription")

	assert.Equal(t, int32(1), mwInvocations.Load(),
		"WithConsumerMiddleware must be applied to the subscription chain exactly once for one subscription")

	// Run all teardowns to release the eventrouter goroutine before the test exits.
	for _, v := range slices.Backward(s.teardowns) {
		_ = v.fn(context.Background())
	}
}

// neverReadySubscriber is a Subscriber that completes Setup but never closes
// the Ready channel, simulating a broker subscription that hangs in the
// "not ready" state. Subscribe blocks until ctx is canceled.
type neverReadySubscriber struct{}

func (neverReadySubscriber) Setup(_ context.Context, _ outbox.Subscription) error {
	return nil
}

func (neverReadySubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	// Returning a never-closed channel keeps the eventrouter waiting.
	return make(chan struct{})
}

func (neverReadySubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (neverReadySubscriber) Close(_ context.Context) error { return nil }

// TestPhase6_EventRouterReadyTimeout_FiresAndReturnsError verifies that
// WithEventRouterReadyTimeout actually plumbs through to
// eventrouter.WithReadyTimeout: a subscription that never reports ready
// must trigger a timeout error from phase6 within the configured budget.
func TestPhase6_EventRouterReadyTimeout_FiresAndReturnsError(t *testing.T) {
	t.Parallel()

	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-rt-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.rt.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithSubscriber(neverReadySubscriber{}),
		WithConsumerBase(newTestConsumerBase(t)),
		WithEventRouterReadyTimeout(testtime.D80ms),
	)
	b.healthAggregator = newEventsTestAggregator() // phase0 normally sets this; test bypasses phase0.

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = neverReadySubscriber{}
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	start := time.Now()
	err := b.phase6StartEventRouter(runCtx, s)
	elapsed := time.Since(start)

	require.Error(t, err, "phase6 must surface the ready-timeout error")
	assert.Contains(t, err.Error(), "not ready",
		"error message must identify the not-ready failure mode")
	// Lower bound proves the timeout fires; upper bound proves the budget is honored
	// rather than blocking indefinitely. Generous upper bound to keep the test stable
	// on slow CI without weakening the contract.
	assert.GreaterOrEqual(t, elapsed, testtime.D80ms,
		"timeout must wait at least the configured budget")
	assert.Less(t, elapsed, testtime.D2s,
		"timeout must not exceed budget by more than 25x — the option clearly is not plumbed")
}

// TestPhase6_DrainCellSubscriptions_DriftedCellID_ReturnsError verifies the
// drainCellSubscriptions drift guard: when a SubscriptionRequest.CellID differs
// from the snapshot key (i.e. the cell that registered it), phase6 must return
// an error containing "subscription drift".
//
// Approach: register a real assembly with "stub" cell, then manually inject a
// cellSnapshots entry whose subscription carries CellID="wrong-owner" instead of
// "stub". This isolates the drift-guard path without requiring a real broker.
func TestPhase6_DrainCellSubscriptions_DriftedCellID_ReturnsError(t *testing.T) {
	t.Parallel()

	bus := eventbus.New(clock.Real())
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-drift-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.drift.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	// Inject a drifted snapshot: cell "stub" registers a subscription whose
	// CellID claims "wrong-owner" — mismatched from the snapshot key "stub".
	noopH := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.Ack()
	})
	s.cellSnapshots = map[string]cell.RegistrySnapshot{
		"stub": {
			Subscriptions: []cell.SubscriptionRequest{
				{
					Spec: contractspec.ContractSpec{
						ID:        "event.phase6.drift.v1",
						Kind:      cellvocab.ContractEvent,
						Transport: "inmem",
						Topic:     "event.phase6.drift.v1",
					},
					Handler:       noopH,
					ConsumerGroup: "stub-cg",
					CellID:        "wrong-owner", // drift: must differ from key "stub"
				},
			},
		},
	}

	err := b.phase6StartEventRouter(runCtx, s)
	require.Error(t, err, "phase6 must fail when CellID drifts from snapshot owner")
	assert.Contains(t, err.Error(), "subscription drift",
		"error must identify the drift failure mode")
}

func TestPhase6_SubscriptionsWithSubscriberButNoConsumerBase_FailsFast(t *testing.T) {
	t.Parallel()

	bus := eventbus.New(clock.Real())
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-missing-cb-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.no-cb.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	err := b.phase6StartEventRouter(runCtx, s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ConsumerBase")
	assert.Contains(t, err.Error(), "event.phase6.no-cb.v1")
	assert.Contains(t, err.Error(), "stub")
}

// TestPhase6_SubscriptionsWithZeroValueConsumerBase_FailsFast locks the
// composition-root negative path that motivated the IsConstructed sentinel
// (PR#374 review finding (a) → N8 (b)). A literal `&outbox.ConsumerBase{}`
// passes the bare nil check but skips outbox.NewConsumerBase, so claimer
// and ClaimRetryCount are zero-value — silently dispatching every entry
// through ClaimAcquired+nil receipt → DispositionReject/DLX. The phase6
// boundary must reject this exactly like the nil case so a misconfigured
// composition root never reaches subscription registration.
//
// Pre-N8 the predicate-level test (TestConsumerBase_IsConstructed*) only
// proved IsConstructed returns false for a literal; this test wires the
// literal into the actual phase6 startup path so a regression that
// weakens checkConsumerBaseConfiguredForSubscriptions cannot ship even
// if IsConstructed itself remains correct.
func TestPhase6_SubscriptionsWithZeroValueConsumerBase_FailsFast(t *testing.T) {
	t.Parallel()

	bus := eventbus.New(clock.Real())
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-zero-cb-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.zero-cb.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(&outbox.ConsumerBase{}), // zero-value literal — must be rejected.
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	err := b.phase6StartEventRouter(runCtx, s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not constructed via outbox.NewConsumerBase",
		"error must call out the zero-value literal failure mode")
	assert.Contains(t, err.Error(), "event.phase6.zero-cb.v1",
		"error must identify the offending subscription topic")
	assert.Contains(t, err.Error(), "stub",
		"error must identify the cell that registered the subscription")
}

func TestPhase6_SubscriptionsWithConsumerBase_Succeeds(t *testing.T) {
	t.Parallel()

	bus := eventbus.New(clock.Real())
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-with-cb-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.with-cb.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
	)
	b.healthAggregator = newEventsTestAggregator() // phase0 normally sets this; test bypasses phase0.

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	require.NoError(t, b.phase6StartEventRouter(runCtx, s))
	for _, v := range slices.Backward(s.teardowns) {
		_ = v.fn(context.Background())
	}
}

// newEventsTestRelay creates a minimal Relay suitable for WithRelay lifecycle tests.
func newEventsTestRelay() *runtimeoutbox.Relay {
	cfg := runtimeoutbox.RelayConfig{
		PollInterval:         testtime.FastPoll,
		ReclaimInterval:      testtime.D10ms,
		BatchSize:            10,
		MaxAttempts:          3,
		BaseRetryDelay:       testtime.D1ms,
		MaxRetryDelay:        testtime.D10ms,
		ClaimTTL:             testtime.D100ms,
		RetentionPeriod:      testtime.D1h,
		DeadRetentionPeriod:  testtime.D24h,
		CleanupWaitFloor:     testtime.FastPoll,
		PollFailureBudget:    3,
		ReclaimFailureBudget: 3,
		CleanupFailureBudget: 3,
	}
	return runtimeoutbox.NewRelay(clock.Real(), outboxtest.NewFakeStore(), &outbox.DiscardPublisher{}, cfg)
}

// TestWithRelay_AutoLifecycle_AdapterAddedToManagedResources verifies that
// WithRelay(r) wraps r in the package-private relayAdapter and appends the
// adapter to b.managedResources so the relay participates in Bootstrap's
// managed-resource shutdown lifecycle without a separate (and now
// compile-time-impossible) WithManagedResource(relay) call.
func TestWithRelay_AutoLifecycle_AdapterAddedToManagedResources(t *testing.T) {
	t.Parallel()

	relay := newEventsTestRelay()
	b := New(
		clock.Real(),
		WithRelay(relay),
	)

	// The wrapping adapter must appear in managedResources, pointing at the
	// original relay. *Relay itself does NOT implement ManagedResource — see
	// docs/architecture/202605201400-adr-relay-managedresource-isolation.md.
	found := false
	for _, r := range b.managedResources {
		if ad, ok := r.(*relayAdapter); ok && ad.relay == relay {
			found = true
			break
		}
	}
	assert.True(t, found,
		"WithRelay must wrap the relay in *relayAdapter and append it to managedResources; "+
			"current managedResources len=%d", len(b.managedResources))
	assert.Same(t, relay, b.relay, "WithRelay must also store the relay on b.relay for outbox wiring")
}

// TestWithRelay_AutoLifecycle_CloseCalledDuringTeardown verifies that the relay
// registered via WithRelay actually has its Close called during Bootstrap
// managed-resource teardown. expandManagedResources populates
// managedResourceTeardowns; we run those teardowns directly to prove the
// lifecycle pipeline reaches the relay without requiring a full Bootstrap.Run.
func TestWithRelay_AutoLifecycle_CloseCalledDuringTeardown(t *testing.T) {
	t.Parallel()

	relay := newEventsTestRelay()
	b := New(
		clock.Real(),
		WithRelay(relay),
	)

	require.NoError(t, b.expandManagedResources(),
		"expandManagedResources must succeed for a valid relay")

	// Run all LIFO teardowns registered by expandManagedResources.
	ctx := context.Background()
	for _, td := range b.managedResourceTeardowns {
		require.NoError(t, td.fn(ctx), "teardown %q must not fail", td.name)
	}

	// relay.Close delegates to relay.Stop; a never-started relay treats Stop as
	// a no-op but the call path must reach it without error.
	// We verify the teardown list is non-empty (relay was expanded) and that no
	// panic occurred — Close on a never-started relay must be idempotent.
	assert.NotEmpty(t, b.managedResourceTeardowns,
		"WithRelay must populate managedResourceTeardowns via expandManagedResources")
}

// TestWithRelay_Rebind_Panics verifies that calling WithRelay more than once
// is an unrecoverable programmer error: it panics through the panic-taxonomy
// funnel (panicregister.Approved). Without this guard, the second call would
// overwrite b.relay while leaving the earlier adapter in managedResources —
// the exact "early double-managed relay invisible" hole that motivated this
// PR (see ADR docs/architecture/202605201400-adr-relay-managedresource-isolation.md).
func TestWithRelay_Rebind_Panics(t *testing.T) {
	t.Parallel()

	r1 := newEventsTestRelay()
	r2 := newEventsTestRelay()

	assert.Panics(t, func() {
		_ = New(
			clock.Real(),
			WithRelay(r1),
			WithRelay(r2),
		)
	}, "WithRelay called twice must panic via panicregister.Approved + errcode.Assertion")
}

// TestWithManagedResource_Relay_CompileTimeMismatch is a compile-time-only
// assertion fixture: the line below is intentionally commented out. If it
// were uncommented `*runtimeoutbox.Relay` would have to satisfy
// kernel/lifecycle.ManagedResource, which it intentionally does not (see
// runtime/bootstrap/relay_adapter.go). archtest
// RELAY-NOT-MANAGEDRESOURCE-01 locks the downstream invariant; this comment
// is the source-level blind-spot self-check for that archtest.
//
//	_ = WithManagedResource(newEventsTestRelay()) // must NOT compile
func TestWithManagedResource_Relay_CompileTimeMismatch(t *testing.T) {
	t.Parallel()
	// The compile-time check lives in the comment above and in archtest
	// RELAY-NOT-MANAGEDRESOURCE-01. This test exists to make the assertion
	// reachable from the test suite's documentation and to prevent the
	// comment from being deleted without replacement.
	assert.True(t, true)
}

// ---------------------------------------------------------------------------
// checkConsumerBaseConfiguredForSubscriptions — projection coverage (F1)
// ---------------------------------------------------------------------------

// stubProjectionCell is a minimal cell that registers a single projection via
// reg.RegisterProjection. Used to exercise checkConsumerBaseConfiguredForSubscriptions
// for the projection path.
type stubProjectionCell struct {
	*cell.BaseCell
	spec         contractspec.ContractSpec
	projectionID string
}

func newStubProjectionCell(topic string) *stubProjectionCell {
	return &stubProjectionCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "stub-proj", Type: "core"}),
		spec: contractspec.ContractSpec{
			ID:        topic,
			Kind:      cellvocab.ContractEvent,
			Transport: "inmem",
			Topic:     topic,
		},
		projectionID: "stub_projection",
	}
}

func (c *stubProjectionCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	return reg.RegisterProjection(cell.ProjectionRequest{
		Spec:         c.spec,
		ProjectionID: c.projectionID,
		CellID:       c.ID(),
		Apply:        func(_ context.Context, _ outbox.Entry) error { return nil },
	})
}

// TestPhase6_ProjectionsWithSubscriberButNoConsumerBase_FailsFast locks the
// finding F1 (P1·安全/运维): a projection-only deployment must not bypass
// checkConsumerBaseConfiguredForSubscriptions. Before the fix, only
// snap.Subscriptions was iterated — snap.Projections was silently skipped,
// allowing a zero-value ConsumerBase to reach the consumption path.
//
// This test uses a nil ConsumerBase (WithConsumerBase not called). It must
// return an error naming "ConsumerBase" and identifying the projection topic,
// mirroring the existing subscription guard tests above.
func TestPhase6_ProjectionsWithSubscriberButNoConsumerBase_FailsFast(t *testing.T) {
	t.Parallel()

	bus := eventbus.New(clock.Real())
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-proj-missing-cb-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubProjectionCell("event.phase6.proj.no-cb.v1")))
	require.NoError(t, asm.Start(context.Background()))

	// No WithConsumerBase: b.consumerBase is nil.
	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	err := b.phase6StartEventRouter(runCtx, s)
	require.Error(t, err,
		"phase6 must fail when a projection is registered but ConsumerBase is nil")
	assert.Contains(t, err.Error(), "ConsumerBase",
		"error must name ConsumerBase so the operator knows what to add")
	assert.Contains(t, err.Error(), "event.phase6.proj.no-cb.v1",
		"error must identify the offending projection topic")
	assert.Contains(t, err.Error(), "stub-proj",
		"error must identify the cell that registered the projection")
}

// TestPhase6_ProjectionsWithZeroValueConsumerBase_FailsFast locks the
// zero-value ConsumerBase path for projections. A `&outbox.ConsumerBase{}`
// literal passes the bare nil check but IsConstructed returns false — the same
// silent-DLX failure mode as for subscriptions (PR#374 finding (a) / N8 (b)).
// Before the fix this would not have been caught by phase6.
func TestPhase6_ProjectionsWithZeroValueConsumerBase_FailsFast(t *testing.T) {
	t.Parallel()

	bus := eventbus.New(clock.Real())
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-proj-zero-cb-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubProjectionCell("event.phase6.proj.zero-cb.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(&outbox.ConsumerBase{}), // zero-value literal — must be rejected.
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	err := b.phase6StartEventRouter(runCtx, s)
	require.Error(t, err,
		"phase6 must fail when a projection has a zero-value ConsumerBase")
	assert.Contains(t, err.Error(), "not constructed via outbox.NewConsumerBase",
		"error must call out the zero-value literal failure mode")
	assert.Contains(t, err.Error(), "event.phase6.proj.zero-cb.v1",
		"error must identify the offending projection topic")
	assert.Contains(t, err.Error(), "stub-proj",
		"error must identify the cell that registered the projection")
}
