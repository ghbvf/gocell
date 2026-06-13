// Package saga is the GoCell runtime engine for L3 WorkflowEventual sagas
// (capability cap-15). It drives saga instances forward by reading the
// kernel/saga/journal.Journal, executing user-supplied Step bodies, then
// writing the resulting journal event(s) and outbox commands inside one
// short database transaction.
//
// # Leader election (single-process unsafe by default)
//
// Without WithLeaderElect the Coordinator runs single-process with NO
// distributed leader election; it is NOT safe for multi-process deployment and
// emits slog.Warn(mode="unsafe_no_leader") at Start. Pass
// WithLeaderElect(distlock.Locker) (PR-05) to gate every drive behind a
// per-instance distributed lock keyed "saga:{definitionID}:{instanceID}":
// contending coordinators skip instances they cannot lock, a crashed leader's
// lock and journal lease both expire via TTL, and Start emits
// slog.Info(mode="leader_elect"). See leader_elect.go.
//
// # Layering
//
// The Definition / Step / Resolver data primitives live in kernel/saga (a
// pure data package). The Coordinator + Dispatcher engine lives here. No
// cell wraps runtime/saga in PR-03 — the cell-side wiring (including
// RegisterReadiness) lands in PR-09.
//
// # Carry-over from PR-02 (#952)
//
//   - RepoReady probe registration: Coordinator satisfies healthz.RepoProber;
//     cell-side registration deferred to PR-09 (tracked in #978).
//   - Enqueuer interface split: deferred until a real producer ships.
//
// Note: The `saga_coordinator_ready` probe is NOT registered with any cell in
// PR-03; it becomes reachable via `/readyz` only after PR-09 wires it through
// cellgen's `RegisterReadiness`.
//
// # Observer (PR-#1181 / #1109)
//
// The Coordinator and its internal Executor share a single executor.Observer
// (set via WithObserver; coordinator.go options.go). The interface carries
// six callbacks, split into two groups:
//
// Executor-emitted (step-level):
//   - ObserveOutcome: called once per Execute/Compensate call at the terminal Result.
//   - ObserveRetry: called between step attempts (attempt N > 1).
//   - ObserveHeartbeatFailure: called on infra-error or stale-lease ticks.
//
// Coordinator-emitted (tick/drive/leader-skip level):
//   - ObserveTick: called once per ClaimPending poll (empty / claimed / error).
//   - ObserveDrive: called once per instance driven (ok / error).
//   - ObserveLeaderSkip: called when acquireLead fails (contended / ctx_canceled / backend_error).
//
// executor.NopObserver is the zero-cost default. executor.WithObserver(nil) is
// silently ignored (builder-noop option); the Executor keeps NopObserver.
// The Coordinator passes the WithObserver option through to the internal Executor
// via NewCoordinator's options — see options.go for WithObserver.
//
// Both the Executor and the Coordinator guard observer calls with the same two
// layers of fail-closed protection:
//  1. Panic recovery: a panicking observer logs Warn with a redacted payload
//     and execution continues (executor.recoverObserverPanic /
//     Coordinator.recoverObserverPanic).
//  2. Bounded goroutine wait: the observer call runs on a fresh goroutine; the
//     caller waits at most observerCallDeadline (default
//     executor.DefaultObserverCallDeadline = 5s) before logging Warn and
//     returning. For the Coordinator this prevents a hung observer from leaking
//     the per-instance distlock (release() and inflightLocks.Delete run after
//     ObserveDrive in tickOnce) and from blocking the shutdown drain
//     (executor.callObserverBounded / Coordinator.safeObserve).
//
// executor.HeartbeatFailureReason is the typed enum for ObserveHeartbeatFailure
// reason values: HeartbeatFailureInfraError (transient backend error) and
// HeartbeatFailureStaleLease (another coordinator owns the lease).
//
// executor.IsLeaseLost is the public predicate compensation walks use to
// detect that RunWithHeartbeat returned because the lease was lost.
//
// During runCompensation, the heartbeat goroutine is maintained via
// executor.RunWithHeartbeat. If the heartbeat reports a stale lease, the
// compensation context is canceled (errLeaseLost) and the walk stops; the
// instance will be re-claimed by another coordinator on its next tick.
//
// # Saga terminal states (PR-#1210)
//
// Five terminal states encode why a saga finished:
//   - StatusSucceeded — all forward steps committed
//   - StatusFailed — forward-phase failure with no rollback (never entered Compensating)
//   - StatusCompensated — forward failure followed by clean rollback
//   - StatusCompensationFailed — rollback itself failed; ops intervention required
//   - StatusExpired — overall saga timeout elapsed at any non-terminal stage
//
// StatusFailed and StatusCompensationFailed are distinct on purpose: the
// former says "we never tried to undo", the latter says "we tried and
// could not". Dashboards and ops runbooks should branch on this distinction.
// See kernel/saga.Status.String() for the wire labels (snake_case).
//
// # PR-03 deferred scope
//
//   - Coordinator-level Start API for producers (typed producer facade):
//     deferred to PR-07/PR-09.
//
// (Per-tick concurrent driving of claimed instances — formerly tracked here as
// #983 — is now delivered; see the tickLoop section below.)
//
// Metrics emission is complete (#1109): the Coordinator and Executor fan all
// observability out through executor.Observer, which runtime/observability/
// metrics.SagaCollector turns into six counters — saga_step_outcome_total,
// saga_step_retry_total, saga_heartbeat_failed_total (step-level) plus
// saga_tick_total, saga_drive_total, and saga_leader_elect_skip_total
// (coordinator-level; leader-elect skip carries reason=contended/ctx_canceled/
// backend_error, where backend_error is the lock-acquire failure rate). slog
// remains the per-instance correlation channel (leader-elect skips and
// ctx-cancel are Debug, backend I/O errors Warn). The counters are only emitted
// when a real metrics Provider is wired via WithObserver; saga is not yet in a
// production cell (examples/orderfulfillment uses NopProvider), so production
// Provider wiring lands with the saga-as-cell migration (PR-09, #978).
//
// # Coordinator lifecycle
//
// NewCoordinator validates required deps (journal/txRunner/outboxEmit/registry
// non-nil; clock panics if nil via clock.MustHaveClock). Start launches a
// single tickLoop goroutine:
//   - tickLoop: calls ClaimPending each PollInterval, drives each claimed
//     instance through one step (Run outside tx, Append + Emit + AfterCommit
//     Kick inside tx). Per-step lease maintenance (heartbeat) is owned by
//     the Executor's per-step heartbeat goroutine (see runtime/saga/executor).
//
// Stop is idempotent. Ready() returns a channel closed once Start transitions
// to running. RepoReady delegates to journal.RepoReady so the wrapping cell
// (PR-09) can register it via cellgen-emitted RegisterReadiness.
//
// tickLoop drives the instances claimed in one tick concurrently — each led
// instance runs driveOne in its own goroutine and the tick wg.Waits for the
// whole batch before claiming the next (#983). Peak concurrency = ClaimBatchSize,
// which is therefore also the drive-concurrency / external-step-IO bound: lower
// ClaimBatchSize when steps open many external connections. Claim count and
// drive fan-out are deliberately one knob because a claimed instance holds a
// journal lease kept alive only by the Executor heartbeat that starts inside
// driveOne; decoupling them (claim a large batch, drive with lower concurrency)
// needs a resident worker pool and is deferred (#978). See Amendment 2026-06-07
// of the saga ADR (docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md).
//
// # Step.Run is the only StepFunc callsite
//
// Inside the saga runtime, saga.StepFunc is invoked exclusively from safeRun (a
// recover-guarded helper in the executor subpackage), which runs BEFORE any
// txRunner.RunInTx, so user code never executes with a database transaction
// held open. SAGA-STEP-RUN-OUTSIDE-TX-01 locks this two ways: A1 confines
// StepFunc calls to safeRun by *signature identity* (an alias or redefinition
// under any import name cannot escape), and A2 forbids any call transitively
// reaching safeRun inside a RunInTx closure via a cross-package taint set
// (named-wrapper and func-literal-var indirection included).
//
// # Coordinator is the single sanctioned JournalCore holder
//
// Coordinator is the only struct in this package that holds a
// journal.JournalCore field — the Heartbeat-free core of journal.Journal, so a
// centralized heartbeat loop is a compile error (#1209). No struct in this
// package may persist the Heartbeat-bearing full journal.Journal (or a bare
// journal.Heartbeater) as a field. Locked by SAGA-JOURNAL-HOLDER-SEAL-01 and
// SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 archtests.
//
// ref: dtm dtmsvr/cron.go (CronTransOnce structure)
// ref: temporalio sdk-go internal_task_pollers.go
// ref: ThreeDotsLabs/watermill message/router.go
package saga
