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
	stopDeadline          time.Time // captured ctx.Deadline() inside Stop — used to verify single-budget invariant in shared-rollback tests
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
	c.stopDeadline, c.stopHasDeadline = ctx.Deadline()
	return c.BaseCell.Stop(ctx)
}

var _ cell.BeforeStopper = (*ctxRecordingCell)(nil)

// afterStartFailingCell starts successfully but its AfterStart hook returns
// an error, exercising startCellWithHooks' AfterStart-fail branch. The
// failing cell joins LIFO rollback at index i alongside previously-started
// cells, all sharing the single rollback ctx derived from rollbackCells's
// newRollbackCtx() (single-budget invariant).
type afterStartFailingCell struct {
	*cell.BaseCell
	beforeStopCtxErr error
	stopCtxErr       error
	stopDeadline     time.Time // captured ctx.Deadline() inside Stop — used to verify single-budget invariant
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
	c.stopDeadline, _ = ctx.Deadline()
	return c.BaseCell.Stop(ctx)
}

var (
	_ cell.AfterStarter  = (*afterStartFailingCell)(nil)
	_ cell.BeforeStopper = (*afterStartFailingCell)(nil)
)

// beforeStartFailingCell implements BeforeStarter and returns an error from
// BeforeStart, triggering the i=0 rollback boundary: rollbackCells(-1) →
// early return with no cells to clean up and no goroutines spawned.
type beforeStartFailingCell struct {
	*cell.BaseCell
}

func newBeforeStartFailingCell(id string) *beforeStartFailingCell {
	return &beforeStartFailingCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
	}
}

func (c *beforeStartFailingCell) BeforeStart(_ context.Context) error {
	return errors.New(c.ID() + " before-start boom")
}

var _ cell.BeforeStarter = (*beforeStartFailingCell)(nil)

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
// the assembly LIFO-rolls-back B (its own resources) then the
// previously-started A. Both teardown paths must see a fresh ctx
// (ctx.Err() == nil) even when the caller's startCtx was canceled by a
// SIGTERM, AND both must share the same parent rollback ctx (single
// HookTimeout budget — not 2 × HookTimeout).
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

	// Decoupling: every rollback hook on every cell sees a fresh ctx.
	assert.NoError(t, failing.beforeStopCtxErr,
		"failing cell BeforeStop must run with fresh ctx — startCtx cancellation must not propagate")
	assert.NoError(t, failing.stopCtxErr,
		"failing cell Stop must run with fresh ctx — c.Stop bypasses invokeHook so rollback root ctx must already be fresh")
	assert.NoError(t, prior.beforeStopCtxErr,
		"prior cell BeforeStop must run with fresh ctx")
	assert.NoError(t, prior.stopCtxErr,
		"prior cell Stop must run with fresh ctx")

	// Single-budget invariant: Stop bypasses invokeHook, so the deadlines
	// observed inside both cells' Stop are the SAME parent rollback ctx
	// deadline. If startCellWithHooks had created a separate rollback ctx
	// for the failing cell, the two deadlines would diverge by up to
	// HookTimeout (regression seen in earlier draft of this PR).
	assert.False(t, failing.stopDeadline.IsZero(),
		"failing cell Stop must observe a deadline (HookTimeout default applied)")
	assert.False(t, prior.stopDeadline.IsZero(),
		"prior cell Stop must observe a deadline")
	assert.Equal(t, failing.stopDeadline, prior.stopDeadline,
		"both cells' Stop must share the same rollback ctx deadline — single HookTimeout budget across the whole rollback")
}

// TestRollbackCells_I0BeforeStartFails covers the i=0 boundary case:
// when the very first cell's BeforeStart hook returns an error, rollbackCells(-1)
// exits immediately without acquiring a rollback ctx — no cells were started so
// there is nothing to roll back. Goroutine leak detection is provided by
// goleak.VerifyTestMain in hook_dispatcher_test.go (package-wide guard).
func TestRollbackCells_I0BeforeStartFails(t *testing.T) {
	t.Parallel()

	a := newTestAssembly(t, Config{
		ID:             "rollback-i0-beforestart",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})

	failing := newBeforeStartFailingCell("A")
	require.NoError(t, a.Register(failing))

	err := a.Start(context.Background())
	require.Error(t, err, "Start must fail because A.BeforeStart returns boom")

	// Snapshots must be nil after a failed start (failStart clears the map).
	assert.Nil(t, a.Snapshots(), "Snapshots() must return nil after failed start")
}
