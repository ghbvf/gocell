package cell

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Test helpers — hook implementations
// ---------------------------------------------------------------------------

// hookCell implements all 4 lifecycle hook interfaces.
type hookCell struct {
	BaseCell
	calls []string
}

func newHookCell(id string) *hookCell {
	return &hookCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: id, Type: CellTypeCore}),
	}
}

func (h *hookCell) BeforeStart(_ context.Context) error {
	h.calls = append(h.calls, "BeforeStart")
	return nil
}

func (h *hookCell) AfterStart(_ context.Context) error {
	h.calls = append(h.calls, "AfterStart")
	return nil
}

func (h *hookCell) BeforeStop(_ context.Context) error {
	h.calls = append(h.calls, "BeforeStop")
	return nil
}

func (h *hookCell) AfterStop(_ context.Context) error {
	h.calls = append(h.calls, "AfterStop")
	return nil
}

// partialHookCell implements only BeforeStarter + AfterStopper.
type partialHookCell struct {
	BaseCell
	calls []string
}

func newPartialHookCell(id string) *partialHookCell {
	return &partialHookCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: id, Type: CellTypeCore}),
	}
}

func (p *partialHookCell) BeforeStart(_ context.Context) error {
	p.calls = append(p.calls, "BeforeStart")
	return nil
}

func (p *partialHookCell) AfterStop(_ context.Context) error {
	p.calls = append(p.calls, "AfterStop")
	return nil
}

// ---------------------------------------------------------------------------
// Compile-time interface checks
// ---------------------------------------------------------------------------

var (
	_ BeforeStarter = (*hookCell)(nil)
	_ AfterStarter  = (*hookCell)(nil)
	_ BeforeStopper = (*hookCell)(nil)
	_ AfterStopper  = (*hookCell)(nil)

	_ BeforeStarter = (*partialHookCell)(nil)
	_ AfterStopper  = (*partialHookCell)(nil)
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestBeforeStarter_TypeAssertion(t *testing.T) {
	hc := newHookCell("hook-cell")
	var c Cell = hc
	bs, ok := c.(BeforeStarter)
	assert.True(t, ok, "hookCell should satisfy BeforeStarter")
	assert.NoError(t, bs.BeforeStart(context.Background()))
	assert.Equal(t, []string{"BeforeStart"}, hc.calls)
}

func TestAfterStarter_TypeAssertion(t *testing.T) {
	hc := newHookCell("hook-cell")
	var c Cell = hc
	as, ok := c.(AfterStarter)
	assert.True(t, ok, "hookCell should satisfy AfterStarter")
	assert.NoError(t, as.AfterStart(context.Background()))
	assert.Equal(t, []string{"AfterStart"}, hc.calls)
}

func TestBeforeStopper_TypeAssertion(t *testing.T) {
	hc := newHookCell("hook-cell")
	var c Cell = hc
	bs, ok := c.(BeforeStopper)
	assert.True(t, ok, "hookCell should satisfy BeforeStopper")
	assert.NoError(t, bs.BeforeStop(context.Background()))
	assert.Equal(t, []string{"BeforeStop"}, hc.calls)
}

func TestAfterStopper_TypeAssertion(t *testing.T) {
	hc := newHookCell("hook-cell")
	var c Cell = hc
	as, ok := c.(AfterStopper)
	assert.True(t, ok, "hookCell should satisfy AfterStopper")
	assert.NoError(t, as.AfterStop(context.Background()))
	assert.Equal(t, []string{"AfterStop"}, hc.calls)
}

func TestLifecycleHook_NegativeTypeAssertion(t *testing.T) {
	plain := NewBaseCell(CellMetadata{ID: "plain-cell"})
	var c Cell = plain

	_, ok1 := c.(BeforeStarter)
	_, ok2 := c.(AfterStarter)
	_, ok3 := c.(BeforeStopper)
	_, ok4 := c.(AfterStopper)

	assert.False(t, ok1, "plain BaseCell should NOT satisfy BeforeStarter")
	assert.False(t, ok2, "plain BaseCell should NOT satisfy AfterStarter")
	assert.False(t, ok3, "plain BaseCell should NOT satisfy BeforeStopper")
	assert.False(t, ok4, "plain BaseCell should NOT satisfy AfterStopper")
}

func TestLifecycleHook_PartialImplementation(t *testing.T) {
	pc := newPartialHookCell("partial")
	var c Cell = pc

	// Should satisfy BeforeStarter and AfterStopper.
	_, ok := c.(BeforeStarter)
	assert.True(t, ok, "partialHookCell should satisfy BeforeStarter")
	_, ok = c.(AfterStopper)
	assert.True(t, ok, "partialHookCell should satisfy AfterStopper")

	// Should NOT satisfy AfterStarter and BeforeStopper.
	_, ok = c.(AfterStarter)
	assert.False(t, ok, "partialHookCell should NOT satisfy AfterStarter")
	_, ok = c.(BeforeStopper)
	assert.False(t, ok, "partialHookCell should NOT satisfy BeforeStopper")
}
