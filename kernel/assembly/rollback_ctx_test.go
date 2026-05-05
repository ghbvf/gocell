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
)

// ctxRecordingCell captures ctx state observed inside BeforeStop and Stop so
// tests can assert on ctx.Err() / ctx.Deadline() without racing the assembly.
// failStart=true makes Start return an error so rollback fires on the
// previously-registered cell.
type ctxRecordingCell struct {
	*cell.BaseCell
	failStart            bool
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

var (
	_ cell.BeforeStopper = (*ctxRecordingCell)(nil)
)

// TestRollbackCells_DerivedCtx covers PR-V1-030-G02: rollback hooks on
// already-started cells must NOT inherit a cancelled startCtx. They must run
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
			name:              "cancelled-startctx-with-default-hook-timeout",
			hookTimeout:       0, // → DefaultHookTimeout
			cancelStartCtx:    startCtxCancelled,
			wantBeforeStopErr: nil,
			wantStopErr:       nil,
			wantHasDeadline:   true,
		},
		{
			name:              "cancelled-startctx-with-explicit-hook-timeout",
			hookTimeout:       2 * time.Second,
			cancelStartCtx:    startCtxCancelled,
			wantBeforeStopErr: nil,
			wantStopErr:       nil,
			wantHasDeadline:   true,
		},
		{
			name:              "cancelled-startctx-with-negative-hook-timeout-no-deadline",
			hookTimeout:       -1, // disables timeout — rollback ctx must have no deadline
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
