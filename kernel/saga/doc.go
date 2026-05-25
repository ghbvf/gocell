// Package saga provides the L3 (WorkflowEventual) saga orchestration state
// machine: the [Status] lifecycle, the [Instance] record, and the pure
// transition functions ([AdvanceSaga], [AdvanceStep], [Transition]) that
// advance an instance. It is the kernel vocabulary that the runtime
// Coordinator and the durable Journal build on.
//
// # Lifecycle
//
// A saga instance moves through a fail-closed state machine. All transitions
// are validated; an illegal move returns an error without mutating the instance.
//
//	Pending ──► Running ──► Succeeded                    (happy path)
//	   │           │
//	   │           ├──► Compensating ──► Compensated      (rollback succeeded)
//	   │           │           └──────► Failed            (compensation itself failed)
//	   │           └──► Failed                            (failed before any step committed)
//	   │
//	   └──► Failed | Expired                              (rejected / expired while queued)
//
//	Expired is reachable from every non-terminal state (Pending, Running,
//	Compensating): the overall timeout (owned by the Coordinator) is orthogonal
//	to step progress.
//
// Pending:      created, no step has run.
// Running:      executing steps forward; CurrentStep advances via AdvanceStep.
// Compensating: a step failed with prior committed work; undoing in reverse.
// Succeeded:    all steps committed (terminal).
// Failed:       terminal; reachable from Running (nothing to undo) or Compensating (second-order failure).
// Compensated:  forward failed, rollback completed cleanly (terminal).
// Expired:      overall timeout elapsed (terminal).
//
// # Time injection
//
// Every mutator takes an explicit now time.Time; the state machine never reads
// the wall clock. This keeps transitions pure and deterministically testable,
// and lets the Coordinator source time from a single clock. A monotonicity
// guard rejects a now that precedes StartedAt or the previous UpdatedAt.
//
// # Not in this package (deferred, with their consumers)
//
//   - Definition / Step / StepFunc / Resolver / InMemoryRegistry live in this
//     same package (kernel/saga). The Coordinator engine that executes them
//     lives in runtime/saga. (CompensateFunc + Step.Compensate were removed
//     in PR-03 round-2 and re-introduced in PR-06 alongside the compensation
//     executor.)
//   - RetryPolicy and per-step / overall timeout execution land in
//     runtime/saga/executor, alongside backoff, jitter, and the sweeper —
//     following kernel/command, which ships its Timeouts config with the
//     sweeper that consumes it, never ahead of it.
//   - The durable append-only Journal lands in kernel/saga/journal.
//   - Instance.LogValue (slog.LogValuer for redacted logging) lands in
//     runtime/saga together with the State payload it needs to redact; PR-01's
//     Instance carries only IDs / status / timestamps (no sensitive fields),
//     so the default slog reflection is safe in the interim.
//
// This skeleton declares only what its own state machine exercises: there are
// no config fields awaiting a future executor.
//
// # References
//
// ref: dtm-labs/dtm dtmsvr/storage/sql/saga_branch.go — saga branch state +
// compensation ordering.
// ref: temporalio/temporal service/history/workflow/state_rebuilder.go —
// append-only workflow state machine.
// ref: kernel/command (in-repo) — L4 state-machine template: iota+1
// zero-invalid status, map-based transition table, fail-closed Transition,
// now-injected AdvanceCommand, NewEntry + ValidateNew construction.
package saga
