# R1D-2: adapters/redis Architecture Review

| Field | Value |
|---|---|
| Role | S1 Architecture |
| Scope | `src/adapters/redis/` |
| Baseline commit | `5096d4f` |
| Evidence base | Current source and tests only |

## Summary

The package splits cleanly into four capabilities: `Client`, `DistLock`, `IdempotencyChecker`, and `Cache`. The dependency direction is mostly good: the adapter depends only on `pkg/errcode`, `kernel/idempotency`, and `go-redis`. The main architectural weakness is that only `IdempotencyChecker` is anchored to a kernel contract; the rest of the exported surface is concrete utility code with underspecified lifecycle and semantics.

## Findings

### A-01 | P1 | `DistLock` renewal lifecycle is detached from the caller lifecycle

- File: `src/adapters/redis/distlock.go:94-126`
- Evidence: `Acquire()` says renewal runs until `Release()` or caller context cancellation, but the goroutine is derived from `context.Background()` instead of the incoming `ctx`.
- Why it matters: the adapter boundary promises lifecycle composition with the caller, but the implementation creates a hidden background process. That makes `DistLock` hard to reason about inside request-scoped or shutdown-scoped orchestration.
- Recommendation: derive renewal from `context.WithCancel(ctx)` and keep the cancel function on the returned lock.

### A-02 | P2 | only `IdempotencyChecker` has a stable interface boundary

- Files: `src/adapters/redis/idempotency.go:14-25`, `src/adapters/redis/cache.go:14-21`, `src/adapters/redis/distlock.go:64-72`
- Evidence: `IdempotencyChecker` has a compile-time assertion against `kernel/idempotency.Checker`; `Cache` and `DistLock` are exported concrete types with no corresponding kernel/runtime interface.
- Additional observation: package-level search in current `src/` shows production code consumes Redis through the idempotency interface, while `DistLock` and `Cache` have no production callsites.
- Why it matters: concrete exported adapters without a contract tend to accrete unstable behavior. They are hard to substitute, hard to mock outside the package, and easy to over-advertise.
- Recommendation: either define minimal consumer-facing interfaces where these capabilities are truly part of the platform, or keep them explicitly as leaf utilities with narrow documentation and no architectural guarantees.

## Verdict

Architecture is acceptable for `IdempotencyChecker`, but not yet for `DistLock`. The module is not structurally broken, yet the lock lifecycle contract needs correction before this package can be treated as a dependable infrastructure primitive.
