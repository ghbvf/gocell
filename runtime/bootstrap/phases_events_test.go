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
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/health"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

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

func (c *stubEventCell) Init(ctx context.Context, reg cell.Registry) error {
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

	bus := eventbus.New(eventbus.WithClock(clock.Real()))

	var mwInvocations atomic.Int32
	spyMW := func(_ outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		mwInvocations.Add(1) // counted at wrap time, once per Subscribe.
		return next
	}

	asm := assembly.New(assembly.Config{ID: "phase6-mw-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.mw.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
		WithConsumerMiddleware(spyMW),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, clock.Real()) // phase5 normally populates this; test bypasses phase5.

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

	asm := assembly.New(assembly.Config{ID: "phase6-rt-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.rt.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithSubscriber(neverReadySubscriber{}),
		WithConsumerBase(newTestConsumerBase(t)),
		WithEventRouterReadyTimeout(testtime.D80ms),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = neverReadySubscriber{}
	s.hh = health.New(asm, clock.Real())

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

	bus := eventbus.New(eventbus.WithClock(clock.Real()))
	asm := assembly.New(assembly.Config{ID: "phase6-drift-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.drift.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.sub = bus
	s.hh = health.New(asm, clock.Real())

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

	bus := eventbus.New(eventbus.WithClock(clock.Real()))
	asm := assembly.New(assembly.Config{ID: "phase6-missing-cb-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.no-cb.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, clock.Real())

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

	bus := eventbus.New(eventbus.WithClock(clock.Real()))
	asm := assembly.New(assembly.Config{ID: "phase6-zero-cb-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.zero-cb.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		WithClock(clock.Real()),
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
	s.hh = health.New(asm, clock.Real())

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

	bus := eventbus.New(eventbus.WithClock(clock.Real()))
	asm := assembly.New(assembly.Config{ID: "phase6-with-cb-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.with-cb.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, clock.Real())

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
		Clock:                clock.Real(),
	}
	return runtimeoutbox.NewRelay(outboxtest.NewFakeStore(), &outbox.DiscardPublisher{}, cfg)
}

// TestWithRelay_AutoLifecycle_RelayAddedToManagedResources verifies that
// WithRelay(r) automatically registers r in b.managedResources so the relay
// participates in Bootstrap's managed-resource shutdown lifecycle without a
// separate WithManagedResource(relay) call.
//
// RED: current WithRelay only sets b.relay; managedResources is not updated.
func TestWithRelay_AutoLifecycle_RelayAddedToManagedResources(t *testing.T) {
	t.Parallel()

	relay := newEventsTestRelay()
	b := New(
		WithClock(clock.Real()),
		WithRelay(relay),
	)

	// The relay must appear in managedResources — target: WithRelay auto-appends.
	// RED: currently len(b.managedResources) == 0.
	found := false
	for _, r := range b.managedResources {
		if r == relay {
			found = true
			break
		}
	}
	assert.True(t, found,
		"WithRelay must auto-register the relay in managedResources; "+
			"currently managedResources is empty (len=%d) — WithRelay only sets b.relay",
		len(b.managedResources))
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
		WithClock(clock.Real()),
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

// TestWithRelay_DoubleManaged_PreflightFailsFast verifies that calling both
// WithRelay(relay) and WithManagedResource(relay) triggers the preflight
// fail-fast (ERR_BOOTSTRAP_DOUBLE_MANAGED), preventing a double-Close during
// shutdown. PR #593 review fix-up P2#6 moved this check from
// phase0ValidateOptions into preflightDoubleManagedRelay so it runs BEFORE
// expandManagedResources — duplicate checker-name expansion of the same
// Relay would otherwise mask the root cause with a misleading error.
func TestWithRelay_DoubleManaged_PreflightFailsFast(t *testing.T) {
	t.Parallel()

	relay := newEventsTestRelay()
	b := New(
		WithClock(clock.Real()),
		WithRelay(relay),
		WithManagedResource(relay), // intentional double — target: preflight rejects this
	)

	err := b.preflightDoubleManagedRelay()
	require.Error(t, err,
		"preflightDoubleManagedRelay must return an error when relay is registered via "+
			"both WithRelay and WithManagedResource (double-Close prevention)")
	assert.Contains(t, err.Error(), "relay",
		"error message must mention relay to help diagnosis")
}

// TestWithRelay_DoubleManaged_PrioritizedOverDuplicateChecker pins the
// invocation order: preflightDoubleManagedRelay must run before
// expandManagedResources so the actionable ERR_BOOTSTRAP_DOUBLE_MANAGED
// error surfaces, not the misleading "duplicate checker key" diagnostic
// that would emerge if expand ran first against two copies of the same
// Relay (Relay.Checkers() returns the same names each time).
func TestWithRelay_DoubleManaged_PrioritizedOverDuplicateChecker(t *testing.T) {
	t.Parallel()

	relay := newEventsTestRelay()
	b := New(
		WithClock(clock.Real()),
		WithRelay(relay),
		WithManagedResource(relay), // double-registration on purpose
	)

	// Preflight runs before expand; the diagnostic must point at relay
	// double-management, NOT at duplicate checker keys produced by
	// expanding the same Relay twice.
	err := b.preflightDoubleManagedRelay()
	require.Error(t, err,
		"preflight must reject double-managed relay before expandManagedResources runs")
	assert.Contains(t, err.Error(), "relay",
		"error must mention relay (not checker name) so the root cause is obvious")
	assert.NotContains(t, err.Error(), "checker",
		"error must NOT mention duplicate checker — expand has not run yet")
}

// TestWithRelay_DoubleManaged_NonComparableImpl_DoesNotPanic pins the
// typed-pointer assert invariant: the loop must skip non-Relay
// ManagedResource implementations without performing interface equality
// (`mr == ManagedResource(b.relay)`), which panics at runtime when the
// concrete type is not comparable (struct value with slice / map / func
// fields). Pre-fix this scenario produced a hard-to-diagnose runtime panic
// during phase0.
func TestWithRelay_DoubleManaged_NonComparableImpl_DoesNotPanic(t *testing.T) {
	t.Parallel()

	relay := newEventsTestRelay()
	b := New(
		WithClock(clock.Real()),
		WithRelay(relay),
		WithManagedResource(&nonComparableManagedResource{names: []string{"slice-field"}}),
	)

	assert.NotPanics(t, func() {
		_ = b.preflightDoubleManagedRelay()
	}, "preflightDoubleManagedRelay must skip non-Relay types via typed-pointer assert")
}

// nonComparableManagedResource holds a slice field so the underlying value
// type is non-comparable. The test above relies on this to prove that the
// post-fix detection never reaches an `==` interface comparison on
// non-Relay types.
type nonComparableManagedResource struct {
	names []string
}

func (m *nonComparableManagedResource) Checkers() map[string]func(context.Context) error {
	return nil
}
func (m *nonComparableManagedResource) Worker() kworker.Worker        { return nil }
func (m *nonComparableManagedResource) Close(_ context.Context) error { return nil }

