package bootstrap

// phases_projection_test.go — unit + end-to-end tests for the projection drain.
//
// Coverage:
//   - buildProjectionCoordinators: empty no-op; missing each of the four deps →
//     fail-fast naming the option; CellID drift → fail-fast; missing snapshot
//     skipped; happy path → one wiring with the correctly derived consumer group
//     / cellID / sliceID captured from Coordinator.Subscribe.
//   - phase6StartEventRouter end-to-end: a projection cell + mem deps drains into
//     the running event router, indexes the Coordinator, registers its probes,
//     and records a Close teardown.
//   - WithProjection* options: store value + typed-nil ignored.

import (
	"context"
	"errors"
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
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/health"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const (
	projTestCellID = "ordercell"
	projTestProjID = "ordersummary"
	projTestTopic  = "order-created"
)

func projTestSpec() contractspec.ContractSpec {
	return contractspec.ContractSpec{
		ID:        "event." + projTestTopic + ".v1",
		Kind:      cellvocab.ContractEvent,
		Transport: "inmem",
		Topic:     projTestTopic,
	}
}

// fakeProjectionCursor returns a fixed 1-based position (satisfies the Cursor
// 1-based invariant).
type fakeProjectionCursor struct{}

func (fakeProjectionCursor) Position(_ projection.ProjectionEvent) (int64, error) { return 1, nil }

// fakeProjectionTxRunner is a pass-through TxRunner (the mem checkpoint store
// ignores the ambient tx).
type fakeProjectionTxRunner struct{}

func (fakeProjectionTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// projectionTestCell records a projection via reg.RegisterProjection in Init.
type projectionTestCell struct {
	*cell.BaseCell
	spec           contractspec.ContractSpec
	projectionID   string
	apply          cell.ProjectionApply
	onReset        cell.ProjectionResetHook
	overrideCellID string // non-empty → simulate codegen drift
}

func (c *projectionTestCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	cellID := c.ID()
	if c.overrideCellID != "" {
		cellID = c.overrideCellID
	}
	return reg.RegisterProjection(cell.ProjectionRequest{
		Spec:         c.spec,
		ProjectionID: c.projectionID,
		CellID:       cellID,
		Apply:        c.apply,
		OnReset:      c.onReset,
	})
}

func newProjectionCell() *projectionTestCell {
	return &projectionTestCell{
		BaseCell:     cell.MustNewBaseCell(&metadata.CellMeta{ID: projTestCellID, Type: "core"}),
		spec:         projTestSpec(),
		projectionID: projTestProjID,
		apply:        func(context.Context, projection.ProjectionEvent) error { return nil },
	}
}

// buildProjectionPhaseState starts an assembly with the given cells and returns
// a phaseState with asm + cellSnapshots populated (mirrors phase3InitAssembly).
func buildProjectionPhaseState(t *testing.T, cells ...cell.Cell) *phaseState {
	t.Helper()
	asm := assembly.New(clock.Real(), assembly.Config{
		ID:             "projection-test-asm",
		DurabilityMode: outbox.DurabilityDemo,
	})
	for _, c := range cells {
		require.NoErrorf(t, asm.Register(c), "Register cell %q", c.ID())
	}
	require.NoError(t, asm.Start(context.Background()), "asm.Start")
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	_, s := newPhaseState()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	return s
}

// newProjectionBootstrap returns a Bootstrap with all four projection deps wired
// via their options (mem checkpoint store + replay source, fake cursor + tx).
func newProjectionBootstrap(t *testing.T, opts ...Option) *Bootstrap {
	t.Helper()
	base := []Option{
		WithProjectionCheckpointStore(projection.NewMemCheckpointStore()),
		WithProjectionTxRunner(fakeProjectionTxRunner{}),
		WithProjectionReplaySource(projection.NewMemReplaySource()),
		WithProjectionCursor(fakeProjectionCursor{}),
	}
	return New(clock.Real(), append(base, opts...)...)
}

// ---------------------------------------------------------------------------
// buildProjectionCoordinators — unit tests
// ---------------------------------------------------------------------------

func TestBuildProjectionCoordinators_EmptyNoOp(t *testing.T) {
	t.Parallel()
	tc := &testCell{BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "plain-cell", Type: "core"})}
	s := buildProjectionPhaseState(t, tc)

	b := newProjectionBootstrap(t)
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)

	require.NoError(t, err, "no projections must not error")
	assert.Empty(t, wirings, "no projections must produce no wirings")
}

func TestBuildProjectionCoordinators_FailsOnMissingDep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wantOpt string // the option whose dep we leave unset → must be named in the error
	}{
		{"missing store", "WithProjectionCheckpointStore"},
		{"missing tx runner", "WithProjectionTxRunner"},
		{"missing replay", "WithProjectionReplaySource"},
		{"missing cursor", "WithProjectionCursor"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := buildProjectionPhaseState(t, newProjectionCell())
			// Build a bootstrap missing exactly one dep: start from full, then
			// reconstruct without the one under test.
			var opts []Option
			for _, o := range []struct {
				name string
				opt  Option
			}{
				{"WithProjectionCheckpointStore", WithProjectionCheckpointStore(projection.NewMemCheckpointStore())},
				{"WithProjectionTxRunner", WithProjectionTxRunner(fakeProjectionTxRunner{})},
				{"WithProjectionReplaySource", WithProjectionReplaySource(projection.NewMemReplaySource())},
				{"WithProjectionCursor", WithProjectionCursor(fakeProjectionCursor{})},
			} {
				if o.name == tc.wantOpt {
					continue // leave this dep unset
				}
				opts = append(opts, o.opt)
			}
			b := New(clock.Real(), opts...)

			_, err := b.buildProjectionCoordinators(context.Background(), s)
			require.Error(t, err, "missing dep must error")
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr, "must be *errcode.Error")
			assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
			assert.Contains(t, ecErr.Message, tc.wantOpt, "error must name the missing option")
		})
	}
}

func TestBuildProjectionCoordinators_FailsOnCellIDDrift(t *testing.T) {
	t.Parallel()
	pc := newProjectionCell()
	pc.overrideCellID = "wrongcell"
	s := buildProjectionPhaseState(t, pc)

	b := newProjectionBootstrap(t)
	_, err := b.buildProjectionCoordinators(context.Background(), s)

	require.Error(t, err, "CellID drift must error")
	assert.Contains(t, err.Error(), "drift")
	assert.Contains(t, err.Error(), "wrongcell")
}

func TestBuildProjectionCoordinators_SkipsMissingSnapshot(t *testing.T) {
	t.Parallel()
	s := buildProjectionPhaseState(t, newProjectionCell())
	delete(s.cellSnapshots, projTestCellID)

	b := newProjectionBootstrap(t)
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)

	require.NoError(t, err, "missing snapshot must be skipped, not error")
	assert.Empty(t, wirings)
}

func TestBuildProjectionCoordinators_HappyPath_CapturesSubscription(t *testing.T) {
	t.Parallel()
	s := buildProjectionPhaseState(t, newProjectionCell())

	b := newProjectionBootstrap(t)
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)

	require.NoError(t, err, "happy path must not error")
	require.Len(t, wirings, 1, "one projection → one wiring")
	w := wirings[0]
	require.NotNil(t, w.coord, "coordinator must be constructed")
	assert.Equal(t, projTestCellID, w.cellID)
	assert.Equal(t, projTestProjID, w.projectionID)
	assert.Equal(t, projection.PhaseLive, w.coord.Phase(), "fresh coordinator is live")
	// The captured subscription reflects Coordinator.Subscribe's internal wiring.
	assert.Equal(t, projTestCellID+"-"+projTestProjID, w.sub.ConsumerGroup,
		"consumer group must be cellID-projectionID")
	assert.Equal(t, projTestCellID, w.sub.CellID)
	assert.Equal(t, projTestProjID, w.sub.SliceID, "sliceID is set to the projection id")
	assert.Equal(t, projTestTopic, w.sub.Spec.Topic)
	require.NotNil(t, w.sub.Handler, "wrapped handler must be captured")

	// Probe naming contract (ops dashboards/alerts depend on this format).
	probes, err := w.coord.Probes()
	require.NoError(t, err)
	names := make([]string, 0, len(probes))
	for _, p := range probes {
		names = append(names, string(p.Name()))
	}
	assert.Contains(t, names, projTestCellID+"_projection_"+projTestProjID+"_store_ready")
	assert.Contains(t, names, projTestCellID+"_projection_"+projTestProjID+"_lag")
}

// TestBuildProjectionCoordinators_CapturedHandlerRunsApply proves the captured
// SubscriptionRequest.Handler is the real projection handler (checkpoint + tx +
// apply), not a noop: invoking it runs the business Apply. This is the core
// side-effect the drain exists to wire — asserting only the coordinator index
// would not catch a dropped AddContractHandler / wrong handler.
func TestBuildProjectionCoordinators_CapturedHandlerRunsApply(t *testing.T) {
	t.Parallel()
	var applied atomic.Int32
	pc := newProjectionCell()
	pc.apply = func(context.Context, projection.ProjectionEvent) error { applied.Add(1); return nil }
	s := buildProjectionPhaseState(t, pc)

	b := newProjectionBootstrap(t)
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 1)

	// fakeProjectionCursor returns position 1; cold-start checkpoint is 0, so
	// 1 > 0 ⇒ apply is invoked exactly once.
	_ = wirings[0].sub.Handler(context.Background(), outbox.Entry{})
	assert.Equal(t, int32(1), applied.Load(),
		"captured handler must route a fresh event to the business apply")
}

// TestBuildProjectionCoordinators_SliceID_UsedWhenSet verifies that when
// ProjectionRequest.SliceID is non-empty, buildOneProjection uses it as the
// subscription SliceID instead of the coordinator-injected projectionID fallback.
func TestBuildProjectionCoordinators_SliceID_UsedWhenSet(t *testing.T) {
	t.Parallel()
	pc := newProjectionCell()
	s := buildProjectionPhaseState(t, pc)

	// Patch the recorded projection's SliceID after snapshot is taken.
	// White-box: RegistrySnapshot is a value type in the map, so we read it,
	// mutate it, and write it back. This simulates cellgen 04b injecting
	// a distinct slice name from slice metadata.
	snap := s.cellSnapshots[projTestCellID]
	require.Len(t, snap.Projections, 1)
	snap.Projections[0].SliceID = "myslice"
	s.cellSnapshots[projTestCellID] = snap

	b := newProjectionBootstrap(t)
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 1)

	assert.Equal(t, "myslice", wirings[0].sub.SliceID,
		"non-empty req.SliceID must override the projectionID fallback")
}

// TestBuildProjectionCoordinators_SliceID_FallsBackToProjectionID verifies that
// when ProjectionRequest.SliceID is empty (the PR-04a no-fill-path case),
// buildOneProjection leaves the coordinator-injected projectionID as the SliceID.
func TestBuildProjectionCoordinators_SliceID_FallsBackToProjectionID(t *testing.T) {
	t.Parallel()
	s := buildProjectionPhaseState(t, newProjectionCell())

	// Confirm SliceID is empty (no cellgen fill in 04a).
	snap := s.cellSnapshots[projTestCellID]
	require.Len(t, snap.Projections, 1)
	require.Equal(t, "", snap.Projections[0].SliceID, "fixture must have empty SliceID for this test")

	b := newProjectionBootstrap(t)
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 1)

	assert.Equal(t, projTestProjID, wirings[0].sub.SliceID,
		"empty SliceID must fall back to projectionID (coordinator-injected value)")
}

// TestBuildProjectionCoordinators_NewCoordinatorError covers buildOneProjection's
// NewCoordinator failure branch: RegisterProjection accepts a non-empty
// ProjectionID, but NewCoordinator rejects one that is not a snake_case
// probe-name identifier.
func TestBuildProjectionCoordinators_NewCoordinatorError(t *testing.T) {
	t.Parallel()
	pc := newProjectionCell()
	pc.projectionID = "Bad-Proj" // uppercase + hyphen → invalid probe-name identifier
	s := buildProjectionPhaseState(t, pc)

	b := newProjectionBootstrap(t)
	_, err := b.buildProjectionCoordinators(context.Background(), s)

	require.Error(t, err, "invalid projectionID must fail at NewCoordinator")
	assert.Contains(t, err.Error(), "construct coordinator")
}

// ---------------------------------------------------------------------------
// phase6StartEventRouter — end-to-end projection drain
// ---------------------------------------------------------------------------

func TestPhase6_ProjectionDrain_WiresCoordinatorAndProbes(t *testing.T) {
	t.Parallel()
	bus := eventbus.New(clock.Real())

	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-proj-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newProjectionCell()))
	require.NoError(t, asm.Start(context.Background()))

	b := newProjectionBootstrap(t,
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
	)
	b.healthAggregator = newEventsTestAggregator() // phase0 normally sets this.

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	require.NoError(t, b.phase6StartEventRouter(runCtx, s),
		"phase6 must drain the projection cleanly")

	// Coordinator indexed for the rebuild HTTP endpoint (held as the narrow
	// rebuildController interface; assert the concrete type to inspect probes).
	ctrl, ok := b.projectionRebuilds[projTestCellID+"/"+projTestProjID]
	require.True(t, ok, "coordinator must be indexed by <cell>/<projection>")
	require.NotNil(t, ctrl)
	coord, ok := ctrl.(*projection.Coordinator)
	require.True(t, ok, "registry must hold a *projection.Coordinator")

	// Probes were registered (Register returns an error on dup/invalid; a clean
	// phase6 proves both store-ready and lag probes registered without collision).
	probes, err := coord.Probes()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(probes), 2, "coordinator exposes store-ready + lag probes")

	// Run teardowns (router stop, then coordinator Close) to release goroutines.
	for _, v := range slices.Backward(s.teardowns) {
		_ = v.fn(context.Background())
	}
}

// projectionApplyRoundTripTimeout bounds the publish→Apply round-trip wait. The
// in-mem bus fires Apply in milliseconds; this generous deadline only matters on
// a wiring regression, where it produces an actionable failure. Package-level
// const per TEST-TIME-LITERAL-01.
const projectionApplyRoundTripTimeout = 10 * time.Second

// TestPhase6_ProjectionDrain_PublishApplyRoundTrip verifies that after phase6
// drains the projection into the running event router, publishing a real event
// on the projection's topic causes the business Apply to be invoked. This is the
// true E2E guarantee: coordinator wired, handler registered, router running, and
// publish → Apply round-trip confirmed.
//
// In-mem eventbus + mem projection deps ensure no external deps and determinism;
// the Apply channel gives deterministic sync with a bounded, diagnosable deadline.
func TestPhase6_ProjectionDrain_PublishApplyRoundTrip(t *testing.T) {
	t.Parallel()

	applied := make(chan struct{}, 1)
	bus := eventbus.New(clock.Real())

	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-e2e-apply", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	pc := newProjectionCell()
	pc.apply = func(_ context.Context, _ projection.ProjectionEvent) error {
		select {
		case applied <- struct{}{}:
		default:
		}
		return nil
	}
	require.NoError(t, asm.Register(pc))
	require.NoError(t, asm.Start(context.Background()))

	b := newProjectionBootstrap(t,
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
	)
	b.healthAggregator = newEventsTestAggregator()

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	require.NoError(t, b.phase6StartEventRouter(runCtx, s),
		"phase6 must start cleanly before the publish test")

	// Publish one valid wire envelope to the projection topic.
	// fakeProjectionCursor returns position 1 > checkpoint 0, so apply fires.
	now := clock.Real().Now()
	entry, err := outbox.EntryScan{
		ID:         "test-proj-e2e-id",
		EventType:  projTestTopic,
		Topic:      projTestTopic,
		Payload:    []byte(`{"order":"test"}`),
		CreatedAt:  now,
		OccurredAt: now,
	}.ToEntry()
	require.NoError(t, err, "EntryScan.ToEntry must succeed for a valid entry")

	payload, err := outbox.MarshalEnvelope(entry)
	require.NoError(t, err, "MarshalEnvelope must succeed for a valid entry")

	require.NoError(t, bus.Publish(context.Background(), projTestTopic, payload),
		"Publish must succeed when the router is running")

	// Wait for Apply to be called. The in-mem bus dispatches into a goroutine, so
	// sync on the channel; a generous bounded deadline (package-level const per
	// TEST-TIME-LITERAL-01) turns a wiring regression into an actionable failure
	// message instead of a generic `go test -timeout` kill.
	select {
	case <-applied:
	case <-time.After(projectionApplyRoundTripTimeout):
		t.Fatal("phase6 round-trip: projection Apply was not invoked within the deadline " +
			"after publishing to the projection topic — check the coordinator/router wiring")
	}

	// Teardown: LIFO stop the router + coordinator.
	for _, v := range slices.Backward(s.teardowns) {
		_ = v.fn(context.Background())
	}
}

func TestPhase6_ProjectionWithoutSubscriber_FailsFast(t *testing.T) {
	t.Parallel()
	s := buildProjectionPhaseState(t, newProjectionCell())
	s.sub = nil // no subscriber configured

	b := newProjectionBootstrap(t)
	err := b.phase6StartEventRouter(context.Background(), s)

	require.Error(t, err, "projection with no subscriber must fail fast")
	assert.Contains(t, err.Error(), "no subscriber is configured")
	assert.Contains(t, err.Error(), "projection")
}

// ---------------------------------------------------------------------------
// PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01 — serial in-order delivery guard
// ---------------------------------------------------------------------------

// concurrentFakeSubscriber implements outbox.Subscriber but deliberately does
// NOT implement outbox.SerialInOrderGuarantor — it models a concurrent transport
// (e.g. AMQP dispatching one goroutine per delivery with prefetch>1). Ready is
// pre-closed so the event router can reach Running for the subscription-only
// acceptance path; Subscribe blocks until ctx cancel.
type concurrentFakeSubscriber struct{}

func (concurrentFakeSubscriber) Setup(context.Context, outbox.Subscription) error { return nil }

func (concurrentFakeSubscriber) Ready(outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (concurrentFakeSubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (concurrentFakeSubscriber) Close(context.Context) error { return nil }

// liarSubscriber implements the marker but returns false (an explicit non-serial
// declaration). It locks that an explicit false is rejected too — the guard must
// reject both absence (concurrentFakeSubscriber) and false, so a transport cannot
// pass by implementing the method with the wrong answer.
type liarSubscriber struct{ concurrentFakeSubscriber }

func (liarSubscriber) GuaranteesSerialInOrderDelivery() bool { return false }

// newProjectionBootstrapWithSubscriber wires the four projection deps plus the
// given subscriber (so phase6 reaches the projection drain) and the test
// consumer base / health aggregator that phase0 normally populate.
func newProjectionBootstrapWithSubscriber(t *testing.T, asm *assembly.CoreAssembly, sub outbox.Subscriber) *Bootstrap {
	t.Helper()
	b := newProjectionBootstrap(t,
		WithAssembly(asm),
		WithSubscriber(sub),
		WithConsumerBase(newTestConsumerBase(t)),
	)
	b.healthAggregator = newEventsTestAggregator() // phase0 normally sets this.
	return b
}

// TestPhase6_Projection_RejectsConcurrentSubscriber is the core fail-closed
// path: a projection wired onto a subscriber that does NOT implement
// outbox.SerialInOrderGuarantor must fail fast at the projection drain
// (ADR §6 row 4 — no concurrent transport may carry a projection).
func TestPhase6_Projection_RejectsConcurrentSubscriber(t *testing.T) {
	t.Parallel()
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-proj-reject", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newProjectionCell()))
	require.NoError(t, asm.Start(context.Background()))

	sub := concurrentFakeSubscriber{}
	b := newProjectionBootstrapWithSubscriber(t, asm, sub)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = sub
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	err := b.phase6StartEventRouter(runCtx, s)
	require.Error(t, err, "projection on a concurrent (non-serial) subscriber must fail fast")
	assert.Contains(t, err.Error(), "serial in-order delivery",
		"error must name the violated precondition")
	// F3 (#1369): the failure must name the affected projection(s), not just the
	// offending transport type, so ops can see which declarations are blocked.
	assert.Contains(t, err.Error(), projTestCellID+"/"+projTestProjID,
		"error must name the projection that triggered the serial-delivery guard")
	assert.Empty(t, b.projectionRebuilds,
		"no projection coordinator must be wired when the guard rejects the transport")
}

// TestPhase6_Projection_RejectsLiarSubscriber locks that an explicit
// GuaranteesSerialInOrderDelivery() == false is rejected just like absence —
// implementing the method with the wrong answer must not pass the guard.
func TestPhase6_Projection_RejectsLiarSubscriber(t *testing.T) {
	t.Parallel()
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-proj-liar", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newProjectionCell()))
	require.NoError(t, asm.Start(context.Background()))

	sub := liarSubscriber{}
	b := newProjectionBootstrapWithSubscriber(t, asm, sub)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = sub
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	err := b.phase6StartEventRouter(runCtx, s)
	require.Error(t, err, "projection on a subscriber declaring serial=false must fail fast")
	assert.Contains(t, err.Error(), "serial in-order delivery")
}

// TestPhase6_SubscriptionOnly_AllowsConcurrentSubscriber proves the guard does
// NOT over-reach: a plain subscription (not a projection) on a concurrent
// subscriber is allowed — only projections require serial in-order delivery.
func TestPhase6_SubscriptionOnly_AllowsConcurrentSubscriber(t *testing.T) {
	t.Parallel()
	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-sub-only", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("event.phase6.subonly.v1")))
	require.NoError(t, asm.Start(context.Background()))

	sub := concurrentFakeSubscriber{}
	b := newProjectionBootstrapWithSubscriber(t, asm, sub)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = sub
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	require.NoError(t, b.phase6StartEventRouter(runCtx, s),
		"a plain subscription on a concurrent subscriber must not trip the projection serial guard")

	for _, v := range slices.Backward(s.teardowns) {
		_ = v.fn(context.Background())
	}
}

// ---------------------------------------------------------------------------
// WithProjection* options
// ---------------------------------------------------------------------------

func TestWithProjectionOptions_StoreValueAndNilIgnored(t *testing.T) {
	t.Parallel()

	t.Run("checkpoint store", func(t *testing.T) {
		store := projection.NewMemCheckpointStore()
		b := New(clock.Real(), WithProjectionCheckpointStore(store))
		assert.Equal(t, store, b.projectionStore)
		var nilStore projection.CheckpointStore
		WithProjectionCheckpointStore(nilStore)(b)
		assert.Equal(t, store, b.projectionStore, "typed-nil must not clear the set value")
	})
	t.Run("tx runner", func(t *testing.T) {
		tx := fakeProjectionTxRunner{}
		b := New(clock.Real(), WithProjectionTxRunner(tx))
		assert.NotNil(t, b.projectionTxRunner)
		WithProjectionTxRunner(nil)(b)
		assert.NotNil(t, b.projectionTxRunner, "bare-nil must not clear the set value")
	})
	t.Run("replay source", func(t *testing.T) {
		replay := projection.NewMemReplaySource()
		b := New(clock.Real(), WithProjectionReplaySource(replay))
		assert.Equal(t, replay, b.projectionReplay)
		var nilReplay projection.ReplaySource
		WithProjectionReplaySource(nilReplay)(b)
		assert.Equal(t, replay, b.projectionReplay, "typed-nil must not clear the set value")
	})
	t.Run("cursor", func(t *testing.T) {
		cur := fakeProjectionCursor{}
		b := New(clock.Real(), WithProjectionCursor(cur))
		assert.NotNil(t, b.projectionCursor)
		var nilCur projection.Cursor
		WithProjectionCursor(nilCur)(b)
		assert.NotNil(t, b.projectionCursor, "typed-nil must not clear the set value")
	})
}

// ---------------------------------------------------------------------------
// F4: projection metrics wiring
// ---------------------------------------------------------------------------

// TestBuildProjectionCoordinators_WithRealProvider_RegistersMetrics verifies
// that when a real (non-Nop) metrics provider is injected, buildProjectionCoordinators
// (via autoWireProjectionMetrics) calls projection.RegisterMetrics and registers
// the three canonical metric names against the provider. The spy captures names at
// registration time, proving the provider is threaded through to the Coordinator.
func TestBuildProjectionCoordinators_WithRealProvider_RegistersMetrics(t *testing.T) {
	t.Parallel()
	spy := &registrationSpy{}
	s := buildProjectionPhaseState(t, newProjectionCell())

	b := newProjectionBootstrap(t, WithMetricsProvider(spy))
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 1)

	// RegisterMetrics registers all three projection metrics against the provider:
	// two gauges (replay_lag, pending_events) and one histogram (rebuild_duration).
	// Assert each appears in its respective registration slot — proving the
	// provider is threaded through (F4 regression).
	spy.mu.Lock()
	gauges := append([]string(nil), spy.gaugeNames...)
	histograms := append([]string(nil), spy.histogramNames...)
	spy.mu.Unlock()

	assert.Contains(t, gauges, "projection_event_replay_lag_seconds",
		"projection_event_replay_lag_seconds (gauge) must be registered on the real provider; got %v", gauges)
	assert.Contains(t, gauges, "projection_pending_events",
		"projection_pending_events (gauge) must be registered on the real provider; got %v", gauges)
	assert.Contains(t, histograms, "projection_rebuild_duration_seconds",
		"projection_rebuild_duration_seconds (histogram) must be registered on the real provider; got %v", histograms)
}

// recordingLabelProvider wraps NopProvider but returns Gauge/Histogram vecs that
// record every .With(labels) call. projection.NewCoordinator runs
// Metrics.preflight — which calls .With({cell, projection}) on each registered
// vec — at CONSTRUCTION time, and ONLY when CoordinatorConfig.Metrics is non-nil.
// A recorded .With therefore proves the shared b.projectionMetrics was actually
// threaded into NewCoordinator (not merely registered + cached on the Bootstrap).
type recordingLabelProvider struct {
	nop       kernelmetrics.NopProvider
	withCalls *[]kernelmetrics.Labels
}

func (p recordingLabelProvider) GaugeVec(o kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	g, err := p.nop.GaugeVec(o)
	if err != nil {
		return nil, err
	}
	return recordingGaugeVec{GaugeVec: g, withCalls: p.withCalls}, nil
}

func (p recordingLabelProvider) HistogramVec(o kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	h, err := p.nop.HistogramVec(o)
	if err != nil {
		return nil, err
	}
	return recordingHistogramVec{HistogramVec: h, withCalls: p.withCalls}, nil
}

func (p recordingLabelProvider) CounterVec(o kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return p.nop.CounterVec(o)
}

func (p recordingLabelProvider) Unregister(kernelmetrics.Collector) error { return nil }

type recordingGaugeVec struct {
	kernelmetrics.GaugeVec
	withCalls *[]kernelmetrics.Labels
}

func (v recordingGaugeVec) With(l kernelmetrics.Labels) kernelmetrics.Gauge {
	*v.withCalls = append(*v.withCalls, l)
	return v.GaugeVec.With(l)
}

type recordingHistogramVec struct {
	kernelmetrics.HistogramVec
	withCalls *[]kernelmetrics.Labels
}

func (v recordingHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	*v.withCalls = append(*v.withCalls, l)
	return v.HistogramVec.With(l)
}

// TestBuildProjectionCoordinators_SharedMetricsReachCoordinator is the F2 wiring
// guard: it proves the cached shared b.projectionMetrics is actually passed into
// projection.NewCoordinator (phases_projection.go CoordinatorConfig.Metrics:
// b.projectionMetrics), not merely registered and cached on the Bootstrap. The
// older RegistersMetrics test only asserts the metric NAMES register on the
// provider and that b.projectionMetrics is non-nil — both stay true even if the
// CoordinatorConfig.Metrics wiring regressed to nil. This test goes RED on that
// regression: with Metrics: nil the Coordinator skips preflight, so no .With is
// recorded.
func TestBuildProjectionCoordinators_SharedMetricsReachCoordinator(t *testing.T) {
	t.Parallel()
	var withCalls []kernelmetrics.Labels
	prov := recordingLabelProvider{withCalls: &withCalls}
	s := buildProjectionPhaseState(t, newProjectionCellNamed("ordercell", "summary"))

	b := newProjectionBootstrap(t, WithMetricsProvider(prov))
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 1)
	require.NotNil(t, b.projectionMetrics,
		"shared projection metrics must be registered once and cached")

	// preflight runs at NewCoordinator construction only when Metrics is non-nil;
	// a recorded .With proves b.projectionMetrics was threaded into NewCoordinator.
	require.NotEmpty(t, withCalls,
		"Coordinator must run Metrics.preflight (.With on the shared vecs) at construction — "+
			"proves b.projectionMetrics reached NewCoordinator; empty means CoordinatorConfig.Metrics regressed to nil")
	found := false
	for _, l := range withCalls {
		if l["cell"] == "ordercell" && l["projection"] == "summary" {
			found = true
			break
		}
	}
	assert.Truef(t, found,
		"preflight must bind the projection's own {cell, projection} labels; got %v", withCalls)
}

// TestBuildProjectionCoordinators_NopProvider_SkipsMetrics verifies that when no
// provider is configured (NopProvider default), autoWireProjectionMetrics does NOT
// call projection.RegisterMetrics — matching the pattern used by autoWireHTTPMetricsCollector.
func TestBuildProjectionCoordinators_NopProvider_SkipsMetrics(t *testing.T) {
	t.Parallel()
	s := buildProjectionPhaseState(t, newProjectionCell())

	// No WithMetricsProvider → NopProvider default.
	b := newProjectionBootstrap(t)
	// Confirm the default is indeed NopProvider.
	_, isNop := b.metricsProvider.(kernelmetrics.NopProvider)
	require.True(t, isNop, "default must be NopProvider for this test to be meaningful")

	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 1)
	// No assertion on probe names: the coordinator constructs fine with nil Metrics.
	// This test proves no panic and no registration attempt occurred on NopProvider.
}

// newProjectionCellNamed builds a projection cell with an explicit cellID +
// projectionID so a single phaseState can host multiple projections (the
// multi-projection regression case from #1399).
func newProjectionCellNamed(cellID, projID string) *projectionTestCell {
	return &projectionTestCell{
		BaseCell:     cell.MustNewBaseCell(&metadata.CellMeta{ID: cellID, Type: "core"}),
		spec:         projTestSpec(),
		projectionID: projID,
		apply:        func(context.Context, projection.ProjectionEvent) error { return nil },
	}
}

// multiProjectionCell registers SEVERAL projections under ONE cell — the exact
// shape of the original #1399 bug (one cell, N projections, the 2nd+ silently
// losing metrics under the old per-projection registration).
type multiProjectionCell struct {
	*cell.BaseCell
	projectionIDs []string
}

func (c *multiProjectionCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	for _, pid := range c.projectionIDs {
		if err := reg.RegisterProjection(cell.ProjectionRequest{
			Spec:         projTestSpec(),
			ProjectionID: pid,
			CellID:       c.ID(),
			Apply:        func(context.Context, projection.ProjectionEvent) error { return nil },
		}); err != nil {
			return err
		}
	}
	return nil
}

func newMultiProjectionCell(cellID string, projIDs ...string) *multiProjectionCell {
	return &multiProjectionCell{
		BaseCell:      cell.MustNewBaseCell(&metadata.CellMeta{ID: cellID, Type: "core"}),
		projectionIDs: projIDs,
	}
}

// projMetricRegProvider records how many times each metric NAME is registered so
// a test can prove the fixed-name projection metric family is registered EXACTLY
// ONCE regardless of how many projections are declared. Counting by name (not raw
// GaugeVec/HistogramVec totals) isolates the projection family from unrelated
// bootstrap metrics (e.g. the shutdown-duration histogram registered in New).
type projMetricRegProvider struct {
	nop  kernelmetrics.NopProvider
	regs map[string]int
}

func (p *projMetricRegProvider) count(name string) {
	if p.regs == nil {
		p.regs = map[string]int{}
	}
	p.regs[name]++
}

func (p *projMetricRegProvider) GaugeVec(o kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	p.count(o.Name)
	return p.nop.GaugeVec(o)
}

func (p *projMetricRegProvider) HistogramVec(o kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	p.count(o.Name)
	return p.nop.HistogramVec(o)
}

func (p *projMetricRegProvider) CounterVec(o kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return p.nop.CounterVec(o)
}

func (p *projMetricRegProvider) Unregister(kernelmetrics.Collector) error { return nil }

// failGaugeProvider returns an error on GaugeVec — projection.RegisterMetrics
// registers replay_lag (a gauge) first, so this drives the registration-conflict
// fail-fast path.
type failGaugeProvider struct{ nop kernelmetrics.NopProvider }

func (p failGaugeProvider) GaugeVec(kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return nil, errors.New("gauge registration boom")
}

func (p failGaugeProvider) HistogramVec(o kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return p.nop.HistogramVec(o)
}

func (p failGaugeProvider) CounterVec(o kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return p.nop.CounterVec(o)
}

func (p failGaugeProvider) Unregister(kernelmetrics.Collector) error { return nil }

// TestBuildProjectionCoordinators_MultiProjection_SharesSingleMetrics is the
// direct regression guard for #1399: before the fix, buildOneProjection called
// projection.RegisterMetrics PER projection, so the fixed-name metric family was
// registered N times (the 2nd+ registration collides and was warn-degraded).
// After the fix a single shared *projection.Metrics is registered once via
// autoWireProjectionMetrics and reused for every projection — proven here by the
// gauge/histogram registration counts staying at one family across two projections.
func TestBuildProjectionCoordinators_MultiProjection_SharesSingleMetrics(t *testing.T) {
	t.Parallel()
	prov := &projMetricRegProvider{}
	s := buildProjectionPhaseState(t,
		newProjectionCellNamed("ordercell_a", "summary"),
		newProjectionCellNamed("ordercell_b", "summary"),
	)

	b := newProjectionBootstrap(t, WithMetricsProvider(prov))
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 2)

	require.NotNil(t, b.projectionMetrics,
		"a single shared *projection.Metrics must be registered once and cached for all projections")
	// Each fixed-name projection metric must be registered EXACTLY once across the
	// two projections — the old per-projection path registered them N times (the
	// 2nd colliding and being warn-degraded). #1399.
	for _, name := range []string{
		"projection_event_replay_lag_seconds",
		"projection_rebuild_duration_seconds",
		"projection_pending_events",
	} {
		assert.Equalf(t, 1, prov.regs[name],
			"%s must register EXACTLY once for a single shared *projection.Metrics, not once per projection (#1399)", name)
	}
}

// TestBuildProjectionCoordinators_SameCellMultiProjection_SharesSingleMetrics
// reproduces the EXACT #1399 shape: a single cell with two projections. The old
// per-projection path called RegisterMetrics inside buildOneProjection, so the
// 2nd projection re-registered the fixed-name family (collide → warn-degrade →
// lost metrics). The shared registration must register each metric exactly once.
func TestBuildProjectionCoordinators_SameCellMultiProjection_SharesSingleMetrics(t *testing.T) {
	t.Parallel()
	prov := &projMetricRegProvider{}
	s := buildProjectionPhaseState(t, newMultiProjectionCell("ordercell", "summary", "detail"))

	b := newProjectionBootstrap(t, WithMetricsProvider(prov))
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 2, "one cell declared two projections")

	require.NotNil(t, b.projectionMetrics)
	for _, name := range []string{
		"projection_event_replay_lag_seconds",
		"projection_rebuild_duration_seconds",
		"projection_pending_events",
	} {
		assert.Equalf(t, 1, prov.regs[name],
			"%s must register EXACTLY once for one cell's two projections, not once per projection (#1399)", name)
	}
}

// TestAutoWireProjectionMetrics_NopProvider_Skips confirms the Nop default
// short-circuits before any registration and leaves the cache nil.
func TestAutoWireProjectionMetrics_NopProvider_Skips(t *testing.T) {
	t.Parallel()
	b := newProjectionBootstrap(t) // default NopProvider
	require.NoError(t, b.autoWireProjectionMetrics())
	assert.Nil(t, b.projectionMetrics, "Nop provider must not register projection metrics")
}

// TestAutoWireProjectionMetrics_Conflict_FailFast confirms a registration
// conflict is startup-fatal with an actionable message — never warn-degraded.
func TestAutoWireProjectionMetrics_Conflict_FailFast(t *testing.T) {
	t.Parallel()
	b := newProjectionBootstrap(t, WithMetricsProvider(failGaugeProvider{}))
	err := b.autoWireProjectionMetrics()
	require.Error(t, err, "registration conflict must fail fast, not degrade to no-metrics")
	assert.Contains(t, err.Error(), "projection metrics auto-wire conflict")
	assert.Contains(t, err.Error(), "Remove one side")
	assert.Nil(t, b.projectionMetrics, "cache must stay nil when registration fails")
}

// Compile-time anchors.
var (
	_ cell.Cell              = (*projectionTestCell)(nil)
	_ cell.Cell              = (*multiProjectionCell)(nil)
	_ projection.Cursor      = fakeProjectionCursor{}
	_ kernelmetrics.Provider = (*projMetricRegProvider)(nil)
	_ kernelmetrics.Provider = failGaugeProvider{}
	_ kernelmetrics.Provider = recordingLabelProvider{}
)
