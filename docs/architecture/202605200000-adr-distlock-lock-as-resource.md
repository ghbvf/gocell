# ADR: distlock — Lock-as-Resource contract (decouple caller ctx from held lock)

- Date: 2026-05-20
- Status: Accepted
- Refs: GH #20 (DISTLOCK-RENEW-CALLER-CONTEXT-01); CLAUDE.md `.claude/rules/gocell/ai-collab.md` Hard 范本

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

`Locker.Acquire` returns a sealed `*Lock` value, intentionally NOT a `context.Context`. The caller-supplied ctx is consumed only for the acquire RPC (SetNX). Once held, the lock lifecycle is decoupled — only `Release()`, renewal failure (`ErrLockLost`), or manager shutdown ends it.

```go
type Locker interface {
    Acquire(ctx context.Context, key string, ttl time.Duration) (*Lock, error)
    Stats() Stats
}

type Lock struct { /* unexported */ }

func (l *Lock) Done() <-chan struct{}    // closes on lock-end
func (l *Lock) Cause() error              // ErrLockReleased / ErrLockLost / shutdown
func (l *Lock) Value(key any) any         // caller-ctx values (no cancellation)
func (l *Lock) Release() error            // idempotent
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

## Enforcement (AI-rebust = Hard)

Two-line defense per ai-collab.md "single sanctioned holder" Hard 范本:

| Defense | Mechanism | Where |
|---------|-----------|-------|
| **#1 type system** | `*Lock` lacks `Deadline()` / `Err()` → cannot satisfy `context.Context` | Go compiler |
| **#2 archtest** | `DISTLOCK-LOCK-NOT-CONTEXT-01` asserts `types.Implements((*Lock)(nil), context.Context) == false` | `tools/archtest/distlock_lock_not_context_test.go` |

Defense #1 is the primary line: any caller misuse fails at compile time. Defense #2 is a regression guard against future commits that add the two methods inadvertently.

## Industry survey

| Library | Caller-ctx vs held lock | Auto-release on caller cancel | Renewal context |
|---------|------------------------|-------------------------------|-----------------|
| **bsm/redislock** | decoupled; "refresh cadence is an application concern" | no | caller-supplied per `Refresh(ctx,...)` |
| **go-redsync/redsync** | decoupled; `Extend` uses `context.Background()` internally | no | independent |
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

### Alternatives considered

- **v1 plan: tighten** (bind manager renewal to caller ctx). Rejected because it fights an explicit industry consensus and embeds the same category error (request-lifecycle = lock-ownership) more deeply.
- **Provide `Lock.AsContext(parent context.Context)` escape hatch**. Rejected for v1 to keep the surface minimal and the Hard contract tight; can be added if a real use case appears.
- **Use `context.AfterFunc` for caller-ctx watching**. Same category error as v1; rejected for same reason.

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
