package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// lcCell is a minimal Cell that implements LifecycleContributor.
type lcCell struct {
	cell.BaseCell
	hooks []cell.LifecycleHook
}

func (c *lcCell) LifecycleHooks() []cell.LifecycleHook { return c.hooks }

var _ cell.LifecycleContributor = (*lcCell)(nil)

// plainCellForLC is a Cell that does NOT implement LifecycleContributor.
type plainCellForLC struct{ cell.BaseCell }

// buildAsmRegistered creates a CoreAssembly with the given cells registered
// (without starting), sufficient for phase3b type-assertion discovery.
func buildAsmRegistered(t *testing.T, cells ...cell.Cell) *assembly.CoreAssembly {
	t.Helper()
	asm := assembly.New(assembly.Config{ID: "testasm", DurabilityMode: cell.DurabilityDemo})
	for _, c := range cells {
		require.NoError(t, asm.Register(c))
	}
	return asm
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

// TestPhase3b_NoCellImplementsContributor verifies no hooks are appended when
// no cell implements LifecycleContributor.
func TestPhase3b_NoCellImplementsContributor(t *testing.T) {
	plain := &plainCellForLC{BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "plain"})}
	asm := buildAsmRegistered(t, plain)

	ml := &mockLifecycle{}
	b := New()
	b.lifecycle = ml

	_, s := newPhaseState()
	s.asm = asm

	require.NoError(t, b.phase3bDiscoverLifecycleContributor(s))
	assert.Empty(t, ml.appended, "no hooks expected when no cell implements LifecycleContributor")
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
	asm := buildAsmRegistered(t, lc)

	ml := &mockLifecycle{}
	b := New()
	b.lifecycle = ml

	_, s := newPhaseState()
	s.asm = asm

	require.NoError(t, b.phase3bDiscoverLifecycleContributor(s))
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
	asm := buildAsmRegistered(t, lcA, lcB)

	ml := &mockLifecycle{}
	b := New()
	b.lifecycle = ml

	_, s := newPhaseState()
	s.asm = asm

	require.NoError(t, b.phase3bDiscoverLifecycleContributor(s))
	require.Len(t, ml.appended, 2)
	assert.Equal(t, "alpha-hook", ml.appended[0].Name)
	assert.Equal(t, "beta-hook", ml.appended[1].Name)
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
	asm := buildAsmRegistered(t, lc)

	ml := &mockLifecycle{}
	b := New()
	b.lifecycle = ml

	_, s := newPhaseState()
	s.asm = asm

	require.NoError(t, b.phase3bDiscoverLifecycleContributor(s))
	assert.Empty(t, ml.appended, "hook with both nil funcs must be skipped")
}

// TestPhase3b_EmptyNameAllowed verifies a hook with Name="" is still appended
// (Name is non-required).
func TestPhase3b_EmptyNameAllowed(t *testing.T) {
	lc := &lcCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "mycore"}),
		hooks: []cell.LifecycleHook{
			{Name: "", OnStart: func(_ context.Context) error { return nil }},
		},
	}
	asm := buildAsmRegistered(t, lc)

	ml := &mockLifecycle{}
	b := New()
	b.lifecycle = ml

	_, s := newPhaseState()
	s.asm = asm

	require.NoError(t, b.phase3bDiscoverLifecycleContributor(s))
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
	asm := buildAsmRegistered(t, lc)

	ml := &mockLifecycle{appendErr: errors.New("already started")}
	b := New()
	b.lifecycle = ml

	_, s := newPhaseState()
	s.asm = asm

	err := b.phase3bDiscoverLifecycleContributor(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mycore")
	assert.Contains(t, err.Error(), "my-hook")
}
