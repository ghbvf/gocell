package assembly

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	ecErr "github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// orderCell records its ID into the provided slice on Stop, enabling reverse-
// order verification.
type orderCell struct {
	*cell.BaseCell
	stopOrder *[]string
}

func newOrderCell(id string, order *[]string) *orderCell {
	return &orderCell{
		BaseCell:  cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L1"}),
		stopOrder: order,
	}
}

func (c *orderCell) Stop(ctx context.Context) error {
	*c.stopOrder = append(*c.stopOrder, c.ID())
	return c.BaseCell.Stop(ctx)
}

// failInitCell fails during Init.
type failInitCell struct {
	*cell.BaseCell
}

func newFailInitCell(id string) *failInitCell {
	return &failInitCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L0"}),
	}
}

func (c *failInitCell) Init(_ context.Context, _ cell.Registry) error {
	return errors.New("init boom")
}

// emptyIDCell returns "" as its ID to test assembly.Register rejection.
// It embeds a validly-constructed BaseCell but overrides ID() to return "".
type emptyIDCell struct {
	*cell.BaseCell
}

func newEmptyIDCell() *emptyIDCell {
	return &emptyIDCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "placeholder-for-empty-id-test", Type: "core"}),
	}
}

func (c *emptyIDCell) ID() string { return "" }

// failStartCell passes Init but fails on Start.
type failStartCell struct {
	*cell.BaseCell
}

func newFailStartCell(id string) *failStartCell {
	return &failStartCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
	}
}

func (c *failStartCell) Start(_ context.Context) error {
	return errors.New("start boom")
}

// failStopCell fails on Stop.
type failStopCell struct {
	*cell.BaseCell
	stopCalled bool
}

func newFailStopCell(id string) *failStopCell {
	return &failStopCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
	}
}

func (c *failStopCell) Stop(_ context.Context) error {
	c.stopCalled = true
	return errors.New("stop boom")
}

type gatedStartCell struct {
	*cell.BaseCell
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newGatedStartCell(id string) *gatedStartCell {
	return &gatedStartCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L0"}),
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (c *gatedStartCell) Start(ctx context.Context) error {
	c.enterOnce.Do(func() { close(c.entered) })
	select {
	case <-c.release:
		return c.BaseCell.Start(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *gatedStartCell) releaseStart() {
	c.releaseOnce.Do(func() { close(c.release) })
}

type gatedStopCell struct {
	*cell.BaseCell
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newGatedStopCell(id string) *gatedStopCell {
	return &gatedStopCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L0"}),
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (c *gatedStopCell) Stop(ctx context.Context) error {
	c.enterOnce.Do(func() { close(c.entered) })
	select {
	case <-c.release:
		return c.BaseCell.Stop(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *gatedStopCell) releaseStop() {
	c.releaseOnce.Do(func() { close(c.release) })
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestAssemblyStartStopHealthy(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "test-assembly", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	c1 := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c1", Type: "core", ConsistencyLevel: "L1"})
	c2 := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c2", Type: "edge", ConsistencyLevel: "L2"})

	require.NoError(t, a.Register(c1))
	require.NoError(t, a.Register(c2))

	require.NoError(t, a.Start(context.Background()))

	health := a.Health()
	assert.Equal(t, "healthy", health["c1"].Status)
	assert.Equal(t, "healthy", health["c2"].Status)

	require.NoError(t, a.Stop(context.Background()))

	health = a.Health()
	assert.Equal(t, "unhealthy", health["c1"].Status)
	assert.Equal(t, "unhealthy", health["c2"].Status)
}

func TestAssemblyStopReverseOrder(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "order-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	var order []string
	c1 := newOrderCell("first", &order)
	c2 := newOrderCell("second", &order)
	c3 := newOrderCell("third", &order)

	require.NoError(t, a.Register(c1))
	require.NoError(t, a.Register(c2))
	require.NoError(t, a.Register(c3))
	require.NoError(t, a.Start(context.Background()))
	require.NoError(t, a.Stop(context.Background()))

	assert.Equal(t, []string{"third", "second", "first"}, order)
}

func TestAssemblyDuplicateCellID(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "dup-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c1 := cell.MustNewBaseCell(&metadata.CellMeta{ID: "same", Type: "core"})
	c2 := cell.MustNewBaseCell(&metadata.CellMeta{ID: "same", Type: "core"})

	require.NoError(t, a.Register(c1))

	err := a.Register(c2)
	require.Error(t, err)
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
}

func TestAssemblyID(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "id-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	assert.Equal(t, "id-test", a.ID())
}

func TestAssemblyRegisterNilCellRejected(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "nil-cell-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	err := a.Register(nil)
	require.Error(t, err)
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
}

func TestAssemblyEmptyCellID(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "empty-id-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	err := a.Register(newEmptyIDCell())
	require.Error(t, err)
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
}

func TestAssemblyFlushHookEventsWithoutDispatcher(t *testing.T) {
	a := &CoreAssembly{}
	assert.True(t, a.FlushHookEvents(0))
}

func TestAssemblyInitFailure(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "init-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	good := cell.MustNewBaseCell(&metadata.CellMeta{ID: "good", Type: "core"})
	bad := newFailInitCell("bad")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)

	// The good cell was Init'd but never Start'd, so unhealthy.
	health := a.Health()
	assert.Equal(t, "unhealthy", health["good"].Status)
	assert.Equal(t, "unhealthy", health["bad"].Status)
}

func TestAssemblyStopWithoutStart(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "no-start", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c", Type: "core"})
	require.NoError(t, a.Register(c))

	// Stop before Start is a no-op (state guard: only Started allows Stop).
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStopOnlyFromStarted(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "guard-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	var order []string
	c := newOrderCell("c1", &order)
	require.NoError(t, a.Register(c))

	// Stop before Start: should be a no-op (no cells stopped).
	require.NoError(t, a.Stop(context.Background()))
	assert.Empty(t, order, "Stop from Stopped state should be a no-op")

	// Start, then Stop from Started state works.
	require.NoError(t, a.Start(context.Background()))
	require.NoError(t, a.Stop(context.Background()))
	assert.Equal(t, []string{"c1"}, order)

	// Stop again: should be no-op (state is Stopped now).
	order = nil
	require.NoError(t, a.Stop(context.Background()))
	assert.Empty(t, order, "Double Stop should be a no-op")
}

func TestAssemblyStopEmpty(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "empty", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartFailureRollback(t *testing.T) {
	// ref: uber-go/fx — Start 失败自动 rollback 已启动的 Cell
	a := newTestAssembly(t, Config{ID: "start-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	var order []string
	good := newOrderCell("good", &order)
	bad := newFailStartCell("bad")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrLifecycleInvalid, ec.Code)

	// good was started then rolled back (Stop called)
	assert.Equal(t, []string{"good"}, order, "rollback should Stop already-started cells")

	// Assembly should be in stopped state, can Start again
	order = nil
	err = a.Start(context.Background())
	require.Error(t, err) // bad still fails
}

func TestAssemblyDoubleStartPrevented(t *testing.T) {
	// ref: uber-go/fx lifecycle.go — 状态机防止重入
	a := newTestAssembly(t, Config{ID: "double-start", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c", Type: "core"})
	require.NoError(t, a.Register(c))
	require.NoError(t, a.Start(context.Background()))

	err := a.Start(context.Background())
	require.Error(t, err, "double start should fail")
}

func TestAssemblyStartWhileStartingRejected(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "start-while-starting", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newGatedStartCell("c")
	t.Cleanup(c.releaseStart)
	require.NoError(t, a.Register(c))

	startDone := make(chan error, 1)
	go func() {
		startDone <- a.Start(context.Background())
	}()
	<-c.entered

	err := a.Start(context.Background())
	require.Error(t, err, "Start while stateStarting must fail")
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "cannot start in current state")

	c.releaseStart()
	require.NoError(t, <-startDone)
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartWhileStoppingRejected(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "start-while-stopping", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newGatedStopCell("c")
	t.Cleanup(c.releaseStop)
	require.NoError(t, a.Register(c))
	require.NoError(t, a.Start(context.Background()))

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- a.Stop(context.Background())
	}()
	<-c.entered

	err := a.Start(context.Background())
	require.Error(t, err, "Start while stateStopping must fail")
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "cannot start in current state")
	require.NoError(t, a.Stop(context.Background()), "Stop while stateStopping is a no-op")

	c.releaseStop()
	require.NoError(t, <-stopDone)
	assert.Nil(t, a.Snapshots())
}

func TestAssemblyRegisterAfterStartRejected(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "reg-after-start", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c1 := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c1", Type: "core"})
	require.NoError(t, a.Register(c1))
	require.NoError(t, a.Start(context.Background()))

	c2 := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c2", Type: "core"})
	err := a.Register(c2)
	require.Error(t, err, "register after start should fail")
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "cannot register")

	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStopContinuesOnError(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "stop-err", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	good := cell.MustNewBaseCell(&metadata.CellMeta{ID: "good", Type: "core"})
	bad1 := newFailStopCell("bad1")
	bad2 := newFailStopCell("bad2")

	// Register order: good, bad1, bad2
	// Stop order (reverse): bad2, bad1, good
	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad1))
	require.NoError(t, a.Register(bad2))
	require.NoError(t, a.Start(context.Background()))

	err := a.Stop(context.Background())
	require.Error(t, err)
	// First error should be from "bad2" (last registered, stopped first).
	assert.Contains(t, err.Error(), "bad2")
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrLifecycleInvalid, ec.Code)
	// Both fail-stop cells should have been called despite errors.
	assert.True(t, bad1.stopCalled)
	assert.True(t, bad2.stopCalled)
}

func TestAssemblyStartWithConfig(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "config-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c1", Type: "core"})
	require.NoError(t, a.Register(c))

	cfgMap := map[string]any{"key": "value"}
	require.NoError(t, a.StartWithConfig(context.Background(), cfgMap))

	health := a.Health()
	assert.Equal(t, "healthy", health["c1"].Status)
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartWithConfigDoubleStart(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "double-cfg", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c1", Type: "core"})
	require.NoError(t, a.Register(c))
	require.NoError(t, a.StartWithConfig(context.Background(), nil))

	err := a.StartWithConfig(context.Background(), nil)
	require.Error(t, err)
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartWithConfigInitFailure(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "init-fail-cfg", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	bad := newFailInitCell("bad")
	require.NoError(t, a.Register(bad))

	err := a.StartWithConfig(context.Background(), nil)
	require.Error(t, err)
}

func TestAssemblyStartWithConfigStartFailureRollback(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "start-fail-cfg", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	var order []string
	good := newOrderCell("good", &order)
	bad := newFailStartCell("bad")
	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.StartWithConfig(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, []string{"good"}, order)
}

func TestAssemblyCellIDs(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "ids-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, a.Register(cell.MustNewBaseCell(&metadata.CellMeta{ID: "a", Type: "core"})))
	require.NoError(t, a.Register(cell.MustNewBaseCell(&metadata.CellMeta{ID: "b", Type: "core"})))

	ids := a.CellIDs()
	assert.Equal(t, []string{"a", "b"}, ids)
}

func TestAssemblyHealthConcurrentWithRegister(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "concurrent-health", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	// Pre-register some cells.
	for i := range 5 {
		id := "pre-" + string(rune('a'+i))
		require.NoError(t, a.Register(cell.MustNewBaseCell(&metadata.CellMeta{
			ID: id, Type: "core", ConsistencyLevel: "L0",
		})))
	}

	// Concurrently call Health() from multiple goroutines while the assembly
	// is in stopped state. This validates the snapshot-under-lock pattern
	// does not race with reads.
	var wg sync.WaitGroup
	const readers = 20

	wg.Add(readers)
	for range readers {
		go func() {
			defer wg.Done()
			h := a.Health()
			assert.Len(t, h, 5)
		}()
	}
	wg.Wait()
}

func TestAssemblyCellLookup(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "lookup-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "x", Type: "core"})
	require.NoError(t, a.Register(c))

	found := a.Cell("x")
	assert.NotNil(t, found)
	assert.Equal(t, "x", found.ID())

	assert.Nil(t, a.Cell("nonexistent"))
}

func TestAssemblyStart_ZeroDurabilityMode_FailsAtAssemblyLevel(t *testing.T) {
	// Zero DurabilityMode is rejected at assembly.Start — before any cell.Init runs.
	a := newTestAssembly(t, Config{ID: "test-zero-durability", Clock: clock.Real()}) // zero DurabilityMode (unset)
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "any-cell", Type: "core"})
	require.NoError(t, a.Register(c))

	err := a.Start(context.Background())
	require.Error(t, err)
	var ec0 *ecErr.Error
	require.True(t, errors.As(err, &ec0))
	assert.Contains(t, ec0.Message, "validate mode failed")
}

func TestAssemblyStart_InvalidDurabilityMode_Rejects(t *testing.T) {
	// Non-zero, non-valid mode (e.g., 99) rejected at assembly level.
	// ref: Kubernetes allowlist validation, Uber fx fail-fast
	a := newTestAssembly(t, Config{ID: "test-invalid-mode", DurabilityMode: cell.DurabilityMode(99), Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "any-cell", Type: "core"})
	require.NoError(t, a.Register(c))

	err := a.Start(context.Background())
	require.Error(t, err)
	var ec99 *ecErr.Error
	require.True(t, errors.As(err, &ec99))
	assert.Contains(t, ec99.Message, "validate mode failed")
}

// ---------------------------------------------------------------------------
// Snapshot lifecycle tests — ref: uber-go/fx App.Done lifecycle state machine.
// ---------------------------------------------------------------------------

// TestAssembly_Snapshots_NilAfterInitFailure verifies that when a cell's
// Init returns an error, Snapshots() returns nil (not populated data
// from cells that succeeded Init before the failure).
func TestAssembly_Snapshots_NilAfterInitFailure(t *testing.T) {
	t.Parallel()

	a := newTestAssembly(t, Config{ID: "snap-init-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	good := cell.MustNewBaseCell(&metadata.CellMeta{ID: "good", Type: "core"})
	bad := newFailInitCell("bad")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)

	snaps := a.Snapshots()
	assert.Nil(t, snaps, "Snapshots() must return nil after Init failure")
}

// TestAssembly_Snapshots_NilAfterStartFailure verifies that when a cell's
// Start returns an error (after a successful Init), Snapshots() returns nil
// because assembly did not reach the Started state.
func TestAssembly_Snapshots_NilAfterStartFailure(t *testing.T) {
	t.Parallel()

	a := newTestAssembly(t, Config{ID: "snap-start-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	good := cell.MustNewBaseCell(&metadata.CellMeta{ID: "good", Type: "core"})
	bad := newFailStartCell("bad")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)

	snaps := a.Snapshots()
	assert.Nil(t, snaps, "Snapshots() must return nil after Start failure")
}

// TestAssembly_Snapshots_NilAfterStop verifies that after a successful
// Start + Stop cycle, Snapshots() returns nil.
func TestAssembly_Snapshots_NilAfterStop(t *testing.T) {
	t.Parallel()

	a := newTestAssembly(t, Config{ID: "snap-after-stop", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c", Type: "core"})

	require.NoError(t, a.Register(c))
	require.NoError(t, a.Start(context.Background()))

	// Before Stop: snapshots should be non-empty.
	snaps := a.Snapshots()
	assert.NotEmpty(t, snaps, "Snapshots() must be non-empty while assembly is running")

	require.NoError(t, a.Stop(context.Background()))

	// After Stop: snapshots should be nil.
	snaps = a.Snapshots()
	assert.Nil(t, snaps, "Snapshots() must return nil after Stop")
}

func TestAssembly_Snapshots_NilWhileStartInProgress(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "snap-while-starting", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newGatedStartCell("c")
	t.Cleanup(c.releaseStart)
	require.NoError(t, a.Register(c))

	startDone := make(chan error, 1)
	go func() {
		startDone <- a.Start(context.Background())
	}()
	<-c.entered

	assert.Nil(t, a.Snapshots(), "Snapshots() must stay nil until Start fully succeeds")

	c.releaseStart()
	require.NoError(t, <-startDone)
	assert.NotEmpty(t, a.Snapshots(), "Snapshots() must publish after Start reaches stateStarted")
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssembly_Snapshots_NilWhileStopInProgress(t *testing.T) {
	a := newTestAssembly(t, Config{ID: "snap-while-stopping", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newGatedStopCell("c")
	t.Cleanup(c.releaseStop)
	require.NoError(t, a.Register(c))
	require.NoError(t, a.Start(context.Background()))
	require.NotEmpty(t, a.Snapshots(), "precondition: snapshots are visible while started")

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- a.Stop(context.Background())
	}()
	<-c.entered

	assert.Nil(t, a.Snapshots(), "Snapshots() must be nil once Stop moves assembly out of stateStarted")

	c.releaseStop()
	require.NoError(t, <-stopDone)
	assert.Nil(t, a.Snapshots(), "Snapshots() must remain nil after Stop completes")
}

// TestAssembly_StartInternal_PerCellConfigIsolation verifies that each cell
// receives an independent deep copy of the config map so that mutations by one
// cell's Init path do not affect sibling cells.
//
// ref: spf13/viper AllSettings() — returns a deep copy for isolation.
// ref: k8s client-go DeepCopyObject — each consumer owns its own value.
func TestAssembly_StartInternal_PerCellConfigIsolation(t *testing.T) {
	t.Parallel()

	const key = "shared_key"
	cfgMap := map[string]any{key: "original"}

	// firstCell records the value it sees and mutates the map it received.
	var firstSeen string
	firstCell := &configMutatingCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID: "first", Type: "core", ConsistencyLevel: "L0",
		}),
		onInit: func(reg cell.Registry) error {
			firstSeen = fmt.Sprintf("%v", reg.Config()[key])
			// Mutate the map — should NOT affect the second cell.
			reg.Config()[key] = "mutated-by-first"
			return nil
		},
	}

	// secondCell reads the value after firstCell's Init has already mutated its own copy.
	var secondSeen string
	secondCell := &configMutatingCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID: "second", Type: "core", ConsistencyLevel: "L0",
		}),
		onInit: func(reg cell.Registry) error {
			secondSeen = fmt.Sprintf("%v", reg.Config()[key])
			return nil
		},
	}

	a := newTestAssembly(t, Config{ID: "isolation-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, a.Register(firstCell))
	require.NoError(t, a.Register(secondCell))

	require.NoError(t, a.StartWithConfig(context.Background(), cfgMap))
	defer a.Stop(context.Background()) //nolint:errcheck // teardown best-effort; assertions cover lifecycle

	assert.Equal(t, "original", firstSeen, "first cell should see original value")
	assert.Equal(t, "original", secondSeen, "second cell should see original value, not mutation from first cell")
}

// configMutatingCell is a test cell that calls onInit during Init.
type configMutatingCell struct {
	*cell.BaseCell
	onInit func(cell.Registry) error
}

func (c *configMutatingCell) Init(ctx context.Context, reg cell.Registry) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	if c.onInit != nil {
		return c.onInit(reg)
	}
	return nil
}

// ---------------------------------------------------------------------------
// STARTUP-ROLLBACK-ERR-JOIN-01: rollback cell-stop errors surfaced in Start
// ---------------------------------------------------------------------------

// sentinelStopCell starts OK but returns a fixed sentinel error from Stop.
// This lets tests assert that the specific sentinel propagates through the
// joined error tree returned by a.Start().
type sentinelStopCell struct {
	*cell.BaseCell
	stopErr error // returned verbatim from Stop
}

func newSentinelStopCell(id string, stopErr error) *sentinelStopCell {
	return &sentinelStopCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L1"}),
		stopErr:  stopErr,
	}
}

func (c *sentinelStopCell) Stop(_ context.Context) error {
	return c.stopErr
}

// afterStartSentinelStopCell has an AfterStart hook that returns an error,
// triggering the rollbackCells(i) branch (failing cell itself is rolled back).
// Stop also returns a sentinel so the test can verify it surfaces in the joined tree.
type afterStartSentinelStopCell struct {
	*cell.BaseCell
	afterStartErr error
	stopErr       error
}

func newAfterStartSentinelStopCell(id string, afterStartErr, stopErr error) *afterStartSentinelStopCell {
	return &afterStartSentinelStopCell{
		BaseCell:      cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L1"}),
		afterStartErr: afterStartErr,
		stopErr:       stopErr,
	}
}

func (c *afterStartSentinelStopCell) AfterStart(_ context.Context) error {
	return c.afterStartErr
}

func (c *afterStartSentinelStopCell) Stop(_ context.Context) error {
	return c.stopErr
}

var _ cell.AfterStarter = (*afterStartSentinelStopCell)(nil)

// walkJoinedErrors traverses the errors.Join tree and collects all leaf errors.
func walkJoinedErrors(err error, collected *[]error) {
	if err == nil {
		return
	}
	type joinedUnwrap interface {
		Unwrap() []error
	}
	if joined, ok := err.(joinedUnwrap); ok {
		for _, e := range joined.Unwrap() {
			walkJoinedErrors(e, collected)
		}
	} else {
		*collected = append(*collected, err)
	}
}

// sentinelStartCell starts with a fixed sentinel error from Start, used to
// trigger rollback of previously-started cells.
type sentinelStartCell struct {
	*cell.BaseCell
	startErr error
}

func newSentinelStartCell(id string, startErr error) *sentinelStartCell {
	return &sentinelStartCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core", ConsistencyLevel: "L1"}),
		startErr: startErr,
	}
}

func (c *sentinelStartCell) Start(_ context.Context) error {
	return c.startErr
}

// TestStartInternal_RollbackCellStopErrorsSurfaced verifies that when startup
// rollback occurs (due to a cell's Start failing), stop errors from already-started
// cells are surfaced in the returned error, not silently discarded.
//
// Cell A starts OK but Stop returns errStopA.
// Cell B's Start returns errStartB, triggering rollback of A.
// Start must return an error satisfying errors.Is(_, errStartB) AND the
// joined tree must contain a *errcode.Error wrapping errStopA.
func TestStartInternal_RollbackCellStopErrorsSurfaced(t *testing.T) {
	t.Parallel()

	errStopA := errors.New("sentinel-stop-a")
	errStartB := errors.New("sentinel-start-b")

	a := newTestAssembly(t, Config{ID: "rollback-stop-surfaced", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	cellA := newSentinelStopCell("A", errStopA)
	cellBSentinel := newSentinelStartCell("B", errStartB)

	require.NoError(t, a.Register(cellA))
	require.NoError(t, a.Register(cellBSentinel))

	err := a.Start(context.Background())
	require.Error(t, err)

	// Cause: errStartB must be findable via errors.Is.
	assert.True(t, errors.Is(err, errStartB), "errors.Is(err, errStartB) must be true; got: %v", err)

	// Rollback stop error: walk joined tree and find a *ecErr.Error that wraps errStopA.
	var allErrs []error
	walkJoinedErrors(err, &allErrs)

	foundStopErr := false
	for _, e := range allErrs {
		if errors.Is(e, errStopA) {
			foundStopErr = true
			var ec *ecErr.Error
			if errors.As(e, &ec) {
				assert.Equal(t, ecErr.ErrLifecycleInvalid, ec.Code,
					"stop error must be wrapped as ErrLifecycleInvalid")
			}
			break
		}
	}
	assert.True(t, foundStopErr, "rolled-back cell A's stop error must appear in joined tree; full err: %v", err)
}

// TestStartInternal_AfterStartFailRollbackIncludesFailingCell verifies that when
// AfterStart fails, the rollbackCells(i) path (which includes the failing cell
// itself) surfaces the failing cell's Stop error in the returned error.
func TestStartInternal_AfterStartFailRollbackIncludesFailingCell(t *testing.T) {
	t.Parallel()

	errAfterStartA := errors.New("sentinel-afterstart-a")
	errStopA := errors.New("sentinel-stop-a-after-rollback")

	a := newTestAssembly(t, Config{ID: "rollback-afterstart-stopsurf", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	cellA := newAfterStartSentinelStopCell("A", errAfterStartA, errStopA)
	require.NoError(t, a.Register(cellA))

	err := a.Start(context.Background())
	require.Error(t, err)

	// AfterStart error must be surfaced as the cause.
	assert.True(t, errors.Is(err, errAfterStartA), "errors.Is(err, errAfterStartA) must be true; got: %v", err)

	// The failing cell A's Stop error must also appear in the joined tree.
	var allErrs []error
	walkJoinedErrors(err, &allErrs)

	foundStopErr := false
	for _, e := range allErrs {
		if errors.Is(e, errStopA) {
			foundStopErr = true
			break
		}
	}
	assert.True(t, foundStopErr, "failing cell A's Stop error from rollbackCells(i) must appear in joined tree; full err: %v", err)
}

// TestRollbackCells_ReturnsCollectedErrorsInLIFO verifies that rollbackCells
// returns the accumulated stop errors in LIFO order (cell[1] error first,
// cell[0] error second) and that rollbackCells(-1) returns nil.
func TestRollbackCells_ReturnsCollectedErrorsInLIFO(t *testing.T) {
	t.Parallel()

	errStop0 := errors.New("sentinel-stop-0")
	errStop1 := errors.New("sentinel-stop-1")

	a := newTestAssembly(t, Config{ID: "rollback-lifo-order", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	cell0 := newSentinelStopCell("cell0", errStop0)
	cell1 := newSentinelStopCell("cell1", errStop1)
	require.NoError(t, a.Register(cell0))
	require.NoError(t, a.Register(cell1))

	// rollbackCells(-1) must return nil immediately (no cells started yet).
	errsNeg := a.rollbackCells(-1)
	assert.Nil(t, errsNeg, "rollbackCells(-1) must return nil")

	// rollbackCells(1) stops cells[1] then cells[0] (LIFO):
	// cell1.Stop returns errStop1, cell0.Stop returns errStop0.
	// Each is wrapped as *errcode.Error by stopCellWithHooks.
	// We expect 2 errors, with errStop1 first (LIFO) and errStop0 second.
	errs := a.rollbackCells(1)
	assert.Len(t, errs, 2, "rollbackCells(1) must collect 2 stop errors")

	// LIFO: cell1 was stopped first so its error appears at index 0.
	assert.True(t, errors.Is(errs[0], errStop1), "first error must wrap errStop1 (LIFO); got: %v", errs[0])
	assert.True(t, errors.Is(errs[1], errStop0), "second error must wrap errStop0 (LIFO); got: %v", errs[1])
}
