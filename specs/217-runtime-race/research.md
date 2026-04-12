# Research: Runtime Race Closure

## Upstream Comparison

| Project | Files checked | Relevant pattern | Takeaway for GoCell |
| --- | --- | --- | --- |
| Uber fx | `app.go`, `internal/lifecycle/lifecycle.go`, `signal.go` | Gate new work before draining in-flight work; use explicit state instead of relying on `WaitGroup` alone | Bootstrap shutdown should use a first-class reload gate, then `select` on a drained signal before calling `asm.Stop` |
| go-zero | `core/service/servicegroup.go`, `core/threading/routinegroup.go` | Parallel worker start/stop orchestration, but no stronger first-error policy than the caller provides | GoCell already goes further by propagating cancellation through context; the worker backlog item is already implemented in current code |
| Viper | `viper.go` `WatchConfig()` | Directory-level watch, filtered event handling, explicit event-loop readiness | Keep the current directory watch approach; the missing piece is not file watching, but shutdown gating around reload callbacks |
| Kratos | `config/config.go` watcher loop | Watcher lifecycle tied to explicit stop signals | Use an explicit stop/drain primitive instead of relying on a bare `WaitGroup` window |

## Diagnosis Summary

### `runtime/eventbus`

- Current `Publish` holds `mu.RLock()` while sending to subscriber channels.
- Current `Close` holds `mu.Lock()` before closing any subscriber channel.
- That lock ordering prevents `send on closed channel` panics.
- Remaining action: make the invariant explicit and keep it protected with a concurrency regression test.

### `runtime/worker`

- `WorkerGroup.Start` already derives a child context and calls `cancel()` on first error.
- `TestWorkerGroup_CancelsSiblingsOnError` already verifies that the long-running sibling does not hang the group after a failing worker returns.
- Remaining action: treat this backlog sub-item as already closed and reflect that in planning/backlog state.

### `runtime/bootstrap`

- The reload callback still uses `assemblyStopped.Load()` plus `reloadWG.Add(1)`.
- Even with the double-check, shutdown can call `Wait()` while a callback races to `Add(1)` after observing the old state.
- This is exactly the `Add-after-Wait` misuse window called out in the backlog.
- Required fix: replace the implicit `WaitGroup` gate with an explicit reload gate that owns three things:
  - admission of new reload callbacks,
  - tracking of in-flight callbacks,
  - a drained channel for shutdown to await with `select`.

## Design Choice

Use a small internal `reloadGate` with mutex-protected state and a drained channel.

Why this is the best fit here:

- It matches the backlog guidance to prefer `channel+select` over a theoretical `WaitGroup` misuse window.
- It mirrors fx's `stop accepting -> drain active -> continue shutdown` lifecycle shape.
- It keeps the fix local to `runtime/bootstrap`, without changing public APIs or introducing new dependencies.
