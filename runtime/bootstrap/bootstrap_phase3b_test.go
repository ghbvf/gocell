package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
)

// runtimeOnlyHookFields are fields present on bootstrap.Hook that are
// intentionally NOT mirrored on kernel/cell.LifecycleHook. These are runtime-
// only observability dimensions stamped by phase3b (or other framework code);
// cells never self-declare them, so exposing them on the kernel interface
// would invite drift between cell-supplied values and registry-stamped truth.
//
// Add a field name here only together with a comment explaining why it is
// runtime-only; the drift guard below treats this as a documented divergence.
var runtimeOnlyHookFields = map[string]string{
	// CellID is stamped by phase3b from assembly.CellIDs(); empty for hooks
	// appended via bootstrap.WithLifecycle. See Hook.CellID godoc + fx's
	// unexported callerFrame for the design precedent.
	"CellID": "runtime-stamped by phase3b, not cell-supplied",
}

// TestLifecycleHookShapeMatchesBootstrapHook is a drift guard: the field-copy
// bridge in phase3bDrainLifecycleHooks silently loses any fields
// added to bootstrap.Hook that are missing from kernel/cell.LifecycleHook
// (or vice-versa). The reflect-based check keeps the two struct shapes in
// lock-step: if bootstrap.Hook gains a Priority field tomorrow, this test
// fails until cell.LifecycleHook mirrors it (or a conscious divergence is
// documented by adding the field to runtimeOnlyHookFields above).
func TestLifecycleHookShapeMatchesBootstrapHook(t *testing.T) {
	hookT := reflect.TypeFor[Hook]()
	cellHookT := reflect.TypeFor[cell.LifecycleHook]()

	fieldSet := func(t reflect.Type) map[string]reflect.Type {
		out := make(map[string]reflect.Type, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			out[f.Name] = f.Type
		}
		return out
	}

	got := fieldSet(hookT)
	want := fieldSet(cellHookT)

	// Pin runtimeOnlyHookFields size: silent whitelist growth would let a
	// future field slip in without an accompanying kernel.LifecycleHook
	// mirror decision. Update this constant and the corresponding map entry
	// together, with a comment explaining why the new field is runtime-only.
	assert.Len(t, runtimeOnlyHookFields, 1,
		"runtimeOnlyHookFields must stay minimal; update this assertion and the map entry together with a reason comment")

	// Every kernel-side field MUST be present on bootstrap.Hook with matching
	// type — phase3b must never drop caller-supplied fields.
	for name, cellType := range want {
		bootType, ok := got[name]
		require.Truef(t, ok,
			"bootstrap.Hook is missing field %q present in cell.LifecycleHook — update bootstrap.Hook or field-copy in phase3b",
			name)
		assert.Equalf(t, cellType.String(), bootType.String(),
			"field %q type drift: cell.LifecycleHook has %s, bootstrap.Hook has %s",
			name, cellType, bootType)
	}

	// bootstrap.Hook may carry extra runtime-only fields, but every such
	// field MUST be explicitly whitelisted in runtimeOnlyHookFields so the
	// divergence is intentional + self-documenting.
	for name := range got {
		if _, mirrored := want[name]; mirrored {
			continue
		}
		_, whitelisted := runtimeOnlyHookFields[name]
		assert.Truef(t, whitelisted,
			"bootstrap.Hook has undocumented field %q missing from cell.LifecycleHook — "+
				"either mirror it on cell.LifecycleHook or add it to runtimeOnlyHookFields with a reason",
			name)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// lcCell is a minimal Cell that registers lifecycle hooks via reg.Lifecycle in Init.
type lcCell struct {
	cell.BaseCell
	hooks []cell.LifecycleHook
}

func (c *lcCell) Init(ctx context.Context, reg cell.Registry) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	for _, h := range c.hooks {
		reg.Lifecycle(h)
	}
	return nil
}

// plainCellForLC is a Cell that does NOT register any lifecycle hooks.
type plainCellForLC struct{ cell.BaseCell }

// buildAsmStarted creates a CoreAssembly with the given cells registered and
// started, populating cellSnapshots. The assembly is stopped on test cleanup.
func buildAsmStarted(t *testing.T, cells ...cell.Cell) *assembly.CoreAssembly {
	t.Helper()
	asm := assembly.New(assembly.Config{ID: "testasm", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	for _, c := range cells {
		require.NoError(t, asm.Register(c))
	}
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })
	return asm
}

// buildPhaseStateWithSnapshots creates a phaseState whose asm and cellSnapshots
// are already populated (mirrors what phase3InitAssembly does in production).
func buildPhaseStateWithSnapshots(t *testing.T, asm *assembly.CoreAssembly) *phaseState {
	t.Helper()
	_, s := newPhaseState()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	return s
}

// ---------------------------------------------------------------------------
// mockLifecycle records Append calls so we can assert ordering.
// ---------------------------------------------------------------------------

type mockLifecycle struct {
	appended  []Hook
	appendErr error
}

func (m *mockLifecycle) Append(h Hook) error {
	if m.appendErr != nil {
		return m.appendErr
	}
	m.appended = append(m.appended, h)
	return nil
}

func (m *mockLifecycle) Start(_ context.Context) error { return nil }
func (m *mockLifecycle) Stop(_ context.Context) error  { return nil }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestPhase3b_NoCellRegistersHooks verifies no hooks are appended when
// no cell calls reg.Lifecycle in Init.
func TestPhase3b_NoCellImplementsContributor(t *testing.T) {
	plain := &plainCellForLC{BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "plain"})}
	asm := buildAsmStarted(t, plain)

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	assert.Empty(t, ml.appended, "no hooks expected when no cell registers Lifecycle hooks")
}

// TestPhase3b_EmptySliceContributor verifies a cell registering no hooks
// (empty slice in Init) is a legal opt-out.
func TestPhase3b_EmptySliceContributor(t *testing.T) {
	lc := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "mycell"}),
		hooks:    []cell.LifecycleHook{}, // non-nil empty
	}
	asm := buildAsmStarted(t, lc)

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	assert.Empty(t, ml.appended, "empty-hooks Init must register zero hooks")
}

// TestPhase3b_DuplicateHookName_FailFast verifies that when two cells
// register a hook with the same non-empty Name, the second Append call
// fails and phase3b surfaces the conflict with the contributing cell id.
// Duplicate-Name detection itself lives in Lifecycle.Append (single source
// of truth, see TestLifecycle_AppendRejectsDuplicateName); phase3b only
// wraps the error with cellID/hookName context.
func TestPhase3b_DuplicateHookName_FailFast(t *testing.T) {
	cellA := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "cellA"}),
		hooks: []cell.LifecycleHook{
			{Name: "shared.hook", OnStart: func(_ context.Context) error { return nil }},
		},
	}
	cellB := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "cellB"}),
		hooks: []cell.LifecycleHook{
			{Name: "shared.hook", OnStart: func(_ context.Context) error { return nil }},
		},
	}
	asm := buildAsmStarted(t, cellA, cellB)
	b := New(WithClock(clock.Real()))
	// Use the real lifecycle so Append's dup-name guard actually fires;
	// mockLifecycle has no state tracking.
	b.lifecycle = NewLifecycle(LifecycleConfig{Clock: clock.Real()})

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	err := b.phase3bDrainLifecycleHooks(s)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateHookName, "must surface ErrDuplicateHookName via %%w chain")
	assert.Contains(t, err.Error(), "cellB", "second cell id must appear in wrapped error")
	assert.Contains(t, err.Error(), "shared.hook", "dup hook name must appear in wrapped error")
}

// TestLifecycle_AppendRejectsDuplicateName locks Append's dup-name guard at
// the layer that both phase3b auto-discovery and WithLifecycle composition
// funnel through. If this layer slips, the phase3b wrapper test above
// still fails — but this test catches the regression sooner + documents
// the sentinel (ErrDuplicateHookName) publicly.
func TestLifecycle_AppendRejectsDuplicateName(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	noop := func(_ context.Context) error { return nil }

	require.NoError(t, lc.Append(Hook{Name: "foo", OnStart: noop}))

	// Same Name via a WithLifecycle-style direct Append must fail.
	err := lc.Append(Hook{Name: "foo", OnStop: noop})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateHookName)

	// Empty Name is still allowed — callers accept the diagnostic cost.
	require.NoError(t, lc.Append(Hook{OnStart: noop}))
	require.NoError(t, lc.Append(Hook{OnStart: noop}))
}

// TestPhase3b_NilSliceContributor verifies a cell with no hooks registered
// in Init is a legal opt-out.
func TestPhase3b_NilSliceContributor(t *testing.T) {
	lc := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "mycell"}),
		hooks:    nil,
	}
	asm := buildAsmStarted(t, lc)

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	assert.Empty(t, ml.appended, "nil hooks must register zero hooks")
}

// TestPhase3b_OneCellTwoHooks verifies both hooks are appended in declaration order.
func TestPhase3b_OneCellTwoHooks(t *testing.T) {
	hooks := []cell.LifecycleHook{
		{Name: "start-db", OnStart: func(_ context.Context) error { return nil }},
		{Name: "start-cache", OnStart: func(_ context.Context) error { return nil }, OnStop: func(_ context.Context) error { return nil }},
	}
	lc := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "mycore"}),
		hooks:    hooks,
	}
	asm := buildAsmStarted(t, lc)

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	require.Len(t, ml.appended, 2)
	assert.Equal(t, "start-db", ml.appended[0].Name)
	assert.Equal(t, "start-cache", ml.appended[1].Name)
}

// TestPhase3b_TwoCellsOrderPreserved verifies CellIDs() registration order
// determines Append order across cells.
func TestPhase3b_TwoCellsOrderPreserved(t *testing.T) {
	lcA := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "alpha"}),
		hooks:    []cell.LifecycleHook{{Name: "alpha-hook", OnStart: func(_ context.Context) error { return nil }}},
	}
	lcB := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "beta"}),
		hooks:    []cell.LifecycleHook{{Name: "beta-hook", OnStop: func(_ context.Context) error { return nil }}},
	}
	// Register alpha first, then beta — order must be preserved.
	asm := buildAsmStarted(t, lcA, lcB)

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	require.Len(t, ml.appended, 2)
	assert.Equal(t, "alpha-hook", ml.appended[0].Name)
	assert.Equal(t, "beta-hook", ml.appended[1].Name)
}

// TestPhase3b_StampsCellIDOnAppendedHook verifies phase3b writes the owning
// cell's ID onto every appended bootstrap.Hook. CellID is the runtime-only
// dimension runHook() emits as "cell" slog attribute; missing this stamp
// would silently degrade observability for any cell-owned hook.
//
// ref: uber-go/fx internal/lifecycle/lifecycle.go — callerFrame captured at
// Append time by the framework, not trusted from the hook author.
func TestPhase3b_StampsCellIDOnAppendedHook(t *testing.T) {
	lcA := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "alpha"}),
		hooks: []cell.LifecycleHook{
			{Name: "alpha-hook-1", OnStart: func(_ context.Context) error { return nil }},
			{Name: "alpha-hook-2", OnStop: func(_ context.Context) error { return nil }},
		},
	}
	lcB := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "beta"}),
		hooks: []cell.LifecycleHook{
			{Name: "beta-hook", OnStart: func(_ context.Context) error { return nil }},
		},
	}
	asm := buildAsmStarted(t, lcA, lcB)

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	require.Len(t, ml.appended, 3)
	assert.Equal(t, "alpha", ml.appended[0].CellID, "hook from alpha must carry CellID=alpha")
	assert.Equal(t, "alpha", ml.appended[1].CellID, "second alpha hook must also carry CellID=alpha")
	assert.Equal(t, "beta", ml.appended[2].CellID, "beta hook must carry CellID=beta")
}

// TestPhase3b_BothNilSkipped verifies a hook with nil OnStart and nil OnStop
// is silently skipped (not appended).
func TestPhase3b_BothNilSkipped(t *testing.T) {
	lc := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "mycore"}),
		hooks: []cell.LifecycleHook{
			{Name: "no-ops", OnStart: nil, OnStop: nil},
		},
	}
	asm := buildAsmStarted(t, lc)

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	assert.Empty(t, ml.appended, "hook with both nil funcs must be skipped")
}

// TestPhase3b_EmptyNameAllowed verifies a hook with Name="" is still appended
// (Name is non-required at the bootstrap level even though Registry.Lifecycle panics
// on empty name — tests here use the snapshot path directly).
func TestPhase3b_EmptyNameAllowed(t *testing.T) {
	// Directly inject a snapshot with an empty-named hook (bypass Registry.Lifecycle
	// panic guard — this tests that phase3b itself does not block empty names).
	asm := assembly.New(assembly.Config{ID: "testasm2", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	plain := &plainCellForLC{BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "mycore"})}
	require.NoError(t, asm.Register(plain))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	// Manually build a phaseState with a snapshot that has an empty-named hook.
	_, s := newPhaseState()
	s.asm = asm
	s.cellSnapshots = map[string]cell.RegistrySnapshot{
		"mycore": {
			LifecycleHooks: []cell.LifecycleHook{
				{Name: "", OnStart: func(_ context.Context) error { return nil }},
			},
		},
	}

	ml := &mockLifecycle{}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	require.NoError(t, b.phase3bDrainLifecycleHooks(s))
	require.Len(t, ml.appended, 1)
	assert.Equal(t, "", ml.appended[0].Name)
}

// TestPhase3b_AppendError_PropagatesWithCellAndHookName verifies that when
// lifecycle.Append returns an error the phase returns an error containing
// both the cell id and hook name.
func TestPhase3b_AppendError_PropagatesWithCellAndHookName(t *testing.T) {
	lc := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "mycore"}),
		hooks: []cell.LifecycleHook{
			{Name: "my-hook", OnStart: func(_ context.Context) error { return nil }},
		},
	}
	asm := buildAsmStarted(t, lc)

	ml := &mockLifecycle{appendErr: errors.New("already started")}
	b := New(WithClock(clock.Real()))
	b.lifecycle = ml

	s := buildPhaseStateWithSnapshots(t, asm)
	defer s.runCancel()

	err := b.phase3bDrainLifecycleHooks(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mycore")
	assert.Contains(t, err.Error(), "my-hook")
}
