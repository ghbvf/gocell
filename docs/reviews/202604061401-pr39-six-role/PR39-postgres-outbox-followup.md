# PR39 Postgres Outbox Relay Follow-up

| Field | Value |
|---|---|
| Scope | `src/adapters/postgres/outbox_relay.go`, `src/adapters/postgres/outbox_relay_test.go`, `src/kernel/outbox/outbox.go` |
| Date | `2026-04-06` |
| Status | `analysis only; not implemented in this report` |
| Basis | Current local worktree + targeted test/review discussion |

## Summary

This follow-up reviews two P1 findings around `OutboxRelay`:

1. `Stop()` can miss an in-flight asynchronous `Start()`.
2. `pollOnce()` continues publishing the rest of the batch after `markQuery` fails.

Verdict:

- The `markQuery` finding is a real correctness issue and should be fixed.
- The `Start/Stop` finding is also real under the assumption that `outbox.Relay` is used as a background worker and concurrent `Start/Stop` must be reliable.
- However, the earliest `go Start(); Stop()` window is partly an API-contract problem, not just an implementation bug.

## Finding 1: Stop Can Miss An In-flight Start

Relevant code:

- `src/adapters/postgres/outbox_relay.go:89`
- `src/adapters/postgres/outbox_relay.go:140`
- `src/adapters/postgres/outbox_relay_test.go:43`

Current behavior:

- `Start()` publishes `running/cancel/done` only after the goroutine enters the function and acquires `r.mu`.
- `Stop()` snapshots `r.cancel` and `r.done` under `r.mu`.
- If `Stop()` runs before `Start()` has published those fields, `Stop()` returns `nil`.
- The existing tests avoid that window by calling `waitForRelayRunning()`.

Assessment:

- This is a valid lifecycle gap for a worker-style contract.
- The current tests prove "`Stop()` works after `Start()` is fully visible", but they do not prove "`Stop()` is safe immediately after spawning `Start()`".
- There is one limit that should be stated clearly: if the caller only does `go relay.Start(ctx)` and the goroutine has not entered `Start()` at all yet, the object itself has no way to observe that pending call unless the API is changed or an external handshake is introduced.

## Finding 2: Keep Publishing After Mark Failure Amplifies Duplicates

Relevant code:

- `src/adapters/postgres/outbox_relay.go:241`
- `src/adapters/postgres/outbox_relay.go:261`

Current behavior:

- `pollOnce()` publishes entries one by one.
- After a successful publish, it runs:

```go
const markQuery = `UPDATE outbox_entries SET published = true, published_at = now() WHERE id = $1`
if _, err := tx.Exec(ctx, markQuery, e.ID); err != nil {
    slog.Error("outbox relay: mark published failed", ...)
}
```

- On mark failure, it only logs and continues with later entries.

Assessment:

- This is a real correctness problem.
- If the mark failure means the transaction is already unusable, later entries in the same batch can still be published successfully but will not be marked.
- That turns one DB failure into a larger duplicate replay window on the next poll.
- The code comment above `pollOnce()` says "on any failure the transaction is rolled back", but the current implementation does not actually do that for mark failures.

Recommended direction:

- Fail fast on the first `markQuery` failure.
- Return an error immediately so the deferred rollback aborts the batch.
- Do not continue publishing the tail of the batch after state persistence has already failed.

## Two Candidate Fix Plans

| Dimension | Plan A: Minimal Fix | Plan B: Full Lifecycle Refactor |
|---|---|---|
| Goal | Close the two current review findings with minimal surface area | Make relay lifecycle semantics explicit and robust |
| API impact | Minimal | Medium |
| Batch mark failure | Fail fast on first mark failure | Same |
| `Start/Stop` handling | Improve only the window after `Start()` enters the function | Explicit state machine with stronger guarantees |
| Recommended now | Yes | Only if relay will be reused broadly as a generic worker |

## Plan A: Minimal Fix

1. Change `pollOnce()` so the first `markQuery` failure returns an error immediately.
2. Let the existing deferred rollback abort the transaction.
3. Add a start-state handshake for the in-function startup window:
   - add `starting bool`
   - add `startedCh`
   - publish `starting/cancel/done/startedCh` before launching the worker goroutines
   - make `Stop()` wait when state is `starting`, instead of returning nil on unset fields
4. Add tests:
   - `TestOutboxRelay_StopWhileStarting`
   - `TestOutboxRelay_PollOnce_StopBatchOnMarkFailure`

Tradeoff:

- This fixes the real worker-state gap once `Start()` has actually begun execution.
- It still cannot fully solve the "goroutine has not entered `Start()` yet" case without extra contract changes.

## Plan B: Full Lifecycle Refactor

1. Introduce an explicit lifecycle state machine:
   - `idle`
   - `starting`
   - `running`
   - `stopping`
2. Give each run explicit coordination channels:
   - `startedCh`
   - `doneCh`
3. Make `Stop()` handle `starting/running/stopping` deterministically.
4. Consider an API change if strict `go Start(); Stop()` behavior is required:
   - `StartAsync()` returning a handle
   - or an outer launcher/worker-group handshake
5. Keep the same fail-fast batch behavior for `markQuery`.
6. Expand tests to cover:
   - start during stop
   - stop during start
   - repeated stop
   - restart after stop
   - mark failure aborting the batch tail

Tradeoff:

- This is the cleaner design.
- It costs more code and requires agreeing on lifecycle semantics instead of only patching the current implementation.

## Testing Gaps To Fill

Current tests cover:

- successful start/stop after full startup visibility
- restart after stop
- publish failure path
- stop timeout behavior

Current tests do not cover:

- immediate `Stop()` after spawning `Start()`
- batch behavior after `markQuery` failure

## Open-source References

### `nikolayk812/pgx-outbox`

Repository:

- `https://github.com/nikolayk812/pgx-outbox`

Useful point:

- `Forward()` returns immediately on publish/ack failure instead of continuing to push the rest of the batch.
- This is a good reference for "state sync failed -> stop the current forwarding cycle".

### `oagudo/outbox`

Repository:

- `https://github.com/oagudo/outbox`

Useful point:

- `Reader` uses explicit started/closed lifecycle flags, context cancellation, and a waitgroup for shutdown.
- It is a useful reference for worker shutdown structure, although it does not fully solve the "goroutine not yet entered Start" edge case either.

### `pkritiotis/go-outbox`

Repository:

- `https://github.com/pkritiotis/go-outbox`

Useful point:

- `Dispatcher` uses explicit done channels to stop background loops.
- It is a useful reference for stop-signal design when one logical component owns multiple internal workers.

## Recommendation

Recommended implementation order:

1. Apply Plan A now.
2. Specifically:
   - fail fast on first `markQuery` failure
   - tighten the `starting -> stop` handshake
   - add the missing regression tests
3. Revisit Plan B only if `OutboxRelay` is expected to become a more general background worker primitive.
