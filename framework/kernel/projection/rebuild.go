package projection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
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
// The rebuild goroutine is detached from the request's cancellation and
// deadline (via context.WithoutCancel) but inherits its values — request_id /
// trace_id / correlation_id / cell_id — so the async lifecycle logs correlate
// to the request that admitted the rebuild. It is canceled only by Close (which
// sets rebuildCancel), never by the triggering request completing.
func (c *Coordinator) Rebuild(ctx context.Context) error {
	if !c.subscribed.Load() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.Rebuild: Subscribe must be called before Rebuild")
	}
	// Admission: CAS Live→Stopped. Only one rebuild at a time.
	if !c.phase.CompareAndSwap(uint32(PhaseLive), uint32(PhaseStopped)) {
		return ErrRebuildInProgress
	}

	// WithoutCancel detaches from the request's cancellation/deadline (a rebuild
	// must outlive the short-lived 202 request) while preserving its values for
	// log correlation; WithCancel layers the Close-driven cancellation on top.
	// clearAmbientPrincipal strips any principal carried by the triggering
	// request context (e.g. the admin who called POST /rebuild) so the replay
	// carrier can install the correct per-event identity without the no-overwrite
	// guard (outbox.RestoreToContext) silently losing to the ambient admin identity.
	// ADR #1609 §5 (F1 flip): event principal wins on the outbox path; system
	// principal wins on the saga-journal path (InstallSystemPrincipal overwrite).
	rctx, cancel := context.WithCancel(clearAmbientPrincipal(context.WithoutCancel(ctx)))
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

	// Phase: Replay — drain (0, head0] from the whole-journal source. The gate is
	// SHUT (set above), so this rebuild goroutine is the SOLE applier — the live
	// handler stays parked.
	c.phase.Store(uint32(PhaseReplay))
	if err := c.drainGap(ctx, 0, head0); err != nil {
		c.failRebuild(ctx, "replay", err)
		return
	}

	// Phase: Catchup — drain (head0, head1] with the gate STILL SHUT, so the
	// rebuild goroutine remains the sole applier (no concurrent live applier —
	// preserving the serial-delivery precondition the exactly-once compare relies
	// on; ADR §6 Row 4 + §Amendment 2026-06-04 Row 8). Only AFTER the bounded
	// drain do we hand off to live delivery.
	c.phase.Store(uint32(PhaseCatchup))
	caughtUp, err := c.catchupPhase(ctx, head0)
	if err != nil {
		// ctx cancel / drain error during catchup: failRebuild opens the gate and
		// restores PhaseLive so live delivery resumes.
		c.failRebuild(ctx, "catchup", err)
		return
	}

	// Hand off to live delivery: the checkpoint has been drained to head₁ (own
	// stream applied + foreign advanced); the residual (head₁, now] tail —
	// including any foreign entry appended during the drain — is consumed by the
	// topic-routed live handler. openGate strictly before PhaseLive so a handler
	// that observes PhaseLive never finds the gate still shut.
	c.openGate()

	// rebuild_duration + the "completed" success signal are recorded ONLY when the
	// catchup drain reached head₁. A degraded catchup (Head error → caughtUp=false)
	// still hands off to live delivery but skips the histogram so dashboards do not
	// mistake a degraded run for a clean completion (it was logged at Warn in
	// catchupPhase).
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

// errDrainDone is the internal sentinel returned by the Replay fn to stop
// iteration early once the `through` cutoff is reached. drainGap filters it out.
var errDrainDone = errors.New("projection.rebuild: drain reached cutoff head")

// drainGap drains the whole-journal slice (from, through] in ascending position
// order, applying this projection's own stream via applyOne and advancing the
// checkpoint past foreign streams via advanceOffsetPastForeign — each in its own
// tx (exactly-once skip + pos<1 guard). Iteration stops once an entry's position
// reaches `through` (events beyond it are handled by the next phase / live
// delivery). `from`==0 drains from the start of history; `through`==0 means the
// source is empty (no events) and the drain is a no-op.
//
// It is the single shared per-spec replay funnel: replayPhase drains (0, head0]
// and catchupPhase drains (head0, head1], so the apply gate and foreign-advance
// branch live in ONE place and cannot drift between the two phases.
//
// Per-spec replay filtering (#1482): the bootstrap-wired ReplaySource is a single
// whole-journal source shared by every projection Coordinator (kernel contract:
// one source serves all projections), so a rebuild interleaves every cell's event
// streams. The business Apply runs ONLY for entries whose routing topic matches
// this projection's subscribed spec — mirroring live topic-routed delivery, so the
// Apply never sees a foreign stream and needs no defensive topic check. Foreign
// entries are NOT applied but DO advance the checkpoint (advanceOffsetPastForeign,
// like Marten async-daemon's high-water gap skip): the checkpoint must record
// journal progress over foreign streams so a rebuild's catchup can terminate —
// see catchupPhase. Filtering thus gates the Apply, not the checkpoint.
//
// Sole applier: drainGap runs on the rebuild goroutine while the gate is SHUT, so
// it is the only writer of the checkpoint during both replay and catchup; there is
// no concurrent live applier (serial-delivery precondition, ADR §6 Row 4).
//
// v1: a drain is bounded by `through` and by Close()/process shutdown (no
// max-entries / max-duration). The triggering request's deadline does NOT bound
// it — the rebuild ctx is derived via context.WithoutCancel, which strips the
// parent deadline so a detached rebuild outlives the short-lived 202 request.
func (c *Coordinator) drainGap(ctx context.Context, from, through int64) error {
	err := c.replay.Replay(ctx, from, func(entry ProjectionEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			// Restore the event's ambient identity into ctx before Apply, matching
			// the live consumer path (SubscriberWithMiddleware) so a rebuild's Apply
			// sees the same trace/audit identity as live delivery (#1368 review F4).
			// RestoreContext is idempotent and does not strip the ambient tx carried
			// in txCtx. (For an outbox carrier this restores observability +
			// principal; the carrier owns the per-source restore semantics.)
			rctx := entry.RestoreContext(txCtx)
			// Per-spec replay filter (#1482): apply only this projection's stream;
			// advance the checkpoint past foreign streams without applying.
			if entry.Stream() == c.spec.Topic {
				return c.applyOne(rctx, entry, c.apply)
			}
			return c.advanceOffsetPastForeign(rctx, entry)
		}); err != nil {
			return fmt.Errorf("projection.rebuild[drain]: %w", err)
		}
		// Stop at the cutoff; events beyond it are handled by the next phase. A
		// Position error here is unexpected — applyOne/advanceOffsetPastForeign
		// resolved this same entry's position just above — so surface it instead
		// of swallowing it (F8).
		pos, posErr := c.cursor.Position(entry)
		if posErr != nil {
			return fmt.Errorf("projection.rebuild[cutoff]: %w", posErr)
		}
		if through > 0 && pos >= through {
			return errDrainDone
		}
		return nil
	})
	if errors.Is(err, errDrainDone) {
		return nil
	}
	return err
}

// advanceOffsetPastForeign advances the projection checkpoint past a replayed
// entry that does NOT belong to this projection's subscribed topic, WITHOUT
// invoking the business Apply or touching the replay-lag metrics.
//
// Rationale: see drainGap. The whole-journal ReplaySource interleaves every
// cell's streams; a projection must skip foreign streams (live delivery is
// topic-routed and never delivers them) yet still record journal progress so a
// rebuild's catchup drain — which advances the checkpoint over (head0, head1] —
// reaches head1 even when foreign entries (including a trailing one) sit in that
// range. Without the foreign advance the checkpoint would stall at the last own
// entry and the bounded drain could never reach its cutoff. The 1-based and
// exactly-once (pos <= current) guards are shared with applyOne via
// resolvePosition so a foreign skip can never move the checkpoint backward or
// past a bad cursor, and the two paths cannot drift.
//
// Ops note (lag-gauge blind spot): foreign entries deliberately do NOT update
// projection_event_replay_lag_seconds (lag is an own-stream apply signal). On a
// journal dominated by foreign streams the lag gauge can therefore stay flat for
// stretches of a rebuild even though work is progressing — the checkpoint and
// pending_events still move; watch those for rebuild progress, not lag.
func (c *Coordinator) advanceOffsetPastForeign(ctx context.Context, entry ProjectionEvent) error {
	pos, proceed, err := c.resolvePosition(ctx, entry)
	if err != nil || !proceed {
		return err
	}
	if err := c.store.SaveOffset(ctx, c.cellID, c.projectionID, pos); err != nil {
		return fmt.Errorf("projection.rebuild[foreign-save]: %w", err)
	}
	return nil
}

// catchupPhase drains the journal gap (head0, head1] — where head1 is the source
// Head captured at catchup start — applying this projection's own stream and
// advancing the checkpoint past foreign streams (drainGap). The gate is still
// SHUT, so the rebuild goroutine is the SOLE applier: there is no concurrent live
// applier, which preserves the serial-delivery precondition the exactly-once
// checkpoint compare relies on (ADR §6 Row 4 + §Amendment 2026-06-04 Row 8). The
// bounded head1 cutoff guarantees termination even on a continuously-growing
// whole-journal source; the residual (head1, now] tail (including any foreign
// entry appended during the drain) is consumed by live delivery after the caller
// opens the gate.
//
// Why a self-drain and not the prior open-gate poll on `checkpoint >= Head`: the
// live handler is topic-routed and never sees foreign streams, so it cannot
// advance the checkpoint past a foreign entry that lands during catchup — an
// open-gate poll against the whole-journal Head would then spin forever once such
// an entry arrives. Draining ourselves (advance past foreign, like Marten's
// high-water gap skip) with the gate shut makes the replay→live handoff strictly
// sequential — no replay+live dual-applier path, matching the Axon/Marten
// streaming-processor consensus.
//
// Return contract:
//   - (true, nil)  — drained to head1; clean completion.
//   - (false, nil) — DEGRADED: a non-fatal Head error aborted the cutoff capture
//     (already logged at Warn). The caller opens the gate (live delivery resumes)
//     but MUST NOT record rebuild_duration / a "completed" success signal.
//   - (false, err) — ctx canceled or a drain apply/save error; the caller routes
//     through failRebuild.
func (c *Coordinator) catchupPhase(ctx context.Context, head0 int64) (caughtUp bool, err error) {
	if cerr := ctx.Err(); cerr != nil {
		return false, cerr
	}
	head1, herr := c.replay.Head(ctx)
	if herr != nil {
		// Non-fatal: cannot determine the catchup cutoff. Signal DEGRADED so the
		// caller opens the gate (live delivery drains the own stream) but skips the
		// success duration/log — recording a clean completion here misleads dashboards.
		slog.WarnContext(ctx, "projection rebuild catchup head check degraded",
			"cell", c.cellID,
			"projection", c.projectionID,
			"error", herr,
		)
		return false, nil
	}
	if derr := c.drainGap(ctx, head0, head1); derr != nil {
		return false, derr
	}
	return true, nil
}

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
