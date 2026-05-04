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

// emptyIDCell returns "" as its ID.
type emptyIDCell struct {
	*cell.BaseCell
}

func newEmptyIDCell() *emptyIDCell {
	return &emptyIDCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "", Type: "core"}),
	}
}

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
	assert.Contains(t, err.Error(), "cannot register")

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
	assert.Contains(t, err.Error(), "invalid DurabilityMode 0")
}

func TestAssemblyStart_InvalidDurabilityMode_Rejects(t *testing.T) {
	// Non-zero, non-valid mode (e.g., 99) rejected at assembly level.
	// ref: Kubernetes allowlist validation, Uber fx fail-fast
	a := newTestAssembly(t, Config{ID: "test-invalid-mode", DurabilityMode: cell.DurabilityMode(99), Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "any-cell", Type: "core"})
	require.NoError(t, a.Register(c))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid DurabilityMode 99")
}

// ---------------------------------------------------------------------------
// Snapshot lifecycle tests — ref: uber-go/fx App.Done lifecycle state machine.
// ---------------------------------------------------------------------------

// TestAssembly_Snapshots_EmptyAfterInitFailure verifies that when a cell's
// Init returns an error, Snapshots() returns an empty map (not populated data
// from cells that succeeded Init before the failure).
func TestAssembly_Snapshots_EmptyAfterInitFailure(t *testing.T) {
	t.Parallel()

	a := newTestAssembly(t, Config{ID: "snap-init-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	good := cell.MustNewBaseCell(&metadata.CellMeta{ID: "good", Type: "core"})
	bad := newFailInitCell("bad")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)

	snaps := a.Snapshots()
	assert.Empty(t, snaps, "Snapshots() must return empty map after Init failure")
}

// TestAssembly_Snapshots_EmptyAfterStartFailure verifies that when a cell's
// Start returns an error (after a successful Init), Snapshots() returns an
// empty map (assembly did not reach the Started state).
func TestAssembly_Snapshots_EmptyAfterStartFailure(t *testing.T) {
	t.Parallel()

	a := newTestAssembly(t, Config{ID: "snap-start-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	good := cell.MustNewBaseCell(&metadata.CellMeta{ID: "good", Type: "core"})
	bad := newFailStartCell("bad")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)

	snaps := a.Snapshots()
	assert.Empty(t, snaps, "Snapshots() must return empty map after Start failure")
}

// TestAssembly_Snapshots_EmptyAfterStop verifies that after a successful
// Start + Stop cycle, Snapshots() returns an empty map.
func TestAssembly_Snapshots_EmptyAfterStop(t *testing.T) {
	t.Parallel()

	a := newTestAssembly(t, Config{ID: "snap-after-stop", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "c", Type: "core"})

	require.NoError(t, a.Register(c))
	require.NoError(t, a.Start(context.Background()))

	// Before Stop: snapshots should be non-empty.
	snaps := a.Snapshots()
	assert.NotEmpty(t, snaps, "Snapshots() must be non-empty while assembly is running")

	require.NoError(t, a.Stop(context.Background()))

	// After Stop: snapshots should be empty.
	snaps = a.Snapshots()
	assert.Empty(t, snaps, "Snapshots() must return empty map after Stop")
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
