package tailer

import (
	"context"
	"time"
)

// Observer is the saga-journal Tailer's best-effort observability sink (metrics
// counters + gauges). It mirrors the runtime/saga/executor.Observer convention:
// all methods MUST be non-blocking, tolerate canceled contexts, and be safe for
// concurrent use — they are called on the tail hot path (some while the
// per-projection distlock is HELD) and must never affect drain correctness. A
// misbehaving observer is contained by the Tailer's safeObserve wrapper: a panic
// is recovered and logged as Warn (payload redacted), and a call that blocks past
// observerCallDeadline is abandoned (logged as Warn) so it cannot pin the held
// lock or stall the drain. The abandoned call's goroutine still runs until the
// observer returns, so implementations MUST stay non-blocking. Keep them trivial
// (atomic instrument increments).
//
// projectionID is the bounded label dimension (assembly-enumerated, like the
// HTTP/saga cell label). Per-event identities (event id, owner token) are NOT
// label dimensions — they are high-cardinality and would explode time-series
// stores; carry them only in logs/traces.
//
// The five methods map 1:1 to the PR-04 observability checklist
// (ADR 202606051200-1609 §4.3):
//   - ObserveLockAcquire      → lock-acquire-failure
//   - ObserveDrain            → drain-error
//   - ObserveCheckpointAdvance → checkpoint-advance-failure
//   - ObserveLag              → pending-lag (HeadSeq − checkpoint)
//   - ObserveLastSuccess      → last-success-timestamp
type Observer interface {
	// ObserveLockAcquire is called when the per-projection distlock acquire FAILS
	// (the leader gate skips this tick). reason is one of LockAcquireResult.
	ObserveLockAcquire(ctx context.Context, projectionID string, reason LockAcquireResult)
	// ObserveDrain is called once per drain that makes progress or fails: ok (≥1
	// event applied), head_error (head-bound fetch failed), store_error (checkpoint
	// load failed), or apply_error (a non-stale replay/apply failure). An idle
	// caught-up tick (0 events, no error) is not reported.
	ObserveDrain(ctx context.Context, projectionID string, result DrainResult)
	// ObserveCheckpointAdvance is called once per AdvanceIfOwner attempt: ok,
	// stale_owner (fenced deposed leader — benign), or error (other advance fault).
	ObserveCheckpointAdvance(ctx context.Context, projectionID string, result AdvanceResult)
	// ObserveLag reports the pending backlog (HeadSeq − checkpoint, clamped ≥ 0)
	// after a successful tick. Backs the pending-events gauge.
	ObserveLag(ctx context.Context, projectionID string, pending int64)
	// ObserveLastSuccess reports the wall-clock time of the last fully-completed
	// tick. Backs the last-success-timestamp gauge (drives the stalled-tailer alert).
	ObserveLastSuccess(ctx context.Context, projectionID string, ts time.Time)
}

// LockAcquireResult is a typed enum of why a per-projection distlock acquire
// failed (the tick was skipped). Stable wire-facing strings used as the
// saga_journal_tailer_lock_acquire_failed_total{reason} metric label. Value set
// frozen by SAGA-METRIC-LABEL-VALUES-FROZEN-01 (tools/archtest/saga_invariants_test.go).
type LockAcquireResult string

const (
	// LockContended means another process holds the per-projection distlock
	// (ErrLockTimeout) — expected/benign in multi-process deployments.
	LockContended LockAcquireResult = "contended"
	// LockCtxCanceled means the acquire was aborted by ctx cancellation/deadline
	// (normal Stop()/shutdown of the tail loop).
	LockCtxCanceled LockAcquireResult = "ctx_canceled"
	// LockBackendError means a distlock backend I/O fault prevented the acquire —
	// the lock-acquire failure rate (operationally interesting; drives the alert).
	LockBackendError LockAcquireResult = "backend_error"
)

// DrainResult is a typed enum classifying a Tailer drain. Stable wire-facing
// strings used as the saga_journal_tailer_drain_total{result} metric label.
// Value set frozen by SAGA-METRIC-LABEL-VALUES-FROZEN-01.
type DrainResult string

const (
	// DrainOK means the drain applied ≥1 event and advanced the checkpoint cleanly.
	DrainOK DrainResult = "ok"
	// DrainHeadError means the head-bound fetch (replay.Head) failed, so the tick's
	// replay upper bound is unknown and the drain aborts before any replay — no
	// event applied, checkpoint untouched. Head is fetched before LoadOffset in
	// drain(), so a head failure aborts before the checkpoint is even loaded —
	// distinct from DrainStoreError (which is specifically a checkpoint LoadOffset
	// fault).
	DrainHeadError DrainResult = "head_error"
	// DrainStoreError means the checkpoint LoadOffset failed (storage fault).
	DrainStoreError DrainResult = "store_error"
	// DrainApplyError means a non-stale replay/apply failure surfaced from the
	// drain (apply returned error, cursor position failed, etc.).
	DrainApplyError DrainResult = "apply_error"
)

// AdvanceResult is a typed enum classifying a single AdvanceIfOwner attempt.
// Stable wire-facing strings used as the
// saga_journal_tailer_checkpoint_advance_total{result} metric label. Value set
// frozen by SAGA-METRIC-LABEL-VALUES-FROZEN-01.
type AdvanceResult string

const (
	// AdvanceOK means the checkpoint advance committed.
	AdvanceOK AdvanceResult = "ok"
	// AdvanceStaleOwner means AdvanceIfOwner rejected this caller as a deposed
	// leader (projection.ErrStaleOwner) — a benign handoff, not an incident.
	AdvanceStaleOwner AdvanceResult = "stale_owner"
	// AdvanceError means the advance failed for another reason (tx/storage fault).
	AdvanceError AdvanceResult = "error"
	// AdvancePoisonSkip means the checkpoint advanced PAST a poison event (a
	// permanent apply error) that was recorded to the dead-letter sink instead of
	// applied (#2110). The advance succeeded; this value distinguishes "skipped a
	// dead-lettered event" from "applied an event" (AdvanceOK) so operators alert
	// on saga_journal_tailer_checkpoint_advance_total{result="poison_skip"} > 0.
	AdvancePoisonSkip AdvanceResult = "poison_skip"
)

// NopObserver is the zero-cost default Observer. All methods are no-ops.
type NopObserver struct{}

// ObserveLockAcquire implements Observer.
func (NopObserver) ObserveLockAcquire(context.Context, string, LockAcquireResult) {
	// Default observer intentionally drops lock-acquire events.
}

// ObserveDrain implements Observer.
func (NopObserver) ObserveDrain(context.Context, string, DrainResult) {
	// Default observer intentionally drops drain events.
}

// ObserveCheckpointAdvance implements Observer.
func (NopObserver) ObserveCheckpointAdvance(context.Context, string, AdvanceResult) {
	// Default observer intentionally drops checkpoint-advance events.
}

// ObserveLag implements Observer.
func (NopObserver) ObserveLag(context.Context, string, int64) {
	// Default observer intentionally drops lag observations.
}

// ObserveLastSuccess implements Observer.
func (NopObserver) ObserveLastSuccess(context.Context, string, time.Time) {
	// Default observer intentionally drops last-success observations.
}

// Compile-time interface satisfaction check.
var _ Observer = NopObserver{}
