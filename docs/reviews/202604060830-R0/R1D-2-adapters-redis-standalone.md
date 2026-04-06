# R1D-2: adapters/redis Standalone Review

| Field | Value |
|---|---|
| Reviewer | R1D-2 (six-seat standalone review) |
| Scope | `src/adapters/redis/` |
| Baseline commit | `5096d4f` |
| Date | `2026-04-06` |
| Evidence base | Current source code and tests only |
| Verification | `go test ./adapters/redis`, `go test -cover ./adapters/redis`, `go test ./kernel/idempotency` |

---

## Summary

`adapters/redis` currently has four source components: `Client`, `DistLock`, `IdempotencyChecker`, and `Cache`. The idempotency critical path is materially better than a naive GET+SET split because `TryProcess()` exists in the adapter and the RabbitMQ consumer path uses it. Basic unit coverage is good (`go test -cover ./adapters/redis` reports 81.4%).

The remaining risk is concentrated in `DistLock`: the public API still overstates the safety of the lock, renewal is not actually bound to the caller context, and TTL validation is incomplete. I found 6 open items: 1 P0, 3 P1, 2 P2.

---

## Findings

### F-01 [S2 Security, S6 Product] P0 -- `DistLock` still cannot fence stale lock holders

**Files:** `src/adapters/redis/distlock.go:35-41`, `src/adapters/redis/distlock.go:90-132`, `src/adapters/redis/doc.go:5`

**Evidence:** `Acquire()` returns only `*Lock`, whose state is `{key, value, cancel}`. The random `value` is used only inside Redis release/renew scripts and is never exposed as a monotonic fencing token that downstream storage can validate.

```go
type Lock struct {
    rdb    cmdable
    key    string
    value  string
    cancel context.CancelFunc
}
```

**Why this matters:** if holder A stalls past TTL, the key can expire and holder B can acquire the same lock. The Lua ownership check prevents A from deleting B's lock later, but it does not prevent A from continuing its business-side effects after B has already entered the critical section. That means the adapter does not provide strong mutual exclusion for write paths that can corrupt state.

**Recommendation:** choose one contract explicitly:
- Narrow the contract: document this as a best-effort single-node lease, not safe for strong-consistency critical sections.
- Or strengthen the contract: return a fencing token and require downstream writers to reject stale tokens.

---

### F-02 [S1 Architecture, S4 Reliability] P1 -- renewal loop ignores caller `ctx` and violates the method contract

**File:** `src/adapters/redis/distlock.go:94-126`

**Evidence:** the `Acquire()` doc says renewal continues until `Release` is called or the context is cancelled, but the implementation creates the renew context from `context.Background()` instead of the caller's context.

```go
// The returned Lock starts a background renewal goroutine that extends the TTL
// at half the lock period until Release is called or the context is cancelled.

renewCtx, cancel := context.WithCancel(context.Background())
go d.renewLoop(renewCtx, lock, ttl)
```

**Impact:** cancelling the caller context does not stop renewal. A timed-out request, aborted workflow, or shutdown path can leave the lock renewing in the background as long as the process stays alive and `Release()` is never reached. This is both a semantic bug and a reliability bug.

**Recommendation:** derive renewal from the caller context, for example `context.WithCancel(ctx)`, and make the `Lock` responsible for stopping only its child context. Add a regression test that acquires with a cancellable context, cancels it, waits longer than TTL, and verifies another caller can acquire.

---

### F-03 [S2 Security, S4 Reliability] P1 -- `Acquire()` accepts invalid TTL values and can panic in `time.NewTicker`

**File:** `src/adapters/redis/distlock.go:96-99`, `src/adapters/redis/distlock.go:136-139`

**Evidence:** `Acquire()` only replaces `ttl` when it is exactly zero. Negative or tiny positive TTLs pass through unchanged. `renewLoop()` then computes `interval := ttl / 2` and calls `time.NewTicker(interval)` without validation.

```go
if ttl == 0 {
    ttl = d.ttl
}

interval := ttl / 2
ticker := time.NewTicker(interval)
```

**Impact:** `ttl <= 1ns` makes `interval <= 0`, which panics. Even when it does not panic, very small TTLs degrade into nonsensical renew behavior. This is public API input and should be validated at the boundary.

**Recommendation:** reject `ttl <= 0`, or clamp to a documented minimum safe value before both `SetNX` and renewal. Add unit tests for negative TTL, sub-millisecond TTL, and minimum accepted TTL.

---

### F-04 [S4 Reliability, S6 Product] P1 -- `IdempotencyChecker` still allows non-expiring keys via `ttl=0`

**Files:** `src/adapters/redis/idempotency.go:49-71`, `src/adapters/rabbitmq/consumer_base.go:31-38`, `src/adapters/rabbitmq/consumer_base.go:106-107`

**Evidence:** both `MarkProcessed()` and `TryProcess()` pass `ttl` directly to Redis `SetNX`. In Redis, zero TTL means no expiry. The main RabbitMQ consumer path currently defaults TTL to 24h before calling `TryProcess()`, but the adapter itself does not protect other callers.

```go
_, err := ic.rdb.SetNX(ctx, key, "1", ttl).Result()
set, err := ic.rdb.SetNX(ctx, key, "1", ttl).Result()
```

**Impact:** a caller that passes `ttl=0` creates permanent dedupe keys. On a long-running consumer or high-cardinality event stream, that turns idempotency state into unbounded memory growth.

**Recommendation:** normalize `ttl <= 0` to `idempotency.DefaultTTL`, or return an explicit error for invalid TTL. Keep the RabbitMQ-side default, but do not rely on every future caller remembering that rule.

---

### F-05 [S5 DX] P2 -- `Cache.Delete()` reports delete failures as `ERR_ADAPTER_REDIS_SET`

**File:** `src/adapters/redis/cache.go:55-58`

**Evidence:**

```go
if err := c.rdb.Del(ctx, key).Err(); err != nil {
    return errcode.Wrap(ErrAdapterRedisSet,
        fmt.Sprintf("redis: cache delete failed (key=%s)", key), err)
}
```

**Impact:** callers and logs cannot distinguish a failed delete from a failed set. This degrades error taxonomy and makes alert routing or operation-specific retry handling harder.

**Recommendation:** introduce `ErrAdapterRedisDelete` or map delete failures to a correctly named existing code.

---

### F-06 [S3 Test] P2 -- tests miss the semantic cases that matter most for locks

**Files:** `src/adapters/redis/distlock_test.go`, `src/adapters/redis/integration_test.go`

**Evidence:** current tests cover acquire/release, contention, constructor defaults, and idempotency happy paths. They do not cover:
- caller context cancellation stopping renewal
- TTL expiry followed by re-acquire
- invalid TTL input handling
- renewal failure / lost lock behavior

**Impact:** the suite proves the happy path, but it does not protect the package against the exact edge cases that define whether a distributed lease is safe to use under load and failure.

**Recommendation:** add at least one unit test and one integration test for renewal/cancellation semantics after fixing F-02/F-03.

---

## Positive Notes

- `IdempotencyChecker.TryProcess()` is present and uses a single `SET NX` operation, so the adapter does expose an atomic check-and-mark primitive.
- `src/adapters/rabbitmq/consumer_base.go:106-123` uses `TryProcess()` directly, which keeps the main consumer path out of the old check-then-mark race.
- `go test -cover ./adapters/redis` passes at 81.4% statement coverage, so the package is not under-tested in general; the gap is in edge-case semantics, not total coverage.

## Verdict

**Blocked on P0** because `DistLock` still lacks a safe fencing story while presenting a generic distributed lock API. If `DistLock` is kept as a public primitive, the contract must be narrowed or the implementation must expose a fencing mechanism. After that, the next fix wave should address context-bound renewal and TTL validation.
