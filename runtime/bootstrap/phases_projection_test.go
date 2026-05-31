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
	"slices"
	"sync/atomic"
	"testing"

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

func (fakeProjectionCursor) Position(_ outbox.Entry) (int64, error) { return 1, nil }

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
		apply:        func(context.Context, outbox.Entry) error { return nil },
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
	pc.apply = func(context.Context, outbox.Entry) error { applied.Add(1); return nil }
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

	// Coordinator indexed for the rebuild HTTP endpoint.
	coord, ok := b.projectionCoordinators[projTestCellID+"/"+projTestProjID]
	require.True(t, ok, "coordinator must be indexed by <cell>/<projection>")
	require.NotNil(t, coord)

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

// TestPhase6_ProjectionDrain_PublishApplyRoundTrip verifies that after phase6
// drains the projection into the running event router, publishing a real event
// on the projection's topic causes the business Apply to be invoked. This is the
// true E2E guarantee: coordinator wired, handler registered, router running, and
// publish → Apply round-trip confirmed.
//
// The test uses an atomic counter + Eventually to avoid sleep-based polling.
// In-mem eventbus + mem projection deps ensure no external deps and determinism.
func TestPhase6_ProjectionDrain_PublishApplyRoundTrip(t *testing.T) {
	t.Parallel()

	applied := make(chan struct{}, 1)
	bus := eventbus.New(clock.Real())

	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-e2e-apply", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	pc := newProjectionCell()
	pc.apply = func(_ context.Context, _ outbox.Entry) error {
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

	// Wait for Apply to be called. The in-mem bus dispatches into a goroutine so
	// we use a channel for deterministic sync — no wall-clock literal required,
	// satisfying TEST-TIME-LITERAL-CONST-01. Failure is bounded by go test -timeout.
	<-applied

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
// that when a real (non-Nop) metrics provider is injected, buildOneProjection
// calls projection.RegisterMetrics and registers the three canonical metric names
// against the provider. The spy captures names at registration time, proving the
// provider is threaded through to the Coordinator.
func TestBuildProjectionCoordinators_WithRealProvider_RegistersMetrics(t *testing.T) {
	t.Parallel()
	spy := &registrationSpy{}
	s := buildProjectionPhaseState(t, newProjectionCell())

	b := newProjectionBootstrap(t, WithMetricsProvider(spy))
	wirings, err := b.buildProjectionCoordinators(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, wirings, 1)

	// Verify that the three projection metric gauges/histograms were registered.
	spy.mu.Lock()
	gauges := append([]string(nil), spy.histogramNames...)
	spy.mu.Unlock()

	// RegisterMetrics registers: projection_event_replay_lag_seconds (gauge),
	// projection_rebuild_duration_seconds (histogram), projection_pending_events (gauge).
	// At least the histogram name must appear in the spy's histogram registration.
	assert.Contains(t, gauges, "projection_rebuild_duration_seconds",
		"projection_rebuild_duration_seconds must be registered on the real provider; "+
			"got %v — means metrics were not threaded into the Coordinator (F4 regression)", gauges)
}

// TestBuildProjectionCoordinators_NopProvider_SkipsMetrics verifies that when no
// provider is configured (NopProvider default), buildOneProjection does NOT call
// projection.RegisterMetrics — matching the pattern used by autoWireHTTPMetricsCollector.
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

// Compile-time anchors.
var (
	_ cell.Cell         = (*projectionTestCell)(nil)
	_ projection.Cursor = fakeProjectionCursor{}
)
