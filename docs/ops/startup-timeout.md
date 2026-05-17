# Startup Timeout Triage

GoCell's `Bootstrap.Run` supervises the `lifecycle.Start` phase with a
configurable time budget. If that budget elapses before all hooks return from
their `OnStart`, `Run` cancels the in-flight startup, rolls back already-started
hooks in LIFO order, and returns `bootstrap.ErrBootstrapStartupTimeout`.

This document describes how to identify the culprit hook and how to tune or
disable the budget.

## When to use this document

A pod that logs `bootstrap: lifecycle startup aborted` with
`reason=startup_budget_exceeded` near its startup timestamp, or a pod that
hangs at startup without ever reaching the running phase, is hitting the startup
backstop.

## How the backstop works

`superviseLifecycleStart` runs `lifecycle.Start(ownerCtx)` in a goroutine and
races it against two abort signals:

1. **Caller-ctx cancel** — the signal sent by the process's OS signal handler
   (SIGTERM during startup, or a test harness cancel).
2. **Startup budget timer** — fires after `WithStartupTimeout` duration
   (default `DefaultStartupTimeout` = 30s).

When either fires, `superviseLifecycleStart` calls `ownerCancel()`, which
delivers a cancellation signal to the wedged hook's `workCtx` (a child of
`ownerCtx`). It then waits for `lifecycle.Start` to unwind, **bounded by
`startupUnwindGraceTimeout` (a fixed 5s internal constant — not a tuning
knob)**:

- **The hook respects ctx** (the common case, including a hook that is merely
  slow to drain): it observes the cancellation and returns within the grace
  window. `Run` performs LIFO rollback and returns the abort error. This path
  is unchanged from before the A1-1 amendment.
- **The hook ignores ctx entirely** (never selects on `ctx.Done()`): the grace
  window elapses, the in-flight `lifecycle.Start` goroutine is **abandoned**,
  and `superviseLifecycleStart` returns anyway. `Run` rolls back and returns,
  so the process exits and the orchestrator (e.g. a Kubernetes Deployment)
  restarts it.

**Important — the irreducible residual:** Go cannot force-kill a goroutine
that ignores ctx. The abandoned `lifecycle.Start` goroutine (and the
ctx-ignoring `OnStart` it is parked in) leaks until the process exits. The
backstop does NOT eliminate that leak — no mechanism in Go can. What it *does*
guarantee is that `Run()` itself stays bounded: a ctx-ignoring hook can no
longer wedge the orchestration layer forever (the pre-amendment behaviour was
a bare unbounded `<-startErr`). A ctx-ignoring `OnStart` is still a bug in the
offending hook (all `OnStart` implementations MUST respect ctx); the backstop
converts it from "silent permanent hang" into "logged abort + clean process
exit + orchestrator restart", not into a substitute for ctx-aware hooks.

When the grace window elapses on a ctx-ignoring hook, the framework logs an
additional structured line:

```
ERROR bootstrap: lifecycle start goroutine abandoned
  reason=onstart_ignored_ctx
  unwind_grace=5s
  hint="the last hook.start Info log line identifies the ctx-ignoring hook; ..."
```

and the returned error is `ErrBootstrapStartupTimeout` joined with an explicit
`lifecycle start goroutine abandoned after 5s unwind grace (OnStart ignored
ctx)` wrap.

This backstop fires **before** `Run()` enters its main serving loop, so it is
completely orthogonal to `WithShutdownTimeout` / `terminationGracePeriodSeconds`
(which govern the teardown phase). The `2 × shutdownTimeout + 10s` grace
formula in `docs/ops/graceful-shutdown-k8s.md` is unaffected.

## Identifying the culprit hook

When the budget fires, the framework logs two structured lines:

```
ERROR bootstrap: lifecycle startup aborted
  reason=startup_budget_exceeded
  budget=30s
  hint="the last hook.start Info log line identifies the in-flight hook"
```

Search the pod's startup log (from process start to the error line) for
`hook.start` at `INFO` level. The last `hook.start` line before the
`lifecycle startup aborted` error identifies the hook whose `OnStart` did not
return. The hook name appears as the `name` structured field:

```
INFO hook.start  name=devicecell.sweeper  cell=devicecell
```

After identifying the hook, check whether its `OnStart` implementation:

- Calls a blocking operation (DB migration, TCP dial, external API) without a
  context-aware timeout.
- Ignores the ctx parameter entirely.
- Uses a frozen fake clock (test-only; the 50 ms startup probe in
  `SweeperLifecycle` uses real time, so frozen fake clocks in production are
  not a concern).

## Tuning

`WithStartupTimeout` controls the whole-Start orchestration budget:

```go
bootstrap.New(
    // Default 30s applies when WithStartupTimeout is omitted.
    bootstrap.WithStartupTimeout(60 * time.Second), // extend for slow infra
    // ...
)
```

| Value | Behaviour |
|-------|-----------|
| 0 (omitted) | Uses `bootstrap.DefaultStartupTimeout` (30s) |
| positive duration | Budget fires after that duration |
| negative duration | Timer disabled; caller-ctx cancel is the only abort path |

Use a negative value only if the deployment environment guarantees that a
SIGTERM (or equivalent caller-ctx cancel) will always arrive to unblock a
wedged startup — for example, in a Kubernetes pod with a short
`terminationGracePeriodSeconds` that can be relied on to send SIGTERM during
slow starts.

`StartTimeout` on individual hooks is a separate, informational field — it
controls the `hook.start_slow` warning threshold, not the deadlock backstop.
Do not confuse the two.

## Relationship to shutdown timeout

The startup backstop (`WithStartupTimeout`, `DefaultStartupTimeout`) fires
pre-Run, before the process enters its serving loop. It has no effect on the
shutdown phase. The existing `terminationGracePeriodSeconds >= 2 ×
shutdownTimeout + 10s` formula documented in `docs/ops/graceful-shutdown-k8s.md`
remains unchanged and does not need to account for startup budget time.

## Related documents

- `docs/ops/graceful-shutdown-k8s.md` — shutdown budget formula and K8s grace period
- `docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md` §D-B — A1-1 backstop design decision
- `runtime/bootstrap/lifecycle.go` — `ErrBootstrapStartupTimeout`, `DefaultStartupTimeout`
- `runtime/bootstrap/options_lifecycle.go` — `WithStartupTimeout`
