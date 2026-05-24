// Package saga is the GoCell runtime engine for L3 WorkflowEventual sagas
// (capability cap-15). It drives saga instances forward by reading the
// kernel/saga/journal.Journal, executing user-supplied Step bodies, then
// writing the resulting journal event(s) and outbox commands inside one
// short database transaction.
//
// # Single-process unsafe mode
//
// PR-03 ships a single-process Coordinator with NO leader-elect; it is NOT
// suitable for production. PR-05 will add distlock-based leader-elect.
// The Coordinator emits slog.Warn(mode="unsafe_no_leader") at Start.
//
// # Layering
//
// The Definition / Step / Registry data primitives live in kernel/saga (a
// pure data package). The Coordinator + Dispatcher engine lives here. No
// cell wraps runtime/saga in PR-03 — the cell-side wiring (including
// RegisterRepoReady) lands in PR-09.
//
// # Carry-over from PR-02 (#952)
//
//   - RepoReady probe registration: Coordinator satisfies healthz.RepoProber;
//     cell-side registration deferred to PR-09 (tracked in #978).
//   - Enqueuer interface split: deferred until a real producer ships.
//
// Note: The `saga_coordinator_ready` probe is NOT registered with any cell in
// PR-03; it becomes reachable via `/readyz` only after PR-09 wires it through
// cellgen's `RegisterRepoReady`.
//
// # PR-03 deferred scope
//
//   - Compensate execution: deferred to PR-06.
//   - Retry policy (per-step backoff): deferred to PR-06.
//   - Leader-elect (distlock): deferred to PR-05.
//   - Per-step parallelism: deferred to PR-06+ (tracked in #983).
//   - Coordinator-level Start API for producers (typed producer facade):
//     deferred to PR-07/PR-09.
//   - Metrics emission (tick / heartbeat / drive counters) deferred to a
//     future PR. PR-03's slog Warn logs provide minimal observability until
//     then.
//
// # Coordinator lifecycle
//
// NewCoordinator validates required deps (journal/txRunner/outboxEmit/registry
// non-nil; clock panics if nil via clock.MustHaveClock). Start launches two
// goroutines:
//   - tickLoop: calls ClaimPending each PollInterval, drives each claimed
//     instance through one step (Run outside tx, Append + Emit + AfterCommit
//     Kick inside tx).
//   - heartbeatLoop: extends leases on actively-driven instances.
//
// Stop is idempotent. Ready() returns a channel closed once Start transitions
// to running. RepoReady delegates to journal.RepoReady so the wrapping cell
// (PR-09) can register it via cellgen-emitted RegisterRepoReady.
//
// tickLoop processes claimed instances sequentially within one tick. If
// Step.Run has high latency, set ClaimBatchSize=1 to keep ticks short and
// lease heartbeats timely. Per-step parallelism is tracked in #983.
//
// # Step.Run is the only StepFunc callsite
//
// Inside this package, saga.StepFunc is invoked exclusively from safeRun (a
// recover-guarded helper). safeRun is called BEFORE txRunner.RunInTx, so user
// code never executes with a database transaction held open. This invariant is
// locked by the SAGA-STEP-RUN-OUTSIDE-TX-01 archtest.
//
// # Coordinator is the single sanctioned Journal holder
//
// Coordinator is the only struct in this package that holds a journal.Journal
// field. Locked by SAGA-JOURNAL-HOLDER-SEAL-01 archtest.
//
// ref: dtm dtmsvr/cron.go (CronTransOnce structure)
// ref: temporalio sdk-go internal_task_pollers.go
// ref: ThreeDotsLabs/watermill message/router.go
package saga
