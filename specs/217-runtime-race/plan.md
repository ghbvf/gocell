# Implementation Plan: Runtime Race Closure

**Branch**: `217-runtime-race`  
**Date**: 2026-04-13  
**Spec**: `specs/217-runtime-race/spec.md`

## Summary

Close the Batch 6A runtime race backlog row by separating stale findings from the live defect:

- `runtime/eventbus`: add proof, not a behavior rewrite.
- `runtime/worker`: confirm the bug is already fixed in current code and preserve that guarantee.
- `runtime/bootstrap`: replace the reload shutdown coordination with an explicit gate that prevents new callbacks from entering once shutdown begins.

This is a TDD-first change set. Tests will land before implementation changes.

## Root Cause

### Eventbus

The original concern came from `Close()` cancelling subscribers and closing channels while `Publish()` might still be sending. Current code already serializes those operations with `RWMutex`, so the remaining gap is documentation and regression coverage.

### WorkerGroup

The historical bug was "first worker failure does not cancel siblings". Current code already derives `groupCtx` and invokes `cancel()` on first error. The backlog entry drifted after the fix landed.

### Bootstrap reload pipeline

The reload callback uses a guard-then-`WaitGroup.Add(1)` pattern:

1. callback reads the shutdown guard,
2. shutdown starts and calls `Wait()`,
3. callback still executes `Add(1)`.

That sequence is a `WaitGroup` misuse window even if the callback exits immediately on the second guard check.

## Planned Changes

### 1. Add an internal reload gate

Introduce an unexported helper in `src/runtime/bootstrap/` that provides:

- `TryEnter() bool`: admit a reload callback only while shutdown has not started.
- `Leave()`: mark callback completion.
- `BeginShutdown() <-chan struct{}`: reject new callbacks and return a drained signal for in-flight work.

Design constraints:

- mutex-protected state,
- no `WaitGroup` for the shutdown gate,
- idempotent shutdown entry,
- deterministic tests.

### 2. Wire bootstrap to the new gate

- Replace `assemblyStopped` and `reloadWG` with the new reload gate.
- In the config watcher callback, call `TryEnter()` before any reload work and `Leave()` on exit.
- In teardown, call `BeginShutdown()` and `select` on the drained channel or teardown context before `asm.Stop(c)`.

### 3. Add regression coverage

- `runtime/bootstrap`: unit tests for the gate itself and keep the existing integration tests green.
- `runtime/eventbus`: concurrency regression test covering overlapping `Publish` and `Close`.
- `runtime/worker`: keep the current cancellation test as proof that the backlog sub-item is already closed.

### 4. Update backlog state

Update `docs/backlog.md` so the runtime race row reflects what this branch actually closes.

## Files Expected To Change

- `src/runtime/bootstrap/bootstrap.go`
- `src/runtime/bootstrap/bootstrap_test.go`
- `src/runtime/bootstrap/reload_gate.go` (new)
- `src/runtime/eventbus/eventbus.go`
- `src/runtime/eventbus/eventbus_test.go`
- `docs/backlog.md`

## Validation Plan

1. `go test ./runtime/bootstrap ./runtime/eventbus ./runtime/worker`
2. `go test -race ./runtime/eventbus ./runtime/worker ./runtime/bootstrap`
3. `go build ./...`

## Risk Notes

- The bootstrap change touches shutdown ordering; regressions would show up as hung shutdown, post-stop reloads, or dropped in-flight reloads.
- The eventbus test must avoid flaky timing and only assert invariants that the lock ordering guarantees.
- The worker item should not be "fixed again" unless a new failing case is proven; otherwise we risk reopening already-correct code.
