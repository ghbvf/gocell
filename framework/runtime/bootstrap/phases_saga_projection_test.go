package bootstrap

// phases_saga_projection_test.go — unit + end-to-end tests for the saga-journal
// projection drain (EPIC #1609 PR-05).
//
// Coverage:
//   - drainCellSagaProjections happy path: a cell declaring a saga-journal
//     projection builds + wires a Tailer — readiness probe registered, worker in
//     b.workers, named teardown recorded.
//   - checkSagaProjectionDeps: missing each required dep (reader / owner store /
//     tx runner / locker) → fail-fast naming the option.
//   - pure saga-journal deployment (no outbox subscriber) boots without tripping
//     the serial-delivery guard.
//   - mixed outbox + saga-journal projections both wire correctly (Coordinator
//     for outbox, Tailer for saga-journal).
//   - WithSaga* options: store value + typed-nil ignored.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/distlock"
	"github.com/ghbvf/gocell/framework/runtime/distlock/locktest"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	"github.com/ghbvf/gocell/framework/runtime/http/health"
	"github.com/ghbvf/gocell/framework/runtime/saga/tailer"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const (
	sagaProjCellID = "ordercell"
	sagaProjProjID = "sagasummary"
)

// sagaProjectionTestCell records a saga-journal projection via
// reg.RegisterProjection (NewSagaJournalProjectionRequest) in Init.
type sagaProjectionTestCell struct {
	*cell.BaseCell
	projectionID   string
	apply          cell.ProjectionApply
	overrideCellID string // non-empty → simulate codegen drift
}

func (c *sagaProjectionTestCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	cellID := c.ID()
	if c.overrideCellID != "" {
		cellID = c.overrideCellID
	}
	return reg.RegisterProjection(
		cell.NewSagaJournalProjectionRequest(c.apply, c.projectionID, cellID, ""))
}

func newSagaProjectionCell() *sagaProjectionTestCell {
	return &sagaProjectionTestCell{
		BaseCell:     cell.MustNewBaseCell(&metadata.CellMeta{ID: sagaProjCellID, Type: "core"}),
		projectionID: sagaProjProjID,
		apply:        func(context.Context, projection.ProjectionEvent) error { return nil },
	}
}

func newSagaProjectionCellNamed(cellID, projID string) *sagaProjectionTestCell {
	return &sagaProjectionTestCell{
		BaseCell:     cell.MustNewBaseCell(&metadata.CellMeta{ID: cellID, Type: "core"}),
		projectionID: projID,
		apply:        func(context.Context, projection.ProjectionEvent) error { return nil },
	}
}

// newSagaTestLocker builds a real distlock.Locker over the in-mem FakeDriver —
// the Tailer requires a non-nil locker (unlike the saga Coordinator's optional
// single-process mode).
func newSagaTestLocker(t *testing.T) distlock.Locker {
	t.Helper()
	l, err := distlock.New(locktest.NewFakeDriver(), clockmock.New(time.Now()))
	require.NoError(t, err, "distlock.New over FakeDriver")
	return l
}

// tailerTestConfig is a valid non-default Config (LeaseTTL ≥ distlock.MinTTL).
func tailerTestConfig() tailer.Config {
	return tailer.Config{PollInterval: time.Second, LeaseTTL: time.Second}
}

// newSerialTestBus returns the in-mem event bus, which implements
// outbox.SerialInOrderGuarantor (the one transport allowed to carry an outbox
// projection).
func newSerialTestBus(t *testing.T) *eventbus.InMemoryEventBus {
	t.Helper()
	return eventbus.New(clock.Real())
}

// newMemGlobalReader builds an in-mem journal.GlobalReader (empty journal: HeadSeq
// is 0, so the Tailer's drain is a clean caught-up no-op — sufficient for wiring
// tests).
func newMemGlobalReader(t *testing.T) journal.GlobalReader {
	t.Helper()
	mj, err := journal.NewMemJournal(clock.Real())
	require.NoError(t, err, "journal.NewMemJournal")
	return mj
}

// sagaProjDeps returns the three saga-journal-specific options (reader + owner
// store + locker); the tx runner is shared with the outbox path
// (WithProjectionTxRunner).
func sagaProjDeps(t *testing.T) []Option {
	t.Helper()
	return []Option{
		WithSagaJournalReader(newMemGlobalReader(t)),
		WithSagaProjectionOwnerCheckpointStore(projection.NewMemOwnerCheckpointStore()),
		WithProjectionTxRunner(fakeProjectionTxRunner{}),
		WithSagaProjectionLocker(newSagaTestLocker(t)),
	}
}

// buildSagaProjectionPhaseState starts an assembly with the given cells and
// returns a phaseState with asm + cellSnapshots + a fresh health aggregator
// populated (mirrors buildProjectionPhaseState + phase0).
func buildSagaProjectionPhaseState(t *testing.T, b *Bootstrap, cells ...cell.Cell) *phaseState {
	t.Helper()
	asm := assembly.New(clock.Real(), assembly.Config{
		ID:             "saga-projection-test-asm",
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
	b.healthAggregator = newEventsTestAggregator() // phase0 normally sets this.
	return s
}

// runTeardowns runs recorded teardowns in LIFO order to release Tailer goroutines.
func runTeardowns(s *phaseState) {
	for _, v := range slices.Backward(s.teardowns) {
		_ = v.fn(context.Background())
	}
}

// ---------------------------------------------------------------------------
// drainCellSagaProjections — happy path
// ---------------------------------------------------------------------------

func TestDrainCellSagaProjections_WiresTailer(t *testing.T) {
	t.Parallel()
	b := New(clock.Real(), sagaProjDeps(t)...)
	s := buildSagaProjectionPhaseState(t, b, newSagaProjectionCell())

	require.NoError(t, b.drainCellSagaProjections(s),
		"a declared saga-journal projection must drain cleanly")
	t.Cleanup(func() { runTeardowns(s) })

	// Worker registered for phase8 start.
	require.Len(t, b.workers, 1, "the Tailer's worker must be appended to b.workers")

	// Readiness probe registered onto the health aggregator under the contract name.
	wantProbe, err := healthz.SagaTailerReadyProbeName(sagaProjCellID, sagaProjProjID)
	require.NoError(t, err)
	_, registered := s.registeredCheckers[wantProbe]
	assert.True(t, registered, "the Tailer readiness probe %q must be registered", wantProbe)

	// Named teardown recorded ("saga-tailer:<cell>/<projection>") and the Close fn
	// itself shuts the (never-started) Tailer down cleanly.
	wantTeardown := "saga-tailer:" + sagaProjCellID + "/" + sagaProjProjID
	var teardownFn func(context.Context) error
	for _, td := range s.teardowns {
		if td.name == wantTeardown {
			teardownFn = td.fn
			break
		}
	}
	require.NotNil(t, teardownFn, "a named Close teardown %q must be recorded", wantTeardown)
	assert.NoError(t, teardownFn(context.Background()),
		"the Tailer Close teardown must return cleanly for a never-started Tailer")
}

// TestDrainCellSagaProjections_EmptyNoOp: a cell with no saga-journal projection
// produces no Tailer.
func TestDrainCellSagaProjections_EmptyNoOp(t *testing.T) {
	t.Parallel()
	b := New(clock.Real(), sagaProjDeps(t)...)
	tc := &testCell{BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "plain-cell", Type: "core"})}
	s := buildSagaProjectionPhaseState(t, b, tc)

	require.NoError(t, b.drainCellSagaProjections(s))
	assert.Empty(t, b.workers, "no saga-journal projection must wire no worker")
}

// TestDrainCellSagaProjections_FailsOnCellIDDrift: a saga-journal ProjectionRequest
// whose codegen-injected CellID disagrees with its snapshot owner is a codegen
// drift and must fail-fast BEFORE any Tailer is built, mirroring the outbox path's
// CellID-drift guard (buildCellProjections). Without the guard the Tailer,
// checkpoint key and probe would be bound to the wrong cell.
func TestDrainCellSagaProjections_FailsOnCellIDDrift(t *testing.T) {
	t.Parallel()
	b := New(clock.Real(), sagaProjDeps(t)...)
	// Cell registered under sagaProjCellID but its projection request declares a
	// different CellID — the simulated codegen drift.
	driftCell := newSagaProjectionCell()
	driftCell.overrideCellID = "wrong-owner"
	s := buildSagaProjectionPhaseState(t, b, driftCell)

	err := b.drainCellSagaProjections(s)
	require.Error(t, err, "CellID drift must error")
	assert.Contains(t, err.Error(), "drift")
	assert.Contains(t, err.Error(), "wrong-owner", "error must name the drifted CellID")
	assert.Empty(t, b.workers, "no Tailer must be wired when drift is detected")
}

// ---------------------------------------------------------------------------
// checkSagaProjectionDeps — fail-fast on each missing dep
// ---------------------------------------------------------------------------

func TestDrainCellSagaProjections_FailsOnMissingDep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wantOpt string // the option whose dep we leave unset → must be named in the error
	}{
		{"missing journal reader", "WithSagaJournalReader"},
		{"missing owner store", "WithSagaProjectionOwnerCheckpointStore"},
		{"missing tx runner", "WithProjectionTxRunner"},
		{"missing locker", "WithSagaProjectionLocker"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Build options missing exactly the one under test.
			all := []struct {
				name string
				opt  Option
			}{
				{"WithSagaJournalReader", WithSagaJournalReader(newMemGlobalReader(t))},
				{"WithSagaProjectionOwnerCheckpointStore", WithSagaProjectionOwnerCheckpointStore(projection.NewMemOwnerCheckpointStore())},
				{"WithProjectionTxRunner", WithProjectionTxRunner(fakeProjectionTxRunner{})},
				{"WithSagaProjectionLocker", WithSagaProjectionLocker(newSagaTestLocker(t))},
			}
			var opts []Option
			for _, o := range all {
				if o.name == tc.wantOpt {
					continue
				}
				opts = append(opts, o.opt)
			}
			b := New(clock.Real(), opts...)
			s := buildSagaProjectionPhaseState(t, b, newSagaProjectionCell())

			err := b.drainCellSagaProjections(s)
			require.Error(t, err, "missing dep must error")
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr, "must be *errcode.Error")
			assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
			assert.Contains(t, ecErr.Message, tc.wantOpt, "error must name the missing option")
			// The cell context must be in the wrapped error chain.
			assert.Contains(t, err.Error(), sagaProjCellID)
		})
	}
}

// ---------------------------------------------------------------------------
// pure saga-journal deployment (no subscriber) — phase6 end-to-end
// ---------------------------------------------------------------------------

// TestPhase6_PureSagaJournal_NoSubscriber_BootsWithoutSerialGuard proves a
// deployment with ONLY saga-journal projections and NO subscriber boots cleanly:
// the saga drain wires the Tailer, and the serial-delivery guard (which is about
// the event-router transport) is never tripped because no outbox projection is
// declared.
func TestPhase6_PureSagaJournal_NoSubscriber_BootsWithoutSerialGuard(t *testing.T) {
	t.Parallel()
	b := New(clock.Real(), sagaProjDeps(t)...)

	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-pure-saga", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newSagaProjectionCell()))
	require.NoError(t, asm.Start(context.Background()))
	b.healthAggregator = newEventsTestAggregator()

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = nil // NO subscriber — a pure saga-journal deployment needs none.
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	require.NoError(t, b.phase6StartEventRouter(runCtx, s),
		"a pure saga-journal deployment (no subscriber) must boot without tripping the serial guard")
	t.Cleanup(func() { runTeardowns(s) })

	require.Len(t, b.workers, 1, "the saga-journal Tailer worker must be wired even with no subscriber")
	wantProbe, err := healthz.SagaTailerReadyProbeName(sagaProjCellID, sagaProjProjID)
	require.NoError(t, err)
	_, registered := s.registeredCheckers[wantProbe]
	assert.True(t, registered, "the Tailer readiness probe must be registered")
}

// ---------------------------------------------------------------------------
// mixed outbox + saga-journal — both wire correctly
// ---------------------------------------------------------------------------

// TestPhase6_MixedProjections_WireBothPaths proves an assembly with one outbox
// projection (→ Coordinator + event-router subscription + rebuild index) and one
// saga-journal projection (→ Tailer worker/probe) wires BOTH paths correctly.
func TestPhase6_MixedProjections_WireBothPaths(t *testing.T) {
	t.Parallel()
	bus := newSerialTestBus(t)

	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-mixed", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	// outbox projection (ordercell/ordersummary) + saga-journal projection (sagacell/sagasummary).
	require.NoError(t, asm.Register(newProjectionCell()))
	require.NoError(t, asm.Register(newSagaProjectionCellNamed("sagacell", "sagasummary")))
	require.NoError(t, asm.Start(context.Background()))

	opts := append(sagaProjDeps(t),
		WithProjectionCheckpointStore(projection.NewMemCheckpointStore()),
		WithProjectionReplaySource(projection.NewMemReplaySource()),
		WithProjectionCursor(fakeProjectionCursor{}),
		WithAssembly(asm),
		WithSubscriber(bus),
		WithConsumerBase(newTestConsumerBase(t)),
	)
	b := New(clock.Real(), opts...)
	b.healthAggregator = newEventsTestAggregator()

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real())

	require.NoError(t, b.phase6StartEventRouter(runCtx, s),
		"mixed outbox + saga-journal projections must both wire cleanly")
	t.Cleanup(func() { runTeardowns(s) })

	// Outbox path: Coordinator indexed for the rebuild endpoint.
	_, ok := b.projectionRebuilds[projTestCellID+"/"+projTestProjID]
	assert.True(t, ok, "the outbox projection must be indexed as a Coordinator")
	// Saga-journal path: Tailer worker + probe wired (NOT in the rebuild index —
	// saga-journal rebuild is out of scope).
	_, sagaIndexed := b.projectionRebuilds["sagacell/sagasummary"]
	assert.False(t, sagaIndexed, "saga-journal projections must NOT be in the rebuild index")
	require.Len(t, b.workers, 1, "exactly the saga-journal Tailer worker must be wired")
	wantProbe, err := healthz.SagaTailerReadyProbeName("sagacell", "sagasummary")
	require.NoError(t, err)
	_, registered := s.registeredCheckers[wantProbe]
	assert.True(t, registered, "the saga-journal Tailer readiness probe must be registered")
}

// ---------------------------------------------------------------------------
// WithSaga* options
// ---------------------------------------------------------------------------

func TestWithSagaProjectionOptions_StoreValueAndNilIgnored(t *testing.T) {
	t.Parallel()

	t.Run("journal reader", func(t *testing.T) {
		gr := newMemGlobalReader(t)
		b := New(clock.Real(), WithSagaJournalReader(gr))
		assert.NotNil(t, b.sagaJournalReader)
		var nilGR journal.GlobalReader
		WithSagaJournalReader(nilGR)(b)
		assert.NotNil(t, b.sagaJournalReader, "typed-nil must not clear the set value")
	})
	t.Run("owner store", func(t *testing.T) {
		store := projection.NewMemOwnerCheckpointStore()
		b := New(clock.Real(), WithSagaProjectionOwnerCheckpointStore(store))
		assert.NotNil(t, b.sagaProjOwnerStore)
		var nilStore projection.OwnerCheckpointStore
		WithSagaProjectionOwnerCheckpointStore(nilStore)(b)
		assert.NotNil(t, b.sagaProjOwnerStore, "typed-nil must not clear the set value")
	})
	t.Run("locker", func(t *testing.T) {
		l := newSagaTestLocker(t)
		b := New(clock.Real(), WithSagaProjectionLocker(l))
		assert.NotNil(t, b.sagaProjLocker)
		var nilLocker distlock.Locker
		WithSagaProjectionLocker(nilLocker)(b)
		assert.NotNil(t, b.sagaProjLocker, "typed-nil must not clear the set value")
	})
	t.Run("tailer config", func(t *testing.T) {
		b := New(clock.Real())
		assert.False(t, b.sagaTailerConfigSet, "config unset by default")
		WithSagaTailerConfig(tailerTestConfig())(b)
		assert.True(t, b.sagaTailerConfigSet, "WithSagaTailerConfig must set the flag")
	})
}

// ---------------------------------------------------------------------------
// per-cell metric observer
// ---------------------------------------------------------------------------

// TestDrainCellSagaProjections_RealProvider_RegistersTailerMetricsOncePerCell
// proves that with a real (non-Nop) provider the saga-tailer metric family is
// registered, and that two saga-journal projections under ONE cell share a single
// per-cell SagaTailerCollector (registered exactly once — a second registration
// for the same cellID would be a duplicate-registration conflict).
func TestDrainCellSagaProjections_RealProvider_RegistersTailerMetricsOncePerCell(t *testing.T) {
	t.Parallel()
	spy := &registrationSpy{}
	b := New(clock.Real(), append(sagaProjDeps(t), WithMetricsProvider(spy))...)

	// Both projections live under ONE cell so the per-cell observer cache is
	// exercised (a second NewSagaTailerCollector for "twincell" would conflict).
	multi := &multiSagaProjectionCell{
		BaseCell:      cell.MustNewBaseCell(&metadata.CellMeta{ID: "twincell", Type: "core"}),
		projectionIDs: []string{"first", "second"},
	}
	s := buildSagaProjectionPhaseState(t, b, multi)

	require.NoError(t, b.drainCellSagaProjections(s))
	t.Cleanup(func() { runTeardowns(s) })

	require.Len(t, b.workers, 2, "two saga-journal projections → two Tailer workers")
	require.Len(t, b.sagaTailerObservers, 1, "one cell → exactly one cached SagaTailerCollector")

	// The per-cell counter family registers exactly once across both projections.
	counts := map[string]int{}
	for _, n := range spy.counters() {
		counts[n]++
	}
	assert.Equal(t, 1, counts["saga_journal_tailer_drain_total"],
		"the per-cell saga tailer metric family must register exactly once, not once per projection")
}

// multiSagaProjectionCell registers SEVERAL saga-journal projections under ONE
// cell — exercises the per-cell observer cache.
type multiSagaProjectionCell struct {
	*cell.BaseCell
	projectionIDs []string
}

func (c *multiSagaProjectionCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	for _, pid := range c.projectionIDs {
		err := reg.RegisterProjection(cell.NewSagaJournalProjectionRequest(
			func(context.Context, projection.ProjectionEvent) error { return nil },
			pid, c.ID(), ""))
		if err != nil {
			return err
		}
	}
	return nil
}

// Compile-time anchors.
var (
	_ cell.Cell = (*sagaProjectionTestCell)(nil)
	_ cell.Cell = (*multiSagaProjectionCell)(nil)
)
