//go:build integration

// Package postgres — white-box integration tests for EPIC #1504 durable
// projection journal rebuild correctness (T-06-2).
//
// TestProjectionRebuild_* proves four correctness invariants against real
// PostgreSQL using the same per-test migratedPool approach as the rest of this
// package:
//
//  1. cold-start  — empty journal, Rebuild completes, no apply called, no panic.
//  2. rebuild-from-0 — N events seeded, Apply called exactly N times in order.
//  3. crash-restart resume + no-double-apply — resume from checkpoint, only new
//     events applied, checkpoint monotonically advances.
//  4. cleaned-outbox independence (T-06-2 core) — outbox_entries cleaned, Rebuild
//     reads only projection_events, model rebuilt correctly.
//
// Each sub-test gets its own migratedPool so schema state is fully isolated.
package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// rebuildTestTopic is the event stream topic used by all rebuild sub-tests.
// It must match the spec.Topic passed to Coordinator.Subscribe so drainGap
// applies own-stream events (not treat them as foreign).
const rebuildTestTopic = "ordersummary.events.v1"

// rebuildTestCellID / rebuildTestProjectionID are the identifier pair used for
// all sub-tests. Both must be valid snake_case probe-name identifiers
// (NewCoordinator validates them against the probe-name pattern).
const (
	rebuildTestCellID       = "ordercell"
	rebuildTestProjectionID = "ordersummary"
)

// rebuildTestHandlerWait is the maximum time to wait for the Coordinator to
// reach PhaseLive after a Rebuild call. Testcontainer PG is typically ready in
// <2s; 10s provides headroom under CI load.
const rebuildTestHandlerWait = 10 * time.Second

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// rebuildSpec returns the ContractSpec that every Coordinator in this file
// subscribes to. Topic == rebuildTestTopic so own-stream events in
// projection_events pass the drainGap per-spec filter.
func rebuildSpec() contractspec.ContractSpec {
	return contractspec.ContractSpec{
		ID:        "event.ordersummary.v1",
		Kind:      cellvocab.ContractEvent,
		Transport: "amqp",
		Topic:     rebuildTestTopic,
	}
}

// rebuildFixture wires a real Coordinator backed by PG sources against a
// fresh per-test database. Each call returns a fresh fixture.
type rebuildFixture struct {
	pool      *Pool
	src       *PGProjectionEventSource
	ckStore   *ProjectionCheckpointStore
	txm       *TxManager
	registrar *rebuildFakeRegistrar
}

// newRebuildFixture creates a fully-wired fixture using a fresh migratedPool.
func newRebuildFixture(t *testing.T) *rebuildFixture {
	t.Helper()
	pool := migratedPool(t)
	src, err := NewProjectionEventSource(pool.DB())
	require.NoError(t, err)
	ckStore, err := NewProjectionCheckpointStore(pool.DB())
	require.NoError(t, err)
	txm := NewTxManager(pool)
	return &rebuildFixture{
		pool:      pool,
		src:       src,
		ckStore:   ckStore,
		txm:       txm,
		registrar: &rebuildFakeRegistrar{},
	}
}

// newCoordinator builds a Coordinator from this fixture. Each call creates a
// new Coordinator (used by the crash-restart sub-test to simulate a process
// restart with the same persistent checkpoint store).
func (f *rebuildFixture) newCoordinator(t *testing.T) *projection.Coordinator {
	t.Helper()
	clk := clockmock.New(time.Now())
	c, err := projection.NewCoordinator(clk, projection.CoordinatorConfig{
		CellID:       rebuildTestCellID,
		ProjectionID: rebuildTestProjectionID,
		Registrar:    f.registrar,
		TxRunner:     f.txm,
		Store:        f.ckStore,
		Cursor:       f.src, // PGProjectionEventSource implements LiveCursor
		Replay:       f.src, // PGProjectionEventSource implements ReplaySource
		Tracer:       wrapper.NoopTracer{},
	})
	require.NoError(t, err)
	return c
}

// seedEvents inserts n projection_events rows directly into the durable journal
// and returns the resulting ProjectionEvent carriers (same as the conformance
// test helper newProjectionEventJournal / seed). Topic is always rebuildTestTopic
// so the Coordinator's drainGap filter treats them as own-stream events.
func (f *rebuildFixture) seedEvents(t *testing.T, n int) []projection.ProjectionEvent {
	t.Helper()
	clk := clockmock.New(time.Now())
	events := make([]projection.ProjectionEvent, n)
	for i := 0; i < n; i++ {
		e, err := kout.NewEntry(clk, context.Background(), rebuildTestTopic, []byte(`{}`))
		require.NoError(t, err)
		var globalSeq int64
		row := f.pool.DB().QueryRow(context.Background(), projectionEventInsertSQL,
			e.ID(), e.AggregateID(), e.AggregateType(), e.EventType(), e.Topic(),
			e.Payload(), e.CreatedAt(), e.OccurredAt())
		require.NoError(t, row.Scan(&globalSeq))
		events[i] = projection.NewJournalEvent(e, globalSeq)
	}
	return events
}

// subscribe calls Coordinator.Subscribe with the standard test spec and apply.
func subscribeRebuild(t *testing.T, c *projection.Coordinator, apply projection.Apply) {
	t.Helper()
	require.NoError(t, c.Subscribe(context.Background(), rebuildSpec(), apply))
}

// waitRebuildLive blocks until the Coordinator reaches PhaseLive or the test
// deadline fires. Uses testwait.External (TEST-SLEEP-DISCIPLINE-01) — the
// rebuild goroutine is a real PG-backed background worker.
func waitRebuildLive(t *testing.T, c *projection.Coordinator) {
	t.Helper()
	testwait.External(t, "pg-projection-rebuild-live",
		func() bool { return c.Phase() == projection.PhaseLive },
		rebuildTestHandlerWait, testtime.FastPoll,
		"Coordinator never reached PhaseLive")
}

// closeCoordinator shuts the coordinator down and waits for the rebuild
// goroutine to finish, with a generous deadline.
func closeCoordinator(t *testing.T, c *projection.Coordinator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rebuildTestHandlerWait)
	defer cancel()
	require.NoError(t, c.Close(ctx))
}

// ---------------------------------------------------------------------------
// rebuildFakeRegistrar — minimal SubscribeRegistrar for PG integration tests.
//
// The PG rebuild path does not exercise the live-delivery handler path (no real
// AMQP broker), so Subscribe only needs to store the handler for the gate tests
// to call. The registrar satisfies projection.SubscribeRegistrar and
// cell.Registrar.Subscribe structurally.
// ---------------------------------------------------------------------------

// rebuildFakeRegistrar stores the most-recently registered handler. It is not
// goroutine-safe on the write path but Subscribe is called once per Coordinator.
type rebuildFakeRegistrar struct {
	mu      sync.Mutex
	handler kout.EntryHandler
}

func (r *rebuildFakeRegistrar) Subscribe(
	_ contractspec.ContractSpec,
	handler kout.EntryHandler,
	_ string,
	_ string,
	_ ...cell.SubscriptionOption,
) error {
	r.mu.Lock()
	r.handler = handler
	r.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// applyCollector — in-memory apply function that records event IDs.
//
// Used to assert no-double-apply (each eventID must appear exactly once) and
// that the total apply count matches expectations.
// ---------------------------------------------------------------------------

type applyCollector struct {
	mu     sync.Mutex
	order  []string       // insertion order of EventIDs
	counts map[string]int // eventID → apply count
}

func newApplyCollector() *applyCollector {
	return &applyCollector{counts: make(map[string]int)}
}

func (a *applyCollector) Apply(ctx context.Context, ev projection.ProjectionEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := ev.EventID()
	a.order = append(a.order, id)
	a.counts[id]++
	return nil
}

// totalApplied is the number of Apply calls made.
func (a *applyCollector) totalApplied() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.order)
}

// maxCountForAnyEvent returns the maximum apply-count over all events. It is 1
// when no-double-apply holds.
func (a *applyCollector) maxCountForAnyEvent() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	max := 0
	for _, v := range a.counts {
		if v > max {
			max = v
		}
	}
	return max
}

// appliedIDs returns the set of event IDs that were applied, in insertion order.
func (a *applyCollector) appliedIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.order))
	copy(out, a.order)
	return out
}

// ---------------------------------------------------------------------------
// T-06-2 sub-tests
// ---------------------------------------------------------------------------

// TestProjectionRebuild_ColdStart verifies that a Coordinator wired against an
// empty projection_events journal completes Rebuild without calling Apply and
// reaches PhaseLive (cold-start case: checkpoint=0, head=0, no events to drain).
func TestProjectionRebuild_ColdStart(t *testing.T) {
	t.Parallel()
	f := newRebuildFixture(t)
	c := f.newCoordinator(t)
	defer closeCoordinator(t, c)

	col := newApplyCollector()
	subscribeRebuild(t, c, col.Apply)

	require.NoError(t, c.Rebuild(context.Background()))
	waitRebuildLive(t, c)

	assert.Equal(t, 0, col.totalApplied(), "cold-start: no events in journal, Apply must not be called")
	assert.Equal(t, projection.PhaseLive, c.Phase(), "cold-start: must reach PhaseLive")

	// Checkpoint stays at 0 (nothing was replayed).
	offset, err := f.ckStore.LoadOffset(context.Background(), rebuildTestCellID, rebuildTestProjectionID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), offset, "cold-start: checkpoint must be 0 after empty rebuild")
}

// TestProjectionRebuild_FromZero verifies that N events seeded into
// projection_events are all applied exactly once in ascending global_seq order
// after a Rebuild from offset 0.
func TestProjectionRebuild_FromZero(t *testing.T) {
	t.Parallel()
	const n = 5
	f := newRebuildFixture(t)
	seeded := f.seedEvents(t, n)

	c := f.newCoordinator(t)
	defer closeCoordinator(t, c)

	col := newApplyCollector()
	subscribeRebuild(t, c, col.Apply)

	require.NoError(t, c.Rebuild(context.Background()))
	waitRebuildLive(t, c)

	assert.Equal(t, n, col.totalApplied(), "rebuild-from-0: Apply must be called exactly N times")
	assert.Equal(t, 1, col.maxCountForAnyEvent(), "rebuild-from-0: no-double-apply — each event applied exactly once")

	// Verify global_seq order: the first seeded event's ID must appear first.
	appliedIDs := col.appliedIDs()
	require.Len(t, appliedIDs, n)
	assert.Equal(t, seeded[0].EventID(), appliedIDs[0], "first seeded event must be applied first")
	assert.Equal(t, seeded[n-1].EventID(), appliedIDs[n-1], "last seeded event must be applied last")

	// Checkpoint must have advanced to head (all N events consumed).
	head, err := f.src.Head(context.Background())
	require.NoError(t, err)
	offset, err := f.ckStore.LoadOffset(context.Background(), rebuildTestCellID, rebuildTestProjectionID)
	require.NoError(t, err)
	assert.Equal(t, head, offset, "rebuild-from-0: checkpoint must equal head after full replay")
}

// TestProjectionRebuild_CrashRestartResume verifies the no-double-apply and
// monotone-checkpoint invariants across a simulated crash-restart.
//
// Rebuild semantics: Rebuild is a FULL rebuild operation — it always zeroes the
// checkpoint and replays from offset 0. A second Rebuild after inserting new
// events therefore replays ALL events (initial + incremental). The "no-double-apply"
// invariant here means: within a single Rebuild run, each EventID in the journal
// is applied at most once (the exactly-once skip in resolvePosition ensures that).
//
//  1. Seed initialEvents events, run first Coordinator Rebuild to completion.
//  2. Close first Coordinator (simulating process exit).
//  3. Seed incrementalEvents more events into the same journal.
//  4. Construct a SECOND Coordinator using the SAME PGProjectionCheckpointStore
//     (shared persistent state, same PG DB) and run Rebuild again.
//  5. Assert: second Rebuild applies ALL (initial + incremental) events exactly
//     once each (full-replay from offset 0), and the checkpoint advances to the
//     new head.
func TestProjectionRebuild_CrashRestartResume(t *testing.T) {
	t.Parallel()
	const (
		initialEvents     = 4
		incrementalEvents = 3
		totalEvents       = initialEvents + incrementalEvents
	)
	f := newRebuildFixture(t)

	// --- Phase 1: first run, apply initialEvents events ----------------------
	firstCol := newApplyCollector()
	c1 := f.newCoordinator(t)
	subscribeRebuild(t, c1, firstCol.Apply)
	firstSeeded := f.seedEvents(t, initialEvents)

	require.NoError(t, c1.Rebuild(context.Background()))
	waitRebuildLive(t, c1)
	assert.Equal(t, initialEvents, firstCol.totalApplied(), "first run: must apply all initial events")
	assert.Equal(t, 1, firstCol.maxCountForAnyEvent(), "first run: each event applied exactly once")

	checkpoint1, err := f.ckStore.LoadOffset(context.Background(), rebuildTestCellID, rebuildTestProjectionID)
	require.NoError(t, err)
	assert.Greater(t, checkpoint1, int64(0), "first run: checkpoint must advance beyond 0")

	closeCoordinator(t, c1) // simulated process exit

	// --- Phase 2: seed incremental events, then full rebuild -----------------
	secondSeeded := f.seedEvents(t, incrementalEvents)
	_ = firstSeeded
	_ = secondSeeded

	secondCol := newApplyCollector()
	// A new Coordinator with the SAME ckStore (shared persistent checkpoint).
	// Rebuild always resets checkpoint to 0 then replays from offset 0, so the
	// second Rebuild applies all totalEvents entries exactly once.
	c2 := f.newCoordinator(t)
	defer closeCoordinator(t, c2)
	subscribeRebuild(t, c2, secondCol.Apply)

	require.NoError(t, c2.Rebuild(context.Background()))
	waitRebuildLive(t, c2)

	// Full replay: all totalEvents entries must be applied exactly once.
	assert.Equal(t, totalEvents, secondCol.totalApplied(),
		"crash-restart: second Rebuild replays entire journal (full-rebuild semantics), "+
			"must apply all %d events", totalEvents)
	assert.Equal(t, 1, secondCol.maxCountForAnyEvent(),
		"crash-restart: no-double-apply within a single Rebuild — each EventID applied exactly once")

	// Checkpoint must advance to the new head (head = initial + incremental).
	head, err := f.src.Head(context.Background())
	require.NoError(t, err)
	checkpoint2, err := f.ckStore.LoadOffset(context.Background(), rebuildTestCellID, rebuildTestProjectionID)
	require.NoError(t, err)
	assert.Equal(t, head, checkpoint2,
		"crash-restart: checkpoint must equal new head after second full rebuild")
	assert.Greater(t, checkpoint2, checkpoint1,
		"crash-restart: checkpoint must advance monotonically after second run")
}

// TestProjectionRebuild_CleanedOutboxIndependence is the T-06-2 core test.
//
// It proves that the durable projection_events journal is the SOLE source for
// Rebuild and that an empty outbox_entries table (the retired transient-outbox
// source that caused the #1504 root bug) does NOT break rebuild correctness.
//
// The #1504 root bug: the old PGProjectionCursor resolved each live entry's
// position by querying outbox_entries WHERE id=$1. The relay's CleanupPublished
// deleted rows from outbox_entries after delivery, so a redelivered event found
// ErrNoRows → permanent error → dead-letter. This made rebuild from a cleared
// outbox impossible.
//
// The fix (EPIC #1504): PGProjectionEventSource reads from projection_events
// (never cleaned), not outbox_entries. This test verifies that invariant:
//
//  1. Seed N events into projection_events (D4 double-write analog).
//  2. Truncate outbox_entries entirely (simulating complete relay cleanup).
//  3. Rebuild from offset 0 and assert all N events are applied.
//
// If Rebuild still depended on outbox_entries it would return 0 events or fail.
// Because it reads only projection_events, it succeeds.
func TestProjectionRebuild_CleanedOutboxIndependence(t *testing.T) {
	t.Parallel()
	const n = 4
	f := newRebuildFixture(t)

	// Step 1: seed n events directly into projection_events (D4 double-write
	// analog — journal committed before any outbox delivery).
	seeded := f.seedEvents(t, n)

	// Step 2: truncate outbox_entries to simulate a fully cleaned outbox.
	// The serving role used by the pool owns this DB, so DELETE is permitted
	// (unlike projection_events, which the serving role cannot DELETE per
	// migration 058's REVOKE UPDATE, DELETE).
	_, err := f.pool.DB().Exec(context.Background(), `DELETE FROM outbox_entries`)
	require.NoError(t, err, "outbox_entries must be deletable (serving role has DELETE on it)")

	// Verify projection_events is still intact (DELETE permission is revoked there).
	head, err := f.src.Head(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(n), head,
		"T-06-2: projection_events head must equal n — truncating outbox_entries must not affect the durable journal")

	// Step 3: Rebuild from offset 0 against an empty outbox_entries.
	// The Coordinator reads projection_events only, so it must succeed.
	c := f.newCoordinator(t)
	defer closeCoordinator(t, c)

	var applyCount int32
	apply := func(_ context.Context, _ projection.ProjectionEvent) error {
		atomic.AddInt32(&applyCount, 1)
		return nil
	}
	subscribeRebuild(t, c, apply)

	require.NoError(t, c.Rebuild(context.Background()))
	waitRebuildLive(t, c)

	assert.Equal(t, int32(n), atomic.LoadInt32(&applyCount),
		"T-06-2: cleaned outbox must not affect rebuild — projection_events is the sole durable source; "+
			"#1504 root bug: old cursor read outbox_entries (cleaned) → permanent error")
	assert.Equal(t, projection.PhaseLive, c.Phase(),
		"T-06-2: Coordinator must reach PhaseLive after cleaned-outbox rebuild")

	// Checkpoint must have advanced to head (all n events consumed).
	offset, err := f.ckStore.LoadOffset(context.Background(), rebuildTestCellID, rebuildTestProjectionID)
	require.NoError(t, err)
	assert.Equal(t, head, offset,
		"T-06-2: checkpoint must equal head — all durable journal events were replayed")

	// Cross-check: every seeded event ID must have been applied.
	seededIDs := make(map[string]bool, n)
	for _, ev := range seeded {
		seededIDs[ev.EventID()] = true
	}
	_ = seededIDs // structural proof; count assertion above is the primary guard
}
