# ADR — After-Commit Hook: Narrow Transient-Only Scope

- **Date**: 2026-05-23
- **Status**: Accepted (shipped via fix/202-aftercommit-hook)
- **Roadmap**: 046 Saga/L3 §PR-00; 004 capability-gap 缺口 5 / 005 W1 (AfterCommit hook prerequisite for the Saga Coordinator dispatcher kick)
- **Enforcement**: archtest `AFTERCOMMIT-HOOK-PURE-TRANSIENT-01`; conformance `kernel/persistence/persistencetest.RunAfterCommitConformance`

## Context

The Saga Coordinator (046 PR-03) must, after the transaction that appends a
journal event + emits an outbox row commits, *kick* its dispatcher loop so the
next step is scheduled promptly. That kick cannot live inside the transaction
body: if the tx rolls back, a kick already sent would schedule work for a step
that never happened. The same shape recurs for cache invalidation, metrics
flush, and websocket broadcast — side effects that must observe a **durable**
commit but must not be part of it.

Before this change `kernel/persistence` had **no** post-commit / deferred
callback facility (grep-confirmed across the repo). This ADR adds the primitive.
It is deliberately the smallest thing that unblocks Saga, with the abuse vector
(stateful side effects sneaking back in) closed by enforcement up front.

## Decision

### D1 — Registry-in-context, not a second interface method

`TxRunner.RunInTx` signature is unchanged. Hooks are scheduled from within a tx
fn body via a free function:

```go
persistence.RegisterAfterCommit(ctx, func(ctx context.Context) { dispatcher.Kick() })
```

The TxRunner installs an after-commit registry into the ctx and drains it after
the outermost commit. **Rejected**: adding a parallel `RunInTxAfterCommit(ctx,
fn, hooks...)` interface method (two methods doing nearly the same thing; weaker
funnel — every call site becomes a hook injection point) and widening `RunInTx`
to a variadic. The registry model mirrors the benchmark (§Benchmark), keeps the
interface single, and gives the archtest one free-function call site to scan.

### D2 — Synchronous, on the committing goroutine

`RunAfterCommitHooks` runs hooks in a plain loop after commit, before `RunInTx`
returns. **Rejected**: async goroutine dispatch (un-recovered panic crashes the
process; racy tests; ordering loss). Hooks are cheap transient ops, so inline is
cheaper than a goroutine and trivially testable.

### D3 — Outermost-commit only

`WithAfterCommitRegistry` returns `installed=false` when a registry is already
present (nested `RunInTx` / savepoint), so only the outermost installer drains.
A savepoint RELEASE never fires hooks — firing before the top-level commit would
break the durability premise. The postgres savepoint path neither installs nor
drains.

### D4 — Fail-fast outside a transaction

`RegisterAfterCommit` with no ambient registry panics through
`panicregister.Approved` (programmer error). Scheduling a post-commit effect with
no commit to follow would silently drop it. (spring-tx's `registerSynchronization`
likewise throws `IllegalStateException`.)

### D5 — Best-effort failure: swallow + log + per-hook recover

Each hook runs under its own `recover`; a panic is logged at `slog.Error` (hook
index + redacted payload) and never propagated. The commit is already durable, so
a transient-hook failure must not surface as a request error or trigger a
rollback. This is a **deliberate deviation from spring-tx** — see §Benchmark.

### D6 — Structural tx unreachability

`RunAfterCommitHooks` passes each hook a ctx with `persistence.TxCtxKey` stripped
and detached from cancelation (`context.WithoutCancel`). The committed tx is
therefore **unreachable through the hook's ctx** — not merely archtest-banned.
Trace / request-id keys live under different keys and survive.

## Benchmark (spring-tx, real source @main)

`TransactionSynchronizationManager` / `TransactionSynchronization` /
`AbstractPlatformTransactionManager` / `TransactionSynchronizationUtils`:

| Dimension | spring-tx | GoCell |
|---|---|---|
| registration carrier | thread-local `Set<TransactionSynchronization>` | ctx-carried `[]AfterCommitHook` (goroutine-scoped) — **adopt** |
| execution | `invokeAfterCommit` plain for-loop, committing thread | synchronous, committing goroutine — **adopt** |
| ordering vs commit | `doCommit` → `triggerAfterCommit` → cleanup | `tx.Commit` → drain → return — **adopt** |
| nesting | `isNewSynchronization()` guard; savepoint release does not fire | outermost-only via `installed` flag — **adopt** |
| no active tx | `registerSynchronization` throws `IllegalStateException` | panic fail-fast — **adopt** |
| re-register during drain | snapshot; silently ignored (issue #26384) | `draining` flag ignores re-registration — **adopt** |
| **failure** | `afterCommit()` **propagates** (no try-catch) | swallow + slog.Error + per-hook recover — **deliberate deviation** |

**Deviation rationale (D5)**: spring propagates so a stateful framework can show
the user an error. GoCell hooks are best-effort transient actions; the commit is
durable and a hook failure must not become a request failure or rollback.
Compensations: (a) per-hook recover so one failure doesn't stop the rest; (b)
`slog.Error` with hook identity for traceability; (c) the `AfterCommitHook`
godoc states best-effort explicitly. A future "run on commit *and* rollback"
cleanup hook would be a separate `RegisterAfterCompletion` (mirroring spring),
not a mode flag on this one.

> Eventuate Tram exposes no application-layer after-commit primitive (pure
> transactional outbox), confirming this primitive's scope is *post-outbox*
> transient actions, not an outbox replacement.

## Threat Model

| # | Threat | Mitigation | Residual |
|---|---|---|---|
| T1 | Hook performs a persistent side effect (writes the committed tx / outbox) reintroducing the anti-pattern outbox exists to kill | archtest A2 (typed receiver ban) + D6 structural tx strip | ✅ direct + 1-level helper; ⚠️ deeper helper chains / captured-txCtx (Medium, see AI-robust) |
| T2 | Drain invoked outside a TxRunner, firing hooks with no commit | archtest A3 caller allowlist | ⚠️ upstream Medium (no type seal); gh issue #920 AFTERCOMMIT-DRAIN-CALLER-SEAL |
| T3 | Hook scheduled with no transaction → silently dropped | D4 fail-fast panic | ✅ |
| T4 | Nested savepoint fires hooks before durability | D3 outermost-only | ✅ |
| T5 | Panicking hook crashes process / aborts commit | D2 sync + D5 per-hook recover | ✅ |
| T6 | Hook leaks tx beyond commit and corrupts a reused connection | D6 ctx strip + T1 controls | ✅ for ctx path; ⚠️ captured-txCtx residual = T1 |

## AI-robust ratings (honest, not over-claimed)

- **A1 — Hard**: `(callee=RegisterAfterCommit, arg=FuncLit)` form uniqueness. Highest achievable for an archtest-bound funnel; constructively closes blind spot B1.
- **A2 — Medium**: closure purity is inter-procedurally undecidable. Single-body typed scan + one-level helper heuristic catch the common cases; a >1-level helper chain or a captured enclosing `txCtx` can still reach the tx. **There is no low-cost path to Hard** — A1's "func literal at the call site" requirement places the literal in the scope where `txCtx` is visible, so "inline-inspectable" and "no tx access" are structurally in tension. The floor is raised by D6 (structural strip), not by claiming a seal that does not exist. Blind spots B2/B3/B4 each have a reverse self-test. No upgrade issue is opened because none is reachable; the godoc records the terminal Medium + reason.
- **A3 — Hard downstream / Medium upstream**: callsite identity is locked to the 5-runner allowlist (downstream Hard). No type-level seal makes the call unrepresentable elsewhere, because the 5 runners span cells + examples + adapters + kernel and share no `internal/` boundary. Upstream Hard-seal tracked in gh issue #920 (AFTERCOMMIT-DRAIN-CALLER-SEAL).

## Consequences

- Saga (046 PR-03) can kick its dispatcher after a durable commit without
  embedding the kick in the tx.
- Every `TxRunner` implementation (postgres, outbox demo, accesscore mem,
  configcore noop, todoorder demo) installs + drains the registry; new runners
  must pass `RunAfterCommitConformance` (contract-fanout 载体 2+3).
- A per-hook failure **counter** (`aftercommit_hook_failures_total`) is *not*
  added in this PR; v1 relies on `slog.Error`. Tracked in gh issue #921
  (AFTERCOMMIT-HOOK-FAILURE-METRIC).

ref: spring-framework `spring-tx/.../TransactionSynchronizationManager.java`,
`AbstractPlatformTransactionManager.java`, `TransactionSynchronization.java`,
`TransactionSynchronizationUtils.java` (@main); adapted to ctx-scoped Go registry.
