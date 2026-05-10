package command

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
)

// rollbackStopBudget caps how long the rollback Stop call waits for the
// sweeper goroutine to exit. The fast-path (ticker has not fired) returns
// immediately; the slow path (ticker mid-tick) returns after the current
// runTick completes. 2s is generous for slow CI.
const rollbackStopBudget = 2 * time.Second

// TestSweeperLifecycle_StartupFailRollback simulates the bootstrap LIFO
// rollback scenario: cell A's sweeper.OnStart succeeds → cell B's OnStart
// fails → bootstrap reverses LIFO and calls A's OnStop. The sweeper
// goroutine MUST exit cleanly within the stop budget, leaving no
// background work running.
//
// PR 441 review F3-C decision (ADR 202605102000-adr-lifecycle-hook-ctx-
// semantics.md): SweeperLifecycle.Start uses context.WithCancel(
// context.Background()) — the worker ctx is intentionally derived from
// background, NOT from the OnStart-supplied startup-deadline ctx
// (matches uber-go/fx hook semantics + cell.LifecycleHook.StartTimeout
// design). The contract this test pins: bootstrap-orchestrated OnStop
// is the sole worker-cancel path, and OnStop must always be called
// during rollback (LIFO invariant of `runtime/bootstrap`).
//
// goleak verifies no sweeper goroutine survives past Stop. If a future
// refactor decouples worker cancellation from OnStop (or moves to
// owner-ctx propagation per backlog LIFECYCLE-OWNER-CTX-PROPAGATION-01),
// the contract being tested here changes — that change should ship with
// an updated ADR + this test rewritten, not silently relaxed.
func TestSweeperLifecycle_StartupFailRollback(t *testing.T) {
	defer goleak.VerifyNone(t)

	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q, clock.Real(),
		kcommand.WithSweeperInterval(time.Hour)) // long interval — no tick fires during this test
	require.NoError(t, err)
	lc := NewSweeperLifecycle("devicecommand.sweeper", sw, clock.Real())

	// Step 1: simulate cell A startup completing (Start returns prompt).
	// Per uber-go/fx hook semantics, OnStart's ctx is the startup deadline;
	// SweeperLifecycle copies the ctx-deadline pattern by returning quickly
	// after spawning the worker goroutine (no select on the OnStart ctx).
	startCtx, startCancel := context.WithTimeout(context.Background(), rollbackStopBudget)
	defer startCancel()
	require.NoError(t, lc.Start(startCtx))

	// Step 2: simulate bootstrap detecting cell B's OnStart failure and
	// reversing LIFO. Cell A's OnStop is the rollback step.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), rollbackStopBudget)
	defer stopCancel()
	require.NoError(t, lc.Stop(stopCtx),
		"rollback OnStop must terminate the sweeper goroutine within budget")

	// Step 3: idempotent Stop after rollback (defensive — bootstrap may call
	// Stop a second time during normal shutdown after rollback).
	require.NoError(t, lc.Stop(stopCtx), "Stop must remain idempotent after rollback")

	// goleak (deferred at top) asserts no sweeper goroutine survives.
}
