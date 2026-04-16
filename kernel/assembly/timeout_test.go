package assembly

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowHookCell blocks in the specified hook until ctx is cancelled, allowing
// tests to drive the hook-timeout path deterministically.
type slowHookCell struct {
	*cell.BaseCell
	slowOn string // phase name matching the phase identifiers below
}

func newSlowHookCell(id, slowOn string) *slowHookCell {
	return &slowHookCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		slowOn:   slowOn,
	}
}

func (c *slowHookCell) block(ctx context.Context, phase string) error {
	if c.slowOn == phase {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (c *slowHookCell) BeforeStart(ctx context.Context) error {
	return c.block(ctx, "BeforeStart")
}
func (c *slowHookCell) AfterStart(ctx context.Context) error {
	return c.block(ctx, "AfterStart")
}
func (c *slowHookCell) BeforeStop(ctx context.Context) error {
	return c.block(ctx, "BeforeStop")
}
func (c *slowHookCell) AfterStop(ctx context.Context) error {
	return c.block(ctx, "AfterStop")
}

var (
	_ cell.BeforeStarter = (*slowHookCell)(nil)
	_ cell.AfterStarter  = (*slowHookCell)(nil)
	_ cell.BeforeStopper = (*slowHookCell)(nil)
	_ cell.AfterStopper  = (*slowHookCell)(nil)
)

func TestHookTimeout_DefaultApplied(t *testing.T) {
	// Config with HookTimeout=0 should use DefaultHookTimeout.
	a := New(Config{ID: "timeout-default", DurabilityMode: cell.DurabilityDemo})
	assert.Equal(t, DefaultHookTimeout, a.cfg.HookTimeout)
}

func TestHookTimeout_CustomValue(t *testing.T) {
	a := New(Config{ID: "timeout-custom", DurabilityMode: cell.DurabilityDemo, HookTimeout: 5 * time.Second})
	assert.Equal(t, 5*time.Second, a.cfg.HookTimeout)
}

func TestHookTimeout_NegativeDisables(t *testing.T) {
	// Negative value must pass through untouched so the hook inherits parent ctx.
	a := New(Config{ID: "timeout-neg", DurabilityMode: cell.DurabilityDemo, HookTimeout: -1})
	assert.Equal(t, time.Duration(-1), a.cfg.HookTimeout)
}

func TestHookTimeout_BeforeStartExceeds(t *testing.T) {
	obs := &captureObserver{}
	// Tight 20ms deadline so the test runs fast.
	a := New(Config{
		ID:             "timeout-bs",
		DurabilityMode: cell.DurabilityDemo,
		HookTimeout:    20 * time.Millisecond,
		HookObserver:   obs,
	})
	require.NoError(t, a.Register(newSlowHookCell("S", "BeforeStart")))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "expected DeadlineExceeded, got %v", err)

	var seen bool
	for _, e := range obs.snapshot() {
		if e.CellID == "S" && e.Hook == cell.HookBeforeStart {
			assert.Equal(t, cell.OutcomeTimeout, e.Outcome)
			assert.True(t, errors.Is(e.Err, context.DeadlineExceeded))
			assert.GreaterOrEqual(t, e.Duration, 20*time.Millisecond)
			seen = true
		}
	}
	assert.True(t, seen, "expected timeout event for BeforeStart")
}

func TestHookTimeout_AfterStartExceeds(t *testing.T) {
	obs := &captureObserver{}
	a := New(Config{
		ID:             "timeout-as",
		DurabilityMode: cell.DurabilityDemo,
		HookTimeout:    20 * time.Millisecond,
		HookObserver:   obs,
	})
	require.NoError(t, a.Register(newSlowHookCell("S", "AfterStart")))

	err := a.Start(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))

	var seen bool
	for _, e := range obs.snapshot() {
		if e.CellID == "S" && e.Hook == cell.HookAfterStart {
			assert.Equal(t, cell.OutcomeTimeout, e.Outcome)
			seen = true
		}
	}
	assert.True(t, seen)
}

func TestHookTimeout_StopPhaseTimeoutContinues(t *testing.T) {
	obs := &captureObserver{}
	a := New(Config{
		ID:             "timeout-stop",
		DurabilityMode: cell.DurabilityDemo,
		HookTimeout:    20 * time.Millisecond,
		HookObserver:   obs,
	})
	require.NoError(t, a.Register(newSlowHookCell("S", "BeforeStop")))
	require.NoError(t, a.Start(context.Background()))

	err := a.Stop(context.Background())
	require.Error(t, err)

	// BeforeStop timed out but Stop + AfterStop must still run.
	var before, after bool
	for _, e := range obs.snapshot() {
		if e.CellID != "S" {
			continue
		}
		switch e.Hook {
		case cell.HookBeforeStop:
			if e.Outcome == cell.OutcomeTimeout {
				before = true
			}
		case cell.HookAfterStop:
			// AfterStop should still be emitted (success, not timeout).
			if e.Outcome == cell.OutcomeSuccess {
				after = true
			}
		}
	}
	assert.True(t, before, "expected BeforeStop timeout event")
	assert.True(t, after, "expected AfterStop success event (stop best-effort)")
}
