# Spec: Runtime Race Closure

**Branch**: `217-runtime-race`  
**Backlog Source**: `docs/backlog.md` Batch 6A `runtime 竞态修复`  
**Date**: 2026-04-13

## Problem

The Batch 6A runtime race item currently mixes three concerns under one backlog row:

1. `R1C2-F01` in `runtime/eventbus`.
2. `R1C2-F03` in `runtime/worker`.
3. `R97-R3-01` in `runtime/bootstrap`.

Current code inspection shows that the backlog row is stale in two places:

- `runtime/worker` already cancels sibling workers on first error and has a regression test.
- `runtime/eventbus` already avoids `send on closed channel` via `RWMutex` ordering, but lacks an explicit regression test and lock-order comment to close the concern cleanly.

The remaining real implementation gap is `runtime/bootstrap`: config reload shutdown still uses a `WaitGroup` pattern that allows a theoretical `Add-after-Wait` misuse window during shutdown.

## Scope

- `src/runtime/bootstrap/`: replace the reload shutdown coordination with an explicit gate that stops new reload callbacks before draining in-flight work.
- `src/runtime/bootstrap/*_test.go`: add unit and integration coverage for the new reload gate semantics.
- `src/runtime/eventbus/*`: add a concurrency regression test and document the publish/close lock invariant.
- `src/runtime/worker/*`: revalidate the existing first-error cancellation behavior and keep the backlog item accurate.
- `docs/backlog.md`: update the runtime race item to reflect the actual closure state.

## Out Of Scope

- `runtime/config` watcher redesign beyond the reload shutdown gate.
- `runtime/worker` API redesign, restart semantics, or periodic worker follow-up items.
- Retry policy changes such as eventbus jitter.
- Any unrelated backlog items in Batch 6A or 6B.

## Acceptance Criteria

1. Shutdown starts by rejecting new config reload callbacks and only then waits for in-flight reload work to finish.
2. Reload drain logic is covered by deterministic unit tests, not just timing-dependent integration behavior.
3. Concurrent `Publish` and `Close` on the in-memory event bus are covered by a regression test and do not panic under `go test -race`.
4. The worker item is explicitly revalidated against current behavior so the backlog row does not claim an unfixed bug.
5. `go test -race ./runtime/eventbus ./runtime/worker ./runtime/bootstrap` passes.
