package assembly

// rollback_ctx_test.go — PR-V1-030-G02: rollback ctx must be decoupled from
// startCtx. When SIGTERM during Start cancels the caller's ctx, rollback hooks
// (BeforeStop/Stop/AfterStop on already-started cells) must still receive a
// fresh, working ctx so they can release resources. This file exercises the
// invariant on three independent paths: BeforeStop (via invokeHook), Stop
// (direct ctx, no invokeHook wrapping), and HookTimeout=-1 passthrough.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// rollbackCtxExplicitHookTimeout is the explicit positive HookTimeout exercised
// in the table-driven test row. Uses testtime.D2s to satisfy
// TEST-TIME-LITERAL-01 — no inline duration literals in test code.
const rollbackCtxExplicitHookTimeout = testtime.D2s

// ctxRecordingCell captures ctx state observed inside BeforeStop and Stop so
// tests can assert on ctx.Err() / ctx.Deadline() without racing the assembly.
// failStart=true makes Start return an error so rollback fires on the
// previously-registered cell.
type ctxRecordingCell struct {
	*cell.BaseCell
	failStart             bool
	beforeStopCtxErr      error
	beforeStopHasDeadline bool
	stopCtxErr            error
	stopHasDeadline       bool
}

func newCtxRecordingCell(id string, failStart bool) *ctxRecordingCell {
	return &ctxRecordingCell{
		BaseCell:  cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
		failStart: failStart,
	}
}

func (c *ctxRecordingCell) Start(ctx context.Context) error {
	if c.failStart {
		return errors.New(c.ID() + " start boom")
	}
	return c.BaseCell.Start(ctx)
}

func (c *ctxRecordingCell) BeforeStop(ctx context.Context) error {
	c.beforeStopCtxErr = ctx.Err()
	_, c.beforeStopHasDeadline = ctx.Deadline()
	return nil
}

func (c *ctxRecordingCell) Stop(ctx context.Context) error {
	c.stopCtxErr = ctx.Err()
	_, c.stopHasDeadline = ctx.Deadline()
	return c.BaseCell.Stop(ctx)
}

var _ cell.BeforeStopper = (*ctxRecordingCell)(nil)

// afterStartFailingCell starts successfully but its AfterStart hook returns
// an error, exercising startCellWithHooks' AfterStart-fail branch — which
// derives an independent rollback ctx for the failing cell's
// stopCellWithHooks before delegating to rollbackCells(i-1).
type afterStartFailingCell struct {
	*cell.BaseCell
	beforeStopCtxErr error
	stopCtxErr       error
}

func newAfterStartFailingCell(id string) *afterStartFailingCell {
	return &afterStartFailingCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
	}
}

func (c *afterStartFailingCell) AfterStart(_ context.Context) error {
	return errors.New(c.ID() + " after-start boom")
}

func (c *afterStartFailingCell) BeforeStop(ctx context.Context) error {
	c.beforeStopCtxErr = ctx.Err()
	return nil
}

func (c *afterStartFailingCell) Stop(ctx context.Context) error {
	c.stopCtxErr = ctx.Err()
	return c.BaseCell.Stop(ctx)
}

var (
	_ cell.AfterStarter  = (*afterStartFailingCell)(nil)
	_ cell.BeforeStopper = (*afterStartFailingCell)(nil)
)

// TestRollbackCells_DerivedCtx covers PR-V1-030-G02: rollback hooks on
// already-started cells must NOT inherit a canceled startCtx. They must run
// against a fresh ctx derived from context.Background(), with a deadline
// derived from cfg.HookTimeout (or no deadline when HookTimeout < 0).
func TestRollbackCells_DerivedCtx(t *testing.T) {
	t.Parallel()

	const startCtxCancelled = true

	cases := []struct {
		name              string
		hookTimeout       time.Duration
		cancelStartCtx    bool
		wantBeforeStopErr error // ctx.Err() observed inside BeforeStop
		wantStopErr       error // ctx.Err() observed inside Stop
		wantHasDeadline   bool  // ctx.Deadline() ok inside hooks
	}{
		{
			name:              "canceled-startctx-with-default-hook-timeout",
			hookTimeout:       0, // → DefaultHookTimeout
			cancelStartCtx:    startCtxCancelled,
			wantBeforeStopErr: nil,
			wantStopErr:       nil,
			wantHasDeadline:   true,
		},
		{
			name:              "canceled-startctx-with-explicit-hook-timeout",
			hookTimeout:       rollbackCtxExplicitHookTimeout,
			cancelStartCtx:    startCtxCancelled,
			wantBeforeStopErr: nil,
			wantStopErr:       nil,
			wantHasDeadline:   true,
		},
		{
			name:              "canceled-startctx-with-negative-hook-timeout-no-deadline",
			hookTimeout:       disableHookTimeout, // declared in timeout_test.go: time.Duration(-1)
			cancelStartCtx:    startCtxCancelled,
			wantBeforeStopErr: nil,
			wantStopErr:       nil,
			wantHasDeadline:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a := newTestAssembly(t, Config{
				ID:             "rollback-ctx-" + tc.name,
				DurabilityMode: cell.DurabilityDemo,
				HookTimeout:    tc.hookTimeout,
				Clock:          clock.Real(),
			})

			good := newCtxRecordingCell("A", false)
			bad := newCtxRecordingCell("B", true)
			require.NoError(t, a.Register(good))
			require.NoError(t, a.Register(bad))

			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancelStartCtx {
				cancel() // simulate SIGTERM-during-Start
			} else {
				defer cancel()
			}

			err := a.Start(ctx)
			require.Error(t, err, "Start must fail because B's Start returns boom")

			// A is the only cell that reaches BeforeStop/Stop during rollback.
			assert.Equal(t, tc.wantBeforeStopErr, good.beforeStopCtxErr,
				"rollback BeforeStop must see fresh ctx (ctx.Err() == nil) — startCtx cancellation must NOT propagate")
			assert.Equal(t, tc.wantStopErr, good.stopCtxErr,
				"rollback Stop must see fresh ctx (ctx.Err() == nil) — c.Stop(ctx) bypasses invokeHook so the rollback root ctx must already be fresh")
			assert.Equal(t, tc.wantHasDeadline, good.beforeStopHasDeadline,
				"BeforeStop ctx deadline must reflect HookTimeout config")
			assert.Equal(t, tc.wantHasDeadline, good.stopHasDeadline,
				"Stop ctx deadline must reflect HookTimeout config")
		})
	}
}

// TestRollbackCells_AfterStartFail_DerivedCtx covers the AfterStart-fail
// branch of startCellWithHooks: cell B's Start succeeds, B.AfterStart fails,
// the assembly first stops B with an independently-derived rollback ctx, then
// rolls back the previously-started cell A. Both teardown paths must see a
// fresh ctx (ctx.Err() == nil) even when the caller's startCtx was canceled
// by a SIGTERM.
func TestRollbackCells_AfterStartFail_DerivedCtx(t *testing.T) {
	t.Parallel()

	a := newTestAssembly(t, Config{
		ID:             "rollback-afterstart-fail",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})

	prior := newCtxRecordingCell("A", false)
	failing := newAfterStartFailingCell("B")
	require.NoError(t, a.Register(prior))
	require.NoError(t, a.Register(failing))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // SIGTERM-during-Start

	err := a.Start(ctx)
	require.Error(t, err)

	// The failing cell B must see a fresh ctx during its own teardown.
	assert.NoError(t, failing.beforeStopCtxErr,
		"failing cell BeforeStop must run with fresh ctx (independent rollback ctx, not startCtx)")
	assert.NoError(t, failing.stopCtxErr,
		"failing cell Stop must run with fresh ctx — c.Stop bypasses invokeHook so the rollback root ctx must already be fresh")

	// The prior cell A must also see a fresh ctx via rollbackCells(i-1).
	assert.NoError(t, prior.beforeStopCtxErr,
		"prior cell BeforeStop must run with fresh ctx (rollbackCells(i-1) path)")
	assert.NoError(t, prior.stopCtxErr,
		"prior cell Stop must run with fresh ctx (rollbackCells(i-1) path)")
}
