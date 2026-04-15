package assembly

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	ecErr "github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		BaseCell:  cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore, ConsistencyLevel: cell.L1}),
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
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore, ConsistencyLevel: cell.L0}),
	}
}

func (c *failInitCell) Init(_ context.Context, _ cell.Dependencies) error {
	return errors.New("init boom")
}

// emptyIDCell returns "" as its ID.
type emptyIDCell struct {
	*cell.BaseCell
}

func newEmptyIDCell() *emptyIDCell {
	return &emptyIDCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: "", Type: cell.CellTypeCore}),
	}
}

// failStartCell passes Init but fails on Start.
type failStartCell struct {
	*cell.BaseCell
}

func newFailStartCell(id string) *failStartCell {
	return &failStartCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
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
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
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
	a := New(Config{ID: "test-assembly", DurabilityMode: cell.DurabilityDemo})

	c1 := cell.NewBaseCell(cell.CellMetadata{ID: "c1", Type: cell.CellTypeCore, ConsistencyLevel: cell.L1})
	c2 := cell.NewBaseCell(cell.CellMetadata{ID: "c2", Type: cell.CellTypeEdge, ConsistencyLevel: cell.L2})

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
	a := New(Config{ID: "order-test", DurabilityMode: cell.DurabilityDemo})

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
	a := New(Config{ID: "dup-test", DurabilityMode: cell.DurabilityDemo})
	c1 := cell.NewBaseCell(cell.CellMetadata{ID: "same", Type: cell.CellTypeCore})
	c2 := cell.NewBaseCell(cell.CellMetadata{ID: "same", Type: cell.CellTypeCore})

	require.NoError(t, a.Register(c1))

	err := a.Register(c2)
	require.Error(t, err)
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
}

func TestAssemblyEmptyCellID(t *testing.T) {
	a := New(Config{ID: "empty-id-test", DurabilityMode: cell.DurabilityDemo})

	err := a.Register(newEmptyIDCell())
	require.Error(t, err)
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
}

func TestAssemblyInitFailure(t *testing.T) {
	a := New(Config{ID: "init-fail", DurabilityMode: cell.DurabilityDemo})

	good := cell.NewBaseCell(cell.CellMetadata{ID: "good", Type: cell.CellTypeCore})
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
	a := New(Config{ID: "no-start", DurabilityMode: cell.DurabilityDemo})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "c", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))

	// Stop before Start is a no-op (state guard: only Started allows Stop).
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStopOnlyFromStarted(t *testing.T) {
	a := New(Config{ID: "guard-test", DurabilityMode: cell.DurabilityDemo})
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
	a := New(Config{ID: "empty", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartFailureRollback(t *testing.T) {
	// ref: uber-go/fx — Start 失败自动 rollback 已启动的 Cell
	a := New(Config{ID: "start-fail", DurabilityMode: cell.DurabilityDemo})

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
	a := New(Config{ID: "double-start", DurabilityMode: cell.DurabilityDemo})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "c", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))
	require.NoError(t, a.Start(context.Background()))

	err := a.Start(context.Background())
	require.Error(t, err, "double start should fail")
}

func TestAssemblyRegisterAfterStartRejected(t *testing.T) {
	a := New(Config{ID: "reg-after-start", DurabilityMode: cell.DurabilityDemo})
	c1 := cell.NewBaseCell(cell.CellMetadata{ID: "c1", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c1))
	require.NoError(t, a.Start(context.Background()))

	c2 := cell.NewBaseCell(cell.CellMetadata{ID: "c2", Type: cell.CellTypeCore})
	err := a.Register(c2)
	require.Error(t, err, "register after start should fail")
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "cannot register")

	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStopContinuesOnError(t *testing.T) {
	a := New(Config{ID: "stop-err", DurabilityMode: cell.DurabilityDemo})

	good := cell.NewBaseCell(cell.CellMetadata{ID: "good", Type: cell.CellTypeCore})
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
	a := New(Config{ID: "config-test", DurabilityMode: cell.DurabilityDemo})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "c1", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))

	cfgMap := map[string]any{"key": "value"}
	require.NoError(t, a.StartWithConfig(context.Background(), cfgMap))

	health := a.Health()
	assert.Equal(t, "healthy", health["c1"].Status)
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartWithConfigDoubleStart(t *testing.T) {
	a := New(Config{ID: "double-cfg", DurabilityMode: cell.DurabilityDemo})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "c1", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))
	require.NoError(t, a.StartWithConfig(context.Background(), nil))

	err := a.StartWithConfig(context.Background(), nil)
	require.Error(t, err)
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartWithConfigInitFailure(t *testing.T) {
	a := New(Config{ID: "init-fail-cfg", DurabilityMode: cell.DurabilityDemo})
	bad := newFailInitCell("bad")
	require.NoError(t, a.Register(bad))

	err := a.StartWithConfig(context.Background(), nil)
	require.Error(t, err)
}

func TestAssemblyStartWithConfigStartFailureRollback(t *testing.T) {
	a := New(Config{ID: "start-fail-cfg", DurabilityMode: cell.DurabilityDemo})

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
	a := New(Config{ID: "ids-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, a.Register(cell.NewBaseCell(cell.CellMetadata{ID: "a", Type: cell.CellTypeCore})))
	require.NoError(t, a.Register(cell.NewBaseCell(cell.CellMetadata{ID: "b", Type: cell.CellTypeCore})))

	ids := a.CellIDs()
	assert.Equal(t, []string{"a", "b"}, ids)
}

func TestAssemblyHealthConcurrentWithRegister(t *testing.T) {
	a := New(Config{ID: "concurrent-health", DurabilityMode: cell.DurabilityDemo})

	// Pre-register some cells.
	for i := range 5 {
		id := "pre-" + string(rune('a'+i))
		require.NoError(t, a.Register(cell.NewBaseCell(cell.CellMetadata{
			ID: id, Type: cell.CellTypeCore, ConsistencyLevel: cell.L0,
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
	a := New(Config{ID: "lookup-test", DurabilityMode: cell.DurabilityDemo})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "x", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))

	found := a.Cell("x")
	assert.NotNil(t, found)
	assert.Equal(t, "x", found.ID())

	assert.Nil(t, a.Cell("nonexistent"))
}

func TestAssemblyStart_ZeroDurabilityMode_FailsAtAssemblyLevel(t *testing.T) {
	// Zero DurabilityMode is rejected at assembly.Start — before any cell.Init runs.
	a := New(Config{ID: "test-zero-durability"}) // zero DurabilityMode (unset)
	c := cell.NewBaseCell(cell.CellMetadata{ID: "any-cell", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid DurabilityMode 0")
}

func TestAssemblyStart_InvalidDurabilityMode_Rejects(t *testing.T) {
	// Non-zero, non-valid mode (e.g., 99) rejected at assembly level.
	// ref: Kubernetes allowlist validation, Uber fx fail-fast
	a := New(Config{ID: "test-invalid-mode", DurabilityMode: cell.DurabilityMode(99)})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "any-cell", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid DurabilityMode 99")
}
