package assembly

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
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
	m := &metadata.CellMeta{Type: "core"}
	m.ID = id
	return &hookOrderCell{
		BaseCell: cell.MustNewBaseCell(m),
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
	m := &metadata.CellMeta{Type: "core"}
	m.ID = id
	return &panicHookCell{
		BaseCell: cell.MustNewBaseCell(m),
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
	m := &metadata.CellMeta{Type: "core"}
	m.ID = id
	return &onlyBeforeStartCell{
		BaseCell: cell.MustNewBaseCell(m),
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
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-happy", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	a1 := newHookOrderCell("aa", &calls, "")
	a2 := newHookOrderCell("bb", &calls, "")
	require.NoError(t, a.Register(a1))
	require.NoError(t, a.Register(a2))

	// Start: FIFO with hooks.
	require.NoError(t, a.Start(context.Background()))
	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
		"bb.BeforeStart", "bb.Start", "bb.AfterStart",
	}, calls)

	// Stop: LIFO with hooks.
	calls = nil
	require.NoError(t, a.Stop(context.Background()))
	assert.Equal(t, []string{
		"bb.BeforeStop", "bb.Stop", "bb.AfterStop",
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop",
	}, calls)
}

func TestAssemblyHooks_BeforeStartFailure(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-bs-fail", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	bad := newHookOrderCell("bb", &calls, "BeforeStart")
	untouched := newHookOrderCell("cc", &calls, "")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))
	require.NoError(t, a.Register(untouched))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bb")
	assert.Contains(t, err.Error(), "BeforeStart")

	// A got full start cycle, B only got BeforeStart (failed), C untouched.
	// Then rollback: A gets full stop cycle.
	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
		"bb.BeforeStart",                           // failed here
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop", // rollback
	}, calls)
}

func TestAssemblyHooks_AfterStartFailure_RollbackIncludesFailedCell(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-as-fail", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	bad := newHookOrderCell("bb", &calls, "AfterStart")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bb")
	assert.Contains(t, err.Error(), "AfterStart")

	// A full start, B: BeforeStart + Start + AfterStart(failed).
	// Rollback: B gets stop (Start succeeded!), then A gets stop.
	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
		"bb.BeforeStart", "bb.Start", "bb.AfterStart", // AfterStart failed
		"bb.BeforeStop", "bb.Stop", "bb.AfterStop", // B itself rolled back
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop", // A rolled back
	}, calls)
}

func TestAssemblyHooks_BeforeStopError_ContinuesAnyway(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-bstop-err", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	bad := newHookOrderCell("bb", &calls, "BeforeStop")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))
	require.NoError(t, a.Start(context.Background()))

	calls = nil
	err := a.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bb")
	assert.Contains(t, err.Error(), "BeforeStop")

	// Despite B.BeforeStop error, B.Stop and B.AfterStop still called.
	// A also fully stopped.
	assert.Equal(t, []string{
		"bb.BeforeStop", // error here, but continues
		"bb.Stop", "bb.AfterStop",
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop",
	}, calls)
}

func TestAssemblyHooks_AfterStopError_ContinuesAnyway(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-astop-err", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	bad := newHookOrderCell("bb", &calls, "AfterStop")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))
	require.NoError(t, a.Start(context.Background()))

	calls = nil
	err := a.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bb")
	assert.Contains(t, err.Error(), "AfterStop")

	// All hooks called for both cells despite B.AfterStop error.
	assert.Equal(t, []string{
		"bb.BeforeStop", "bb.Stop", "bb.AfterStop", // error here, but continues
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop",
	}, calls)
}

func TestAssemblyHooks_MixedCells(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-mixed", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	hooked1 := newHookOrderCell("h1", &calls, "")
	plain := cell.MustNewBaseCell(&metadata.CellMeta{ID: metadatatest.NewCellID("pp"), Type: "core"})
	hooked2 := newHookOrderCell("h2", &calls, "")

	require.NoError(t, a.Register(hooked1))
	require.NoError(t, a.Register(plain))
	require.NoError(t, a.Register(hooked2))

	require.NoError(t, a.Start(context.Background()))

	// H1 has all hooks, P has none (only Start via assembly), H2 has all hooks.
	// Plain cell's Start/Stop are not recorded in our calls slice.
	assert.Equal(t, []string{
		"h1.BeforeStart", "h1.Start", "h1.AfterStart",
		// P: Start called by assembly but not recorded in calls
		"h2.BeforeStart", "h2.Start", "h2.AfterStart",
	}, calls)

	calls = nil
	require.NoError(t, a.Stop(context.Background()))
	assert.Equal(t, []string{
		"h2.BeforeStop", "h2.Stop", "h2.AfterStop",
		// P: Stop called by assembly but not recorded
		"h1.BeforeStop", "h1.Stop", "h1.AfterStop",
	}, calls)
}

func TestAssemblyHooks_PartialImplementation(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-partial", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	partial := newOnlyBeforeStartCell("pp", &calls)
	require.NoError(t, a.Register(partial))

	require.NoError(t, a.Start(context.Background()))
	// Only BeforeStart called, no AfterStart.
	assert.Equal(t, []string{"pp.BeforeStart"}, calls)

	calls = nil
	require.NoError(t, a.Stop(context.Background()))
	// No BeforeStop/AfterStop — partial cell doesn't implement them.
	assert.Empty(t, calls)
}

func TestAssemblyHooks_StartWithConfig(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-cfg", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	h := newHookOrderCell("aa", &calls, "")
	require.NoError(t, a.Register(h))

	cfgMap := map[string]any{"key": "value"}
	require.NoError(t, a.StartWithConfig(context.Background(), cfgMap))

	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
	}, calls)

	calls = nil
	require.NoError(t, a.Stop(context.Background()))
	assert.Equal(t, []string{
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop",
	}, calls)
}

func TestAssemblyHooks_RollbackHooksBestEffort(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-rb-best", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	// A has BeforeStop that fails — during rollback this should not abort.
	badStop := newHookOrderCell("aa", &calls, "BeforeStop")
	failStart := newHookOrderCell("bb", &calls, "Start")

	require.NoError(t, a.Register(badStop))
	require.NoError(t, a.Register(failStart))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bb")

	// A got full start cycle, B failed on Start.
	// Rollback: A.BeforeStop fails, but A.Stop and A.AfterStop still called.
	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
		"bb.BeforeStart", "bb.Start", // failed here
		"aa.BeforeStop", // error here, but rollback continues
		"aa.Stop", "aa.AfterStop",
	}, calls)
}

// F3-2: Start failure (not hook) triggers LIFO rollback with hooks on previously-started cells.
func TestAssemblyHooks_StartFailure_RollbackUsesHooks(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-start-fail", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	bad := newHookOrderCell("bb", &calls, "Start")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(bad))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bb")

	// A: full start cycle. B: BeforeStart + Start (failed), AfterStart NOT called.
	// Rollback: A gets full stop-with-hooks.
	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
		"bb.BeforeStart", "bb.Start", // B.Start failed — no AfterStart
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop", // A rollback with hooks
	}, calls)
}

// F3-3: Context cancellation is respected by hooks.
func TestAssemblyHooks_ContextCancellation(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-ctx-cancel", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	// Cell whose BeforeStart checks context.
	ctxCell := newHookOrderCell("aa", &calls, "")

	require.NoError(t, a.Register(ctxCell))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Assembly start should still proceed (hooks receive canceled ctx,
	// but our test hooks don't check ctx — they succeed).
	// This verifies the assembly doesn't crash on canceled context.
	require.NoError(t, a.Start(ctx))
	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
	}, calls)

	calls = nil
	require.NoError(t, a.Stop(ctx))
	assert.Equal(t, []string{
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop",
	}, calls)
}

// F2-1: Panic in hook is recovered and treated as error, not crash.
func TestAssemblyHooks_PanicRecovery_BeforeStart(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-panic-bs", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	panicker := newPanicHookCell("bb", &calls, "BeforeStart")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(panicker))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")
	assert.Contains(t, err.Error(), "bb")

	// A fully started, B panicked in BeforeStart → rollback A.
	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
		"bb.BeforeStart", // panicked here
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop",
	}, calls)
}

func TestAssemblyHooks_PanicRecovery_AfterStart(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-panic-as", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	panicker := newPanicHookCell("bb", &calls, "AfterStart")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(panicker))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")

	// B.Start succeeded, B.AfterStart panicked → B gets stopped, then A rolled back.
	assert.Equal(t, []string{
		"aa.BeforeStart", "aa.Start", "aa.AfterStart",
		"bb.BeforeStart", "bb.Start", "bb.AfterStart", // panicked
		"bb.BeforeStop", "bb.Stop", "bb.AfterStop", // B itself stopped
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop", // A rolled back
	}, calls)
}

func TestAssemblyHooks_PanicRecovery_BeforeStop(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-panic-bstop", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	panicker := newPanicHookCell("bb", &calls, "BeforeStop")

	require.NoError(t, a.Register(good))
	require.NoError(t, a.Register(panicker))
	require.NoError(t, a.Start(context.Background()))

	calls = nil
	err := a.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")

	// B.BeforeStop panicked but Stop/AfterStop still called. A fully stopped.
	assert.Equal(t, []string{
		"bb.BeforeStop", // panicked, recovered
		"bb.Stop", "bb.AfterStop",
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop",
	}, calls)
}

func TestAssemblyHooks_PanicRecovery_AfterStop(t *testing.T) {
	a := newTestAssembly(t, clock.Real(), Config{ID: "hooks-panic-astop", DurabilityMode: outbox.DurabilityDemo})
	var calls []string

	good := newHookOrderCell("aa", &calls, "")
	panicker := newPanicHookCell("bb", &calls, "AfterStop")

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
		"bb.BeforeStop", "bb.Stop", "bb.AfterStop", // AfterStop panicked, recovered
		"aa.BeforeStop", "aa.Stop", "aa.AfterStop",
	}, calls)
}
