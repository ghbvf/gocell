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
//	   │           ├──► Compensating ──► Compensated          (rollback succeeded)
//	   │           │           └──────► CompensationFailed    (rollback failed)
//	   │           └──► Failed                                (failed before rollback)
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
// Failed:             terminal; forward failure before rollback is entered.
// Compensated:        forward failed, rollback completed cleanly (terminal).
// Expired:            overall timeout elapsed (terminal).
// CompensationFailed: terminal; rollback itself failed and needs ops intervention.
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
//   - Definition / Step / StepFunc / CompensateFunc / RetryPolicy / Resolver /
//     InMemoryRegistry live in this same package (kernel/saga). The Coordinator
//     engine and the per-step Executor that execute them live in runtime/saga
//     and runtime/saga/executor.
//   - The backoff / jitter algorithm that consumes RetryPolicy, the per-step
//     context.WithTimeout enforcement, and the heartbeat goroutine live in
//     runtime/saga/executor; this package carries only the static config
//     (RetryPolicy value + Step.Timeout / Definition.Timeout).
//   - The durable append-only Journal lands in kernel/saga/journal.
//   - Instance.LogValue (slog.LogValuer for redacted logging) lands in
//     runtime/saga together with the State payload it needs to redact;
//     Instance carries only IDs / status / timestamps (no sensitive fields),
//     so the default slog reflection is safe in the interim.
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
