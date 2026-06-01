# ADR: distlock — Lock-as-Resource contract (decouple caller ctx from held lock)

- Date: 2026-05-20
- Status: Accepted
- Refs: GH #20 (DISTLOCK-RENEW-CALLER-CONTEXT-01); CLAUDE.md `.claude/rules/gocell/ai-robust.md` Hard 范本

## Context

`runtime/distlock.Locker.Acquire` previously returned `(context.Context, func() error, error)` where the returned `lockCtx` was derived from the caller-supplied ctx via `context.WithCancelCause(ctx)`. This pulled three semantics into one wire:

1. **Lock-end signal** — manager cancels `lockCtx` with `ErrLockReleased` / `ErrLockLost` / shutdown cause.
2. **Caller-ctx cancellation propagation** — parent cancel auto-canceled `lockCtx`.
3. **Value & deadline propagation** — `lockCtx.Value` / `lockCtx.Deadline` reflected the caller ctx.

But the manager's renewal loop ignored caller ctx entirely (it used `context.Background()` for the `Driver.Renew` deadline). When a caller's ctx was cancelled but `release()` was forgotten:

- `lockCtx` saw `parent ctx cancelled` and closed `Done()` — caller's business path saw the lock as ended.
- Manager continued scheduling renewals, holding the Redis key live indefinitely.

Other workers were blocked by the abandoned-but-still-renewed lock. GH #20 described this as a P1 correctness defect.

Two repair directions were considered:

1. **Tighten** — bind manager renewal lifecycle to caller ctx (the v1 plan). Caller cancel → both `lockCtx` end AND manager removes lock.
2. **Decouple** — move caller ctx out of held-lock lifecycle entirely. Caller cancel does nothing to a held lock; only explicit `Release` / renewal failure / shutdown ends it.

Industry survey (recorded in this ADR §"Industry survey") showed five out of five popular distributed-lock libraries take direction 2. The decisive argument: caller-ctx cancellation expresses "I no longer care about the outcome of THIS request", not "release the resource I acquired". Conflating the two — what gocell's old contract did — is a category error.

## Decision

`Locker.Acquire` returns a sealed `*Lock` value, intentionally NOT a `context.Context`. The caller-supplied ctx is consumed only for the acquire RPC (SetNX). Once held, the lock lifecycle is decoupled — only `Release()`, `Orphan()` (added in §Amendment 2026-05-31), or renewal failure (`ErrLockLost`) ends it. Forced *locker-level* shutdown is deferred to a follow-up — see §"Out of scope".

```go
type Locker interface {
    Acquire(ctx context.Context, key string, ttl time.Duration) (*Lock, error)
    Stats() Stats
}

type Lock struct { /* unexported */ }

func (l *Lock) Done() <-chan struct{}    // closes on lock-end
func (l *Lock) Cause() error              // ErrLockReleased / ErrLockOrphaned / ErrLockLost
func (l *Lock) Value(key any) any         // caller-ctx values (no cancellation)
func (l *Lock) Release() error            // idempotent
func (l *Lock) Orphan()                   // idempotent; see §Amendment 2026-05-31
```

Caller MUST `defer lock.Release()`. Process crash falls back to Redis TTL. Living caller that forgets `Release` leaks the lock until process exit (mirroring bsm/redislock and redsync behavior).

### Why `*Lock` is not `context.Context`

`*Lock` intentionally omits `Deadline() (time.Time, bool)` and `Err() error`. Without those methods, the Go type system refuses any call site that requires `context.Context`:

```go
db.QueryContext(lock, ...)                         // COMPILE ERROR — type mismatch
http.NewRequestWithContext(lock, ...)              // COMPILE ERROR
outbox.Emit(lock, ...)                             // COMPILE ERROR
```

Callers cannot accidentally pass a held lock to a downstream RPC and assume caller-ctx semantics. This is the misuse pattern that GH #20 exposed and that this decoupling closes off permanently.

`Lock.Value(key)` is preserved via `context.WithoutCancel(callerCtx)` stored inside the lock, so trace IDs and auth claims remain accessible without re-plumbing.

## Enforcement (two-layer split — downstream Hard + upstream Medium)

Per ai-robust.md "Funnel 双向锁评级", funnel claims must be evaluated for both directions:

| Direction | Mechanism | AI-robust |
|-----------|-----------|-----------|
| **Downstream** (caller cannot pass `*Lock` where `context.Context` is required) | Go type system — `*Lock` lacks `Deadline()` / `Err()`, so calls like `db.QueryContext(lock, ...)` fail at compile time | **Hard** — violation literally not representable in source |
| **Upstream** (implementer cannot add `Deadline()` / `Err()` methods to `*Lock` and accidentally satisfy `context.Context`) | archtest `DISTLOCK-LOCK-NOT-CONTEXT-01` static check via `typesutil.ImplementsInterface` + blind-spot reverse self-check | **Medium** — package-internal mutation is detectable only by CI archtest; no sealed marker makes the mutation impossible at compile time |

Downstream is the primary line (Hard, type-system). Upstream is a regression guard against future commits that mutate `*Lock`'s method set. The combined posture is **Hard downstream + Medium upstream** — the highest practically achievable for this shape (the alternative would require a sealed-interface wrapper that hurts ergonomics for marginal benefit, since the upstream attack surface is package-internal and CI-gated).

A blind-spot reverse self-check (`TestDistlockLockNotContext01_BlindSpotSelfCheck`) constructs an in-memory fixture type that DOES implement context.Context and asserts the detector reports it — guarding against the failure mode "the forward check returns false trivially because the implementation regressed".

## Industry survey

| Library | Caller-ctx vs held lock | Auto-release on caller cancel | Renewal context |
|---------|------------------------|-------------------------------|-----------------|
| **bsm/redislock** | decoupled; "refresh cadence is an application concern" | no | caller-supplied per `Refresh(ctx,...)` |
| **go-redsync/redsync** | decoupled; caller-ctx scopes the acquire RPC only (`LockContext`); renewal (`Extend`/`ExtendContext`) is application-driven and accepts a per-call ctx, not a session-wide one | no | per-`Extend` caller-supplied ctx |
| **etcd `client/v3/concurrency`** | session-scoped keepalive; `Lock(ctx)` ctx scopes acquire only | no | session ctx, not per-op |
| **HashiCorp consul/api** | decoupled; `stopCh` scopes acquisition only | no | `RenewPeriodic` runs until `Unlock` |
| **Apache Curator** | decoupled; ZK ephemeral nodes + heartbeat sessions | no (session-based) | session heartbeat, not per-op |

Common pattern: caller-supplied context controls the **acquisition RPC**; once the lock is acquired, lifecycle decouples and ends only via explicit Release/Unlock or session/TTL expiry.

References:
- bsm/redislock README; redislock.go `Refresh`
- go-redsync/redsync `mutex.go` `Extend`/`ExtendContext`
- etcd-io/etcd `client/v3/concurrency/{mutex,session}.go`
- hashicorp/consul `api/lock.go`
- Apache Curator `InterProcessMutex`; Tech Note 10

## Consequences

### Positive

- Caller misuse of `*Lock` as a `context.Context` is compile-time impossible.
- Renewal semantics match prevailing industry convention — easier for future contributors to reason about.
- Archtest maintenance burden minimal — `types.Implements` check is one assertion, no AST pattern matching.

### Negative / accepted

- Caller MUST `defer lock.Release()`. Forgetting it leaks the lock until process exit. Mitigation: godoc + ADR + standard `defer` idiom. Identical risk profile to bsm/redislock and redsync — accepted industry trade-off.
- No safety net option (e.g. `WithMaxLockAge`) in this iteration. May add later if a real caller demonstrates the need; deferred to keep the minimal surface.
- API breaking: 36 test callsites updated; no production callers existed (GH backlog explicitly noted "首个生产 distlock caller 前触发" / "before first production distlock caller").
- **callerCtx values lifetime extension**: `Lock.Value` requires `Lock` to retain `context.WithoutCancel(callerCtx)` for the held duration. Every value reachable from callerCtx is pinned for the full lock lifetime — auto-renewals included. Mitigation: godoc warns callers to keep callerCtx values small/immutable (trace IDs, auth claims, span contexts) and to parameterize tokens / PII / large buffers explicitly. Concrete blast radius: at most one held lock per call site; values are still GC-eligible after `lock.Release()` returns.
- **fail-stays-held DoS surface**: a buggy caller (or a malicious actor with API access) can Acquire and then drop the lock handle without Release, blocking peers for the full TTL window. Blast radius: same key only, not framework-wide. Mitigations: (a) TTL is the only ceiling — choose seconds-to-minutes scale matching critical-section worst case (godoc states this); (b) process crash falls back to Redis TTL expiry; (c) `WithMaxLockAge` deferred to v2 if a real workload demands it. This trade-off is identical to bsm/redislock, redsync, etcd `concurrency`, consul, Curator — accepted industry posture.

### Alternatives considered

- **v1 plan: tighten** (bind manager renewal to caller ctx). Rejected because it fights an explicit industry consensus and embeds the same category error (request-lifecycle = lock-ownership) more deeply.
- **Provide `Lock.AsContext(parent context.Context)` escape hatch**. Rejected for v1 to keep the surface minimal and the Hard contract tight; can be added if a real use case appears.
- **Use `context.AfterFunc` for caller-ctx watching**. Same category error as v1; rejected for same reason.

## Out of scope (deferred)

- **`Lock.AsContext(parent context.Context)` escape hatch.** Rejected for v1
  to keep the surface minimal and the Hard contract tight; can be added if a
  real use case appears. See §"Alternatives considered".
- **`WithMaxLockAge` safety net** for forgotten `Release()`. May add later if a
  real caller demonstrates the need; deferred to keep the minimal surface. See
  §"Consequences / Negative".
- **Explicit `Locker.Shutdown()` / `Close()` entry point.** Deferred — but the
  reason is *no caller-relevant work for it to do today*, not the absence of an
  integrator (the original "open when first ManagedResource integrator arrives"
  framing was imprecise; corrected here per the 2026-05-31 review).

  Verified facts at 2026-05-31 (grep over the tree, not assertion):
  - **A `Locker` lifecycle integrator already exists** — `cmd/corebundle`'s saga
    module is where a `distlock.Locker` would be constructed and a saga
    `Coordinator.Stop()` is wired. So "no integrator" was never the real blocker.
  - **There is no hard blocker** to adding `Close()`. The manager's `runOnce` /
    `markCause` plumbing already supports a third lock-end cause; adding the
    method is mechanically straightforward.
  - **What makes it dead code *today*** is twofold: (1) there are **zero
    production `distlock.New(...)` construction sites and zero `WithLeaderElect`
    callers** — saga leader-election has not been wired into a production bundle
    yet (the only `distlock.New` / `WithLeaderElect` references are tests and the
    `adapters/redis` doc example); and (2) the `Manager` **self-drains**: once
    every lock reaches a terminal disposition (`Release` or `Orphan`) the
    `pendingReleases` counter hits zero, `Drained()` closes, and the manager
    goroutine exits — so there is no leaked goroutine for a process-wide
    `Close()` to reclaim. A `Close()` shipped now would have no live caller and
    no resource to free → unreachable code, which violates the "dead code = lie"
    principle this ADR holds.

  When saga leader-election is wired into a production bundle (a real
  `distlock.New` + `WithLeaderElect` callsite), `Close() error` becomes
  caller-relevant — as a backstop for the case where `Coordinator.Stop()` is
  skipped/panics, or where a future second consumer shares one `Locker`. At that
  point add `Close()` (force-orphan all residual locks → stop manager → **no**
  release I/O, consistent with the Orphan philosophy that shutdown must not
  depend on backend reachability), register the `Locker` as a
  `bootstrap.WithManagedCloser`, and add shutdown-semantics tests. Tracked as
  backlog `DISTLOCK-LOCKER-SHUTDOWN-01`; **trigger = saga enters production**,
  not "an integrator appears" (it already has).

  **Still deferred after the 2026-05-31 amendment** — that amendment added a
  *per-lock* `Orphan()` (which has a real production caller, `Coordinator.Stop`),
  not the *locker-level* shutdown this bullet describes; `DISTLOCK-LOCKER-SHUTDOWN-01`
  stays open.

### Amendment 2026-05-31 — per-lock `Orphan()` (issue #1116)

A real production caller arrived: `runtime/saga.Coordinator.Stop()` needs to
let go of in-flight per-instance distlocks during graceful shutdown **without**
a release round-trip that could hang or fail when the backend is unreachable at
shutdown time (PR #1108 mitigated this at the consumer layer with
release-on-cancel; this amendment moves the capability into the primitive).

This is a *per-lock* disposition, distinct from the still-deferred
*locker-level* `Shutdown()/Close()` above. It is the bounded-handoff member of
the same family etcd `client/v3/concurrency` exposes:

| GoCell | etcd equivalent | Semantics | Backend key |
|--------|-----------------|-----------|-------------|
| `Lock.Release()` | `Session.Close()` | end critical section now | deleted immediately (Driver.Release I/O) |
| `Lock.Orphan()` (new) | `Session.Orphan()` | stop renewal, hand off | left to expire on its lease TTL — ~1×TTL from the last successful renewal (no I/O) |

Decision:

- Add `func (l *Lock) Orphan()` — stops lease renewal, sets
  `Cause() == ErrLockOrphaned`, closes `Done()`, performs **no** `Driver`
  call. The backend key expires on its lease TTL, handing the lock to a
  competitor. The bound is best-effort: Orphan stops *future* renewals
  immediately and cancels any in-flight renewal, but a renewal whose write
  already reached the backend may extend the lease one more TTL window from its
  commit. The takeover bound is therefore ~1×TTL from the last successful
  renewal (not a hard cap measured from the Orphan call).
- `Orphan()` and `Release()` are **mutually exclusive and idempotent**: both
  closures in `Acquire` share one `sync.Once`, so the first call of either
  wins and any later call of either is a no-op. "Double disposition" /
  "orphan-then-release deletes the key" is therefore not representable
  (structural Hard, no archtest needed). This also makes saga's "Stop orphans,
  the owning tick later calls release() which degrades to a no-op" correct by
  construction.
- `ErrLockOrphaned` uses `KindInternal` (mirroring `ErrLockReleased`): a
  deliberate local disposition that, if it ever surfaced to an HTTP handler,
  would be a server-side bug — fail-closed 500, never a misleading 409.
- The `Locker` interface is unchanged (the method lives on `*Lock`); the
  locker-level `Shutdown/Close` remains out of scope.

Enforcement: the "Orphan performs zero backend I/O" invariant — its defining
property — is guarded by archtest `DISTLOCK-ORPHAN-NO-DRIVER-IO-01` (Medium;
Go ceiling — `Manager.driver` is reachable from any method, so a type-system
seal is not available; type-aware callee resolution scoped to `handleOrphan`
is the maximum). The mutual-exclusion/idempotence invariant above is
structural Hard (shared `sync.Once`).

#### Threat-model / consequences re-evaluation (per ai-robust.md "ADR amendment 落地必查")

The two §Consequences risk rows below are re-evaluated against `Orphan()`; both stay ✅ (no cell flips to ⚠️/❌). One new **availability trade-off** is added for completeness — it is not a security row and does not flip any existing ✅.

- **fail-stays-held DoS surface** — ✅ unchanged. `Orphan()` does not widen the
  ceiling: an orphaned key is bounded by the *same* TTL window as a
  crash-fallback or a forgotten `Release()`. It strictly cannot hold longer
  than the existing worst case (it stops renewal, so the key can only expire
  *sooner* than a still-renewed lock). Same-key blast radius, not
  framework-wide — identical posture to the original row.
- **callerCtx values lifetime extension** — ✅ unchanged. `Orphan()` ends the
  lock (closes `Done()`, the manager drops `lockState`), so captured callerCtx
  values become GC-eligible exactly as they do after `Release()`. No new
  pinning window.
- **saga Stop liveness trade-off (new, availability only)** — after
  `runtime/saga.Coordinator.Stop()`, orphaned per-instance distlock keys linger
  in the backend for up to `Config.LeaseDuration` before expiring (vs the
  previous immediate-release behavior). A coordinator restarting within that
  window will skip those instances until the lease lapses. This is a **liveness
  cost, not a safety degradation**: no existing ✅ row flips; the journal
  `lease_id` CAS continues to fence any late commits from the orphaned step. The
  trade-off is deliberate — I/O-free shutdown cannot hang even when the backend
  is unreachable at shutdown time.

`*Lock` still does not implement `context.Context` (Orphan adds no
`Deadline()/Err()`), so `DISTLOCK-LOCK-NOT-CONTEXT-01` and the Enforcement
table above are unaffected.

## Implementation notes

- `*Lock` stores caller-ctx values via `context.WithoutCancel(ctx)` (Go 1.21+). Values pass through `Lock.Value`; cancellation and deadline do not.
- `Lock.Cause` is set via `atomic.Value` BEFORE `done` channel is closed, providing happens-before for readers that observe `<-Done()`.
- `markCause` is protected by `sync.Once` for the release-vs-lost race.
- `state.lock` (in `manager.go lockState`) replaces the previous `state.cancel context.CancelCauseFunc`. Manager handlers call `state.lock.markCause(...)` instead of `state.cancel(...)`.

## Validation

- `go build ./...` — clean
- `go test -race -count=3 ./runtime/distlock/...` — pass
- `go test ./adapters/redis/...` (short) — pass
- `go test ./tools/archtest/ -run TestDistlockLockNotContext01` — pass

The new test cases `TC5_HeldThroughCancel` and `TC5_ValuesPropagateButCancelDoesNot` directly validate the decoupling: parent cancel does not affect a held lock; values still propagate.
