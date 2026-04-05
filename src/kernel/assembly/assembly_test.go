package assembly

import (
	"context"
	"errors"
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
	a := New(Config{ID: "test-assembly"})

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
	a := New(Config{ID: "order-test"})

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
	a := New(Config{ID: "dup-test"})
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
	a := New(Config{ID: "empty-id-test"})

	err := a.Register(newEmptyIDCell())
	require.Error(t, err)
	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
}

func TestAssemblyInitFailure(t *testing.T) {
	a := New(Config{ID: "init-fail"})

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
	a := New(Config{ID: "no-start"})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "c", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))

	// Stop before Start should not panic.
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStopEmpty(t *testing.T) {
	a := New(Config{ID: "empty"})
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartFailureRollback(t *testing.T) {
	// ref: uber-go/fx — Start 失败自动 rollback 已启动的 Cell
	a := New(Config{ID: "start-fail"})

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
	a := New(Config{ID: "double-start"})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "c", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))
	require.NoError(t, a.Start(context.Background()))

	err := a.Start(context.Background())
	require.Error(t, err, "double start should fail")
}

func TestAssemblyRegisterAfterStartRejected(t *testing.T) {
	a := New(Config{ID: "reg-after-start"})
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
	a := New(Config{ID: "stop-err"})

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
	a := New(Config{ID: "config-test"})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "c1", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))

	cfgMap := map[string]any{"key": "value"}
	require.NoError(t, a.StartWithConfig(context.Background(), cfgMap))

	health := a.Health()
	assert.Equal(t, "healthy", health["c1"].Status)
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartWithConfigDoubleStart(t *testing.T) {
	a := New(Config{ID: "double-cfg"})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "c1", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))
	require.NoError(t, a.StartWithConfig(context.Background(), nil))

	err := a.StartWithConfig(context.Background(), nil)
	require.Error(t, err)
	require.NoError(t, a.Stop(context.Background()))
}

func TestAssemblyStartWithConfigInitFailure(t *testing.T) {
	a := New(Config{ID: "init-fail-cfg"})
	bad := newFailInitCell("bad")
	require.NoError(t, a.Register(bad))

	err := a.StartWithConfig(context.Background(), nil)
	require.Error(t, err)
}

func TestAssemblyStartWithConfigStartFailureRollback(t *testing.T) {
	a := New(Config{ID: "start-fail-cfg"})

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
	a := New(Config{ID: "ids-test"})
	require.NoError(t, a.Register(cell.NewBaseCell(cell.CellMetadata{ID: "a", Type: cell.CellTypeCore})))
	require.NoError(t, a.Register(cell.NewBaseCell(cell.CellMetadata{ID: "b", Type: cell.CellTypeCore})))

	ids := a.CellIDs()
	assert.Equal(t, []string{"a", "b"}, ids)
}

func TestAssemblyCellLookup(t *testing.T) {
	a := New(Config{ID: "lookup-test"})
	c := cell.NewBaseCell(cell.CellMetadata{ID: "x", Type: cell.CellTypeCore})
	require.NoError(t, a.Register(c))

	found := a.Cell("x")
	assert.NotNil(t, found)
	assert.Equal(t, "x", found.ID())

	assert.Nil(t, a.Cell("nonexistent"))
}
