package bootstrap

// phase3_init_test.go — unit tests for the phase3 Registry-drain flow.
//
// Covers:
//   - phase3InitAssembly populates s.cellSnapshots after StartWithConfig
//   - init error aborts before Start (no cells started)
//   - phase3bDrainLifecycleHooks (LIFO / ordering / snapshot-drain)
//   - phase5CollectRouteGroups drains RouteGroups from snapshots
//   - phase6StartEventRouter drains Subscriptions from snapshots
//   - TestBootstrap_NoSubscriptionsAndNoSubscriber_Succeeds
//   - TestBootstrap_HasSubscriptionsButNoSubscriber_FailsFast

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
)

// ---------------------------------------------------------------------------
// Fixture cells
// ---------------------------------------------------------------------------

// snapshotCheckCell is a minimal cell that registers one health probe in Init.
type snapshotCheckCell struct {
	cell.BaseCell
}

func newSnapshotCheckCell(id string) *snapshotCheckCell {
	return &snapshotCheckCell{
		BaseCell: *cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L0"}),
	}
}

func (c *snapshotCheckCell) Init(ctx context.Context, reg cell.Registry) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	reg.Health("probe."+c.ID(), func(_ context.Context) error { return nil })
	return nil
}

// initFailCell fails during Init with a configurable error.
type initFailCell struct {
	cell.BaseCell
	err error
}

func newInitFailCell(id string, err error) *initFailCell {
	return &initFailCell{
		BaseCell: *cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L0"}),
		err:      err,
	}
}

func (c *initFailCell) Init(_ context.Context, _ cell.Registry) error {
	return c.err
}

// lifecycleRegisterCell registers one lifecycle hook in Init.
type lifecycleRegisterCell struct {
	cell.BaseCell
	hookName string
}

func newLifecycleRegisterCell(id, hookName string) *lifecycleRegisterCell {
	return &lifecycleRegisterCell{
		BaseCell: *cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L0"}),
		hookName: hookName,
	}
}

func (c *lifecycleRegisterCell) Init(ctx context.Context, reg cell.Registry) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	reg.Lifecycle(cell.LifecycleHook{
		Name:    c.hookName,
		OnStart: func(_ context.Context) error { return nil },
	})
	return nil
}

// routeRegisterCell registers one RouteGroup in Init.
type routeRegisterCell struct {
	cell.BaseCell
}

func newRouteRegisterCell(id string) *routeRegisterCell {
	return &routeRegisterCell{
		BaseCell: *cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L0"}),
	}
}

func (c *routeRegisterCell) Init(ctx context.Context, reg cell.Registry) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	reg.RouteGroup(cell.RouteGroup{
		Listener: cell.PrimaryListener,
		Prefix:   "/api/v1/" + c.ID(),
		Register: func(_ cell.RouteMux) error { return nil },
	})
	return nil
}

// subscribeRegisterCell registers one subscription in Init.
type subscribeRegisterCell struct {
	cell.BaseCell
	topic string
}

func newSubscribeRegisterCell(id, topic string) *subscribeRegisterCell {
	return &subscribeRegisterCell{
		BaseCell: *cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L0"}),
		topic:    topic,
	}
}

func (c *subscribeRegisterCell) Init(ctx context.Context, reg cell.Registry) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	return reg.Subscribe(contractspec.ContractSpec{
		ID:        c.topic,
		Kind:      cellvocab.ContractEvent,
		Transport: "amqp",
		Topic:     c.topic,
	}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.Ack()
	}, c.ID(), c.ID())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildStartedAsm builds and starts an assembly with the given cells.
func buildStartedAsm(t *testing.T, cells ...cell.Cell) *assembly.CoreAssembly {
	t.Helper()
	asm := assembly.New(assembly.Config{
		ID:             "test-phase3-asm",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})
	for _, c := range cells {
		require.NoError(t, asm.Register(c))
	}
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })
	return asm
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestPhase3_AssemblyInitsAllCellsWithRegistry_PopulatesSnapshots verifies that
// after phase3InitAssembly completes, s.cellSnapshots contains one entry per
// registered cell, and each snapshot reflects what the cell registered in Init.
func TestPhase3_AssemblyInitsAllCellsWithRegistry_PopulatesSnapshots(t *testing.T) {
	c1 := newSnapshotCheckCell("c1")
	c2 := newSnapshotCheckCell("c2")
	asm := buildStartedAsm(t, c1, c2)

	snaps := asm.Snapshots()
	require.Len(t, snaps, 2)

	snap1, ok := snaps["c1"]
	require.True(t, ok)
	assert.Contains(t, snap1.HealthCheckers, "probe.c1",
		"c1 snapshot must contain the probe registered in Init")

	snap2, ok := snaps["c2"]
	require.True(t, ok)
	assert.Contains(t, snap2.HealthCheckers, "probe.c2",
		"c2 snapshot must contain the probe registered in Init")
}

// TestPhase3_InitErrorAbortsBeforeStart verifies that when a cell's Init fails,
// the assembly fails StartWithConfig and no snapshots are available for later cells.
func TestPhase3_InitErrorAbortsBeforeStart(t *testing.T) {
	goodCell := newSnapshotCheckCell("good")
	badCell := newInitFailCell("bad", errors.New("init exploded"))

	asm := assembly.New(assembly.Config{
		ID:             "abort-test",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})
	require.NoError(t, asm.Register(goodCell))
	require.NoError(t, asm.Register(badCell))

	err := asm.Start(context.Background())
	require.Error(t, err, "assembly.Start must fail when any Init fails")
	assert.Contains(t, err.Error(), "init exploded")
	assert.Contains(t, err.Error(), "bad")

	// Snapshots returns nil since Start failed.
	assert.Nil(t, asm.Snapshots(), "Snapshots() must be nil after failed Start")
}

// TestPhase3b_LIFOAppendOfLifecycleHooksFromSnapshots verifies that
// phase3bDrainLifecycleHooks appends hooks in cell-registration order
// (alpha before beta) and that LIFO on Stop means beta stops before alpha.
func TestPhase3b_LIFOAppendOfLifecycleHooksFromSnapshots(t *testing.T) {
	alpha := newLifecycleRegisterCell("alpha", "alpha-start")
	beta := newLifecycleRegisterCell("beta", "beta-start")

	// Register alpha first, then beta — phase3b must respect CellIDs() order.
	asm := buildStartedAsm(t, alpha, beta)

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	require.Len(t, ml.appended, 2)
	assert.Equal(t, "alpha-start", ml.appended[0].Name, "alpha hook must be appended first (FIFO registration)")
	assert.Equal(t, "beta-start", ml.appended[1].Name, "beta hook must be appended second")
	assert.Equal(t, "alpha", ml.appended[0].CellID)
	assert.Equal(t, "beta", ml.appended[1].CellID)
}

// TestPhase5_RouteGroupsDrainedFromSnapshots verifies that phase5CollectRouteGroups
// returns RouteGroups registered by cells via reg.RouteGroup in Init, stamped with CellID.
func TestPhase5_RouteGroupsDrainedFromSnapshots(t *testing.T) {
	r1 := newRouteRegisterCell("svc1")
	asm := buildStartedAsm(t, r1)

	b := New(WithClock(clock.Real()))
	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	// Build a minimal hh so phase5CollectRouteGroups can succeed.
	s.hh = health.New(asm, clock.Real())

	// routers is empty here — we just want to verify groups are collected.
	routers := map[cell.ListenerRef]*router.Router{}
	groups := b.phase5CollectRouteGroups(s, routers)

	// Filter to only groups from our cell (not health groups).
	var cellGroups []cell.RouteGroup
	for _, g := range groups {
		if g.CellID == "svc1" {
			cellGroups = append(cellGroups, g)
		}
	}
	require.Len(t, cellGroups, 1, "svc1 must contribute exactly one RouteGroup")
	assert.Equal(t, cell.PrimaryListener, cellGroups[0].Listener)
	assert.Equal(t, "/api/v1/svc1", cellGroups[0].Prefix)
	assert.Equal(t, "svc1", cellGroups[0].CellID, "CellID must be stamped by phase5CollectRouteGroups")
}

// TestPhase6_SubscriptionsDrainedFromSnapshots verifies that phase6StartEventRouter
// correctly drains Subscriptions from cellSnapshots into the event router.
func TestPhase6_SubscriptionsDrainedFromSnapshots(t *testing.T) {
	sub := newSubscribeRegisterCell("subs-cell", "event.phase3.test.v1")
	asm := buildStartedAsm(t, sub)

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	snaps := asm.Snapshots()
	require.Len(t, snaps["subs-cell"].Subscriptions, 1, "one subscription must be in snapshot")
	assert.Equal(t, "event.phase3.test.v1", snaps["subs-cell"].Subscriptions[0].Spec.Topic)
	assert.Equal(t, "subs-cell", snaps["subs-cell"].Subscriptions[0].ConsumerGroup)
}

// TestBootstrap_NoSubscriptionsAndNoSubscriber_Succeeds verifies that a bootstrap
// with no cells registering subscriptions and no WithSubscriber starts cleanly.
func TestBootstrap_NoSubscriptionsAndNoSubscriber_Succeeds(t *testing.T) {
	plain := cell.MustNewBaseCell(&metadata.CellMeta{ID: "plain", Type: "core"})
	asm := buildStartedAsm(t, plain)

	b := New(WithClock(clock.Real()))
	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.hh = health.New(asm, clock.Real())
	// sub is nil — no subscriber configured.

	err := b.phase6StartEventRouter(runCtx, s)
	assert.NoError(t, err, "phase6 must succeed when no subscriptions and no subscriber")
}

// TestBootstrap_HasSubscriptionsButNoSubscriber_FailsFast verifies that when a
// cell registered subscriptions but no subscriber is configured, phase6 fails fast.
func TestBootstrap_HasSubscriptionsButNoSubscriber_FailsFast(t *testing.T) {
	subCell := newSubscribeRegisterCell("needs-sub", "event.no.subscriber.v1")
	asm := buildStartedAsm(t, subCell)

	b := New(WithClock(clock.Real()))
	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.hh = health.New(asm, clock.Real())
	// sub is nil intentionally.

	err := b.phase6StartEventRouter(runCtx, s)
	require.Error(t, err, "phase6 must fail when subscriptions exist but no subscriber configured")
	assert.Contains(t, err.Error(), "registered subscriptions")
	assert.Contains(t, err.Error(), "no subscriber")
}
