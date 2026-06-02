package projection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// ErrRebuildInProgress is returned by Rebuild when a rebuild is already
// running. Callers should map this to HTTP 409 Conflict.
var ErrRebuildInProgress = errcode.New(errcode.KindConflict, errcode.ErrConflict,
	"projection.Rebuild: rebuild already in progress; only one rebuild may run at a time")

// Rebuild triggers a background full rebuild of the projection's read-model.
// It transitions the Coordinator through the 4-phase lifecycle:
//
//	Stop → Reset → Replay → Catchup → Live
//
// Rebuild returns immediately (nil) if the CAS admission succeeds; the caller
// should respond 202 Accepted. If a rebuild is already running, Rebuild returns
// [ErrRebuildInProgress] (caller maps to 409 Conflict).
//
// Subscribe must have been called before Rebuild — the projection must have a
// registered apply function and spec before rebuild can start.
//
// The rebuild goroutine is detached from the request context: it is only
// canceled by Close (which sets rebuildCancel).
func (c *Coordinator) Rebuild(ctx context.Context) error {
	if !c.subscribed.Load() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.Rebuild: Subscribe must be called before Rebuild")
	}
	// Admission: CAS Live→Stopped. Only one rebuild at a time.
	if !c.phase.CompareAndSwap(uint32(PhaseLive), uint32(PhaseStopped)) {
		return ErrRebuildInProgress
	}

	rctx, cancel := context.WithCancel(context.Background())
	c.rebuildCancel.Store(&cancel)
	c.rebuildWG.Add(1)
	go c.runRebuild(rctx)
	return nil
}

// runRebuild executes the full rebuild lifecycle. It is the only writer of
// phase values after the initial CAS (so plain Store is correct for subsequent
// phase transitions).
//
// Every error/cancel edge ends with phase=PhaseLive and openGate. Panics from
// business apply/onReset are recovered here (mirrors kernel/wrapper.WrapConsumer
// containment — ref: AxonFramework TrackingEventProcessor worker-loop /
// Watermill Recoverer): the goroutine logs and calls failRebuild (phase→Live,
// openGate) instead of crashing the process.
func (c *Coordinator) runRebuild(ctx context.Context) {
	defer c.rebuildWG.Done()
	// Panic recovery: registered after Done (LIFO so runs first). recover() stops
	// the panic; Done then runs as the remaining deferred function. This mirrors
	// kernel/wrapper.WrapConsumer containment (AxonFramework TrackingEventProcessor
	// worker-loop / Watermill Recoverer): contain business panics, restore PhaseLive.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		panicErr := redaction.RedactError(fmt.Errorf("%v", r))
		slog.ErrorContext(ctx, "projection rebuild panic recovered",
			"cell", c.cellID,
			"projection", c.projectionID,
			"panic", panicErr,
		)
		c.failRebuild(ctx, "panic", panicErr)
	}()

	slog.InfoContext(ctx, "projection rebuild started",
		"cell", c.cellID,
		"projection", c.projectionID,
	)
	start := c.clk.Now()

	// Phase: Stopped — shut gate so live handler parks.
	c.shutGate()

	head0, err := c.captureHead(ctx)
	if err != nil {
		c.failRebuild(ctx, "captureHead", err)
		return
	}

	// Phase: Reset — OnReset + SaveOffset(0) in one tx.
	c.phase.Store(uint32(PhaseReset))
	if err := c.resetPhase(ctx); err != nil {
		c.failRebuild(ctx, "resetPhase", err)
		return
	}

	// Phase: Replay — replay from 0 through head0.
	c.phase.Store(uint32(PhaseReplay))
	if err := c.replayPhase(ctx, head0); err != nil {
		c.failRebuild(ctx, "replayPhase", err)
		return
	}

	// Phase: Catchup — open gate, let live handler consume (head0, ∞).
	c.phase.Store(uint32(PhaseCatchup))
	c.openGate()

	// Catchup: wait until checkpoint ≥ current Head (or ctx cancel).
	caughtUp, err := c.catchupPhase(ctx)
	if err != nil {
		// ctx cancel during catchup: gate is already open, live handler continues.
		// Route through failRebuild for consistent logging; openGate is idempotent.
		c.failRebuild(ctx, "catchupPhase", err)
		return
	}

	// rebuild_duration + the "completed" success signal are recorded ONLY when
	// catchup actually reached the head. A degraded catchup (Head/LoadOffset
	// error → caughtUp=false) returns to PhaseLive without observing the
	// histogram, so dashboards/alerting do not mistake a degraded run for a
	// clean completion (it was already logged at Warn in catchupPhase).
	durSecs := c.clk.Since(start).Seconds()
	if caughtUp {
		c.metrics.observeRebuildDuration(ctx, c.cellID, c.projectionID, durSecs)
		slog.InfoContext(ctx, "projection rebuild completed",
			"cell", c.cellID,
			"projection", c.projectionID,
			"duration_seconds", durSecs,
		)
	} else {
		slog.WarnContext(ctx, "projection rebuild completed with degraded catchup",
			"cell", c.cellID,
			"projection", c.projectionID,
			"duration_seconds", durSecs,
		)
	}
	c.phase.Store(uint32(PhaseLive))
}

// captureHead records head₀ for the catchup cutoff.
func (c *Coordinator) captureHead(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	head, err := c.replay.Head(ctx)
	if err != nil {
		return 0, fmt.Errorf("projection.rebuild: Head: %w", err)
	}
	return head, nil
}

// resetPhase runs OnReset + SaveOffset(0) in a single transaction.
// If the transaction fails, the checkpoint is NOT zeroed (whole tx rollback).
func (c *Coordinator) resetPhase(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if c.onReset != nil {
			if err := c.onReset(txCtx); err != nil {
				return fmt.Errorf("projection.rebuild[reset]: onReset: %w", err)
			}
		}
		if err := c.store.SaveOffset(txCtx, c.cellID, c.projectionID, 0); err != nil {
			return fmt.Errorf("projection.rebuild[reset]: SaveOffset(0): %w", err)
		}
		return nil
	})
}

// errReplayDone is the internal sentinel returned by the Replay fn to stop
// iteration early once head0 is reached. replayPhase filters it out.
var errReplayDone = errors.New("projection.rebuild: replay reached head0")

// replayPhase replays events from offset 0 through head0 (inclusive).
// Each event is applied via applyOne in its own tx (exactly-once skip + pos<1
// guard apply). Stops at head0 (catchup picks up from there).
//
// v1: replay is bounded only by ctx cancellation (no max-entries / max-duration).
// Operators bound duration via the ctx timeout passed to Rebuild.
func (c *Coordinator) replayPhase(ctx context.Context, head0 int64) error {
	err := c.replay.Replay(ctx, 0, func(entry outbox.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			// Restore the event's observability + principal into ctx before Apply,
			// matching the live consumer path (SubscriberWithMiddleware) so a
			// rebuild's Apply sees the same trace/audit identity as live delivery
			// (#1368 review F4). RestoreToContext is idempotent and does not strip
			// the ambient tx carried in txCtx.
			rctx := entry.Observability().RestoreToContext(txCtx)
			rctx = entry.Principal().RestoreToContext(rctx)
			return c.applyOne(rctx, entry, c.apply)
		}); err != nil {
			return fmt.Errorf("projection.rebuild[replay]: %w", err)
		}
		// Stop at head0; events at positions > head0 are handled by catchup. A
		// Position error here is unexpected — applyOne resolved this same entry's
		// position just above — so surface it instead of swallowing it (F8).
		pos, posErr := c.cursor.Position(entry)
		if posErr != nil {
			return fmt.Errorf("projection.rebuild[cutoff]: %w", posErr)
		}
		if head0 > 0 && pos >= head0 {
			return errReplayDone
		}
		return nil
	})
	if errors.Is(err, errReplayDone) {
		return nil
	}
	return err
}

// catchupPhase waits until the checkpoint has caught up to the current head.
// The gate is already open, so live event handlers are processing events.
//
// Return contract:
//   - (true, nil)  — the checkpoint reached the head; a clean completion.
//   - (false, nil) — DEGRADED: a non-fatal Head/LoadOffset error aborted the
//     catchup check (already logged at Warn). The live path continues, but the
//     caller MUST NOT record rebuild_duration / a "completed" success signal.
//   - (false, err) — ctx canceled; the caller routes through failRebuild.
func (c *Coordinator) catchupPhase(ctx context.Context) (caughtUp bool, err error) {
	for {
		if cerr := ctx.Err(); cerr != nil {
			return false, cerr
		}
		caught, ierr := c.isCaughtUp(ctx)
		if ierr != nil {
			// Non-fatal: store/Head error in catchup; live handler continues.
			// Signal DEGRADED (caughtUp=false) so the caller skips the success
			// duration/log — recording a clean completion here misleads dashboards.
			slog.WarnContext(ctx, "projection rebuild catchup check degraded",
				"cell", c.cellID,
				"projection", c.projectionID,
				"error", ierr,
			)
			return false, nil
		}
		if caught {
			return true, nil
		}
		// Yield briefly to let the live handler process incoming events.
		sleepUntil := c.clk.Now().Add(catchupPollInterval)
		if serr := c.clk.Sleep(ctx, sleepUntil); serr != nil {
			return false, serr
		}
	}
}

// isCaughtUp checks whether the checkpoint >= current head.
func (c *Coordinator) isCaughtUp(ctx context.Context) (bool, error) {
	head, err := c.replay.Head(ctx)
	if err != nil {
		return false, err
	}
	cp, err := c.store.LoadOffset(ctx, c.cellID, c.projectionID)
	if err != nil {
		return false, err
	}
	return cp >= head, nil
}

// catchupPollInterval is the polling period during Catchup phase.
const catchupPollInterval = 5 * time.Millisecond

// failRebuild restores PhaseLive and opens the gate on any rebuild error.
// It logs a structured slog error with the phase name and error for ops diagnostics.
// openGate is idempotent, so calling failRebuild after the gate is already open
// (e.g. from catchupPhase) is safe.
func (c *Coordinator) failRebuild(ctx context.Context, phase string, err error) {
	slog.ErrorContext(ctx, "projection rebuild failed",
		"cell", c.cellID,
		"projection", c.projectionID,
		"phase", phase,
		"error", err,
	)
	c.openGate()
	c.phase.Store(uint32(PhaseLive))
}

// Close cancels any in-flight rebuild and waits for it to finish.
// It then closes the done channel. Close is idempotent and safe to call
// concurrently from multiple goroutines; only the first call closes the done
// channel (subsequent calls are no-ops). ctx controls the wait timeout.
func (c *Coordinator) Close(ctx context.Context) error {
	c.closeOnce.Do(func() { close(c.done) })

	if cf := c.rebuildCancel.Load(); cf != nil {
		(*cf)()
	}

	// Wait for rebuild goroutine to finish, or until ctx is done.
	waited := make(chan struct{})
	go func() {
		c.rebuildWG.Wait()
		close(waited)
	}()
	select {
	case <-waited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
