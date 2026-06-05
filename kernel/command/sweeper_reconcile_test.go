package command_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestCommandSweeper_ImplementsReconciler pins the compile-time contract that a
// *command.Sweeper satisfies reconcile.Reconciler. This is the PR-A8 headline:
// the kernel command sweeper is the reconcile runtime's first real consumer,
// driven by a reconcile.Loop instead of the (now-deleted) runtime control shell.
func TestCommandSweeper_ImplementsReconciler(t *testing.T) {
	t.Parallel()
	var _ reconcile.Reconciler = (*command.Sweeper)(nil)
	// also keep the readiness gate the Loop relies on.
	var _ interface{ Validate() error } = (*command.Sweeper)(nil)
}

// TestCommandSweeper_ReconcileBehaviorPreserved verifies Reconcile produces the
// SAME scan→expire→Ack effect as a direct SweepTick at the clock's current time:
// the StatusExpired transitions are unchanged, only the "now" source moves from
// an explicit caller parameter to the injected clock.
func TestCommandSweeper_ReconcileBehaviorPreserved(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := created.Add(testtime.D5min) // past the 1m overall deadline
	expired := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)

	scanner := &mockScanner{entries: []command.Entry{expired}}
	q := &mockAckQueue{}
	clk := clockmock.New(now)

	s, err := command.NewSweeper(scanner, q, clk)
	require.NoError(t, err)

	res, err := s.Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res, "success returns zero Result (RequeueAfter=0 → Loop default tick)")

	calls := q.Calls()
	require.Len(t, calls, 1, "the expired command must be Ack'd exactly as SweepTick would")
	assert.Equal(t, "cmd-1", calls[0].id)
	assert.Equal(t, command.AckTimeout, calls[0].reason)
}

// TestCommandSweeper_ReconcileIgnoresEntityID verifies the Sweeper is a bulk
// scan-all actor: a non-empty req.EntityID behaves identically to the
// resync-all Request{} pulse (the Sweeper has no per-entity path).
func TestCommandSweeper_ReconcileIgnoresEntityID(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := created.Add(testtime.D5min)
	expired := command.NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), command.Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)

	scanner := &mockScanner{entries: []command.Entry{expired}}
	q := &mockAckQueue{}
	s, err := command.NewSweeper(scanner, q, clockmock.New(now))
	require.NoError(t, err)

	_, err = s.Reconcile(context.Background(), reconcile.Request{EntityID: "ignored-device-id"})
	require.NoError(t, err)
	assert.Equal(t, 1, q.CallCount(), "EntityID must not narrow the scan; full sweep runs regardless")
}

// TestCommandSweeper_ReconcileScanErrorTransient verifies a scan failure is
// returned to the Loop as a transient (non-permanent) error so the Loop backs
// off and retries on the next tick — sweep failures are never dead-letters.
func TestCommandSweeper_ReconcileScanErrorTransient(t *testing.T) {
	t.Parallel()
	scanErr := errors.New("db unavailable")
	scanner := &mockScanner{err: scanErr}
	q := &mockAckQueue{}
	s, err := command.NewSweeper(scanner, q, clock.Real())
	require.NoError(t, err)

	res, err := s.Reconcile(context.Background(), reconcile.Request{})
	require.Error(t, err)
	assert.ErrorIs(t, err, scanErr)
	assert.False(t, reconcile.IsPermanent(err), "sweep errors are transient, never permanent")
	assert.Equal(t, reconcile.Result{}, res)
}

// TestCommandSweeper_ReconcileNilReceiverFailClosed pins that a nil receiver
// fails closed with an error rather than panicking on s.clk — mirroring
// SweepTick's head guard before the clock is dereferenced.
func TestCommandSweeper_ReconcileNilReceiverFailClosed(t *testing.T) {
	t.Parallel()
	var s *command.Sweeper
	_, err := s.Reconcile(context.Background(), reconcile.Request{})
	require.Error(t, err)
}

// TestCommandSweeper_ReconcileZeroValueFailClosed pins that the zero-value
// &command.Sweeper{} literal (built==false) fails closed before touching the
// nil clock.
func TestCommandSweeper_ReconcileZeroValueFailClosed(t *testing.T) {
	t.Parallel()
	var s command.Sweeper
	_, err := s.Reconcile(context.Background(), reconcile.Request{})
	require.Error(t, err)
}

// TestNewSweeper_NilClockPanics pins that the clock is a mandatory positional
// dependency (clock.MustHaveClock programmer-error guard), per
// CLOCK-POSITIONAL-INJECTION-01.
func TestNewSweeper_NilClockPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_, _ = command.NewSweeper(&mockScanner{}, &mockAckQueue{}, nil)
	})
}
