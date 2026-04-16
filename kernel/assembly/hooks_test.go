package assembly

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// hookOrderCell records all lifecycle + hook calls to a shared slice.
// Set failOn to make a specific hook return an error.
type hookOrderCell struct {
	*cell.BaseCell
	calls  *[]string
	failOn string // "BeforeStart", "AfterStart", "BeforeStop", "AfterStop", "" = no failure
}

func newHookOrderCell(id string, calls *[]string, failOn string) *hookOrderCell {
	return &hookOrderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		calls:    calls,
		failOn:   failOn,
	}
}

func (c *hookOrderCell) record(phase string) error {
	*c.calls = append(*c.calls, c.ID()+"."+phase)
	if c.failOn == phase {
		return errors.New(c.ID() + " " + phase + " boom")
	}
	return nil
}

func (c *hookOrderCell) Start(ctx context.Context) error {
	if err := c.record("Start"); err != nil {
		return err
	}
	return c.BaseCell.Start(ctx)
}

func (c *hookOrderCell) Stop(ctx context.Context) error {
	if err := c.record("Stop"); err != nil {
		return err
	}
	return c.BaseCell.Stop(ctx)
}

// Hooks intentionally ignore ctx — hookOrderCell verifies phase ordering,
// not ctx propagation. See slowHookCell in timeout_test.go for ctx-aware
// coverage (exercises the wrapped hookCtx returned by invokeHook).
func (c *hookOrderCell) BeforeStart(_ context.Context) error {
	return c.record("BeforeStart")
}

func (c *hookOrderCell) AfterStart(_ context.Context) error {
	return c.record("AfterStart")
}

func (c *hookOrderCell) BeforeStop(_ context.Context) error {
	return c.record("BeforeStop")
}

func (c *hookOrderCell) AfterStop(_ context.Context) error {
	return c.record("AfterStop")
}

// panicHookCell panics in the specified hook phase.
type panicHookCell struct {
	*cell.BaseCell
	calls   *[]string
	panicOn string
}

func newPanicHookCell(id string, calls *[]string, panicOn string) *panicHookCell {
	return &panicHookCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		calls:    calls,
		panicOn:  panicOn,
	}
}

func (c *panicHookCell) Start(ctx context.Context) error {
	*c.calls = append(*c.calls, c.ID()+".Start")
	return c.BaseCell.Start(ctx)
}

func (c *panicHookCell) Stop(ctx context.Context) error {
	*c.calls = append(*c.calls, c.ID()+".Stop")
	return c.BaseCell.Stop(ctx)
}

func (c *panicHookCell) BeforeStart(_ context.Context) error {
	*c.calls = append(*c.calls, c.ID()+".BeforeStart")
	if c.panicOn == "BeforeStart" {
		panic(c.ID() + " BeforeStart panic!")
	}
	return nil
}

func (c *panicHookCell) AfterStart(_ context.Context) error {
	*c.calls = append(*c.calls, c.ID()+".AfterStart")
	if c.panicOn == "AfterStart" {
		panic(c.ID() + " AfterStart panic!")
	}
	return nil
}

func (c *panicHookCell) BeforeStop(_ context.Context) error {
	*c.calls = append(*c.calls, c.ID()+".BeforeStop")
	if c.panicOn == "BeforeStop" {
		panic(c.ID() + " BeforeStop panic!")
	}
	return nil
}

func (c *panicHookCell) AfterStop(_ context.Context) error {
	*c.calls = append(*c.calls, c.ID()+".AfterStop")
	if c.panicOn == "AfterStop" {
		panic(c.ID() + " AfterStop panic!")
	}
	return nil
}

// Compile-time checks.
var (
	_ cell.BeforeStarter = (*hookOrderCell)(nil)
	_ cell.AfterStarter  = (*hookOrderCell)(nil)
	_ cell.BeforeStopper = (*hookOrderCell)(nil)
	_ cell.AfterStopper  = (*hookOrderCell)(nil)

	_ cell.BeforeStarter = (*panicHookCell)(nil)
	_ cell.AfterStarter  = (*panicHookCell)(nil)
	_ cell.BeforeStopper = (*panicHookCell)(nil)
	_ cell.AfterStopper  = (*panicHookCell)(nil)
)

// onlyBeforeStartCell implements only BeforeStarter.
type onlyBeforeStartCell struct {
	*cell.BaseCell
	calls *[]string
}

func newOnlyBeforeStartCell(id string, calls *[]string) *onlyBeforeStartCell {
	return &onlyBeforeStartCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		calls:    calls,
	}
}

func (c *onlyBeforeStartCell) BeforeStart(_ context.Context) error {
	*c.calls = append(*c.calls, c.ID()+".BeforeStart")
	return nil
}

var _ cell.BeforeStarter = (*onlyBeforeStartCell)(nil)

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestAssemblyHooks_HappyPath(t *testing.T) {
	a := New(Config{ID: "hooks-happy", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	a1 := newHookOrderCell("A", &calls, "")
	a2 := newHookOrderCell("B", &calls, "")
	require.NoError(t, a.Register(a1))
	require.NoError(t, a.Register(a2))

	// Start: FIFO with hooks.
	require.NoError(t, a.Start(context.Background()))
	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
		"B.BeforeStart", "B.Start", "B.AfterStart",
	}, calls)

	// Stop: LIFO with hooks.
	calls = nil
	require.NoError(t, a.Stop(context.Background()))
	assert.Equal(t, []string{
		"B.BeforeStop", "B.Stop", "B.AfterStop",
		"A.BeforeStop", "A.Stop", "A.AfterStop",
	}, calls)
}

func TestAssemblyHooks_BeforeStartFailure(t *testing.T) {
	a := New(Config{ID: "hooks-bs-fail", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	bad := newHookOrderCell("B", &calls, "BeforeStart")
	untouched := newHookOrderCell("C", &calls, "")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))
	require.NoError(t, a.Register(untouched))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "B")
	assert.Contains(t, err.Error(), "BeforeStart")

	// A got full start cycle, B only got BeforeStart (failed), C untouched.
	// Then rollback: A gets full stop cycle.
	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
		"B.BeforeStart", // failed here
		"A.BeforeStop", "A.Stop", "A.AfterStop", // rollback
	}, calls)
}

func TestAssemblyHooks_AfterStartFailure_RollbackIncludesFailedCell(t *testing.T) {
	a := New(Config{ID: "hooks-as-fail", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	bad := newHookOrderCell("B", &calls, "AfterStart")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "B")
	assert.Contains(t, err.Error(), "AfterStart")

	// A full start, B: BeforeStart + Start + AfterStart(failed).
	// Rollback: B gets stop (Start succeeded!), then A gets stop.
	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
		"B.BeforeStart", "B.Start", "B.AfterStart", // AfterStart failed
		"B.BeforeStop", "B.Stop", "B.AfterStop", // B itself rolled back
		"A.BeforeStop", "A.Stop", "A.AfterStop", // A rolled back
	}, calls)
}

func TestAssemblyHooks_BeforeStopError_ContinuesAnyway(t *testing.T) {
	a := New(Config{ID: "hooks-bstop-err", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	bad := newHookOrderCell("B", &calls, "BeforeStop")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))
	require.NoError(t, a.Start(context.Background()))

	calls = nil
	err := a.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "B")
	assert.Contains(t, err.Error(), "BeforeStop")

	// Despite B.BeforeStop error, B.Stop and B.AfterStop still called.
	// A also fully stopped.
	assert.Equal(t, []string{
		"B.BeforeStop", // error here, but continues
		"B.Stop", "B.AfterStop",
		"A.BeforeStop", "A.Stop", "A.AfterStop",
	}, calls)
}

func TestAssemblyHooks_AfterStopError_ContinuesAnyway(t *testing.T) {
	a := New(Config{ID: "hooks-astop-err", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	bad := newHookOrderCell("B", &calls, "AfterStop")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))
	require.NoError(t, a.Start(context.Background()))

	calls = nil
	err := a.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "B")
	assert.Contains(t, err.Error(), "AfterStop")

	// All hooks called for both cells despite B.AfterStop error.
	assert.Equal(t, []string{
		"B.BeforeStop", "B.Stop", "B.AfterStop", // error here, but continues
		"A.BeforeStop", "A.Stop", "A.AfterStop",
	}, calls)
}

func TestAssemblyHooks_MixedCells(t *testing.T) {
	a := New(Config{ID: "hooks-mixed", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	hooked1 := newHookOrderCell("H1", &calls, "")
	plain := cell.NewBaseCell(cell.CellMetadata{ID: "P", Type: cell.CellTypeCore})
	hooked2 := newHookOrderCell("H2", &calls, "")

	require.NoError(t, a.Register(hooked1))
	require.NoError(t, a.Register(plain))
	require.NoError(t, a.Register(hooked2))

	require.NoError(t, a.Start(context.Background()))

	// H1 has all hooks, P has none (only Start via assembly), H2 has all hooks.
	// Plain cell's Start/Stop are not recorded in our calls slice.
	assert.Equal(t, []string{
		"H1.BeforeStart", "H1.Start", "H1.AfterStart",
		// P: Start called by assembly but not recorded in calls
		"H2.BeforeStart", "H2.Start", "H2.AfterStart",
	}, calls)

	calls = nil
	require.NoError(t, a.Stop(context.Background()))
	assert.Equal(t, []string{
		"H2.BeforeStop", "H2.Stop", "H2.AfterStop",
		// P: Stop called by assembly but not recorded
		"H1.BeforeStop", "H1.Stop", "H1.AfterStop",
	}, calls)
}

func TestAssemblyHooks_PartialImplementation(t *testing.T) {
	a := New(Config{ID: "hooks-partial", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	partial := newOnlyBeforeStartCell("P", &calls)
	require.NoError(t, a.Register(partial))

	require.NoError(t, a.Start(context.Background()))
	// Only BeforeStart called, no AfterStart.
	assert.Equal(t, []string{"P.BeforeStart"}, calls)

	calls = nil
	require.NoError(t, a.Stop(context.Background()))
	// No BeforeStop/AfterStop — partial cell doesn't implement them.
	assert.Empty(t, calls)
}

func TestAssemblyHooks_StartWithConfig(t *testing.T) {
	a := New(Config{ID: "hooks-cfg", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	h := newHookOrderCell("A", &calls, "")
	require.NoError(t, a.Register(h))

	cfgMap := map[string]any{"key": "value"}
	require.NoError(t, a.StartWithConfig(context.Background(), cfgMap))

	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
	}, calls)

	calls = nil
	require.NoError(t, a.Stop(context.Background()))
	assert.Equal(t, []string{
		"A.BeforeStop", "A.Stop", "A.AfterStop",
	}, calls)
}

func TestAssemblyHooks_RollbackHooksBestEffort(t *testing.T) {
	a := New(Config{ID: "hooks-rb-best", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	// A has BeforeStop that fails — during rollback this should not abort.
	badStop := newHookOrderCell("A", &calls, "BeforeStop")
	failStart := newHookOrderCell("B", &calls, "Start")

	require.NoError(t, a.Register(badStop))
	require.NoError(t, a.Register(failStart))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "B")

	// A got full start cycle, B failed on Start.
	// Rollback: A.BeforeStop fails, but A.Stop and A.AfterStop still called.
	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
		"B.BeforeStart", "B.Start", // failed here
		"A.BeforeStop", // error here, but rollback continues
		"A.Stop", "A.AfterStop",
	}, calls)
}

// F3-2: Start failure (not hook) triggers LIFO rollback with hooks on previously-started cells.
func TestAssemblyHooks_StartFailure_RollbackUsesHooks(t *testing.T) {
	a := New(Config{ID: "hooks-start-fail", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	bad := newHookOrderCell("B", &calls, "Start")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "B")

	// A: full start cycle. B: BeforeStart + Start (failed), AfterStart NOT called.
	// Rollback: A gets full stop-with-hooks.
	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
		"B.BeforeStart", "B.Start", // B.Start failed — no AfterStart
		"A.BeforeStop", "A.Stop", "A.AfterStop", // A rollback with hooks
	}, calls)
}

// F3-3: Context cancellation is respected by hooks.
func TestAssemblyHooks_ContextCancellation(t *testing.T) {
	a := New(Config{ID: "hooks-ctx-cancel", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	// Cell whose BeforeStart checks context.
	ctxCell := newHookOrderCell("A", &calls, "")

	require.NoError(t, a.Register(ctxCell))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Assembly start should still proceed (hooks receive cancelled ctx,
	// but our test hooks don't check ctx — they succeed).
	// This verifies the assembly doesn't crash on cancelled context.
	require.NoError(t, a.Start(ctx))
	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
	}, calls)

	calls = nil
	require.NoError(t, a.Stop(ctx))
	assert.Equal(t, []string{
		"A.BeforeStop", "A.Stop", "A.AfterStop",
	}, calls)
}

// F2-1: Panic in hook is recovered and treated as error, not crash.
func TestAssemblyHooks_PanicRecovery_BeforeStart(t *testing.T) {
	a := New(Config{ID: "hooks-panic-bs", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	panicker := newPanicHookCell("B", &calls, "BeforeStart")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(panicker))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")
	assert.Contains(t, err.Error(), "B")

	// A fully started, B panicked in BeforeStart → rollback A.
	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
		"B.BeforeStart", // panicked here
		"A.BeforeStop", "A.Stop", "A.AfterStop",
	}, calls)
}

func TestAssemblyHooks_PanicRecovery_AfterStart(t *testing.T) {
	a := New(Config{ID: "hooks-panic-as", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	panicker := newPanicHookCell("B", &calls, "AfterStart")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(panicker))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")

	// B.Start succeeded, B.AfterStart panicked → B gets stopped, then A rolled back.
	assert.Equal(t, []string{
		"A.BeforeStart", "A.Start", "A.AfterStart",
		"B.BeforeStart", "B.Start", "B.AfterStart", // panicked
		"B.BeforeStop", "B.Stop", "B.AfterStop", // B itself stopped
		"A.BeforeStop", "A.Stop", "A.AfterStop", // A rolled back
	}, calls)
}

func TestAssemblyHooks_PanicRecovery_BeforeStop(t *testing.T) {
	a := New(Config{ID: "hooks-panic-bstop", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	panicker := newPanicHookCell("B", &calls, "BeforeStop")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(panicker))
	require.NoError(t, a.Start(context.Background()))

	calls = nil
	err := a.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")

	// B.BeforeStop panicked but Stop/AfterStop still called. A fully stopped.
	assert.Equal(t, []string{
		"B.BeforeStop", // panicked, recovered
		"B.Stop", "B.AfterStop",
		"A.BeforeStop", "A.Stop", "A.AfterStop",
	}, calls)
}

func TestAssemblyHooks_PanicRecovery_AfterStop(t *testing.T) {
	a := New(Config{ID: "hooks-panic-astop", DurabilityMode: cell.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("A", &calls, "")
	panicker := newPanicHookCell("B", &calls, "AfterStop")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(panicker))
	require.NoError(t, a.Start(context.Background()))

	calls = nil
	err := a.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")

	// B: BeforeStop + Stop execute normally, AfterStop panics (recovered).
	// A: fully stopped.
	assert.Equal(t, []string{
		"B.BeforeStop", "B.Stop", "B.AfterStop", // AfterStop panicked, recovered
		"A.BeforeStop", "A.Stop", "A.AfterStop",
	}, calls)
}
